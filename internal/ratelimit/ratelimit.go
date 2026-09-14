// Package ratelimit counts requests per key in a sliding window shared by all api pods through PostgreSQL
// (rate_limit_counters, migrations/postgres/0061_rate_limit_counters.sql, D-096). It is used by the public dashboard
// share link endpoints (docs/contracts/api.md "Public share link endpoints").
//
// Counting: requests are counted per (key, bucket), buckets are one window long (a minute). The count of a key is
// estimated as previous bucket × (unelapsed share of the current bucket) + current bucket, the usual sliding window
// approximation.
//
// Sharing: every pod keeps the cluster-wide counts it last read plus its own requests not yet written (pending). A
// request is decided locally while the pod's pending requests stay below 1/Fanout of the remaining headroom
// (limit − known count); otherwise it is written to PostgreSQL synchronously (INSERT … ON CONFLICT DO UPDATE SET
// n = n + excluded.n RETURNING n) and decided on the fresh count. A background flush writes pending counts and reads
// the counts of all recently used keys every SyncInterval. So a key far from its limit costs no database write per
// request, a key near its limit is enforced exactly, and the total overshoot is bounded by the number of pods /
// Fanout × the remaining headroom (none with up to Fanout pods sending bursts of one request each).
//
// Failures: when PostgreSQL cannot be reached the limiter counts locally (per pod) and retries after FailBackoff.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Key identifies a counter: the first 16 bytes of sha256(purpose, subject). Subjects (client IPs, tokens) are never
// stored.
type Key [16]byte

// NewKey derives the key of subject counted for purpose.
func NewKey(purpose, subject string) Key {
	sum := sha256.Sum256([]byte(purpose + "\x00" + subject))
	var k Key
	copy(k[:], sum[:16])
	return k
}

// Counts are the cluster-wide counts of a key in the current and the previous bucket.
type Counts struct {
	Current, Previous int
}

// Store persists the counters.
type Store interface {
	// Add adds the deltas (≥ 0; 0 only reads) to the counters of bucket and returns the counts of bucket (after the
	// update) and of the bucket before it (bucket − window) for every key of deltas.
	Add(ctx context.Context, bucket time.Time, window time.Duration, deltas map[Key]int) (map[Key]Counts, error)
	// Prune deletes the buckets that start before before and returns how many rows were deleted.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// Options configure a Limiter. Zero values use the defaults.
type Options struct {
	Store        Store         // nil: process-local counting only
	Window       time.Duration // bucket and window length (1 minute)
	SyncInterval time.Duration // background flush interval (1 s)
	SyncTimeout  time.Duration // timeout of one store call (1 s)
	FailBackoff  time.Duration // local-only counting after a store error (5 s)
	Fanout       int           // share of the headroom one pod may use without writing (8)
	MaxKeys      int           // keys kept in memory; least recently used beyond it are dropped at flush (100000)
	Log          *slog.Logger
	Now          func() time.Time
}

// Limiter is a cluster-wide sliding window rate limiter. The zero value is not usable; use New.
type Limiter struct {
	o           Options
	mu          sync.Mutex
	keys        map[Key]*entry
	failedUntil time.Time
	lastFlush   time.Time
}

type entry struct {
	bucket    time.Time // start of the bucket cur and pending belong to
	cur, prev int       // cluster-wide counts known to this pod (own written requests included)
	pending   int       // own requests not written yet
	inflight  int       // own requests being written by a synchronous Allow
	synced    bool      // cur/prev were read from the store at least once
	used      time.Time
}

// New creates a limiter.
func New(o Options) *Limiter {
	if o.Window <= 0 {
		o.Window = time.Minute
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = time.Second
	}
	if o.SyncTimeout <= 0 {
		o.SyncTimeout = time.Second
	}
	if o.FailBackoff <= 0 {
		o.FailBackoff = 5 * time.Second
	}
	if o.Fanout <= 0 {
		o.Fanout = 8
	}
	if o.MaxKeys <= 0 {
		o.MaxKeys = 100000
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Limiter{o: o, keys: map[Key]*entry{}}
}

// Shared reports whether counts are shared through a store.
func (l *Limiter) Shared() bool { return l.o.Store != nil }

func (l *Limiter) bucketOf(t time.Time) time.Time { return t.UTC().Truncate(l.o.Window) }

// entry returns the entry of k rolled to the bucket of now (l.mu held).
func (l *Limiter) entry(k Key, now time.Time) *entry {
	b := l.bucketOf(now)
	e, ok := l.keys[k]
	if !ok {
		e = &entry{bucket: b}
		l.keys[k] = e
	}
	if !e.bucket.Equal(b) {
		if e.bucket.Add(l.o.Window).Equal(b) {
			e.prev = e.cur + e.pending // pending of the finished bucket are only known locally
		} else {
			e.prev = 0
		}
		e.cur, e.pending, e.inflight, e.bucket, e.synced = 0, 0, 0, b, e.synced && e.bucket.Add(l.o.Window).Equal(b)
	}
	e.used = now
	return e
}

// known is the estimated window count from the store's view (pending excluded).
func (l *Limiter) known(e *entry, now time.Time) float64 {
	frac := float64(now.Sub(e.bucket)) / float64(l.o.Window)
	return float64(e.prev)*(1-frac) + float64(e.cur)
}

// Allow counts one request of k and reports whether it is within limit requests per window.
func (l *Limiter) Allow(ctx context.Context, k Key, limit int) bool {
	now := l.o.Now()
	l.mu.Lock()
	e := l.entry(k, now)
	known := l.known(e, now)
	unsent := e.pending + e.inflight
	if known+float64(unsent)+1 > float64(limit) {
		l.mu.Unlock()
		return false
	}
	local := l.o.Store == nil || now.Before(l.failedUntil)
	if local || (e.synced && float64((unsent+1)*l.o.Fanout) <= float64(limit)-known) {
		e.pending++
		l.mu.Unlock()
		return true
	}
	delta, bucket := e.pending+1, e.bucket
	e.pending = 0
	e.inflight += delta
	l.mu.Unlock()

	counts, err := l.add(ctx, bucket, map[Key]int{k: delta})
	l.mu.Lock()
	defer l.mu.Unlock()
	e = l.entry(k, now)
	same := e.bucket.Equal(bucket)
	if same {
		e.inflight -= delta
	}
	if err != nil {
		if same {
			e.pending += delta
		}
		return true // fail open to local counting (the request was counted locally)
	}
	if same {
		l.apply(e, counts[k])
	}
	return l.known(e, now)+float64(e.pending+e.inflight) <= float64(limit)
}

// Exceeded reports whether k already reached limit in the window, without counting. It uses the counts known to this
// pod (refreshed by the background flush).
func (l *Limiter) Exceeded(k Key, limit int) bool {
	now := l.o.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(k, now)
	return l.known(e, now)+float64(e.pending+e.inflight) >= float64(limit)
}

// apply merges counts read from the store (responses of concurrent calls may arrive out of order: counts only grow).
func (l *Limiter) apply(e *entry, c Counts) {
	e.cur, e.synced = max(e.cur, c.Current), true
	if c.Previous > e.prev {
		e.prev = c.Previous
	}
}

func (l *Limiter) add(ctx context.Context, bucket time.Time, deltas map[Key]int) (map[Key]Counts, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.o.SyncTimeout)
	defer cancel()
	counts, err := l.o.Store.Add(cctx, bucket, l.o.Window, deltas)
	if err != nil {
		l.mu.Lock()
		first := !l.o.Now().Before(l.failedUntil)
		l.failedUntil = l.o.Now().Add(l.o.FailBackoff)
		l.mu.Unlock()
		if first {
			l.o.Log.Warn("rate limit counters unavailable: counting per pod", "err", err)
		}
	}
	return counts, err
}

// maxBatch is the number of keys written in one statement.
const maxBatch = 1000

// Flush writes the pending counts and reads the counts of the keys used since the previous flush; it also drops keys
// unused for two windows. Run calls it every SyncInterval.
func (l *Limiter) Flush(ctx context.Context) {
	if l.o.Store == nil {
		l.evict(l.o.Now())
		return
	}
	now := l.o.Now()
	bucket := l.bucketOf(now)
	l.mu.Lock()
	since := l.lastFlush
	l.lastFlush = now
	deltas := map[Key]int{}
	for k, e := range l.keys {
		if e.used.Before(since) && e.pending == 0 {
			continue
		}
		e = l.entry(k, e.used) // no roll beyond the entry's last use
		if !e.bucket.Equal(bucket) {
			continue // used in an older bucket only: nothing to write for the current one
		}
		deltas[k] = e.pending
		e.pending = 0
	}
	l.mu.Unlock()
	l.evict(now)
	if len(deltas) == 0 {
		return
	}
	keys := make([]Key, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	for start := 0; start < len(keys); start += maxBatch {
		batch := map[Key]int{}
		for _, k := range keys[start:min(start+maxBatch, len(keys))] {
			batch[k] = deltas[k]
		}
		counts, err := l.add(ctx, bucket, batch)
		l.mu.Lock()
		for k, d := range batch {
			e, ok := l.keys[k]
			if !ok || !e.bucket.Equal(bucket) {
				continue
			}
			if err != nil {
				e.pending += d
				continue
			}
			l.apply(e, counts[k])
		}
		l.mu.Unlock()
	}
}

func (l *Limiter) evict(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, e := range l.keys {
		if now.Sub(e.used) >= 2*l.o.Window {
			delete(l.keys, k)
		}
	}
	if over := len(l.keys) - l.o.MaxKeys; over > 0 {
		type ku struct {
			k Key
			t time.Time
		}
		all := make([]ku, 0, len(l.keys))
		for k, e := range l.keys {
			all = append(all, ku{k, e.used})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, x := range all[:over] {
			delete(l.keys, x.k)
		}
	}
}

// Run flushes every SyncInterval until ctx is done (final flush included).
func (l *Limiter) Run(ctx context.Context) {
	t := time.NewTicker(l.o.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			l.Flush(context.WithoutCancel(ctx))
			return
		case <-t.C:
			l.Flush(ctx)
		}
	}
}

// PruneJob returns a job (api leader) that deletes buckets older than two windows every window.
func PruneJob(st Store, window time.Duration, log *slog.Logger) func(ctx context.Context) {
	if window <= 0 {
		window = time.Minute
	}
	return func(ctx context.Context) {
		t := time.NewTicker(window)
		defer t.Stop()
		for {
			if n, err := st.Prune(ctx, time.Now().UTC().Truncate(window).Add(-window)); err != nil && ctx.Err() == nil {
				log.Warn("cannot prune rate limit counters", "err", err)
			} else if n > 0 {
				log.Debug("pruned rate limit counters", "rows", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
}
