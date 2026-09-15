package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/savedview"
)

// startSavedViews enables the saved views of the Logs and Metrics Explorers (docs/contracts/api.md "Saved views",
// D-118; postgres auth mode only).
func startSavedViews(pool *pgxpool.Pool, srv *api.Server) {
	if pool == nil {
		return
	}
	srv.SetSavedViews(savedview.NewManager(savedview.NewPGStore(pool)))
}
