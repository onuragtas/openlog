package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// The writer buffers concluded runs and inserts them into job_runs and their metric mirror in batches, like
// the synthetics writer. Losing a buffered run (process death before a flush) leaves a gap in the history
// only: PostgreSQL holds the monitor's current state, which is what the list and the alerting read.

// BlockInserter inserts rows into <table>_local on the shard of keyColumns (clickhouse.ShardedWriter, D-018).
type BlockInserter interface {
	Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error
}

// RunSink receives concluded runs. Add must not block.
type RunSink interface {
	Add(rows []Run)
}

// WriterOptions configure a Writer.
type WriterOptions struct {
	FlushInterval time.Duration // default 5s
	MaxBatch      int           // runs per insert, default 500
	MaxBuffered   int           // runs kept while ClickHouse is unavailable, default 5000 (oldest dropped)
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

// Writer is the RunSink backed by ClickHouse.
type Writer struct {
	ins BlockInserter
	o   WriterOptions

	mu    sync.Mutex
	buf   []Run
	retry *runBatch
	kick  chan struct{}

	cRows   *prometheus.CounterVec
	cErrors prometheus.Counter
}

var _ RunSink = (*Writer)(nil)

type runBatch struct {
	token string
	rows  []Run
}

// NewWriter creates a writer; call Run.
func NewWriter(ins BlockInserter, o WriterOptions) *Writer {
	if o.FlushInterval <= 0 {
		o.FlushInterval = 5 * time.Second
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 500
	}
	if o.MaxBuffered < o.MaxBatch {
		o.MaxBuffered = max(o.MaxBatch, 5000)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	w := &Writer{ins: ins, o: o, kick: make(chan struct{}, 1),
		cRows: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_job_run_rows_total",
			Help: "Job monitor runs by outcome (written to ClickHouse, dropped because the buffer was full)."}, []string{"result"}),
		cErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_job_run_write_errors_total",
			Help: "Failed inserts of job monitor runs (retried)."}),
	}
	w.cRows.WithLabelValues("written")
	w.cRows.WithLabelValues("dropped")
	if o.Registerer != nil {
		o.Registerer.MustRegister(w.cRows, w.cErrors)
	}
	return w
}

// Add implements RunSink.
func (w *Writer) Add(rows []Run) {
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

// Flush inserts at most one batch and returns the number of runs written.
func (w *Writer) Flush(ctx context.Context) int {
	w.mu.Lock()
	b := w.retry
	if b == nil {
		if len(w.buf) == 0 {
			w.mu.Unlock()
			return 0
		}
		n := min(len(w.buf), w.o.MaxBatch)
		b = &runBatch{token: "jobs:" + uuid.NewString(), rows: append([]Run(nil), w.buf[:n]...)}
		w.buf = append(w.buf[:0:0], w.buf[n:]...)
		w.retry = b
	}
	w.mu.Unlock()

	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// A failed batch is retried with the same deduplication token (ReplicatedMergeTree drops a block it
	// already has), so a retry never duplicates a run.
	if err := w.ins.Insert(ictx, "job_runs", []string{"tenant_id", "monitor_id"}, runColumns, b.token+":runs", runRows(b.rows)); err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write job runs; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	if err := w.ins.Insert(ictx, "metrics", []string{"tenant_id", "host_id"}, metricColumns, b.token+":metrics", metricRows(b.rows)); err != nil {
		w.cErrors.Inc()
		w.o.Log.Warn("cannot write job run metrics; retrying", "rows", len(b.rows), "err", err)
		return 0
	}
	w.mu.Lock()
	w.retry = nil
	w.mu.Unlock()
	w.cRows.WithLabelValues("written").Add(float64(len(b.rows)))
	return len(b.rows)
}
