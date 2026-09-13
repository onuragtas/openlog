package fleet

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
)

// StateLoader loads an organization's fleet state (PGStore.LoadOrgState).
type StateLoader interface {
	LoadOrgState(ctx context.Context, tenantID string) (OrgState, error)
}

// StateCacheOptions configure StateCache. Zero values take the defaults in brackets.
type StateCacheOptions struct {
	TTL         time.Duration // entries are reloaded after this [15s]; the contract caps it at 30s
	MaxStale    time.Duration // serve an entry this long past its last successful load while PostgreSQL fails [5m]
	RetryAfter  time.Duration // while loads fail, retry at most this often per tenant [5s]
	LoadTimeout time.Duration // [2s]
	MaxEntries  int           // [100000]
	Registerer  prometheus.Registerer
	Log         *slog.Logger
	Now         func() time.Time
}

type stateEntry struct {
	state     OrgState
	found     bool
	fetched   time.Time
	refreshAt time.Time
}

// StateCache is the pod-local cache of policies, overrides and current rollouts used by the sync
// handler, following the license key cache pattern (internal/tenant/cache.go): TTL, singleflight
// per tenant, stale entries while PostgreSQL is unavailable.
type StateCache struct {
	src StateLoader
	o   StateCacheOptions
	sf  singleflight.Group

	mu      sync.RWMutex
	entries map[string]stateEntry
	lastErr time.Time

	results *prometheus.CounterVec
}

// NewStateCache creates a cache.
func NewStateCache(src StateLoader, o StateCacheOptions) *StateCache {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.TTL, 15*time.Second)
	def(&o.MaxStale, 5*time.Minute)
	def(&o.RetryAfter, 5*time.Second)
	def(&o.LoadTimeout, 2*time.Second)
	if o.MaxEntries <= 0 {
		o.MaxEntries = 100000
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &StateCache{src: src, o: o, entries: map[string]stateEntry{},
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_fleet_policy_cache_total",
			Help: "Fleet policy cache lookups by result: hit, miss (store load), stale (served while the store fails), unavailable.",
		}, []string{"result"})}
	if o.Registerer != nil {
		o.Registerer.MustRegister(c.results)
	}
	return c
}

// Get returns the state of a tenant's organization. found is false when the tenant has no
// organization; ok is false when the state is unknown (store unavailable, nothing cached).
func (c *StateCache) Get(ctx context.Context, tenantID string) (st OrgState, found, ok bool) {
	c.mu.RLock()
	e, have := c.entries[tenantID]
	c.mu.RUnlock()
	if have && c.o.Now().Before(e.refreshAt) {
		c.results.WithLabelValues("hit").Inc()
		return e.state, e.found, true
	}
	v, err, _ := c.sf.Do(tenantID, func() (any, error) {
		c.mu.RLock()
		cur, have := c.entries[tenantID]
		c.mu.RUnlock()
		if have && c.o.Now().Before(cur.refreshAt) {
			return cur, nil
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.o.LoadTimeout)
		st, err := c.src.LoadOrgState(lctx, tenantID)
		cancel()
		now := c.o.Now()
		switch {
		case err == nil, errors.Is(err, ErrNotFound):
			ne := stateEntry{state: st, found: err == nil, fetched: now, refreshAt: now.Add(c.o.TTL)}
			c.put(tenantID, ne, now)
			c.results.WithLabelValues("miss").Inc()
			return ne, nil
		}
		c.logError(err, now)
		if have && now.Sub(cur.fetched) <= c.o.MaxStale {
			cur.refreshAt = now.Add(c.o.RetryAfter)
			c.put(tenantID, cur, now)
			c.results.WithLabelValues("stale").Inc()
			return cur, nil
		}
		c.results.WithLabelValues("unavailable").Inc()
		return nil, err
	})
	if err != nil {
		return OrgState{}, false, false
	}
	e = v.(stateEntry)
	return e.state, e.found, true
}

func (c *StateCache) logError(err error, now time.Time) {
	c.mu.Lock()
	quiet := now.Sub(c.lastErr) < 10*time.Second
	if !quiet {
		c.lastErr = now
	}
	c.mu.Unlock()
	if !quiet {
		c.o.Log.Warn("cannot load fleet policy; agent updates are paused for uncached organizations", "err", err)
	}
}

func (c *StateCache) put(k string, e stateEntry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.o.MaxEntries {
		for key, old := range c.entries {
			if now.Sub(old.fetched) > c.o.MaxStale {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= c.o.MaxEntries {
			c.entries = map[string]stateEntry{}
		}
	}
	c.entries[k] = e
}
