package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/version"
	"github.com/onuragtas/openlog/internal/vuln"
)

// startVulnerabilities enables /api/v1/vulnerabilities/* and returns the leader task that keeps the
// findings current (OPENLOG_VULN_ENABLED, D-142): one job that syncs the feed for the ecosystems the
// installation actually runs and then matches every host's packages against the catalog.
//
// It is a leader task because it writes one row per finding for the whole installation; two pods doing it
// would write the same rows twice with different timestamps and make "since when" meaningless.
func startVulnerabilities(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn,
	srv *api.Server, reg prometheus.Registerer, log *slog.Logger) []leaderTask {
	if pool == nil || !cfg.Vuln.Enabled {
		return nil
	}
	catalog := vuln.NewPGStore(pool)
	srv.SetVulnerabilities(catalog)

	writers := &lazyShardedInserter{bootstrap: conn, log: log.With("job", "vulnerability-matcher"),
		opts: clickhouse.ShardedOptions{Conn: clickhouse.OptionsFromConfig(cfg.Common), Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster}}
	writers.opts.Conn.MaxConns = 2
	writers.runCtx = context.WithoutCancel(ctx)

	var fetcher *vuln.Fetcher
	if cfg.Vuln.FeedURL != "" {
		fetcher = vuln.NewFetcher(vuln.FetcherOptions{BaseURL: cfg.Vuln.FeedURL,
			Timeout: cfg.Vuln.FeedTimeout, UserAgent: "openlog/" + version.String(),
			Log: log.With("job", "vulnerability-feed")})
	} else {
		// An empty feed URL is how an installation says "I fill the catalog myself" (an air-gapped mirror
		// loading it through the database); matching still runs against whatever is stored.
		log.Info("the vulnerability feed is not configured; matching against the stored catalog only")
	}
	matcher := vuln.NewMatcher(conn, catalog, fetcher, vuln.NewInsertWriter(writers), vuln.MatcherOptions{
		Database: cfg.ClickHouseDatabase, Cluster: cfg.ClickHouseCluster,
		Interval: cfg.Vuln.MatchInterval, SyncInterval: cfg.Vuln.SyncInterval,
		Log: log.With("job", "vulnerability-matcher"), Registerer: reg,
	})
	log.Info("vulnerability matching enabled", "match_interval", cfg.Vuln.MatchInterval,
		"sync_interval", cfg.Vuln.SyncInterval, "feed", cfg.Vuln.FeedURL)
	return []leaderTask{{"vulnerability-matcher", matcher.Run}}
}
