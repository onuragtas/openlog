package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The sweeper is the half of job monitoring that produces facts from silence: every interval it claims the
// monitors whose expected time plus grace has passed, concludes a missed (or overrun) run for each, and
// advances their expectation. It runs on the api leader, like the synthetics scheduler — the claim is
// FOR UPDATE ... SKIP LOCKED, so even if two pods believed they were the leader, a missed run would still
// be concluded once.

// SweeperOptions configure a Sweeper.
type SweeperOptions struct {
	// Interval is how often overdue monitors are looked for (default 30s). It bounds how late an alert can
	// be beyond the grace period, so it is deliberately shorter than the smallest useful grace.
	Interval time.Duration
	// MaxPerSweep bounds the monitors concluded in one pass (default 200); the rest are taken by the next
	// pass a moment later.
	MaxPerSweep int
	Log         *slog.Logger
	Registerer  prometheus.Registerer
}

// Sweeper concludes the runs that never reported.
type Sweeper struct {
	store SweepStore
	sink  RunSink
	o     SweeperOptions

	cRuns   *prometheus.CounterVec
	cErrors prometheus.Counter
	now     func() time.Time
}

// NewSweeper creates a sweeper; call Run.
func NewSweeper(store SweepStore, sink RunSink, o SweeperOptions) *Sweeper {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.MaxPerSweep <= 0 {
		o.MaxPerSweep = 200
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Sweeper{store: store, sink: sink, o: o, now: time.Now,
		cRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_job_monitor_overdue_total",
			Help: "Job monitor runs concluded by the sweeper because nothing reported, by status."}, []string{"status"}),
		cErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "openlog_job_monitor_sweep_errors_total",
			Help: "Failed sweeps of overdue job monitors."}),
	}
	s.cRuns.WithLabelValues(StatusMissed)
	s.cRuns.WithLabelValues(StatusOverrun)
	if o.Registerer != nil {
		o.Registerer.MustRegister(s.cRuns, s.cErrors)
	}
	return s
}

// Run sweeps every Interval until ctx is done.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.o.Interval)
	defer t.Stop()
	for {
		// A sweep at start closes the gap left by a leadership change: the monitors that went overdue while
		// no pod held the lock are concluded now rather than at the next tick.
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep concludes one pass of overdue monitors and returns how many runs it wrote.
func (s *Sweeper) Sweep(ctx context.Context) int {
	total := 0
	// A pass that fills its limit is followed immediately by another, so a backlog (an installation that was
	// down for an hour) drains in seconds instead of one batch per interval.
	for {
		runs, err := s.store.ClaimOverdue(ctx, s.now().UTC(), s.o.MaxPerSweep)
		if err != nil {
			if ctx.Err() == nil {
				s.cErrors.Inc()
				s.o.Log.Warn("cannot claim overdue job monitors", "err", err)
			}
			return total
		}
		if len(runs) == 0 {
			return total
		}
		for _, r := range runs {
			s.cRuns.WithLabelValues(r.Status).Inc()
			s.o.Log.Info("job monitor did not report in time", "monitor", r.Name, "monitor_id", r.MonitorID,
				"status", r.Status, "late_seconds", int(r.LateSeconds))
		}
		s.sink.Add(runs)
		total += len(runs)
		if len(runs) < s.o.MaxPerSweep || ctx.Err() != nil {
			return total
		}
	}
}
