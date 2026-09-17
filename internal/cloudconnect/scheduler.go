package cloudconnect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PollRunner performs one poll (collector.go; the scheduler tests replace it).
type PollRunner interface {
	Collect(ctx context.Context, due Due) Run
}

// Scheduling defaults.
const (
	DefaultMaxConcurrent           = 10
	DefaultTenantMaxConcurrent     = 4
	DefaultConnectionMaxConcurrent = 2
	DefaultTick                    = 15 * time.Second
	// MaxErrorBackoff bounds the exponential backoff of a failing scope.
	MaxErrorBackoff = 30 * time.Minute
)

// RunnerOptions configure a Runner.
type RunnerOptions struct {
	// MaxConcurrent bounds the polls of all organizations together (default DefaultMaxConcurrent).
	MaxConcurrent int
	// TenantMaxConcurrent bounds the concurrent polls of one organization (default DefaultTenantMaxConcurrent).
	TenantMaxConcurrent int
	// ConnectionMaxConcurrent bounds the concurrent polls of one connection, so a connection with many
	// regions does not open a burst of requests against one cloud account and trip its rate limits
	// (default DefaultConnectionMaxConcurrent).
	ConnectionMaxConcurrent int
	// Tick is how often due scopes are claimed (default DefaultTick).
	Tick time.Duration
	// BatchSize is how many due rows one claim takes (default 4 x MaxConcurrent).
	BatchSize  int
	Log        *slog.Logger
	Now        func() time.Time
	Registerer prometheus.Registerer
}

// Runner claims due scopes and polls them. It is an api leader task (internal/app): the leader election makes
// one process the scheduler, and claiming advances next_run_at in the same statement, so a scope is never
// polled twice even while leadership moves between pods — which matters more here than for synthetics,
// because a duplicate poll costs the customer money at the provider.
type Runner struct {
	store     ScheduleStore
	collector PollRunner
	o         RunnerOptions

	global chan struct{}

	mu          sync.Mutex
	tenants     map[string]chan struct{}
	connections map[string]chan struct{}

	cPolls   *prometheus.CounterVec
	cMetrics prometheus.Counter
	cCalls   prometheus.Counter
}

// NewRunner creates the scheduler; call Run.
func NewRunner(store ScheduleStore, collector PollRunner, o RunnerOptions) *Runner {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = DefaultMaxConcurrent
	}
	if o.TenantMaxConcurrent <= 0 {
		o.TenantMaxConcurrent = DefaultTenantMaxConcurrent
	}
	if o.TenantMaxConcurrent > o.MaxConcurrent {
		o.TenantMaxConcurrent = o.MaxConcurrent
	}
	if o.ConnectionMaxConcurrent <= 0 {
		o.ConnectionMaxConcurrent = DefaultConnectionMaxConcurrent
	}
	if o.ConnectionMaxConcurrent > o.TenantMaxConcurrent {
		o.ConnectionMaxConcurrent = o.TenantMaxConcurrent
	}
	if o.Tick <= 0 {
		o.Tick = DefaultTick
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 4 * o.MaxConcurrent
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	r := &Runner{store: store, collector: collector, o: o,
		global:      make(chan struct{}, o.MaxConcurrent),
		tenants:     map[string]chan struct{}{},
		connections: map[string]chan struct{}{},
		cPolls: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_cloud_polls_total",
			Help: "Cloud metric polls by outcome."}, []string{"provider", "result"}),
		cMetrics: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_cloud_metrics_collected_total",
			Help: "Data points collected from cloud provider APIs."}),
		cCalls: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_cloud_provider_requests_total",
			Help: "Requests made to cloud provider APIs (what the provider bills for)."}),
	}
	for _, p := range Providers() {
		for _, s := range []string{StatusOK, StatusPartial, StatusError} {
			r.cPolls.WithLabelValues(p, s)
		}
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(r.cPolls, r.cMetrics, r.cCalls)
	}
	return r
}

func (r *Runner) now() time.Time {
	if r.o.Now != nil {
		return r.o.Now()
	}
	return time.Now()
}

// Run claims and polls due scopes until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.o.Tick)
	defer t.Stop()
	for {
		// A full batch means more scopes were due than one claim takes (a backlog after a restart or a
		// leadership change): keep claiming instead of waiting for the next tick.
		for r.RunOnce(ctx) >= r.o.BatchSize && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce claims the due scopes, polls them and returns how many were claimed.
func (r *Runner) RunOnce(ctx context.Context) int {
	due, err := r.store.Claim(ctx, r.now().UTC(), r.o.BatchSize)
	if err != nil {
		if ctx.Err() == nil {
			r.o.Log.Warn("cannot claim due cloud connections", "err", err)
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.pollOne(ctx, d)
		}()
	}
	wg.Wait()
	return len(due)
}

// pollOne polls one claimed scope. The slots are taken innermost first (connection, then tenant, then
// global), so a connection at its own limit waits without occupying a slot another connection could use.
func (r *Runner) pollOne(ctx context.Context, d Due) {
	conn := r.sem(r.connections, d.Connection.ID, r.o.ConnectionMaxConcurrent)
	if !acquire(ctx, conn) {
		return
	}
	defer func() { <-conn }()
	tenant := r.sem(r.tenants, d.TenantID, r.o.TenantMaxConcurrent)
	if !acquire(ctx, tenant) {
		return
	}
	defer func() { <-tenant }()
	if !acquire(ctx, r.global) {
		return
	}
	defer func() { <-r.global }()

	run := r.collector.Collect(ctx, d)
	r.cPolls.WithLabelValues(d.Connection.Provider, run.Status).Inc()
	r.cMetrics.Add(float64(run.Metrics))
	r.cCalls.Add(float64(run.APICalls))
	if run.Status != StatusOK {
		r.o.Log.Debug("cloud poll did not complete", "connection_id", d.Connection.ID, "provider", d.Connection.Provider,
			"scope", run.Scope, "status", run.Status, "error", run.Error)
	}

	// A failing scope is backed off, so a broken region stops costing a request every interval. The count of
	// consecutive failures comes from the schedule row the claim returned.
	backoff := time.Duration(0)
	if run.Status == StatusError {
		backoff = errorBackoff(consecutiveErrors(d)+1, d.Connection.Interval())
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.store.Record(rctx, d.Connection.ID, run, backoff); err != nil {
		r.o.Log.Warn("cannot record cloud poll", "connection_id", d.Connection.ID, "scope", run.Scope, "err", err)
	}
}

// consecutiveErrors reads the failure count the claim carried for this scope.
func consecutiveErrors(d Due) int {
	for _, s := range d.Connection.Status {
		if s.Scope == d.Scope {
			return s.ConsecutiveErrors
		}
	}
	return 0
}

// errorBackoff is how far a failing scope's next poll is pushed out: the interval doubled per consecutive
// failure, capped at MaxErrorBackoff. It never polls sooner than the normal interval.
func errorBackoff(consecutive int, interval time.Duration) time.Duration {
	if consecutive <= 1 {
		return 0
	}
	d := interval
	for range min(consecutive-1, 16) {
		d *= 2
		if d >= MaxErrorBackoff {
			return MaxErrorBackoff
		}
	}
	return min(d, MaxErrorBackoff)
}

// sem returns a named semaphore, created on first use.
func (r *Runner) sem(m map[string]chan struct{}, key string, size int) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := m[key]
	if !ok {
		s = make(chan struct{}, size)
		m[key] = s
	}
	return s
}

// acquire takes a slot unless ctx is done first. A done context is checked before the select: both cases are
// ready when the semaphore has room and the runtime picks one at random, so without this guard a shutdown (or
// a lost leadership) would keep starting polls. The claimed rows are not lost by stopping here; their
// next_run_at has moved on and they are polled again at their next interval.
func acquire(ctx context.Context, sem chan struct{}) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
