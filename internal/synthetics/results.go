package synthetics

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api/query"
)

// maxErrorBytes bounds the stored error message (the UI shows it next to the check, which names its URL).
const maxErrorBytes = 512

// Result is one run of one check from one location: the row of synthetic_runs and the source of the emitted
// metric data points.
type Result struct {
	CheckID  string
	TenantID string
	Location string
	// Name, URL and Method are copied from the definition at run time (checker.go).
	Name   string
	URL    string
	Method string

	At            time.Time
	Success       bool
	StatusCode    int
	DurationMs    float64
	DNSMs         float64
	ConnectMs     float64
	TLSMs         float64
	FirstByteMs   float64
	ResponseBytes int64
	ErrorKind     string
	Error         string
}

// fail marks the result failed with a kind and a message.
func (r Result) fail(kind, msg string) Result {
	r.Success = false
	r.ErrorKind = kind
	r.Error = truncate(msg, maxErrorBytes)
	return r
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ResultSink receives finished runs. Add must not block.
type ResultSink interface {
	Add(rows []Result)
}

// Metric names mirrored into `metrics` so metric alert rules (alerting.md §2.2) and dashboards can use the
// checks like any other metric. Both are gauges, so metrics_1m rolls them up (apm.md §8, 395 days).
const (
	// MetricSuccess is 1 for a successful run and 0 for a failed one; avg over a window is the uptime ratio.
	MetricSuccess = "synthetics.check.success"
	// MetricDuration is the total run time in milliseconds (also recorded for a failed run).
	MetricDuration = "synthetics.check.duration"
)

// scopeName marks the emitted data points as openlog's own (they have no OTLP sender).
const scopeName = "openlog/synthetics"

// BlockInserter inserts rows into <table>_local on the shard of keyColumns (clickhouse.ShardedWriter, D-018).
type BlockInserter interface {
	Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error
}

var runColumns = []string{"tenant_id", "check_id", "check_name", "location", "timestamp", "success",
	"status_code", "error_kind", "error_message", "duration_ms", "dns_ms", "connect_ms", "tls_ms",
	"first_byte_ms", "response_bytes", "url", "method"}

// metricColumns is the column list of `metrics` (schema 0002_metrics, 0081); the order matches the values
// built by metricRows.
var metricColumns = []string{"tenant_id", "metric_name", "metric_type", "temporality", "is_monotonic", "unit",
	"description", "service_name", "host_id", "host_name", "series_id", "resource_attributes", "scope_name",
	"attributes", "start_timestamp", "timestamp", "value", "count", "sum", "bucket_counts", "explicit_bounds",
	"flags", "quantiles", "quantile_values"}

// WriterOptions configure a Writer.
type WriterOptions struct {
	FlushInterval time.Duration // default 5s
	MaxBatch      int           // results per insert, default 1000
	MaxBuffered   int           // results kept while ClickHouse is unavailable, default 10000 (oldest dropped)
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

// Writer buffers results and inserts them into synthetic_runs and metrics in batches. A failed batch is
// retried with the same deduplication token (ReplicatedMergeTree drops a block it already has), so a retry
// never duplicates rows. Losing results (process death before a flush) only leaves a gap in the history;
// PostgreSQL keeps the last outcome of every check.
type Writer struct {
	ins BlockInserter
	o   WriterOptions

	mu    sync.Mutex
	buf   []Result
	retry *resultBatch
	kick  chan struct{}

	cRows   *prometheus.CounterVec
	cErrors prometheus.Counter
}

type resultBatch struct {
	token string
	rows  []Result
}

// NewWriter creates a writer; call Run.
func NewWriter(ins BlockInserter, o WriterOptions) *Writer {
	if o.FlushInterval <= 0 {
		o.FlushInterval = 5 * time.Second
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 1000
	}
	if o.MaxBuffered < o.MaxBatch {
		o.MaxBuffered = max(o.MaxBatch, 10000)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	w := &Writer{ins: ins, o: o, kick: make(chan struct{}, 1),
		cRows: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_synthetic_result_rows_total",
			Help: "Synthetic check results by outcome (written to ClickHouse, dropped because the buffer was full)."}, []string{"result"}),
		cErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_synthetic_result_write_errors_total",
			Help: "Failed inserts of synthetic check results (retried)."}),
	}
	w.cRows.WithLabelValues("written")
	w.cRows.WithLabelValues("dropped")
	if o.Registerer != nil {
		o.Registerer.MustRegister(w.cRows, w.cErrors)
	}
	return w
}

// Add implements ResultSink.
func (w *Writer) Add(rows []Result) {
	if len(rows) == 0 {
		return
	}
	w.mu.Lock()
	w.buf = append(w.buf, rows...)
	inRetry := 0
	if w.retry != nil {
		inRetry = len(w.retry.rows)
	}
	if over := len(w.buf) + inRetry - w.o.MaxBuffered; over > 0 {
		over = min(over, len(w.buf))
		w.buf = append(w.buf[:0:0], w.buf[over:]...)
		w.cRows.WithLabelValues("dropped").Add(float64(over))
	}
	full := len(w.buf) >= w.o.MaxBatch
	w.mu.Unlock()
	if full {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
}

// Run flushes every FlushInterval (or when a batch is full) until ctx is done, then flushes once more.
func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(w.o.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			for w.Flush(fctx) > 0 {
			}
			cancel()
			return
		case <-t.C:
		case <-w.kick:
		}
		for w.Flush(ctx) > 0 && ctx.Err() == nil {
		}
	}
}

// Flush inserts at most one batch and returns the number of results written (0 when nothing was written).
func (w *Writer) Flush(ctx context.Context) int {
	w.mu.Lock()
	b := w.retry
	if b == nil {
		if len(w.buf) == 0 {
			w.mu.Unlock()
			return 0
		}
		n := min(len(w.buf), w.o.MaxBatch)
		b = &resultBatch{token: "synthetics:" + uuid.NewString(), rows: append([]Result(nil), w.buf[:n]...)}
		w.buf = append(w.buf[:0:0], w.buf[n:]...)
		w.retry = b
	}
	w.mu.Unlock()

	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The runs and their metric mirror are separate tables with separate sharding keys, so they are two
	// inserts; each keeps its own part of the token and is skipped on a retry once it succeeded.
	if err := w.ins.Insert(ictx, "synthetic_runs", []string{"tenant_id", "check_id"}, runColumns, b.token+":runs", runRows(b.rows)); err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write synthetic check results; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	if err := w.ins.Insert(ictx, "metrics", []string{"tenant_id", "host_id"}, metricColumns, b.token+":metrics", metricRows(b.rows)); err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write synthetic check metrics; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	w.mu.Lock()
	w.retry = nil
	w.mu.Unlock()
	w.cRows.WithLabelValues("written").Add(float64(len(b.rows)))
	return len(b.rows)
}

func runRows(rows []Result) [][]any {
	out := make([][]any, len(rows))
	for i, r := range rows {
		out[i] = []any{r.TenantID, r.CheckID, r.Name, r.Location, r.At.UTC(), r.Success, uint16(clampStatus(r.StatusCode)),
			r.ErrorKind, r.Error, float32(r.DurationMs), float32(r.DNSMs), float32(r.ConnectMs), float32(r.TLSMs),
			float32(r.FirstByteMs), uint32(max(r.ResponseBytes, 0)), r.URL, r.Method}
	}
	return out
}

func clampStatus(code int) int {
	if code < 0 || code > 599 {
		return 0
	}
	return code
}

// metricRows mirrors every result as two gauge data points (MetricSuccess, MetricDuration).
func metricRows(rows []Result) [][]any {
	out := make([][]any, 0, 2*len(rows))
	for _, r := range rows {
		attrs := map[string]string{"check.id": r.CheckID, "check.name": r.Name, "location": r.Location,
			"http.request.method": r.Method, "url.full": r.URL}
		if r.StatusCode > 0 {
			attrs["http.response.status_code"] = fmt.Sprintf("%d", r.StatusCode)
		}
		if r.ErrorKind != "" {
			attrs["error.kind"] = r.ErrorKind
		}
		success := 0.0
		if r.Success {
			success = 1
		}
		out = append(out, metricRow(r, MetricSuccess, "1", success, attrs), metricRow(r, MetricDuration, "ms", r.DurationMs, attrs))
	}
	return out
}

func metricRow(r Result, name, unit string, value float64, attrs map[string]string) []any {
	ts := r.At.UTC()
	return []any{r.TenantID, name, "gauge", "unspecified", false, unit, "", "", "", "",
		seriesID(r.TenantID, name, attrs), map[string]string{}, scopeName, attrs, ts, ts, value,
		uint64(0), float64(0), []uint64{}, []float64{}, uint32(0), []float64{}, []float64{}}
}

// seriesID is the stable 64-bit hash of (tenant, metric name, attributes) the processor computes for OTLP
// data points (internal/processor/convert.go): the same series across runs, a new series when an attribute
// changes.
func seriesID(tenant, metric string, attrs map[string]string) uint64 {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(tenant)
	b.WriteByte(0)
	b.WriteString(metric)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(1)
		b.WriteString(attrs[k])
	}
	return xxhash.Sum64String(b.String())
}

// ---- reading (GET /synthetics/checks[/{id}]/results) ----

// Reading limits.
const (
	// HistoryMaxRange bounds a results query (the runs are kept 30 days).
	HistoryMaxRange = 31 * 24 * time.Hour
	// MaxFailures is the longest list of recent failures one response carries.
	MaxFailures = 200
	// summaryMaxRows bounds the bucketed query (checks x buckets).
	summaryMaxRows = 20000
)

// Summary is the uptime and latency of one check over a range.
type Summary struct {
	Runs     uint64
	Failures uint64
	// Uptime is the share of successful runs in percent; NaN without runs.
	Uptime float64
	AvgMs  float64
	P50Ms  float64
	P95Ms  float64
	P99Ms  float64
	// Points is the per-bucket series (the list's sparkline, the detail's chart), oldest first.
	Points []Point
}

// Point is one bucket of a check's history.
type Point struct {
	T        time.Time
	Runs     uint64
	Failures uint64
	Uptime   float64
	P95Ms    float64
}

// Failure is one failed run (the detail view's list).
type Failure struct {
	At         time.Time
	Location   string
	StatusCode int
	ErrorKind  string
	Error      string
	DurationMs float64
}

// Summaries returns the uptime and latency of the tenant's checks in [from, to), bucketed by step for the
// series. checkID limits the result to one check ("" = every check of the tenant). Checks without a run in
// the range are absent from the map.
func Summaries(ctx context.Context, sc *query.Scope, checkID string, from, to time.Time, step time.Duration) (map[string]Summary, error) {
	if !to.After(from) {
		return nil, invalid("to", "must be after from")
	}
	if to.Sub(from) > HistoryMaxRange {
		return nil, invalid("from", "the range is at most 31 days")
	}
	if step < time.Minute {
		step = time.Minute
	}
	out := map[string]Summary{}
	// The totals are aggregated over the whole range, so the percentiles are exact (merging per-bucket
	// percentiles would not be).
	totals := sc.From(query.SyntheticRuns).Columns(
		"check_id AS cid",
		"count() AS n",
		"countIf(NOT success) AS bad",
		"toFloat64(avg(duration_ms)) AS avg_ms",
		"toFloat64(quantile(0.5)(duration_ms)) AS p50",
		"toFloat64(quantile(0.95)(duration_ms)) AS p95",
		"toFloat64(quantile(0.99)(duration_ms)) AS p99",
	).GroupBy("cid").OrderBy("cid").Limit(summaryMaxRows)
	rangeWhere(totals, checkID, from, to)
	rows, err := sc.Query(ctx, totals)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid                     string
			n, bad                  uint64
			avgMs, p50, p95, p99    float64
			runs, failures, uptimeN = uint64(0), uint64(0), float64(0)
		)
		if err := rows.Scan(&cid, &n, &bad, &avgMs, &p50, &p95, &p99); err != nil {
			return nil, fmt.Errorf("scan synthetic run totals: %w", err)
		}
		runs, failures = n, bad
		uptimeN = math.NaN()
		if runs > 0 {
			uptimeN = float64(runs-failures) / float64(runs) * 100
		}
		out[cid] = Summary{Runs: runs, Failures: failures, Uptime: uptimeN, AvgMs: avgMs, P50Ms: p50,
			P95Ms: p95, P99Ms: p99, Points: []Point{}}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	return out, loadPoints(ctx, sc, out, checkID, from, to, step)
}

// loadPoints fills the per-bucket series of the summaries already loaded.
func loadPoints(ctx context.Context, sc *query.Scope, out map[string]Summary, checkID string, from, to time.Time, step time.Duration) error {
	q := sc.From(query.SyntheticRuns).Columns(
		"check_id AS cid",
		"intDiv(toUnixTimestamp64Milli(timestamp) - {s_from:Int64}, {s_step:Int64}) AS b",
		"count() AS n",
		"countIf(NOT success) AS bad",
		"toFloat64(quantile(0.95)(duration_ms)) AS p95",
	).Param("s_step", step.Milliseconds()).GroupBy("cid", "b").OrderBy("cid", "b").Limit(summaryMaxRows)
	rangeWhere(q, checkID, from, to)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid    string
			b      int64
			n, bad uint64
			p95    float64
		)
		if err := rows.Scan(&cid, &b, &n, &bad, &p95); err != nil {
			return fmt.Errorf("scan synthetic run buckets: %w", err)
		}
		s, ok := out[cid]
		if !ok {
			continue
		}
		uptime := math.NaN()
		if n > 0 {
			uptime = float64(n-bad) / float64(n) * 100
		}
		s.Points = append(s.Points, Point{T: from.Add(time.Duration(b) * step).UTC(), Runs: n, Failures: bad,
			Uptime: uptime, P95Ms: p95})
		out[cid] = s
	}
	return rows.Err()
}

// Failures returns the newest failed runs of one check in [from, to).
func Failures(ctx context.Context, sc *query.Scope, checkID string, from, to time.Time, limit int) ([]Failure, error) {
	if checkID == "" {
		return nil, invalid("check_id", "required")
	}
	if limit <= 0 || limit > MaxFailures {
		limit = MaxFailures
	}
	q := sc.From(query.SyntheticRuns).Columns(
		"toInt64(toUnixTimestamp64Milli(timestamp)) AS at_ms",
		"location AS loc",
		"status_code AS code",
		"error_kind AS kind",
		"error_message AS msg",
		"toFloat64(duration_ms) AS ms",
	).Where("NOT success").OrderBy("timestamp DESC").Limit(limit)
	rangeWhere(q, checkID, from, to)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Failure{}
	for rows.Next() {
		var (
			atMs      int64
			loc, kind string
			msg       string
			code      uint16
			ms        float64
		)
		if err := rows.Scan(&atMs, &loc, &code, &kind, &msg, &ms); err != nil {
			return nil, fmt.Errorf("scan synthetic failures: %w", err)
		}
		out = append(out, Failure{At: time.UnixMilli(atMs).UTC(), Location: loc, StatusCode: int(code),
			ErrorKind: kind, Error: msg, DurationMs: ms})
	}
	return out, rows.Err()
}

// rangeWhere adds the time range (and the check filter) shared by the result queries.
func rangeWhere(q *query.Select, checkID string, from, to time.Time) {
	q.Where("timestamp >= fromUnixTimestamp64Milli({s_from:Int64}) AND timestamp < fromUnixTimestamp64Milli({s_to:Int64})").
		Param("s_from", from.UnixMilli()).Param("s_to", to.UnixMilli())
	if checkID != "" {
		q.Where("check_id = {s_check:String}").Param("s_check", checkID)
	}
}
