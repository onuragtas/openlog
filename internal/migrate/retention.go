package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// DefaultAPMRetentionDays is the TTL the APM tables are created with (schema/clickhouse/0006_apm.sql).
const DefaultAPMRetentionDays = 30

// APMRetentionSetting is the table_settings row that records the applied APM retention.
const APMRetentionSetting = "apm_retention_days"

// apmTTL lists the APM _local tables and their TTL expression (%d = days), as in 0006_apm.sql.
var apmTTL = []struct{ table, expr string }{
	{"apm_transactions_1m_local", "timestamp + INTERVAL %d DAY"},
	{"apm_service_edges_1m_local", "timestamp + INTERVAL %d DAY"},
	{"apm_service_links_1m_local", "timestamp + INTERVAL %d DAY"},
	{"apm_db_queries_1m_local", "timestamp + INTERVAL %d DAY"},
	{"apm_errors_1m_local", "timestamp + INTERVAL %d DAY"},
	{"apm_error_groups_local", "toDateTime(last_seen) + INTERVAL %d DAY"},
	{"apm_services_local", "toDateTime(last_seen) + INTERVAL %d DAY"},
	{"apm_service_hosts_local", "toDateTime(last_seen) + INTERVAL %d DAY"},
}

// APMRetentionStatements returns the ALTER statements that set the APM retention to days.
func APMRetentionStatements(cluster string, days int) ([]string, error) {
	if !clusterRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid cluster name %q", cluster)
	}
	if days < 1 || days > 3650 {
		return nil, fmt.Errorf("APM retention must be between 1 and 3650 days, got %d", days)
	}
	out := make([]string, 0, len(apmTTL))
	for _, t := range apmTTL {
		out = append(out, fmt.Sprintf("ALTER TABLE openlog.%s ON CLUSTER '%s' MODIFY TTL %s", t.table, cluster, fmt.Sprintf(t.expr, days)))
	}
	return out, nil
}

// AppliedAPMRetention returns the recorded APM retention (DefaultAPMRetentionDays when nothing was recorded).
func AppliedAPMRetention(ctx context.Context, conn clickhouse.Conn) (int, error) {
	var exists uint8
	if err := conn.QueryRow(ctx, "EXISTS TABLE openlog.table_settings").Scan(&exists); err != nil {
		return 0, fmt.Errorf("check table_settings: %w", err)
	}
	if exists == 0 {
		return DefaultAPMRetentionDays, nil
	}
	var value string
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"name": APMRetentionSetting}))
	if err := conn.QueryRow(qctx, "SELECT argMax(value, updated_at) FROM openlog.table_settings WHERE name = {name:String}").Scan(&value); err != nil {
		return 0, fmt.Errorf("read %s: %w", APMRetentionSetting, err)
	}
	if strings.TrimSpace(value) == "" {
		return DefaultAPMRetentionDays, nil
	}
	return strconv.Atoi(value)
}

// ApplyAPMRetention sets the TTL of the APM tables to days when it differs from the recorded value
// (OPENLOG_APM_RETENTION_DAYS, apm.md §8). The ALTERs run ON CLUSTER; each is idempotent, so a failure part way is
// repaired by the next run (the value is recorded only after all tables changed). It reports whether it changed
// anything.
func ApplyAPMRetention(ctx context.Context, conn clickhouse.Conn, cluster string, days int, log *slog.Logger) (bool, error) {
	stmts, err := APMRetentionStatements(cluster, days)
	if err != nil {
		return false, err
	}
	current, err := AppliedAPMRetention(ctx, conn)
	if err != nil {
		return false, err
	}
	if current == days {
		log.Debug("apm retention unchanged", "days", days)
		return false, nil
	}
	log.Info("changing apm retention", "from_days", current, "to_days", days, "tables", len(stmts))
	for _, st := range stmts {
		if err := conn.Exec(ctx, st); err != nil {
			return false, fmt.Errorf("apm retention: %w\n%s", err, st)
		}
	}
	ictx := ch.Context(ctx, ch.WithSettings(ch.Settings{"distributed_foreground_insert": 1}))
	if err := conn.Exec(ictx, "INSERT INTO openlog.table_settings (name, value) VALUES (?, ?)", APMRetentionSetting, strconv.Itoa(days)); err != nil {
		return false, fmt.Errorf("record %s: %w", APMRetentionSetting, err)
	}
	return true, nil
}
