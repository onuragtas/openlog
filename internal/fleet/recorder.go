package fleet

import (
	"container/list"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HostWriter persists sync reports (PGStore.UpsertHosts).
type HostWriter interface {
	UpsertHosts(ctx context.Context, recs []HostRecord) error
}

// RecorderOptions configure Recorder. Zero values take the defaults in brackets.
type RecorderOptions struct {
	MaxPending    int           // bound on queued reports; the oldest is dropped when full [10000]
	BatchSize     int           // reports per statement [200]
	FlushInterval time.Duration // [1s]
	WriteTimeout  time.Duration // [10s]
	Registerer    prometheus.Registerer
	Log           *slog.Logger
}

// Recorder writes sync reports to PostgreSQL asynchronously in small batches. Record never blocks:
// reports for the same host are coalesced (the newest wins) and, when the queue is full, the oldest
// report is dropped. A failed batch is dropped too; agents report again on their next sync.
type Recorder struct {
	w HostWriter
	o RecorderOptions

	mu      sync.Mutex
	queue   *list.List               // of *HostRecord, oldest first
	byKey   map[string]*list.Element // tenant \x00 host → element
	wake    chan struct{}
	flushMu sync.Mutex

	dropped prometheus.Counter
	written *prometheus.CounterVec
	pending prometheus.GaugeFunc
}

// NewRecorder creates a recorder. Call Run to write.
func NewRecorder(w HostWriter, o RecorderOptions) *Recorder {
	if o.MaxPending <= 0 {
		o.MaxPending = 10000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	r := &Recorder{w: w, o: o, queue: list.New(), byKey: map[string]*list.Element{}, wake: make(chan struct{}, 1),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_fleet_host_reports_dropped_total", Help: "Agent sync reports dropped because the write queue was full.",
		}),
		written: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_fleet_host_reports_written_total", Help: "Agent sync reports written to PostgreSQL by result (ok, error).",
		}, []string{"result"}),
	}
	r.pending = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "openlog_fleet_host_reports_pending", Help: "Agent sync reports waiting to be written.",
	}, func() float64 { return float64(r.Len()) })
	if o.Registerer != nil {
		o.Registerer.MustRegister(r.dropped, r.written, r.pending)
	}
	return r
}

// Record queues a report.
func (r *Recorder) Record(rec HostRecord) {
	key := rec.TenantID + "\x00" + rec.Report.HostID
	r.mu.Lock()
	if el, ok := r.byKey[key]; ok {
		old := el.Value.(*HostRecord)
		if rec.RolloutID == "" {
			rec.RolloutID = old.RolloutID
		}
		el.Value = &rec
	} else {
		if r.queue.Len() >= r.o.MaxPending {
			oldest := r.queue.Front()
			o := oldest.Value.(*HostRecord)
			delete(r.byKey, o.TenantID+"\x00"+o.Report.HostID)
			r.queue.Remove(oldest)
			r.dropped.Inc()
		}
		r.byKey[key] = r.queue.PushBack(&rec)
	}
	full := r.queue.Len() >= r.o.BatchSize
	r.mu.Unlock()
	if full {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// Len is the number of queued reports.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queue.Len()
}

func (r *Recorder) take() []HostRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := min(r.queue.Len(), r.o.BatchSize)
	out := make([]HostRecord, 0, n)
	for range n {
		el := r.queue.Front()
		rec := el.Value.(*HostRecord)
		delete(r.byKey, rec.TenantID+"\x00"+rec.Report.HostID)
		r.queue.Remove(el)
		out = append(out, *rec)
	}
	return out
}

// Flush writes everything queued (in batches).
func (r *Recorder) Flush(ctx context.Context) {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	for {
		batch := r.take()
		if len(batch) == 0 {
			return
		}
		wctx, cancel := context.WithTimeout(ctx, r.o.WriteTimeout)
		err := r.w.UpsertHosts(wctx, batch)
		cancel()
		if err != nil {
			r.written.WithLabelValues("error").Add(float64(len(batch)))
			r.o.Log.Warn("cannot write agent sync reports", "reports", len(batch), "err", err)
			return
		}
		r.written.WithLabelValues("ok").Add(float64(len(batch)))
	}
}

// Run writes queued reports every FlushInterval (or as soon as a batch is full) until ctx is done,
// then flushes once more.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(r.o.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.Flush(fctx)
			cancel()
			return
		case <-t.C:
		case <-r.wake:
		}
		r.Flush(ctx)
	}
}
