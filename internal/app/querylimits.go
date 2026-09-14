package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
)

// startQueryLimits applies the per-tenant query limit layers to db (docs/contracts/usage.md §4.5): OPENLOG_QUERY_*
// defaults < plan limits.query < organization setting (org_query_limits) < OPENLOG_QUERY_TENANT_LIMITS, reloaded from
// PostgreSQL every quota.DefaultQueryLimitsRefresh. catalog may be nil (no plan layer).
func startQueryLimits(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, catalog *quota.Catalog, db *query.DB, component string,
	log *slog.Logger) *quota.QueryLimitsResolver {
	r := &quota.QueryLimitsResolver{Query: cfg.Query, Catalog: catalog, Store: quota.PGStore{Pool: pool}, Log: log.With("job", "query-limits")}
	if err := r.Load(ctx); err != nil {
		log.Warn("cannot load plan and organization query limits; using OPENLOG_QUERY_* until the next reload", "err", err)
	}
	go r.Run(ctx)
	db.SetLimitsFunc(component, r.Limits)
	return r
}
