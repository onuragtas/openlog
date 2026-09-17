package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/cloudconnect"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// startCloudConnect enables /api/v1/cloud/* and returns the leader tasks of the managed cloud service
// metrics (OPENLOG_CLOUD_ENABLED, D-135): the poller that collects due scopes and the writer that stores
// their data points. Postgres auth mode only.
//
// Both are leader tasks, so exactly one pod polls; the claim in PostgreSQL (FOR UPDATE SKIP LOCKED plus
// advancing next_run_at) is what guarantees it even while leadership moves between pods. That matters more
// here than for synthetics: a duplicate poll is a duplicate bill at the provider.
func startCloudConnect(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn,
	srv *api.Server, reg prometheus.Registerer, log *slog.Logger) ([]leaderTask, error) {
	if pool == nil || !cfg.CloudConnect.Enabled {
		return nil, nil
	}
	c := cfg.CloudConnect
	kr, err := alertKeyring(cfg)
	if err != nil {
		return nil, err
	}
	store := cloudconnect.NewPGStore(pool)

	// Data points are written with direct shard inserts, like the synthetic run results: the writer is
	// created on the first insert, so the api starts (and connections poll) while ClickHouse or its topology
	// is unavailable. It outlives a single leadership term on purpose.
	writers := &lazyShardedInserter{bootstrap: conn, log: log.With("job", "cloud-metrics"),
		opts: clickhouse.ShardedOptions{Conn: clickhouse.OptionsFromConfig(cfg.Common), Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster}}
	writers.opts.Conn.MaxConns = 2
	writers.runCtx = context.WithoutCancel(ctx)

	writer := cloudconnect.NewWriter(writers, cloudconnect.WriterOptions{
		Log: log.With("job", "cloud-metrics"), Registerer: reg})
	collector := cloudconnect.NewCollector(writer, cloudconnect.CollectorOptions{
		Keys: kr, Log: log.With("job", "cloud-poller"),
		ProviderOptions: cloudconnect.ProviderOptions{HTTP: &http.Client{Timeout: c.RequestTimeout}},
	})
	// The management API shares the collector, so "test connection" uses exactly the code path a poll uses.
	srv.SetCloudConnect(cloudconnect.NewManager(store, cloudconnect.ManagerOptions{
		Keys: kr, Tester: collector, Log: log}))

	if !kr.Configured() {
		log.Warn("cloud connections: OPENLOG_SECRETS_KEY is not configured, so cloud credentials cannot be saved and nothing is polled")
	}
	runner := cloudconnect.NewRunner(store, collector, cloudconnect.RunnerOptions{
		MaxConcurrent: c.MaxConcurrent, TenantMaxConcurrent: c.TenantMaxConcurrent,
		ConnectionMaxConcurrent: c.ConnectionMaxConcurrent,
		Log:                     log.With("job", "cloud-poller"), Registerer: reg,
	})
	log.Info("cloud connections enabled", "max_concurrent", c.MaxConcurrent,
		"tenant_max_concurrent", c.TenantMaxConcurrent, "connection_max_concurrent", c.ConnectionMaxConcurrent)
	return []leaderTask{
		{"cloud-poller", runner.Run},
		{"cloud-metrics", writer.Run},
	}, nil
}
