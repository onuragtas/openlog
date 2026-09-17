package cloudconnect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/processor"
)

// Collected points become rows of `metrics` — the same table the processor writes OTLP data points into, with
// the same columns, the same sharding key and the same series id. That is the whole point of the feature: a
// managed database is a metric source like any other, so the Metrics Explorer, the metric alert rules
// (alerting.md §2.2) and dashboards work with no new path, and metrics_1m rolls the points up for 395 days
// (apm.md §8).
//
// The series id is computed with processor.SeriesID, not a copy of it: a second implementation of that hash
// would drift from the processor's and split one series in two.

// BlockInserter inserts rows into <table>_local on the shard of keyColumns (clickhouse.ShardedWriter, D-018).
type BlockInserter interface {
	Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error
}

// metricColumns is the column list of `metrics` (schema 0002_metrics, 0081); the order matches the values
// built by metricRows.
var metricColumns = []string{"tenant_id", "metric_name", "metric_type", "temporality", "is_monotonic", "unit",
	"description", "service_name", "host_id", "host_name", "series_id", "resource_attributes", "scope_name",
	"attributes", "start_timestamp", "timestamp", "value", "count", "sum", "bucket_counts", "explicit_bounds",
	"flags", "quantiles", "quantile_values"}

// metricKeyColumns is the sharding key of the metrics table, so direct inserts land where the Distributed
// engine would have put them.
var metricKeyColumns = []string{"tenant_id", "host_id"}

// scopeName marks the emitted data points as openlog's own (they have no OTLP sender).
const scopeName = "openlog/cloudconnect"

// WriterOptions configure a Writer.
type WriterOptions struct {
	FlushInterval time.Duration // default 5s
	MaxBatch      int           // data points per insert, default 5000
	MaxBuffered   int           // points kept while ClickHouse is unavailable, default 50000 (oldest dropped)
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

// Writer buffers collected points and inserts them into `metrics` in batches. A failed batch is retried with
// the same deduplication token (ReplicatedMergeTree drops a block it already has), so a retry never
// duplicates rows. Losing points (process death before a flush) only leaves a gap: the next poll re-reads an
// overlapping window, and PostgreSQL keeps the last outcome of every scope.
type Writer struct {
	ins BlockInserter
	o   WriterOptions

	mu    sync.Mutex
	buf   []CollectedPoint
	retry *pointBatch
	kick  chan struct{}

	cRows   *prometheus.CounterVec
	cErrors prometheus.Counter
}

type pointBatch struct {
	token string
	rows  []CollectedPoint
}

var _ PointSink = (*Writer)(nil)

// NewWriter creates a writer; call Run.
func NewWriter(ins BlockInserter, o WriterOptions) *Writer {
	if o.FlushInterval <= 0 {
		o.FlushInterval = 5 * time.Second
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 5000
	}
	if o.MaxBuffered < o.MaxBatch {
		o.MaxBuffered = max(o.MaxBatch, 50000)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	w := &Writer{ins: ins, o: o, kick: make(chan struct{}, 1),
		cRows: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_cloud_metric_rows_total",
			Help: "Cloud metric data points (written to ClickHouse, dropped because the buffer was full)."}, []string{"result"}),
		cErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_cloud_metric_write_errors_total",
			Help: "Failed inserts of cloud metric data points (retried)."}),
	}
	w.cRows.WithLabelValues("written")
	w.cRows.WithLabelValues("dropped")
	if o.Registerer != nil {
		o.Registerer.MustRegister(w.cRows, w.cErrors)
	}
	return w
}

// Add implements PointSink.
func (w *Writer) Add(rows []CollectedPoint) {
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

// Flush inserts at most one batch and returns the number of points written (0 when nothing was written).
func (w *Writer) Flush(ctx context.Context) int {
	w.mu.Lock()
	b := w.retry
	if b == nil {
		if len(w.buf) == 0 {
			w.mu.Unlock()
			return 0
		}
		n := min(len(w.buf), w.o.MaxBatch)
		b = &pointBatch{token: "cloudconnect:" + uuid.NewString(), rows: append([]CollectedPoint(nil), w.buf[:n]...)}
		w.buf = append(w.buf[:0:0], w.buf[n:]...)
		w.retry = b
	}
	w.mu.Unlock()

	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.ins.Insert(ictx, "metrics", metricKeyColumns, metricColumns, b.token, metricRows(b.rows)); err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write cloud metrics; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	w.mu.Lock()
	w.retry = nil
	w.mu.Unlock()
	w.cRows.WithLabelValues("written").Add(float64(len(b.rows)))
	return len(b.rows)
}

// metricRows renders collected points as rows of `metrics`. Every point is a gauge: the provider already
// aggregated the window (Average, Sum, Maximum), so what openlog stores is the value at that timestamp, and
// the statistic travels as an attribute rather than as an OTLP temporality openlog would have to invent.
func metricRows(rows []CollectedPoint) [][]any {
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		var (
			name     = r.Name()
			resAttrs = r.ResourceAttributes(r.ConnectionID, r.ConnectionName)
			attrs    = r.Attributes()
			ts       = r.Timestamp.UTC()
		)
		out = append(out, []any{
			r.TenantID, name, "gauge", "unspecified", false, r.Unit,
			"", "", "", "",
			processor.SeriesID(r.TenantID, name, resAttrs, attrs), resAttrs, scopeName,
			attrs, ts, ts, r.Value,
			uint64(0), float64(0), []uint64{}, []float64{}, uint32(0), []float64{}, []float64{},
		})
	}
	return out
}
