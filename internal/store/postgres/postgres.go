// Package postgres is openlog's PostgreSQL access layer: connection pool,
// schema migrations (migrations/postgres) and the auth.Store implementation.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configure the connection pool.
type Options struct {
	DSN         string // OPENLOG_POSTGRES_DSN
	Password    string // OPENLOG_POSTGRES_PASSWORD, overrides the DSN password when set
	MaxConns    int32  // OPENLOG_POSTGRES_MAX_CONNS
	Application string // application_name
	// TLS files (OPENLOG_POSTGRES_TLS_CA_FILE / _CERT_FILE / _KEY_FILE) override sslrootcert /
	// sslcert / sslkey of the DSN. sslmode stays in the DSN (use verify-full in production).
	TLSCAFile   string
	TLSCertFile string
	TLSKeyFile  string
}

// Open creates a pool. It does not connect; use WaitReady to block until
// the server is reachable.
func Open(ctx context.Context, o Options) (*pgxpool.Pool, error) {
	if strings.TrimSpace(o.DSN) == "" {
		return nil, errors.New("OPENLOG_POSTGRES_DSN is required when OPENLOG_AUTH_MODE=postgres")
	}
	dsn, err := dsnWithTLSFiles(o.DSN, o)
	if err != nil {
		return nil, fmt.Errorf("OPENLOG_POSTGRES_DSN: %w", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Never echo the DSN: it may contain the password.
		return nil, fmt.Errorf("OPENLOG_POSTGRES_DSN: %w", err)
	}
	if o.Password != "" {
		cfg.ConnConfig.Password = o.Password
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	if o.Application != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = o.Application
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	return pgxpool.NewWithConfig(ctx, cfg)
}

// WaitReady pings until PostgreSQL answers or ctx is done.
func WaitReady(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	backoff := 500 * time.Millisecond
	for {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := pool.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		log.Warn("waiting for postgres", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}
