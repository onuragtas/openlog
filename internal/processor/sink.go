package processor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// ShardingKeys lists, per table, the columns of the Distributed table's sharding
// key cityHash64(...) in schema/clickhouse. Direct inserts compute the same hash.
var ShardingKeys = map[string][]string{
	TableMetrics:            {"tenant_id", "host_id"},
	TableMetricExemplars:    {"tenant_id", "trace_id"}, // exemplars.go (D-130)
	TableLogs:               {"tenant_id", "host_id"},
	TableSpans:              {"tenant_id", "trace_id"},
	TableHosts:              {"tenant_id", "host_id"},
	TableInventoryItems:     {"tenant_id", "host_id"},
	TableInventorySnapshots: {"tenant_id", "host_id"},
	TableRelinkQueue:        {"tenant_id", "trace_id"},
	TableUsageIngest:        {"tenant_id", "signal"},
}

// ShardingExpressions returns table -> "cityHash64(col, ...)" for VerifyShardingKeys.
func ShardingExpressions() map[string]string {
	out := make(map[string]string, len(ShardingKeys))
	for t, cols := range ShardingKeys {
		out[t] = "cityHash64(" + strings.Join(cols, ", ") + ")"
	}
	return out
}

// ClickHouseWriter writes rows to <database>.<table> Distributed tables
// (OPENLOG_PROCESSOR_INSERT_MODE=distributed).
type ClickHouseWriter struct {
	Conn     clickhouse.Conn
	Database string
}

// Write implements Writer.
func (w ClickHouseWriter) Write(ctx context.Context, table, token string, columns []string, rows [][]any) error {
	return clickhouse.Insert(ctx, w.Conn, "`"+w.Database+"`."+table, columns, token, rows)
}

// DirectWriter writes rows into the *_local table of each row's shard
// (OPENLOG_PROCESSOR_INSERT_MODE=direct, D-018).
type DirectWriter struct {
	W *clickhouse.ShardedWriter
}

// Write implements Writer.
func (d DirectWriter) Write(ctx context.Context, table, token string, columns []string, rows [][]any) error {
	return d.W.Insert(ctx, table, ShardingKeys[table], columns, token, rows)
}

// OpenDirectWriter verifies that the Distributed tables use the sharding keys the
// processor computes and loads the cluster topology, retrying while the schema or
// ClickHouse is not ready. A sharding key mismatch is returned immediately.
// Callers run the returned writer's Run (topology refresh) and Close.
func OpenDirectWriter(ctx context.Context, conn clickhouse.Conn, cfg config.Config, log *slog.Logger, reg prometheus.Registerer) (*clickhouse.ShardedWriter, error) {
	backoff := time.Second
	for {
		err := clickhouse.VerifyShardingKeys(ctx, conn, cfg.ClickHouseDatabase, ShardingExpressions())
		if err == nil {
			o := clickhouse.OptionsFromConfig(cfg.Common)
			w, werr := clickhouse.NewShardedWriter(ctx, conn, clickhouse.ShardedOptions{
				Conn: o, Database: cfg.ClickHouseDatabase, Cluster: cfg.ClickHouseCluster,
				RefreshInterval: cfg.Processor.TopologyRefresh, InsertQuorum: cfg.Processor.InsertQuorum,
			}, log, reg)
			if werr == nil {
				return w, nil
			}
			err = werr
		} else if errors.Is(err, clickhouse.ErrShardingKeyMismatch) {
			return nil, err
		}
		log.Warn("waiting for clickhouse schema and topology (direct inserts)", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}
