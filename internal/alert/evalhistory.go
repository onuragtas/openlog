package alert

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api/query"
)

// EvaluationRow is one row of alert_evaluations (docs/contracts/alerting.md §3.6).
type EvaluationRow struct {
	TenantID   string
	RuleID     string
	RuleType   string
	SeriesKey  string // "" = the rule row
	Labels     map[string]string
	At         time.Time // end of the evaluated window
	Value      float64   // NaN = NULL
	State      string
	Result     string
	DurationMs uint32
}

// EvaluationSink receives the summaries of committed evaluations. Add must not block.
type EvaluationSink interface {
	Add(rows []EvaluationRow)
}

// EvaluationRows builds the rows of a committed plan: the rule row and one row per series.
func EvaluationRows(rule *Rule, p *Plan) []EvaluationRow {
	firing, pending := 0, 0
	for _, s := range p.Summaries {
		switch s.State {
		case StateFiring:
			firing++
		case StatePending:
			pending++
		}
	}
	state := StateOK
	switch {
	case firing > 0:
		state = StateFiring
	case pending > 0:
		state = StatePending
	}
	result := p.Result
	if result == "" {
		result = "ok"
	}
	dur := uint32(min(p.Duration.Milliseconds(), math.MaxUint32))
	rows := make([]EvaluationRow, 0, len(p.Summaries)+1)
	rows = append(rows, EvaluationRow{TenantID: rule.TenantID, RuleID: rule.ID, RuleType: rule.Type, Labels: map[string]string{},
		At: p.EvalEnd, Value: float64(firing), State: state, Result: result, DurationMs: dur})
	for _, s := range p.Summaries {
		labels := s.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		rows = append(rows, EvaluationRow{TenantID: rule.TenantID, RuleID: rule.ID, RuleType: rule.Type, SeriesKey: s.Key, Labels: labels,
			At: p.EvalEnd, Value: s.Value, State: s.State, Result: result, DurationMs: dur})
	}
	return rows
}

// BlockInserter inserts rows into <table>_local on the shard of keyColumns (clickhouse.ShardedWriter, D-018).
type BlockInserter interface {
	Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error
}

// EvaluationWriterOptions configure an EvaluationWriter.
type EvaluationWriterOptions struct {
	FlushInterval time.Duration // default 5s
	MaxBatch      int           // rows per insert, default 10000
	MaxBuffered   int           // rows kept while ClickHouse is unavailable, default 100000 (oldest dropped)
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

var evaluationColumns = []string{"tenant_id", "rule_id", "series_key", "evaluated_at", "labels", "value", "state", "result", "rule_type", "duration_ms"}

// EvaluationWriter buffers evaluation rows and inserts them in batches. A failed batch is retried with the same
// deduplication token (ReplicatedMergeTree drops a block it already has), so a retry never duplicates rows.
type EvaluationWriter struct {
	ins BlockInserter
	o   EvaluationWriterOptions

	mu    sync.Mutex
	buf   []EvaluationRow
	retry *evalBatch
	kick  chan struct{}

	cRows   *prometheus.CounterVec
	cErrors prometheus.Counter
}

type evalBatch struct {
	token string
	rows  []EvaluationRow
}

// NewEvaluationWriter creates a writer; call Run.
func NewEvaluationWriter(ins BlockInserter, o EvaluationWriterOptions) *EvaluationWriter {
	if o.FlushInterval <= 0 {
		o.FlushInterval = 5 * time.Second
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 10000
	}
	if o.MaxBuffered < o.MaxBatch {
		o.MaxBuffered = max(o.MaxBatch, 100000)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	w := &EvaluationWriter{ins: ins, o: o, kick: make(chan struct{}, 1),
		cRows: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_alert_evaluation_rows_total",
			Help: "Alert evaluation summary rows by outcome (written to ClickHouse, dropped because the buffer was full)."}, []string{"result"}),
		cErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_alert_evaluation_write_errors_total",
			Help: "Failed inserts of alert evaluation summaries (retried)."}),
	}
	w.cRows.WithLabelValues("written")
	w.cRows.WithLabelValues("dropped")
	if o.Registerer != nil {
		o.Registerer.MustRegister(w.cRows, w.cErrors)
	}
	return w
}

// Add implements EvaluationSink.
func (w *EvaluationWriter) Add(rows []EvaluationRow) {
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
func (w *EvaluationWriter) Run(ctx context.Context) {
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

// Flush inserts at most one batch and returns the number of rows written (0 when nothing was written).
func (w *EvaluationWriter) Flush(ctx context.Context) int {
	w.mu.Lock()
	b := w.retry
	if b == nil {
		if len(w.buf) == 0 {
			w.mu.Unlock()
			return 0
		}
		n := min(len(w.buf), w.o.MaxBatch)
		b = &evalBatch{token: "alert-eval:" + uuid.NewString(), rows: append([]EvaluationRow(nil), w.buf[:n]...)}
		w.buf = append(w.buf[:0:0], w.buf[n:]...)
		w.retry = b
	}
	w.mu.Unlock()

	rows := make([][]any, len(b.rows))
	for i, r := range b.rows {
		var v *float64
		if !math.IsNaN(r.Value) && !math.IsInf(r.Value, 0) {
			x := r.Value
			v = &x
		}
		rows[i] = []any{r.TenantID, r.RuleID, r.SeriesKey, r.At.UTC(), r.Labels, v, r.State, r.Result, r.RuleType, r.DurationMs}
	}
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := w.ins.Insert(ictx, "alert_evaluations", []string{"tenant_id", "rule_id"}, evaluationColumns, b.token, rows)
	cancel()
	if err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write alert evaluation summaries; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	w.mu.Lock()
	w.retry = nil
	w.mu.Unlock()
	w.cRows.WithLabelValues("written").Add(float64(len(b.rows)))
	return len(b.rows)
}

// ---- reading (GET /alerts/rules/{id}/evaluations) ----

// HistoryPoint is one bucket of a series history.
type HistoryPoint struct {
	At    time.Time // latest evaluation in the bucket
	Value float64   // value of the latest evaluation with a value in the bucket; NaN = none
	State string    // worst state in the bucket: firing > pending > ok
}

// HistorySeries is the history of one series.
type HistorySeries struct {
	Key    string
	Labels map[string]string
	Points []HistoryPoint
}

// HistoryEvaluation is one bucket of the rule rows.
type HistoryEvaluation struct {
	At           time.Time
	Firing       float64 // maximum number of firing series
	Evaluations  uint64
	Errors       uint64
	MaxDuration  uint32
	LastDuration uint32
}

// History is the evaluation history of a rule.
type History struct {
	From, To    time.Time
	Step        time.Duration
	Evaluations []HistoryEvaluation
	Series      []HistorySeries
	Truncated   bool
}

// History limits.
const (
	historyMaxPoints = 500
	historyMaxSeries = 50
	historyMaxRows   = 50000
	HistoryMaxRange  = 30 * 24 * time.Hour
)

var stateRank = map[string]int{StateOK: 0, StatePending: 1, StateFiring: 2}

// EvaluationHistory reads the summaries of ruleID in [from, to) through sc, in at most historyMaxPoints buckets
// per series. Series with the most non-ok buckets come first (at most historyMaxSeries).
func EvaluationHistory(ctx context.Context, sc *query.Scope, ruleID string, from, to time.Time) (*History, error) {
	if !to.After(from) {
		return nil, invalid("to", "must be after from")
	}
	if to.Sub(from) > HistoryMaxRange {
		return nil, invalid("from", "the range is at most 30 days")
	}
	step := max(10*time.Second, time.Duration(ceilDiv(int64(to.Sub(from)), historyMaxPoints)))
	step = time.Duration(ceilDiv(int64(step), int64(time.Second))) * time.Second
	h := &History{From: from, To: to, Step: step, Evaluations: []HistoryEvaluation{}, Series: []HistorySeries{}}
	q := sc.From(query.AlertEvaluations).Columns(
		"series_key AS sk",
		"intDiv(toUnixTimestamp64Milli(evaluated_at) - {h_from:Int64}, {h_step:Int64}) AS b",
		"toInt64(toUnixTimestamp64Milli(max(evaluated_at))) AS at_ms",
		"argMax(value, evaluated_at) AS v",
		"toUInt8(max(multiIf(state = 'firing', 2, state = 'pending', 1, 0))) AS st",
		"argMax(labels, evaluated_at) AS lb",
		"max(value) AS vmax",
		"count() AS n",
		"countIf(result = 'error') AS errs",
		"max(duration_ms) AS dmax",
		"argMax(duration_ms, evaluated_at) AS dlast",
	).Where("rule_id = {rule_id:String}").Param("rule_id", ruleID).
		Where("evaluated_at >= fromUnixTimestamp64Milli({h_from:Int64}) AND evaluated_at < fromUnixTimestamp64Milli({h_to:Int64})").
		Param("h_from", from.UnixMilli()).Param("h_to", to.UnixMilli()).Param("h_step", step.Milliseconds()).
		GroupBy("sk", "b").OrderBy("sk", "b").Limit(historyMaxRows + 1)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		s      HistorySeries
		nonOK  int
		lastAt time.Time
	}
	series := map[string]*acc{}
	count := 0
	for rows.Next() {
		count++
		if count > historyMaxRows {
			h.Truncated = true
			break
		}
		var (
			sk          string
			b, atMs     int64
			v, vmax     *float64
			st          uint8
			lb          map[string]string
			n, errs     uint64
			dmax, dlast uint32
		)
		if err := rows.Scan(&sk, &b, &atMs, &v, &st, &lb, &vmax, &n, &errs, &dmax, &dlast); err != nil {
			return nil, fmt.Errorf("scan alert evaluations: %w", err)
		}
		at := time.UnixMilli(atMs).UTC()
		if sk == "" {
			e := HistoryEvaluation{At: at, Evaluations: n, Errors: errs, MaxDuration: dmax, LastDuration: dlast, Firing: math.NaN()}
			if vmax != nil {
				e.Firing = *vmax
			}
			h.Evaluations = append(h.Evaluations, e)
			continue
		}
		a, ok := series[sk]
		if !ok {
			a = &acc{s: HistorySeries{Key: sk}}
			series[sk] = a
		}
		p := HistoryPoint{At: at, Value: math.NaN(), State: StateOK}
		if v != nil {
			p.Value = *v
		}
		for name, rank := range stateRank {
			if int(st) == rank {
				p.State = name
			}
		}
		if p.State != StateOK {
			a.nonOK++
		}
		if at.After(a.lastAt) {
			a.lastAt, a.s.Labels = at, lb
		}
		a.s.Points = append(a.s.Points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]*acc, 0, len(series))
	for _, a := range series {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].nonOK != list[j].nonOK {
			return list[i].nonOK > list[j].nonOK
		}
		return list[i].s.Key < list[j].s.Key
	})
	if len(list) > historyMaxSeries {
		list, h.Truncated = list[:historyMaxSeries], true
	}
	for _, a := range list {
		if a.s.Labels == nil {
			a.s.Labels = map[string]string{}
		}
		h.Series = append(h.Series, a.s)
	}
	return h, nil
}
