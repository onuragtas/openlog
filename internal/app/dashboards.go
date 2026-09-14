package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/dashboard"
)

// startDashboardAPI enables custom dashboards (docs/contracts/api.md "Dashboards"; postgres auth mode only).
func startDashboardAPI(pool *pgxpool.Pool, srv *api.Server) {
	if pool == nil {
		return
	}
	srv.SetDashboards(dashboard.NewManager(dashboard.NewPGStore(pool)))
}
