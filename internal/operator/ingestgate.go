package operator

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// GateSource is what IngestGate loads (Store).
type GateSource interface {
	SuspendedTenants(ctx context.Context) (byTenant, byOrg map[string]string, err error)
	LoadHostLimits(ctx context.Context) (map[string]*HostLimitState, error)
	SaveSourceCounts(ctx context.Context, hour time.Time, instance string, counts map[string]int) error
}

// GateOptions configure IngestGate.
type GateOptions struct {
	Refresh time.Duration // [30s]
	// MaxStale: statuses older than this are not enforced (fail open) [15m].
	MaxStale time.Duration
	// LocalHostTTL is how long a host admitted by this pod counts until it shows up in the known hosts [15m].
	LocalHostTTL time.Duration
	// MaxSourcesPerTenant bounds the client addresses remembered per tenant per hour [5000].
	MaxSourcesPerTenant int
	Instance            string
	Registerer          prometheus.Registerer
	Log                 *slog.Logger
	Now                 func() time.Time
}

type hostGateState struct {
	limit int64
	known map[string]struct{}
	local map[string]time.Time // admitted by this pod, not yet known
}

// IngestGate enforces organization suspension and hard host limits in ingest and counts distinct client addresses
// per tenant (SaaS mode). It implements ingest.Gate.
type IngestGate struct {
	src GateSource
	o   GateOptions

	mu        sync.Mutex
	suspended map[string]string
	hosts     map[string]*hostGateState
	loadedAt  time.Time

	srcMu    sync.Mutex
	srcHour  time.Time
	sources  map[string]map[string]struct{}
	srcDirty map[string]bool

	rejected *prometheus.CounterVec
}

// NewIngestGate creates a gate; call Run (or Refresh) to load.
func NewIngestGate(src GateSource, o GateOptions) *IngestGate {
	if o.Refresh <= 0 {
		o.Refresh = 30 * time.Second
	}
	if o.MaxStale <= 0 {
		o.MaxStale = 15 * time.Minute
	}
	if o.LocalHostTTL <= 0 {
		o.LocalHostTTL = 15 * time.Minute
	}
	if o.MaxSourcesPerTenant <= 0 {
		o.MaxSourcesPerTenant = 5000
	}
	if o.Instance == "" {
		o.Instance, _ = os.Hostname()
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	g := &IngestGate{src: src, o: o, suspended: map[string]string{}, hosts: map[string]*hostGateState{},
		sources: map[string]map[string]struct{}{}, srcDirty: map[string]bool{},
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_saas_ingest_rejected_total", Help: "Ingest requests or resources rejected by organization suspension or host limits, by reason.",
		}, []string{"reason"})}
	if o.Registerer != nil {
		o.Registerer.MustRegister(g.rejected)
	}
	return g
}

// Refresh reloads suspensions and host limits and flushes client address counts.
func (g *IngestGate) Refresh(ctx context.Context) error {
	byTenant, _, err := g.src.SuspendedTenants(ctx)
	if err != nil {
		return err
	}
	limits, err := g.src.LoadHostLimits(ctx)
	if err != nil {
		return err
	}
	now := g.o.Now()
	g.mu.Lock()
	next := make(map[string]*hostGateState, len(limits))
	for t, l := range limits {
		st := &hostGateState{limit: l.Limit, known: l.Known, local: map[string]time.Time{}}
		if prev := g.hosts[t]; prev != nil {
			for h, at := range prev.local {
				if _, ok := l.Known[h]; !ok && now.Sub(at) < g.o.LocalHostTTL {
					st.local[h] = at
				}
			}
		}
		next[t] = st
	}
	g.suspended, g.hosts, g.loadedAt = byTenant, next, now
	g.mu.Unlock()
	return g.flushSources(ctx, now)
}

// Run refreshes every Refresh until ctx is done.
func (g *IngestGate) Run(ctx context.Context) {
	failing := false
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := g.Refresh(rctx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				g.o.Log.Warn("SaaS ingest gate refresh failed; enforcing the last known state", "err", err, "max_stale", g.o.MaxStale)
			}
			failing = true
		case err == nil && failing:
			g.o.Log.Info("SaaS ingest gate refresh recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = g.flushSources(fctx, g.o.Now().Add(time.Hour))
			cancel()
			return
		case <-time.After(g.o.Refresh):
		}
	}
}

func (g *IngestGate) fresh(now time.Time) bool {
	return !g.loadedAt.IsZero() && now.Sub(g.loadedAt) <= g.o.MaxStale
}

// Suspended reports whether the tenant's organization is suspended.
func (g *IngestGate) Suspended(tenantID string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.fresh(g.o.Now()) {
		return "", false
	}
	reason, ok := g.suspended[tenantID]
	if ok {
		g.rejected.WithLabelValues("org_suspended").Inc()
	}
	return reason, ok
}

// RejectHosts returns the host ids of hosts whose data must be rejected and the tenant's host limit.
func (g *IngestGate) RejectHosts(tenantID string, hosts []string) (map[string]bool, int64) {
	if len(hosts) == 0 {
		return nil, 0
	}
	now := g.o.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.hosts[tenantID]
	if st == nil || !g.fresh(now) {
		return nil, 0
	}
	var rejected map[string]bool
	for _, h := range hosts {
		if _, ok := st.known[h]; ok {
			continue
		}
		if _, ok := st.local[h]; ok {
			st.local[h] = now
			continue
		}
		if int64(len(st.known)+len(st.local)) < st.limit {
			st.local[h] = now
			continue
		}
		if rejected == nil {
			rejected = map[string]bool{}
		}
		rejected[h] = true
	}
	if len(rejected) > 0 {
		g.rejected.WithLabelValues("host_limit").Add(float64(len(rejected)))
	}
	return rejected, st.limit
}

// ObserveSource counts the client address of an authenticated request (only counts leave the pod).
func (g *IngestGate) ObserveSource(tenantID, addr string) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "" {
		return
	}
	now := g.o.Now().UTC().Truncate(time.Hour)
	g.srcMu.Lock()
	defer g.srcMu.Unlock()
	if !now.Equal(g.srcHour) && !g.srcHour.IsZero() && now.After(g.srcHour) {
		// A new hour: the previous hour's counts were flushed at least once per refresh; start over.
		g.sources, g.srcDirty = map[string]map[string]struct{}{}, map[string]bool{}
	}
	if now.After(g.srcHour) {
		g.srcHour = now
	}
	set := g.sources[tenantID]
	if set == nil {
		set = map[string]struct{}{}
		g.sources[tenantID] = set
	}
	if _, ok := set[host]; ok || len(set) >= g.o.MaxSourcesPerTenant {
		return
	}
	set[host] = struct{}{}
	g.srcDirty[tenantID] = true
}

func (g *IngestGate) flushSources(ctx context.Context, _ time.Time) error {
	g.srcMu.Lock()
	if len(g.srcDirty) == 0 {
		g.srcMu.Unlock()
		return nil
	}
	counts := make(map[string]int, len(g.srcDirty))
	for t := range g.srcDirty {
		counts[t] = len(g.sources[t])
	}
	hour := g.srcHour
	g.srcDirty = map[string]bool{}
	g.srcMu.Unlock()
	if err := g.src.SaveSourceCounts(ctx, hour, g.o.Instance, counts); err != nil {
		g.srcMu.Lock()
		if g.srcHour.Equal(hour) {
			for t := range counts {
				g.srcDirty[t] = true
			}
		}
		g.srcMu.Unlock()
		return err
	}
	return nil
}

// SaveSourceCounts upserts distinct client address counts of one hour and instance.
func (s Store) SaveSourceCounts(ctx context.Context, hour time.Time, instance string, counts map[string]int) error {
	tenants := make([]string, 0, len(counts))
	ns := make([]int32, 0, len(counts))
	for t, n := range counts {
		tenants, ns = append(tenants, t), append(ns, int32(min(n, 1<<30)))
	}
	if len(instance) > 255 {
		instance = instance[:255]
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO ingest_source_counts (tenant_id, hour, instance, distinct_ips, updated_at)
		SELECT t, $3, $4, n, now() FROM unnest($1::text[], $2::int[]) AS u(t, n)
		ON CONFLICT (tenant_id, hour, instance) DO UPDATE SET distinct_ips = greatest(ingest_source_counts.distinct_ips, EXCLUDED.distinct_ips), updated_at = now()`,
		tenants, ns, hour.UTC(), instance)
	return err
}
