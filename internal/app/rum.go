package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/ingest"
	"github.com/onuragtas/openlog/internal/rum"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// startRUMIngest enables POST /v1/rum on the ingest listener (docs/contracts/rum.md §3, D-136) and returns a
// function that flushes the pending last_used_at writes before the PostgreSQL pool closes.
//
// Postgres auth mode only: browser keys live in PostgreSQL, and `static` mode has no key store at all. When
// it is not enabled the endpoint is not registered, so an installation that never creates a browser key
// exposes no unauthenticated-looking public path for anyone to probe.
func startRUMIngest(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, svc *ingest.Service,
	reg prometheus.Registerer, log *slog.Logger) func() {
	if pool == nil || !cfg.RUM.Enabled {
		return func() {}
	}
	keys := rum.NewKeys(postgres.NewStore(pool), rum.CacheOptions{
		// The same cache policy as ingest license keys: a revoked key keeps working for at most
		// OPENLOG_AUTH_CACHE_TTL, and a PostgreSQL outage does not stop accepting known keys.
		TTL: cfg.AuthCache.TTL, NegativeTTL: cfg.AuthCache.NegativeTTL, MaxStale: cfg.AuthCache.MaxStale,
		Hasher: KeyHasher(cfg), Registerer: reg, Log: log,
	})
	svc.SetRUMGeoHeader(cfg.RUM.GeoHeader)
	svc.SetRUM(keys)
	done := make(chan struct{})
	go func() { defer close(done); keys.Run(ctx) }()
	log.Info("real user monitoring ingest enabled", "path", "/v1/rum", "geo_header", cfg.RUM.GeoHeader)
	return func() { <-done }
}
