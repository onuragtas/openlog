//go:build integration

// Integration test of PGQueue against a real PostgreSQL (16+):
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:5432/openlog?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/updatereq
//
// The test creates a throw-away schema with organizations/users stubs and the 0009 migration, and
// drops it afterwards; nothing in the public schema is touched.
package updatereq

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/migrations"
)

func TestPGQueue(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("updatereq_test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mig, err := migrations.Postgres.ReadFile("postgres/0009_update_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE organizations (id uuid PRIMARY KEY)",
		"CREATE TABLE users (id uuid PRIMARY KEY)",
		"CREATE TABLE system_state (key text PRIMARY KEY, value jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())",
		string(mig),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	q := PGQueue{Pool: pool}
	// Age every row instead of waiting (the queue compares with the database clock).
	advance := func(d time.Duration) {
		if _, err := pool.Exec(ctx, `UPDATE update_requests SET requested_at = requested_at - make_interval(secs => $1),
			picked_at = picked_at - make_interval(secs => $1)`, d.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	queueContract(t, q, advance)

	var expired int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM update_requests WHERE state = 'expired'").Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expired = %d, %v", expired, err)
	}
	// Concurrent claims never hand out the same request twice.
	advance(MinGap)
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx, "INSERT INTO update_requests (action) VALUES ('check')"); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	type res struct {
		r   *Request
		err error
	}
	ch := make(chan res, 10)
	for i := 0; i < 10; i++ {
		go func() { r, err := q.Claim(ctx); ch <- res{r, err} }()
	}
	for i := 0; i < 10; i++ {
		got := <-ch
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.r != nil {
			if seen[got.r.ID] {
				t.Fatalf("request %s claimed twice", got.r.ID)
			}
			seen[got.r.ID] = true
		}
	}
	// 5 new + the apply enqueued at the end of the contract.
	if len(seen) != 6 {
		t.Fatalf("claimed %d requests, want 6", len(seen))
	}
	if _, err := pool.Exec(ctx, "SELECT 1 FROM "+schema+".update_requests LIMIT 1"); err != nil {
		t.Fatal(err)
	}
}
