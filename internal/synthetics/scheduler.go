package synthetics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CheckRunner performs one run (checker.go; the scheduler tests replace it).
type CheckRunner interface {
	Run(ctx context.Context, due Due) Result
}

// Scheduling defaults.
const (
	DefaultMaxConcurrent       = 20
	DefaultTenantMaxConcurrent = 5
	DefaultTick                = 5 * time.Second
)

// RunnerOptions configure a Runner.
type RunnerOptions struct {
	// MaxConcurrent bounds the runs of all organizations together (default DefaultMaxConcurrent).
	MaxConcurrent int
	// TenantMaxConcurrent bounds the concurrent runs of one organization, so a tenant with many checks
	// cannot use the whole pool (default DefaultTenantMaxConcurrent).
	TenantMaxConcurrent int
	// Tick is how often due checks are claimed (default DefaultTick).
	Tick time.Duration
	// BatchSize is how many due rows one claim takes (default 4 x MaxConcurrent).
	BatchSize  int
	Log        *slog.Logger
	Now        func() time.Time
	Registerer prometheus.Registerer
}

// Runner claims due checks and runs them. It is an api leader task (internal/app): the leader election makes
// one process the scheduler, and claiming advances next_run_at in the same statement, so a check never runs
// twice even while leadership moves between pods.
type Runner struct {
	store   ScheduleStore
	checker CheckRunner
	sink    ResultSink
	o       RunnerOptions

	global chan struct{}

	mu      sync.Mutex
	tenants map[string]chan struct{}

	cRuns *prometheus.CounterVec
}

// NewRunner creates the scheduler; call Run.
func NewRunner(store ScheduleStore, checker CheckRunner, sink ResultSink, o RunnerOptions) *Runner {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = DefaultMaxConcurrent
	}
	if o.TenantMaxConcurrent <= 0 {
		o.TenantMaxConcurrent = DefaultTenantMaxConcurrent
	}
	if o.TenantMaxConcurrent > o.MaxConcurrent {
		o.TenantMaxConcurrent = o.MaxConcurrent
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
	r := &Runner{store: store, checker: checker, sink: sink, o: o,
		global:  make(chan struct{}, o.MaxConcurrent),
		tenants: map[string]chan struct{}{},
		cRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_synthetic_runs_total",
			Help: "Synthetic check runs by outcome."}, []string{"result"}),
	}
	r.cRuns.WithLabelValues("success")
	r.cRuns.WithLabelValues("failure")
	if o.Registerer != nil {
		o.Registerer.MustRegister(r.cRuns)
	}
	return r
}

func (r *Runner) now() time.Time {
	if r.o.Now != nil {
		return r.o.Now()
	}
	return time.Now()
}

// Run claims and runs due checks until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.o.Tick)
	defer t.Stop()
	for {
		// A full batch means more rows were due than one claim takes (a backlog after a restart or a
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

// RunOnce claims the due checks, runs them and returns how many were claimed.
func (r *Runner) RunOnce(ctx context.Context) int {
	due, err := r.store.Claim(ctx, r.now().UTC(), r.o.BatchSize)
	if err != nil {
		if ctx.Err() == nil {
			r.o.Log.Warn("cannot claim due synthetic checks", "err", err)
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.runOne(ctx, d)
		}()
	}
	wg.Wait()
	return len(due)
}

// runOne runs one claimed check. The tenant slot is taken before the global one, so an organization at its
// own limit waits without occupying a slot other organizations could use.
func (r *Runner) runOne(ctx context.Context, d Due) {
	tenant := r.tenantSem(d.TenantID)
	if !acquire(ctx, tenant) {
		return
	}
	defer func() { <-tenant }()
	if !acquire(ctx, r.global) {
		return
	}
	defer func() { <-r.global }()

	res := r.checker.Run(ctx, d)
	outcome := "failure"
	if res.Success {
		outcome = "success"
	}
	r.cRuns.WithLabelValues(outcome).Inc()
	if !res.Success {
		r.o.Log.Debug("synthetic check failed", "check_id", res.CheckID, "location", res.Location,
			"error_kind", res.ErrorKind, "status_code", res.StatusCode)
	}
	// The last outcome goes to PostgreSQL (the list view's current status), the run itself to ClickHouse.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	if err := r.store.Record(rctx, res); err != nil {
		r.o.Log.Warn("cannot record synthetic check result", "check_id", res.CheckID, "err", err)
	}
	cancel()
	if r.sink != nil {
		r.sink.Add([]Result{res})
	}
}

// tenantSem returns the per-organization semaphore, created on first use. The map holds one small channel
// per organization that ever ran a check in this process.
func (r *Runner) tenantSem(tenant string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	sem, ok := r.tenants[tenant]
	if !ok {
		sem = make(chan struct{}, r.o.TenantMaxConcurrent)
		r.tenants[tenant] = sem
	}
	return sem
}

// acquire takes a slot unless ctx is done first. A done context is checked before the select: both cases of
// a select are ready when the semaphore has room, and the runtime picks one at random, so without this guard
// a shutdown (or a lost leadership) would keep starting runs. The claimed rows are not lost by stopping
// here; their next_run_at has moved on and they run again at their next interval.
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
