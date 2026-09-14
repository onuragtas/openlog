package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

// New pool connections use a rotated OPENLOG_POSTGRES_TLS_CA_FILE without a restart (D-048).
func TestPoolReloadsTLSFiles(t *testing.T) {
	old, err := testcerts.Generate(filepath.Join(t.TempDir(), "old"), "db.example")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := testcerts.Generate(filepath.Join(t.TempDir(), "new"), "db.example")
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.crt")
	write := func(from string) {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ca, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(old.CAFile)
	defer func(v time.Duration) { config.ReloadCheckInterval = v }(config.ReloadCheckInterval)
	config.ReloadCheckInterval = 0

	o := Options{TLSCAFile: ca}
	dsn, err := dsnWithTLSFiles("postgres://openlog@db.example/openlog?sslmode=verify-full", o)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadTLSFiles(cfg, dsn, o); err != nil {
		t.Fatal(err)
	}
	verifies := func(caFile string) bool {
		cc := cfg.ConnConfig.Copy()
		if err := cfg.BeforeConnect(context.Background(), cc); err != nil {
			t.Fatal(err)
		}
		pool, err := config.LoadCertPool(caFile)
		if err != nil {
			t.Fatal(err)
		}
		return cc.TLSConfig != nil && cc.TLSConfig.RootCAs.Equal(pool) && cc.TLSConfig.ServerName == "db.example"
	}
	if !verifies(old.CAFile) {
		t.Fatal("initial connection does not use the configured CA")
	}
	write(renewed.CAFile)
	if !verifies(renewed.CAFile) {
		t.Fatal("rotated CA file not used by a new connection")
	}
	if err := reloadTLSFiles(cfg, dsn, Options{}); err != nil {
		t.Errorf("no TLS files: %v", err)
	}
}
