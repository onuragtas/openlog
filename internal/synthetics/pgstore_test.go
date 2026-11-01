package synthetics_test

// What Validate accepts has to be what the table accepts, because a row the table refuses is an internal error for someone
// filling in a valid form (skipped without a database):
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55433/openlog?sslmode=disable \
//	  go test -count=1 -run PG ./internal/synthetics

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/synthetics"
	"github.com/onuragtas/openlog/migrations"
)

func TestPGStoreAcceptsWhatValidateAccepts(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 4, Application: "openlog-synthetics-test"})
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
	var orgID string
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name, tenant_id) VALUES ($1, $1) RETURNING id::text`, "syn-"+uuid.NewString()).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	store := synthetics.NewPGStore(pool)
	for name, in := range map[string]synthetics.Input{
		"http":              {Name: "http", Type: synthetics.TypeHTTP, URL: "https://example.com"},
		"tcp":               {Name: "tcp", Type: synthetics.TypeTCP, Target: "example.com:443"},
		"tls":               {Name: "tls", Type: synthetics.TypeTLS, Target: "example.com:443"},
		"dns":               {Name: "dns", Type: synthetics.TypeDNS, Target: "example.com", DNSRecordType: "A"},
		"shortest interval": {Name: "fast", Type: synthetics.TypeTCP, Target: "example.com:443", IntervalSeconds: synthetics.MinIntervalSecs, TimeoutMs: synthetics.MinTimeoutMs},
		"longest interval":  {Name: "slow", Type: synthetics.TypeHTTP, URL: "https://example.com", IntervalSeconds: synthetics.MaxIntervalSecs, TimeoutMs: synthetics.MaxTimeoutMs},
	} {
		t.Run(name, func(t *testing.T) {
			in.Enabled = true
			if err := in.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			created, err := store.Create(ctx, orgID, in, synthetics.Actor{})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if created.Type != in.Type || created.Target != in.Target {
				t.Errorf("stored %+v", created)
			}
			if _, err := store.Update(ctx, orgID, created.ID, in, synthetics.Actor{}); err != nil {
				t.Errorf("update: %v", err)
			}
		})
	}
}
