package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/diskspace"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// diskUsageInterval is how often the leader asks ClickHouse how full its disks are. A disk fills over hours, not
// seconds, and the query hits every replica, so this is deliberately slow.
const diskUsageInterval = 5 * time.Minute

// startDiskSpace measures how full the ClickHouse data disks are (leader task). Table TTLs bound the age of the
// data but never its size, so this is the only number that says whether retention is actually keeping up with
// the ingest rate. It is exported as openlog_clickhouse_disk_used_ratio and stored in system_state so the API
// can answer without a Prometheus scrape.
func startDiskSpace(cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, srv *api.Server,
	reg prometheus.Registerer, log *slog.Logger) []leaderTask {
	if conn == nil {
		return nil
	}
	c := &diskspace.Checker{
		CH:         conn,
		Cluster:    cfg.ClickHouseCluster,
		Interval:   diskUsageInterval,
		Registerer: reg,
		Log:        log.With("job", "clickhouse-disk-usage"),
	}
	if pool != nil {
		store := diskspace.PGStore{Pool: pool}
		// Read once per round rather than cached: the check runs every few minutes, so a change made in the UI
		// applies on the next one without a restart and without an invalidation path to get wrong.
		c.Settings = func(ctx context.Context) (diskspace.Effective, error) {
			s, _, err := store.Get(ctx)
			if err != nil {
				return diskspace.Defaults(), err
			}
			return s.Resolve(), nil
		}
		c.Save = func(ctx context.Context, s diskspace.Snapshot) error {
			return postgres.PutSystemState(ctx, pool, diskspace.SystemStateKey, s)
		}
		// Carries the levels already reported across a restart and across a change of leader, so a disk sitting
		// at 91 % is not reported again every time an api pod comes up.
		c.Load = func(ctx context.Context) (diskspace.Snapshot, bool, error) {
			var s diskspace.Snapshot
			_, found, err := postgres.GetSystemState(ctx, pool, diskspace.SystemStateKey, &s)
			return s, found, err
		}
		// Every api pod serves the page from the stored snapshot, not by querying ClickHouse itself: only the
		// leader measures, and a page load must not become a second source of the same query.
		srv.SetDiskSpace(api.DiskSpaceDeps{
			Snapshot: c.Load,
			Settings: store,
			SaaS:     cfg.Usage.SaaSMode,
			Interval: diskUsageInterval,
		})
	}
	return []leaderTask{{"clickhouse-disk-usage", c.Run}}
}
