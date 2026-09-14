package migrate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// DefaultAPMRetentionDays is the TTL the APM tables are created with (schema/clickhouse/0006_apm.sql).
const DefaultAPMRetentionDays = 30

// APMRetentionSetting is the table_settings row that records the applied APM retention.
const APMRetentionSetting = "apm_retention_days"

// APMRetentionStatements returns the ALTER statements that set the APM retention to days without tiered storage
// moves (the APM tables of TTLTables). openlog-migrate applies the retention through ApplyTableTTLs (ttl.go).
func APMRetentionStatements(cluster string, days int) ([]string, error) {
	if !clusterRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid cluster name %q", cluster)
	}
	if days < 1 || days > 3650 {
		return nil, fmt.Errorf("APM retention must be between 1 and 3650 days, got %d", days)
	}
	var out []string
	for _, t := range TTLTables {
		if t.Days == 0 {
			out = append(out, TTLStatement(cluster, t.Table, TTLExpression(t, days, config.StorageTier{})))
		}
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
