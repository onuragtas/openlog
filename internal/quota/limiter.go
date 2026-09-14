package quota

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// IngestStatus is what an ingest pod needs to enforce a tenant's quota (tenant_quota_status).
type IngestStatus struct {
	TenantID           string
	PlanID             string
	Blocked            bool
	IngestBytes        int64
	IngestLimitBytes   int64
	RateBytesPerSecond int64
	BurstBytes         int64
	PeriodStart        time.Time
}

// StatusSource loads quota status for the ingest limiter (PGStore).
type StatusSource interface {
	LoadIngestStatuses(ctx context.Context) (map[string]IngestStatus, error)
	// LiveIngestPods counts running ingest instances (openlog-ingest and openlog-allinone heartbeats).
	LiveIngestPods(ctx context.Context) (int, error)
}

// Decision is the outcome of IngestLimiter.Allow.
type Decision struct {
	Allowed    bool
	Reason     string // quota_exceeded or rate_limited
	RetryAfter time.Duration
	Message    string
}

// Rejection reasons.
const (
	ReasonQuotaExceeded = "quota_exceeded"
	ReasonRateLimited   = "rate_limited"
)

// LimiterOptions configure IngestLimiter. Zero values take the defaults in brackets.
type LimiterOptions struct {
	Refresh time.Duration // status reload period [30s]
	// Pods divides tenant rates among ingest pods; 0 = LiveIngestPods.
	Pods int
	// BlockedRetryAfter is the Retry-After of requests over the monthly quota [5m].
	BlockedRetryAfter time.Duration
	// MaxStale: when the status could not be reloaded for this long, every request is admitted (fail open) [15m].
	MaxStale   time.Duration
	Registerer prometheus.Registerer
	Log        *slog.Logger
	Now        func() time.Time
}

// IngestLimiter enforces quotas in ingest (SaaS mode, D-080): requests of tenants whose monthly ingest quota is used
// up are rejected, and every tenant with a rate limit gets a per-pod token bucket of rate / pods. Status comes from
// PostgreSQL (written by the api leader's evaluator), so pods coordinate without a new dependency; the per-pod split
// is approximate when load is unevenly balanced (docs/operations/saas.md).
type IngestLimiter struct {
	src StatusSource
	o   LimiterOptions

	mu       sync.Mutex
	statuses map[string]IngestStatus
	buckets  map[string]*TokenBucket
	pods     int
	loadedAt time.Time

	rejected *prometheus.CounterVec
	podsG    prometheus.Gauge
}

// NewIngestLimiter creates a limiter; call Run (or Refresh) to load statuses.
func NewIngestLimiter(src StatusSource, o LimiterOptions) *IngestLimiter {
	if o.Refresh <= 0 {
		o.Refresh = 30 * time.Second
	}
	if o.BlockedRetryAfter <= 0 {
		o.BlockedRetryAfter = 5 * time.Minute
	}
	if o.MaxStale <= 0 {
		o.MaxStale = 15 * time.Minute
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	l := &IngestLimiter{
		src: src, o: o, statuses: map[string]IngestStatus{}, buckets: map[string]*TokenBucket{}, pods: max(o.Pods, 1),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_quota_ingest_rejected_total", Help: "Ingest requests rejected by tenant quota enforcement, by reason.",
		}, []string{"reason"}),
		podsG: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_quota_ingest_pods", Help: "Ingest pods the per-tenant ingest rate limits are divided by.",
		}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(l.rejected, l.podsG)
	}
	return l
}

// Refresh reloads the statuses and the pod count.
func (l *IngestLimiter) Refresh(ctx context.Context) error {
	st, err := l.src.LoadIngestStatuses(ctx)
	if err != nil {
		return fmt.Errorf("load quota status: %w", err)
	}
	pods := l.o.Pods
	if pods <= 0 {
		n, err := l.src.LiveIngestPods(ctx)
		if err != nil {
			l.o.Log.Warn("cannot count ingest pods; keeping the previous count", "err", err)
			l.mu.Lock()
			pods = l.pods
			l.mu.Unlock()
		} else {
			pods = n
		}
	}
	pods = max(pods, 1)
	now := l.o.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statuses, l.pods, l.loadedAt = st, pods, now
	l.podsG.Set(float64(pods))
	for t, b := range l.buckets {
		s, ok := st[t]
		if !ok || s.RateBytesPerSecond <= 0 {
			delete(l.buckets, t)
			continue
		}
		b.SetRate(float64(s.RateBytesPerSecond)/float64(pods), float64(s.BurstBytes)/float64(pods), now)
	}
	return nil
}

// Run refreshes every Refresh until ctx is done.
func (l *IngestLimiter) Run(ctx context.Context) {
	failing := false
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := l.Refresh(rctx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				l.o.Log.Warn("quota status refresh failed; enforcing the last known status", "err", err, "max_stale", l.o.MaxStale)
			}
			failing = true
		case err == nil && failing:
			l.o.Log.Info("quota status refresh recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.o.Refresh):
		}
	}
}

// Allow decides whether a request of bytes (uncompressed payload) from tenant is admitted.
func (l *IngestLimiter) Allow(tenant string, bytes int) Decision {
	now := l.o.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loadedAt.IsZero() || now.Sub(l.loadedAt) > l.o.MaxStale {
		return Decision{Allowed: true} // fail open: PostgreSQL unreachable for too long
	}
	s, ok := l.statuses[tenant]
	if !ok {
		return Decision{Allowed: true}
	}
	if s.Blocked {
		l.rejected.WithLabelValues(ReasonQuotaExceeded).Inc()
		return Decision{Reason: ReasonQuotaExceeded, RetryAfter: l.o.BlockedRetryAfter, Message: fmt.Sprintf(
			"monthly ingest quota of plan %q is used up (%.2f of %.2f GiB); upgrade the plan or wait for the next billing period",
			s.PlanID, float64(s.IngestBytes)/GiB, float64(s.IngestLimitBytes)/GiB)}
	}
	if s.RateBytesPerSecond <= 0 {
		return Decision{Allowed: true}
	}
	b := l.buckets[tenant]
	if b == nil {
		b = NewTokenBucket(float64(s.RateBytesPerSecond)/float64(l.pods), float64(s.BurstBytes)/float64(l.pods), now)
		l.buckets[tenant] = b
	}
	ok, wait := b.Take(float64(bytes), now)
	if ok {
		return Decision{Allowed: true}
	}
	l.rejected.WithLabelValues(ReasonRateLimited).Inc()
	wait = max(wait, time.Second)
	return Decision{Reason: ReasonRateLimited, RetryAfter: wait, Message: fmt.Sprintf(
		"ingest rate limit of plan %q exceeded (%d bytes/s); retry after %d s", s.PlanID, s.RateBytesPerSecond, int(math.Ceil(wait.Seconds())))}
}
