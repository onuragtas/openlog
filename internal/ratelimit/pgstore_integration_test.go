package ratelimit_test

// PostgreSQL integration test of the shared counters (skipped without a database):
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55433/openlog?sslmode=disable \
//	  go test -count=1 -run PG ./internal/ratelimit

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/ratelimit"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

func TestPGStoreSharedLimit(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 20, Application: "openlog-ratelimittest"})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	ms, err := postgres.LoadMigrations(migrations.PostgresFS(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool, ms, log); err != nil {
		t.Fatal(err)
	}
	st := ratelimit.PGStore{Pool: pool}

	// Two "pods" with 8 concurrent workers each hammer one key.
	now := time.Now().UTC().Truncate(time.Minute).Add(20 * time.Second)
	clock := func() time.Time { return now }
	k := ratelimit.NewKey("test", uuid.NewString())
	const limit = 300
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for p := 0; p < 2; p++ {
		l := ratelimit.New(ratelimit.Options{Store: st, Now: clock})
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 60; i++ {
					if l.Allow(ctx, k, limit) {
						allowed.Add(1)
					}
				}
			}()
		}
	}
	wg.Wait()
	// Concurrent synchronous writes are counted conservatively, so slightly fewer than limit may pass.
	got := allowed.Load()
	if got < limit*9/10 || got > limit+limit/8 {
		t.Fatalf("allowed %d of 960 with limit %d across two pods", got, limit)
	}
	counts, err := st.Add(ctx, now.Truncate(time.Minute), time.Minute, map[ratelimit.Key]int{k: 0})
	if err != nil || counts[k].Current < int(got) {
		t.Fatalf("stored count %+v, %v", counts[k], err)
	}

	// The previous bucket is returned and pruning keeps it.
	prevBucket := now.Truncate(time.Minute).Add(-time.Minute)
	old := now.Truncate(time.Minute).Add(-5 * time.Minute)
	if _, err := st.Add(ctx, prevBucket, time.Minute, map[ratelimit.Key]int{k: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(ctx, old, time.Minute, map[ratelimit.Key]int{k: 1}); err != nil {
		t.Fatal(err)
	}
	counts, _ = st.Add(ctx, now.Truncate(time.Minute), time.Minute, map[ratelimit.Key]int{k: 0})
	if counts[k].Previous != 7 {
		t.Fatalf("previous bucket = %+v", counts[k])
	}
	if n, err := st.Prune(ctx, prevBucket); err != nil || n < 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	counts, _ = st.Add(ctx, now.Truncate(time.Minute), time.Minute, map[ratelimit.Key]int{k: 0})
	if counts[k].Previous != 7 || counts[k].Current < int(got) {
		t.Fatalf("prune removed recent buckets: %+v", counts[k])
	}
}
