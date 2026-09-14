package tenant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeKeys struct {
	mu       sync.Mutex
	keys     map[string]KeyInfo
	err      error
	touchErr error
	lookups  int
	touched  []string
	block    chan struct{}
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return string(h[:])
}

func newFake() *fakeKeys { return &fakeKeys{keys: map[string]KeyInfo{}} }

func (f *fakeKeys) set(key, id, tenant string) {
	f.mu.Lock()
	f.keys[hashKey(key)] = KeyInfo{KeyID: id, TenantID: tenant}
	f.mu.Unlock()
}

func (f *fakeKeys) revoke(key string) {
	f.mu.Lock()
	delete(f.keys, hashKey(key))
	f.mu.Unlock()
}

func (f *fakeKeys) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeKeys) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups
}

func (f *fakeKeys) LookupLicenseKey(_ context.Context, hs [][]byte) (KeyInfo, error) {
	f.mu.Lock()
	f.lookups++
	block, err := f.block, f.err
	var info KeyInfo
	ok := false
	for _, h := range hs {
		if info, ok = f.keys[string(h)]; ok {
			break
		}
	}
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return KeyInfo{}, err
	}
	if !ok {
		return KeyInfo{}, ErrUnknownKey
	}
	return info, nil
}

func (f *fakeKeys) TouchLicenseKeys(_ context.Context, ids []string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return f.touchErr
	}
	f.touched = append(f.touched, ids...)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newCache(f *fakeKeys, o CacheOptions) (*Cached, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	o.Now = c.Now
	return NewCached(f, o), c
}

var ctx = context.Background()

func TestCachedPositiveTTLAndRevocation(t *testing.T) {
	f := newFake()
	f.set("k1", "id1", "tenant-1")
	c, clk := newCache(f, CacheOptions{TTL: time.Minute, NegativeTTL: 10 * time.Second})
	for i := 0; i < 5; i++ {
		if got, err := c.Resolve(ctx, "k1"); err != nil || got != "tenant-1" {
			t.Fatalf("Resolve = %q, %v", got, err)
		}
	}
	if f.count() != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", f.count())
	}
	// Revoked in the store: still accepted until the entry expires.
	f.revoke("k1")
	clk.Advance(59 * time.Second)
	if got, err := c.Resolve(ctx, "k1"); err != nil || got != "tenant-1" {
		t.Fatalf("within TTL: %q, %v", got, err)
	}
	clk.Advance(2 * time.Second)
	if _, err := c.Resolve(ctx, "k1"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("after TTL: err = %v, want ErrUnknownKey", err)
	}
	if f.count() != 2 {
		t.Errorf("lookups = %d, want 2", f.count())
	}
}

func TestCachedNegative(t *testing.T) {
	f := newFake()
	c, clk := newCache(f, CacheOptions{TTL: time.Minute, NegativeTTL: 10 * time.Second})
	for i := 0; i < 3; i++ {
		if _, err := c.Resolve(ctx, "nope"); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("err = %v", err)
		}
	}
	if f.count() != 1 {
		t.Fatalf("lookups = %d, want 1", f.count())
	}
	// A key created meanwhile becomes valid after the negative TTL.
	f.set("nope", "id", "t")
	clk.Advance(11 * time.Second)
	if got, err := c.Resolve(ctx, "nope"); err != nil || got != "t" {
		t.Fatalf("after negative TTL: %q %v", got, err)
	}
	if _, err := c.Resolve(ctx, ""); !errors.Is(err, ErrUnknownKey) || f.count() != 2 {
		t.Fatalf("empty key: %v lookups=%d", err, f.count())
	}
}

func TestCachedServesStaleDuringOutage(t *testing.T) {
	f := newFake()
	f.set("k1", "id1", "tenant-1")
	c, clk := newCache(f, CacheOptions{TTL: time.Minute, MaxStale: 10 * time.Minute, RetryInterval: 5 * time.Second})
	if _, err := c.Resolve(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	f.setErr(errors.New("connection refused"))
	clk.Advance(2 * time.Minute)
	for i := 0; i < 10; i++ {
		if got, err := c.Resolve(ctx, "k1"); err != nil || got != "tenant-1" {
			t.Fatalf("stale resolve = %q, %v", got, err)
		}
	}
	if f.count() != 2 {
		t.Fatalf("lookups during outage = %d, want 2 (retries throttled)", f.count())
	}
	clk.Advance(6 * time.Second)
	_, _ = c.Resolve(ctx, "k1")
	if f.count() != 3 {
		t.Fatalf("lookups after retry interval = %d, want 3", f.count())
	}
	// Unknown keys cannot be served during the outage: retryable error.
	if _, err := c.Resolve(ctx, "other"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncached key during outage: %v, want ErrUnavailable", err)
	}
	// Beyond MaxStale the known key is no longer served.
	clk.Advance(10 * time.Minute)
	if _, err := c.Resolve(ctx, "k1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("beyond max stale: %v, want ErrUnavailable", err)
	}
	// Recovery.
	f.setErr(nil)
	if got, err := c.Resolve(ctx, "k1"); err != nil || got != "tenant-1" {
		t.Fatalf("after recovery: %q %v", got, err)
	}
}

func TestCachedSingleflight(t *testing.T) {
	f := newFake()
	f.set("k1", "id1", "tenant-1")
	f.block = make(chan struct{})
	c, _ := newCache(f, CacheOptions{})
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := c.Resolve(ctx, "k1"); err != nil || got != "tenant-1" {
				errs <- fmt.Errorf("%q %v", got, err)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(f.block)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if f.count() != 1 {
		t.Fatalf("lookups = %d, want 1 (singleflight)", f.count())
	}
}

func TestCachedTouches(t *testing.T) {
	f := newFake()
	f.set("k1", "id1", "t1")
	f.set("k2", "id2", "t2")
	c, _ := newCache(f, CacheOptions{})
	_, _ = c.Resolve(ctx, "k1")
	_, _ = c.Resolve(ctx, "k1")
	_, _ = c.Resolve(ctx, "k2")
	_, _ = c.Resolve(ctx, "unknown")
	f.touchErr = errors.New("down")
	if err := c.FlushTouches(ctx); err == nil {
		t.Fatal("flush error not reported")
	}
	f.touchErr = nil
	if err := c.FlushTouches(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.touched) != 2 {
		t.Fatalf("touched = %v, want id1,id2 once (kept after failure)", f.touched)
	}
	if err := c.FlushTouches(ctx); err != nil || len(f.touched) != 2 {
		t.Fatalf("second flush rewrote: %v", f.touched)
	}
}

func TestCachedBoundedSize(t *testing.T) {
	f := newFake()
	f.set("good", "id", "t")
	c, _ := newCache(f, CacheOptions{MaxEntries: 10})
	_, _ = c.Resolve(ctx, "good")
	for i := 0; i < 1000; i++ {
		_, _ = c.Resolve(ctx, fmt.Sprintf("random-%d", i))
	}
	if c.Len() > 10 {
		t.Fatalf("cache grew to %d entries", c.Len())
	}
}
