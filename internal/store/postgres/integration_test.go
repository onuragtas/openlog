//go:build integration

// Integration tests against a real PostgreSQL 16.
//
//	go test -tags integration -count=1 ./internal/store/postgres
//
// Without OPENLOG_TEST_POSTGRES_DSN the test starts the compose project
// openlog-pgtest (test/integration/postgres, host port PGTEST_PORT or 55432)
// and removes it afterwards (PGTEST_KEEP=1 keeps it).
package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

var (
	pool *pgxpool.Pool
	log  = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
)

func compose(args ...string) error {
	root, _ := filepath.Abs("../../..")
	cmd := exec.Command("docker", append([]string{"compose", "-p", "openlog-pgtest", "-f",
		filepath.Join(root, "test/integration/postgres/docker-compose.yml")}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %v: %v\n%s", args, err, out.String())
	}
	return nil
}

func TestMain(m *testing.M) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	started := false
	if dsn == "" {
		port := os.Getenv("PGTEST_PORT")
		if port == "" {
			port = "55432"
		}
		if err := compose("up", "-d", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		started = true
		dsn = "postgres://openlog:openlog@127.0.0.1:" + port + "/openlog?sslmode=disable"
	}
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var err error
		pool, err = postgres.Open(ctx, postgres.Options{DSN: dsn, Application: "openlog-pgtest"})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer pool.Close()
		if err := postgres.WaitReady(ctx, pool, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}()
	if started && os.Getenv("PGTEST_KEEP") != "1" {
		if err := compose("down", "-v", "--remove-orphans"); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	os.Exit(code)
}

func migrate(t *testing.T) {
	t.Helper()
	ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(context.Background(), pool, ms, log); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsIdempotentAndConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- postgres.Migrate(context.Background(), pool, ms, log)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != len(ms) {
		t.Fatalf("schema_migrations rows = %d (%v), want %d", n, err, len(ms))
	}
}

func TestServiceConformance(t *testing.T) {
	migrate(t)
	st := postgres.NewStore(pool)
	authtest.Run(t, func(*testing.T) auth.Store { return st })
}

// TestLastOwnerConcurrent demotes two owners concurrently: exactly one may succeed.
func TestLastOwnerConcurrent(t *testing.T) {
	migrate(t)
	st := postgres.NewStore(pool)
	for round := 0; round < 10; round++ {
		e := authtest.NewEnv(t, st, auth.Config{})
		_, owner1 := e.Bootstrap("race")
		owner2 := e.AddUser(owner1, "owner2", auth.RoleOwner)
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i, s := range []*authtest.Session{owner1, owner2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = st.UpdateMemberRole(context.Background(), s.P.OrgID, s.P.UserID, auth.RoleAdmin)
			}()
		}
		wg.Wait()
		failed := 0
		for _, err := range results {
			if errors.Is(err, auth.ErrLastOwner) {
				failed++
			} else if err != nil {
				t.Fatal(err)
			}
		}
		if failed != 1 {
			t.Fatalf("round %d: %d demotions failed, want exactly 1 (%v)", round, failed, results)
		}
	}
}

func TestCleanup(t *testing.T) {
	migrate(t)
	st := postgres.NewStore(pool)
	e := authtest.NewEnv(t, st, auth.Config{SessionTTL: time.Hour})
	_, s := e.Bootstrap("cleanup")
	if err := st.AddLoginFailure(context.Background(), []byte("k"), e.Now()); err != nil {
		t.Fatal(err)
	}
	// The env clock is two days in the past, so both rows are older than a day.
	sessions, failures, err := st.Cleanup(context.Background(), time.Now())
	if err != nil || sessions < 1 || failures < 1 {
		t.Fatalf("cleanup = %d %d %v", sessions, failures, err)
	}
	if _, _, err := st.GetSessionByTokenHash(context.Background(), auth.HashSecret(s.Token)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("expired session still present: %v", err)
	}
}
