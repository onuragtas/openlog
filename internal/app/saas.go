package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/ingest"
	"github.com/onuragtas/openlog/internal/mail"
	"github.com/onuragtas/openlog/internal/operator"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/usage"
)

// startSaaS enables the operator console (postgres auth mode) and, with OPENLOG_SAAS_MODE=true, the users limit and
// the leader jobs: host limit sync, trial lifecycle and abuse detection (docs/operations/saas.md, D-105, D-106).
func startSaaS(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, svc *auth.Service, srv *api.Server,
	log *slog.Logger) ([]leaderTask, error) {
	if pool == nil || svc == nil {
		return nil, nil
	}
	catalog, err := quota.LoadCatalog(cfg.Usage)
	if err != nil {
		return nil, err
	}
	s := cfg.SaaS
	if s.SignupTrialPlan != "" {
		p, ok := catalog.Plan(s.SignupTrialPlan)
		if !ok || p.TrialDays <= 0 {
			return nil, fmt.Errorf("OPENLOG_SAAS_SIGNUP_TRIAL_PLAN: %q is not a plan of the catalog with trial_days > 0", s.SignupTrialPlan)
		}
	}
	store := operator.Store{Pool: pool}
	srv.SetOperator(api.OperatorDeps{Store: store, Catalog: catalog, SaaS: cfg.Usage.SaaSMode, SupportTTL: s.SupportSessionTTL})
	if err := srv.ReloadSuspensions(ctx); err != nil {
		log.Warn("cannot load suspended organizations yet", "err", err)
	}
	go srv.RunOperatorCache(ctx)
	if !cfg.Usage.SaaSMode {
		return nil, nil
	}
	plans := quota.PGStore{Pool: pool}
	svc.SetMemberLimit(operator.MemberLimiter{Catalog: catalog, Plans: plans, Pool: pool}.Check)
	reader := &usage.Reader{Conn: conn, Database: cfg.ClickHouseDatabase}
	var mailer auth.Mailer
	if m := cfg.Alert.SMTP; m.Host != "" {
		ml, err := mail.New(mail.Config{Host: m.Host, Port: m.Port, Username: m.Username, Password: m.Password, From: m.From,
			TLS: m.TLS, InsecureSkipVerify: m.InsecureSkipVerify})
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SMTP_*: %w", err)
		}
		mailer = ml
	}
	tasks := []leaderTask{
		{"saas-host-limits", (&operator.HostSync{Catalog: catalog, Plans: plans, Usage: reader, Store: store, Interval: s.HostSyncInterval,
			Log: log.With("job", "saas-host-limits")}).Run},
		{"saas-trials", (&operator.TrialJob{Store: store, Owners: plans, Catalog: catalog, Mailer: mailer, PublicURL: cfg.Alert.PublicURL,
			NotifyDays: s.TrialNotifyDays, SignupTrialPlan: s.SignupTrialPlan, Interval: s.LifecycleInterval, Log: log.With("job", "saas-trials")}).Run},
		{"saas-abuse-detection", (&operator.AbuseDetector{Store: store, Plans: plans, Catalog: catalog, Usage: reader, Config: s,
			Log: log.With("job", "saas-abuse-detection"), Suspended: func(ctx context.Context) { _ = srv.ReloadSuspensions(ctx) }}).Run},
	}
	log.Info("SaaS operations enabled", "signup_trial_plan", s.SignupTrialPlan, "auto_suspend", s.AutoSuspend)
	return tasks, nil
}

// startIngestGate enables suspension and host limit enforcement in ingest (SaaS mode; called by startIngestQuota).
func startIngestGate(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, svc *ingest.Service, reg prometheus.Registerer, log *slog.Logger) {
	if !cfg.Usage.SaaSMode || pool == nil {
		return
	}
	g := operator.NewIngestGate(operator.Store{Pool: pool}, operator.GateOptions{Refresh: cfg.Usage.QuotaRefreshInterval, Registerer: reg,
		Log: log.With("job", "ingest-saas-gate")})
	go g.Run(ctx)
	svc.SetGate(g)
}
