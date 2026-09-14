package usage

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

var clusterRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// QueryCollector copies query compute of tenant queries from system.query_log (all replicas) into usage_queries_1h.
// api and alert queries carry log_comment {"component","tenant_id"} (internal/api/query). Hours are re-collected on
// every run while they are inside Lookback; usage_queries_1h keeps the latest collection (ReplacingMergeTree), so a
// re-run or several collectors never double count.
type QueryCollector struct {
	Conn     clickhouse.Conn // writer: reads system.query_log and inserts
	Database string
	Cluster  string
	Interval time.Duration // default 15m
	Lookback time.Duration // default 3h
	Log      *slog.Logger
	Now      func() time.Time
}

// QueryRow is one collected (tenant, hour, component).
type QueryRow struct {
	TenantID  string
	Hour      time.Time
	Component string
	QueryUsage
	CPUMicroseconds uint64
}

// Collect reads [from, to) from system.query_log and inserts the hourly rows; it returns the number of rows.
func (c *QueryCollector) Collect(ctx context.Context, from, to time.Time) (int, error) {
	if !clusterRe.MatchString(c.Cluster) {
		return 0, fmt.Errorf("invalid cluster %q", c.Cluster)
	}
	db := c.Database
	if db == "" {
		db = "openlog"
	}
	if !dbRe.MatchString(db) {
		return 0, fmt.Errorf("invalid database %q", db)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"from": from.UTC().Format(chTime), "to": to.UTC().Format(chTime)}),
		ch.WithSettings(ch.Settings{"skip_unavailable_shards": 1}))
	rows, err := c.Conn.Query(qctx, fmt.Sprintf(`SELECT JSONExtractString(log_comment, 'tenant_id') AS tenant,
       JSONExtractString(log_comment, 'component') AS component,
       toStartOfHour(toDateTime(event_time, 'UTC')) AS h,
       count(), countIf(type != 'QueryFinish'), sum(read_rows), sum(read_bytes),
       sum(ProfileEvents['UserTimeMicroseconds'] + ProfileEvents['SystemTimeMicroseconds']), sum(memory_usage)
FROM clusterAllReplicas('%s', system.query_log)
WHERE event_date >= toDate({from:DateTime('UTC')}) AND event_time >= {from:DateTime('UTC')} AND event_time < {to:DateTime('UTC')}
  AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND is_initial_query
  AND startsWith(log_comment, '{')
GROUP BY tenant, component, h
HAVING tenant != ''`, c.Cluster))
	if err != nil {
		return 0, fmt.Errorf("read system.query_log: %w", err)
	}
	var values [][]any
	collected := now().UTC()
	for rows.Next() {
		var r QueryRow
		if err := rows.Scan(&r.TenantID, &r.Component, &r.Hour, &r.Queries, &r.Failed, &r.ReadRows, &r.ReadBytes, &r.CPUMicroseconds, &r.MemoryBytesPeak); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, []any{r.TenantID, r.Hour, r.Component, r.Queries, r.Failed, r.ReadRows, r.ReadBytes, r.CPUMicroseconds, r.MemoryBytesPeak, collected})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, nil
	}
	err = clickhouse.InsertWithSettings(ctx, c.Conn, "`"+db+"`.usage_queries_1h",
		[]string{"tenant_id", "hour", "component", "queries", "failed", "read_rows", "read_bytes", "cpu_microseconds", "memory_bytes", "collected_at"},
		ch.Settings{"distributed_foreground_insert": 1}, values)
	if err != nil {
		return 0, fmt.Errorf("insert usage_queries_1h: %w", err)
	}
	return len(values), nil
}

// Run collects every Interval until ctx is done (leader task).
func (c *QueryCollector) Run(ctx context.Context) {
	interval, lookback := c.Interval, c.Lookback
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	if lookback <= 0 {
		lookback = 3 * time.Hour
	}
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	for {
		t := now().UTC()
		from := t.Truncate(time.Hour).Add(-lookback)
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		n, err := c.Collect(cctx, from, t)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn("query usage collection failed", "err", err)
		} else if err == nil {
			log.Debug("query usage collected", "rows", n, "from", from)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
