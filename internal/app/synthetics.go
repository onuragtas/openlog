package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/synthetics"
	"github.com/onuragtas/openlog/internal/version"
)

// startSynthetics enables /api/v1/synthetics/* and returns the leader tasks of the scheduled outside-in
// checks (OPENLOG_SYNTHETICS_ENABLED, D-132): the scheduler that runs due checks and the writer that stores
// their results. Postgres auth mode only.
//
// Both are leader tasks, so exactly one pod schedules checks; the claim in PostgreSQL (FOR UPDATE SKIP
// LOCKED plus advancing next_run_at) is what guarantees it even while leadership moves between pods.
func startSynthetics(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, srv *api.Server,
	reg prometheus.Registerer, log *slog.Logger) []leaderTask {
	if pool == nil || !cfg.Synthetics.Enabled {
		return nil
	}
	s := cfg.Synthetics
	store := synthetics.NewPGStore(pool)
	srv.SetSynthetics(store)

	allowPrivate := cfg.SyntheticsAllowPrivateNetworks()
	if allowPrivate && cfg.API.Auth.SignupEnabled {
		log.Warn("OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS=true with sign-up enabled: organization members can make the api request internal addresses")
	}

	// Results are written with direct shard inserts, like the alert evaluation history: the writer is created
	// on the first insert, so the api starts (and checks run) while ClickHouse or its topology is unavailable.
	// It outlives a single leadership term on purpose; a later term reuses the same connections.
	writers := &lazyShardedInserter{bootstrap: conn, log: log.With("job", "synthetics-results"),
		opts: clickhouse.ShardedOptions{Conn: clickhouse.OptionsFromConfig(cfg.Common), Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster}}
	writers.opts.Conn.MaxConns = 2
	writers.runCtx = context.WithoutCancel(ctx)

	writer := synthetics.NewWriter(writers, synthetics.WriterOptions{Log: log.With("job", "synthetics-results"), Registerer: reg})
	checker := synthetics.NewChecker(synthetics.CheckerOptions{
		AllowPrivateNetworks: allowPrivate, MaxResponseBytes: s.MaxResponseBytes, MaxRedirects: s.MaxRedirects,
		UserAgent: "openlog-synthetics/" + version.String(),
	})
	runner := synthetics.NewRunner(store, checker, writer, synthetics.RunnerOptions{
		MaxConcurrent: s.MaxConcurrent, TenantMaxConcurrent: s.TenantMaxConcurrent,
		Log: log.With("job", "synthetics-scheduler"), Registerer: reg,
	})
	log.Info("synthetic monitoring enabled", "max_concurrent", s.MaxConcurrent,
		"tenant_max_concurrent", s.TenantMaxConcurrent, "allow_private_networks", allowPrivate)
	return []leaderTask{
		{"synthetics-scheduler", runner.Run},
		{"synthetics-results", writer.Run},
	}
}
