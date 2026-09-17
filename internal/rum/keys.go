package rum

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"

	"github.com/onuragtas/openlog/internal/tenant"
)

// Browser key resolution in ingest (rum.md §3). This mirrors tenant.Cached — a bounded in-memory cache with
// a TTL, a negative TTL, singleflight and stale-while-PostgreSQL-is-down — because a browser key is resolved
// on exactly the same hot path and must survive the same outages. It is a separate cache rather than a reuse
// of tenant.Cached because a browser key resolves to *more* than a tenant: the application name it is forced
// to write under, the origins it may be used from, and its own rate limit.

// PrefixBrowserKey is the visible prefix of a generated browser key, so a leaked value is recognizable and is
// never confused with an ingest license key (olk_) or an API key (ola_).
const PrefixBrowserKey = "olb_"

// Errors returned by Keys.Resolve.
var (
	// ErrUnknownKey is an unknown or revoked browser key.
	ErrUnknownKey = errors.New("unknown or missing browser key")
	// ErrOriginNotAllowed is a known key used from an origin outside its allowlist.
	ErrOriginNotAllowed = errors.New("origin is not allowed for this browser key")
	// ErrUnavailable is returned when the key is not cached and the key store cannot be reached.
	ErrUnavailable = errors.New("browser key store unavailable")
)

// Key is a resolved, non-revoked browser key.
type Key struct {
	KeyID    string
	TenantID string
	// ServiceName and Environment are forced onto every payload (payload.go): a copied key cannot write
	// telemetry under the name of a service a backend agent owns.
	ServiceName string
	Environment string
	Origins     []string
	// RateLimitPerMinute bounds the RUM events this key may produce, per ingest pod (§3.4).
	RateLimitPerMinute int
	// SampleRate is the share of sessions the SDK is told to keep. The weight of every stored span is
	// derived from it server-side, so a page cannot inflate its own traffic by claiming a sample rate.
	SampleRate float64
}

// Store is the persistent browser key store (PostgreSQL).
type Store interface {
	// LookupBrowserKey returns the active key whose stored hash is one of hashes (KeyHasher.Candidates,
	// current format first), or ErrUnknownKey when it does not exist or is revoked.
	LookupBrowserKey(ctx context.Context, hashes [][]byte) (Key, error)
	// TouchBrowserKeys records that keys were used at "at" (coarsely).
	TouchBrowserKeys(ctx context.Context, keyIDs []string, at time.Time) error
}

// CacheOptions configure Keys. Zero values take the defaults in brackets.
type CacheOptions struct {
	TTL           time.Duration // positive entries are refreshed after this [60s]
	NegativeTTL   time.Duration // unknown keys are re-checked after this [10s]
	MaxStale      time.Duration // serve a known key this long past its last lookup while the store fails [15m]
	RetryInterval time.Duration // while the store fails, re-try a stale key at most this often [5s]
	LookupTimeout time.Duration // per store lookup [2s]
	TouchInterval time.Duration // last_used_at flush period [60s]
	MaxEntries    int           // bound on cached keys [10000]
	Hasher        *tenant.KeyHasher
	Registerer    prometheus.Registerer
	Log           *slog.Logger
	Now           func() time.Time
}

func (o *CacheOptions) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.TTL, 60*time.Second)
	def(&o.NegativeTTL, 10*time.Second)
	def(&o.MaxStale, 15*time.Minute)
	def(&o.RetryInterval, 5*time.Second)
	def(&o.LookupTimeout, 2*time.Second)
	def(&o.TouchInterval, 60*time.Second)
	if o.MaxEntries <= 0 {
		// Browser keys are per application, not per machine: an installation has tens, not 100 000.
		o.MaxEntries = 10000
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

type entry struct {
	key       Key
	found     bool
	fetched   time.Time
	refreshAt time.Time
}

// Keys resolves browser keys and enforces their per-key rate limit.
type Keys struct {
	store Store
	o     CacheOptions
	sf    singleflight.Group

	mu      sync.RWMutex
	entries map[string]entry // key: sha256(browser key)
	buckets map[string]*bucket

	touchMu sync.Mutex
	touched map[string]struct{}

	results  *prometheus.CounterVec
	rejected *prometheus.CounterVec
	lastErr  time.Time
}

// NewKeys creates a resolver. Call Run to flush last_used_at updates.
func NewKeys(store Store, o CacheOptions) *Keys {
	o.defaults()
	k := &Keys{
		store: store, o: o, entries: map[string]entry{}, buckets: map[string]*bucket{},
		touched: map[string]struct{}{},
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_rum_key_resolutions_total",
			Help: "Browser key resolutions by result: hit, miss, negative, stale, unavailable, origin_denied.",
		}, []string{"result"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_rum_rejected_events_total",
			Help: "RUM events rejected before storage, by reason.",
		}, []string{"reason"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(k.results, k.rejected)
	}
	return k
}

// Rejected counts events dropped for reason (payload validation and rate limiting).
func (k *Keys) Rejected(reason string, n int) {
	if n > 0 {
		k.rejected.WithLabelValues(reason).Add(float64(n))
	}
}

// Resolve returns the key for value, checking that origin is on its allowlist. An empty origin is refused:
// every browser sends one on a cross-origin POST, so a request without it is not the browser traffic this
// endpoint exists for.
func (k *Keys) Resolve(ctx context.Context, value, origin string) (Key, error) {
	if value == "" {
		return Key{}, ErrUnknownKey
	}
	sum := sha256.Sum256([]byte(value))
	ck := string(sum[:])

	k.mu.RLock()
	e, ok := k.entries[ck]
	k.mu.RUnlock()
	if ok && k.o.Now().Before(e.refreshAt) {
		k.results.WithLabelValues(hitLabel(e)).Inc()
		return k.answer(e, origin)
	}

	v, err, _ := k.sf.Do(ck, func() (any, error) {
		k.mu.RLock()
		cur, have := k.entries[ck]
		k.mu.RUnlock()
		if have && k.o.Now().Before(cur.refreshAt) {
			return cur, nil
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k.o.LookupTimeout)
		found, err := k.store.LookupBrowserKey(lctx, k.o.Hasher.Candidates(value))
		cancel()
		now := k.o.Now()
		switch {
		case err == nil:
			ne := entry{key: found, found: true, fetched: now, refreshAt: now.Add(k.o.TTL)}
			k.put(ck, ne, now)
			k.results.WithLabelValues("miss").Inc()
			return ne, nil
		case errors.Is(err, ErrUnknownKey):
			ne := entry{fetched: now, refreshAt: now.Add(k.o.NegativeTTL)}
			k.put(ck, ne, now)
			k.results.WithLabelValues("negative").Inc()
			return ne, nil
		}
		k.logStoreError(err, now)
		if have && now.Sub(cur.fetched) <= k.o.MaxStale {
			cur.refreshAt = now.Add(k.o.RetryInterval)
			k.put(ck, cur, now)
			k.results.WithLabelValues("stale").Inc()
			return cur, nil
		}
		k.results.WithLabelValues("unavailable").Inc()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	})
	if err != nil {
		return Key{}, err
	}
	return k.answer(v.(entry), origin)
}

func hitLabel(e entry) string {
	if e.found {
		return "hit"
	}
	return "negative"
}

func (k *Keys) answer(e entry, origin string) (Key, error) {
	if !e.found {
		return Key{}, ErrUnknownKey
	}
	if !OriginAllowed(e.key.Origins, origin) {
		k.results.WithLabelValues("origin_denied").Inc()
		return Key{}, ErrOriginNotAllowed
	}
	k.touchMu.Lock()
	if len(k.touched) < k.o.MaxEntries {
		k.touched[e.key.KeyID] = struct{}{}
	}
	k.touchMu.Unlock()
	return e.key, nil
}

// Allow admits n RUM events for the key and reports how long to wait when it does not. The budget is a token
// bucket per key **per ingest pod**: a cluster-wide counter would mean a PostgreSQL round trip on a path that
// exists to absorb traffic from the open internet. The consequence is stated rather than hidden — with p
// pods the effective ceiling is p × the configured limit (rum.md §3.4).
func (k *Keys) Allow(key Key, n int, now time.Time) (bool, time.Duration) {
	if key.RateLimitPerMinute <= 0 || key.KeyID == "" {
		return true, 0
	}
	rate := float64(key.RateLimitPerMinute) / 60
	k.mu.Lock()
	b, ok := k.buckets[key.KeyID]
	if !ok {
		if len(k.buckets) >= k.o.MaxEntries {
			k.buckets = map[string]*bucket{}
		}
		// Burst = one minute of budget, so a page that loads a hundred events at once is not punished for
		// arriving in one request while a sustained flood still converges on the configured rate.
		b = newBucket(rate, float64(key.RateLimitPerMinute), now)
		k.buckets[key.KeyID] = b
	} else {
		b.setRate(rate, float64(key.RateLimitPerMinute), now)
	}
	k.mu.Unlock()
	ok, wait := b.take(float64(n), now)
	if !ok {
		k.Rejected("rate_limited", n)
	}
	return ok, wait
}

func (k *Keys) logStoreError(err error, now time.Time) {
	k.mu.Lock()
	quiet := now.Sub(k.lastErr) < 10*time.Second
	if !quiet {
		k.lastErr = now
	}
	k.mu.Unlock()
	if !quiet {
		k.o.Log.Warn("browser key lookup failed; serving cached keys", "err", err)
	}
}

// put stores an entry, evicting negative entries first when the cache is full.
func (k *Keys) put(ck string, e entry, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.entries[ck]; !exists && len(k.entries) >= k.o.MaxEntries {
		for key, old := range k.entries {
			if now.Sub(old.fetched) > k.o.MaxStale || (!old.found && now.After(old.refreshAt)) {
				delete(k.entries, key)
			}
		}
		if len(k.entries) >= k.o.MaxEntries {
			k.entries = map[string]entry{}
		}
	}
	k.entries[ck] = e
}

// Len returns the number of cached entries.
func (k *Keys) Len() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.entries)
}

// FlushTouches writes pending last_used_at updates; failures keep the ids for the next flush.
func (k *Keys) FlushTouches(ctx context.Context) error {
	k.touchMu.Lock()
	ids := make([]string, 0, len(k.touched))
	for id := range k.touched {
		ids = append(ids, id)
	}
	k.touched = map[string]struct{}{}
	k.touchMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	if err := k.store.TouchBrowserKeys(ctx, ids, k.o.Now()); err != nil {
		k.touchMu.Lock()
		for _, id := range ids {
			if len(k.touched) < k.o.MaxEntries {
				k.touched[id] = struct{}{}
			}
		}
		k.touchMu.Unlock()
		return err
	}
	return nil
}

// Run flushes last_used_at updates until ctx is done, then flushes once more.
func (k *Keys) Run(ctx context.Context) {
	t := time.NewTicker(k.o.TouchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = k.FlushTouches(fctx)
			cancel()
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := k.FlushTouches(fctx); err != nil && ctx.Err() == nil {
				k.o.Log.Warn("cannot update browser key last_used_at", "err", err)
			}
			cancel()
		}
	}
}
