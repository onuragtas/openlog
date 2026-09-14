package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// openQueryDB returns the tenant query layer of api/alert (D-047): a separate connection as the read-only
// OPENLOG_CLICKHOUSE_READ_USER when configured, otherwise writer (with a warning), with the per-tenant
// OPENLOG_QUERY_* limits. Writes (APM edge linking, alert evaluation history) keep using writer.
func openQueryDB(ctx context.Context, cfg config.Config, writer clickhouse.Conn, component string, timeout time.Duration, log *slog.Logger) (*query.DB, func(), error) {
	conn, closeConn := writer, func() {}
	if r := cfg.ClickHouseRead; r.User == "" {
		log.Warn("OPENLOG_CLICKHOUSE_READ_USER is not set: tenant queries use the writer user OPENLOG_CLICKHOUSE_USER; configure a read-only user (docs/contracts/config.md)")
	} else {
		o := clickhouse.OptionsFromConfig(cfg.Common)
		o.User, o.Password = r.User, r.Password
		c, err := clickhouse.OpenRetry(ctx, o, func(err error) {
			log.Warn("waiting for clickhouse (read user)", "user", r.User, "err", err)
		})
		if err != nil {
			return nil, nil, err
		}
		conn, closeConn = c, func() { c.Close() }
		checkReadOnly(ctx, c, r.User, log)
	}
	db := query.New(conn, cfg.ClickHouseDatabase, timeout)
	db.SetLimits(component, cfg.Query)
	d := cfg.Query.Defaults
	log.Info("clickhouse query limits", "max_memory_usage", d.MaxMemoryUsage, "max_rows_to_read", d.MaxRowsToRead,
		"max_bytes_to_read", d.MaxBytesToRead, "tenant_overrides", cfg.Query.TenantIDs())
	return db, closeConn, nil
}

// checkReadOnly warns when the read user can write (its profile lacks readonly).
func checkReadOnly(ctx context.Context, c clickhouse.Conn, user string, log *slog.Logger) {
	var ro string
	if err := c.QueryRow(ctx, "SELECT toString(getSetting('readonly'))").Scan(&ro); err != nil {
		log.Warn("cannot check that the clickhouse read user is read-only", "user", user, "err", err)
		return
	}
	if ro == "0" {
		log.Warn("clickhouse read user is not read-only: set readonly=2 in its settings profile", "user", user)
	}
}
