package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/jobs"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// startJobs enables /api/v1/jobs/* and returns the leader tasks of cron and heartbeat monitoring (D-141):
// the sweeper that concludes the runs nothing reported, and the writer that stores concluded runs.
//
// The ping endpoint is served by every api pod — a crontab must not depend on which pod holds a lock — but
// the sweeper is a leader task, because concluding a missed run is a decision that must be made once. The
// claim in PostgreSQL (FOR UPDATE SKIP LOCKED plus advancing expected_at) is what guarantees that even
// while leadership moves between pods.
func startJobs(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, srv *api.Server,
	reg prometheus.Registerer, log *slog.Logger) []leaderTask {
	if pool == nil || !cfg.Jobs.Enabled {
		return nil
	}
	store := jobs.NewPGStore(pool)

	// Runs are written with direct shard inserts, like the synthetic check results: the writer is created on
	// the first insert, so the api starts (and pings are accepted) while ClickHouse or its topology is
	// unavailable.
	writers := &lazyShardedInserter{bootstrap: conn, log: log.With("job", "job-runs"),
		opts: clickhouse.ShardedOptions{Conn: clickhouse.OptionsFromConfig(cfg.Common), Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster}}
	writers.opts.Conn.MaxConns = 2
	writers.runCtx = context.WithoutCancel(ctx)

	writer := jobs.NewWriter(writers, jobs.WriterOptions{Log: log.With("job", "job-runs"), Registerer: reg})
	srv.SetJobs(store, store, writer)
	// Unlike the synthetics writer, this one runs on **every** api pod, not on the leader: a ping is served
	// by whichever pod the load balancer picked, and a run buffered on a pod that never flushes would be a
	// run that silently disappears from the history.
	go writer.Run(ctx)

	sweeper := jobs.NewSweeper(store, writer, jobs.SweeperOptions{
		Interval: cfg.Jobs.SweepInterval, MaxPerSweep: cfg.Jobs.MaxPerSweep,
		Log: log.With("job", "job-monitor-sweeper"), Registerer: reg,
	})
	log.Info("job monitoring enabled", "sweep_interval", cfg.Jobs.SweepInterval, "max_per_sweep", cfg.Jobs.MaxPerSweep)
	return []leaderTask{{"job-monitor-sweeper", sweeper.Run}}
}
