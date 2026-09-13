package tenant

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
)

// ErrUnavailable is returned when a key is not cached and the key store
// cannot be reached. Ingest answers 503 / UNAVAILABLE so agents retry.
var ErrUnavailable = errors.New("license key store unavailable")

// KeyInfo is a resolved, non-revoked license key.
type KeyInfo struct {
	KeyID    string
	TenantID string
}

// KeyStore is the persistent license key store (PostgreSQL).
type KeyStore interface {
	// LookupLicenseKey returns the active key with SHA-256 hash keyHash, or
	// ErrUnknownKey when it does not exist or is revoked.
	LookupLicenseKey(ctx context.Context, keyHash []byte) (KeyInfo, error)
	// TouchLicenseKeys records that keys were used at "at" (coarsely).
	TouchLicenseKeys(ctx context.Context, keyIDs []string, at time.Time) error
}

// CacheOptions configure Cached. Zero values take the defaults in brackets.
type CacheOptions struct {
	TTL           time.Duration // positive entries are refreshed after this [60s]
	NegativeTTL   time.Duration // unknown keys are re-checked after this [10s]
	MaxStale      time.Duration // serve a known key this long past its last successful lookup while the store fails [15m]
	RetryInterval time.Duration // while the store fails, re-try a stale key at most this often [5s]
	LookupTimeout time.Duration // per store lookup [2s]
	TouchInterval time.Duration // last_used_at flush period [60s]
	MaxEntries    int           // bound on cached keys (memory) [100000]
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
		o.MaxEntries = 100000
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

type entry struct {
	info      KeyInfo
	found     bool
	fetched   time.Time // last successful lookup
	refreshAt time.Time // next lookup attempt
}

// Cached is a Resolver backed by a KeyStore with a local TTL cache. Concurrent
// misses for the same key share one lookup (singleflight). When the store
// fails, keys looked up successfully within MaxStale keep resolving, so
// ingest survives short PostgreSQL outages. A revoked key keeps resolving on
// a pod until its entry expires (TTL).
type Cached struct {
	store KeyStore
	o     CacheOptions
	sf    singleflight.Group

	mu      sync.RWMutex
	entries map[string]entry // key: sha256(license key)

	touchMu sync.Mutex
	touched map[string]struct{}

	results *prometheus.CounterVec
	lastErr time.Time
}

// NewCached creates a caching resolver. Call Run to flush last_used_at updates.
func NewCached(store KeyStore, o CacheOptions) *Cached {
	o.defaults()
	c := &Cached{
		store: store, o: o, entries: map[string]entry{}, touched: map[string]struct{}{},
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_license_key_resolutions_total",
			Help: "License key resolutions by result: hit, miss (store lookup), negative (unknown key), stale (served while the store fails), unavailable.",
		}, []string{"result"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(c.results)
	}
	return c
}

// Resolve implements Resolver.
func (c *Cached) Resolve(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrUnknownKey
	}
	sum := sha256.Sum256([]byte(key))
	k := string(sum[:])

	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	if ok && c.o.Now().Before(e.refreshAt) {
		c.results.WithLabelValues(hitLabel(e)).Inc()
		return c.answer(e)
	}

	v, err, _ := c.sf.Do(k, func() (any, error) {
		c.mu.RLock()
		cur, have := c.entries[k]
		c.mu.RUnlock()
		if have && c.o.Now().Before(cur.refreshAt) {
			return cur, nil
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.o.LookupTimeout)
		info, err := c.store.LookupLicenseKey(lctx, sum[:])
		cancel()
		now := c.o.Now()
		switch {
		case err == nil:
			ne := entry{info: info, found: true, fetched: now, refreshAt: now.Add(c.o.TTL)}
			c.put(k, ne, now)
			c.results.WithLabelValues("miss").Inc()
			return ne, nil
		case errors.Is(err, ErrUnknownKey):
			ne := entry{fetched: now, refreshAt: now.Add(c.o.NegativeTTL)}
			c.put(k, ne, now)
			c.results.WithLabelValues("negative").Inc()
			return ne, nil
		}
		c.logStoreError(err, now)
		if have && now.Sub(cur.fetched) <= c.o.MaxStale {
			cur.refreshAt = now.Add(c.o.RetryInterval)
			c.put(k, cur, now)
			c.results.WithLabelValues("stale").Inc()
			return cur, nil
		}
		c.results.WithLabelValues("unavailable").Inc()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	})
	if err != nil {
		return "", err
	}
	return c.answer(v.(entry))
}

func hitLabel(e entry) string {
	if e.found {
		return "hit"
	}
	return "negative"
}

func (c *Cached) answer(e entry) (string, error) {
	if !e.found {
		return "", ErrUnknownKey
	}
	c.touchMu.Lock()
	if len(c.touched) < c.o.MaxEntries {
		c.touched[e.info.KeyID] = struct{}{}
	}
	c.touchMu.Unlock()
	return e.info.TenantID, nil
}

func (c *Cached) logStoreError(err error, now time.Time) {
	c.mu.Lock()
	quiet := now.Sub(c.lastErr) < 10*time.Second
	if !quiet {
		c.lastErr = now
	}
	c.mu.Unlock()
	if !quiet {
		c.o.Log.Warn("license key lookup failed; serving cached keys", "err", err)
	}
}

// put stores an entry, evicting when the cache is full: first entries that
// can no longer be served, then negative entries, then everything.
func (c *Cached) put(k string, e entry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.o.MaxEntries {
		for key, old := range c.entries {
			if now.Sub(old.fetched) > c.o.MaxStale || (!old.found && now.After(old.refreshAt)) {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= c.o.MaxEntries {
			for key, old := range c.entries {
				if !old.found {
					delete(c.entries, key)
				}
			}
		}
		if len(c.entries) >= c.o.MaxEntries {
			c.entries = map[string]entry{}
		}
	}
	c.entries[k] = e
}

// Len returns the number of cached entries.
func (c *Cached) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// FlushTouches writes pending last_used_at updates. On failure the ids are
// kept for the next flush.
func (c *Cached) FlushTouches(ctx context.Context) error {
	c.touchMu.Lock()
	ids := make([]string, 0, len(c.touched))
	for id := range c.touched {
		ids = append(ids, id)
	}
	c.touched = map[string]struct{}{}
	c.touchMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	if err := c.store.TouchLicenseKeys(ctx, ids, c.o.Now()); err != nil {
		c.touchMu.Lock()
		for _, id := range ids {
			if len(c.touched) < c.o.MaxEntries {
				c.touched[id] = struct{}{}
			}
		}
		c.touchMu.Unlock()
		return err
	}
	return nil
}

// Run flushes last_used_at updates every TouchInterval until ctx is done,
// then flushes once more.
func (c *Cached) Run(ctx context.Context) {
	t := time.NewTicker(c.o.TouchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.FlushTouches(fctx)
			cancel()
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := c.FlushTouches(fctx); err != nil && ctx.Err() == nil {
				c.o.Log.Warn("cannot update license key last_used_at", "err", err)
			}
			cancel()
		}
	}
}
