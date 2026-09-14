package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/report"
	"github.com/onuragtas/openlog/internal/mail"
	"github.com/onuragtas/openlog/internal/oql"
)

// startDashboardAPI enables custom dashboards with version history, share links and scheduled reports
// (docs/contracts/api.md "Dashboards"; postgres auth mode only) and returns the leader task that sends the reports.
func startDashboardAPI(cfg config.Config, pool *pgxpool.Pool, db *query.DB, srv *api.Server, log *slog.Logger) ([]leaderTask, error) {
	if pool == nil {
		return nil, nil
	}
	store := dashboard.NewPGStore(pool)
	srv.SetDashboards(dashboard.NewManager(store))

	var mailer auth.Mailer
	if s := cfg.Alert.SMTP; s.Host != "" {
		m, err := mail.New(mail.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From,
			TLS: s.TLS, InsecureSkipVerify: s.InsecureSkipVerify})
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SMTP_*: %w", err)
		}
		mailer = m
	} else {
		log.Info("OPENLOG_SMTP_HOST is not set: scheduled dashboard reports cannot be sent")
	}
	images, err := reportImages(cfg, srv, log) // renderer.go: PNG widget images (D-097)
	if err != nil {
		return nil, err
	}
	job := report.NewJob(report.Options{
		Store: store, Mailer: mailer, PublicURL: cfg.Alert.PublicURL, Log: log.With("job", "dashboard-reports"),
		Images: images, ImageTimeout: cfg.Renderer.Timeout,
		// Widget queries run with the organization's tenant scope and its query limits, like API queries.
		Run: func(ctx context.Context, tenantID string, p *oql.Plan) (*oql.Result, error) {
			sc, err := db.Scope(tenantID)
			if err != nil {
				return nil, err
			}
			return oql.Execute(ctx, sc, p)
		},
	})
	return []leaderTask{{"dashboard-reports", job.Run}}, nil
}
