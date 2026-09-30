package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

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
func startDiskSpace(cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, reg prometheus.Registerer, log *slog.Logger) []leaderTask {
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
		c.Save = func(ctx context.Context, s diskspace.Snapshot) error {
			return postgres.PutSystemState(ctx, pool, diskspace.SystemStateKey, s)
		}
	}
	return []leaderTask{{"clickhouse-disk-usage", c.Run}}
}
