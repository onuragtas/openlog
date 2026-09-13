package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/version"
)

// alertKeyring loads the channel secrets keyring (OPENLOG_SECRETS_KEY, OPENLOG_SECRETS_KEY_PREVIOUS).
func alertKeyring(cfg config.Config) (*secrets.Keyring, error) {
	return secrets.NewKeyring(cfg.Alert.SecretsKey, cfg.Alert.SecretsKeyPrevious)
}

// alertSender creates the notification sender (egress policy, global SMTP server).
func alertSender(cfg config.Config) *notify.Sender {
	s := cfg.Alert.SMTP
	return notify.NewSender(notify.Options{
		Timeout: cfg.Alert.DeliveryTimeout, BlockPrivate: cfg.Alert.BlockPrivateDestinations,
		UserAgent: "openlog-alert/" + version.String(),
		SMTP: notify.SMTPConfig{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From,
			TLS: s.TLS, InsecureSkipVerify: s.InsecureSkipVerify},
	})
}

// startAlertAPI enables /api/v1/alerts/* on srv (docs/contracts/alerting.md). pool is nil in static auth mode, where
// alerting is not available.
func startAlertAPI(cfg config.Config, pool *pgxpool.Pool, srv *api.Server, apmSettings apm.SettingsStore, log *slog.Logger) error {
	if pool == nil {
		return nil
	}
	kr, err := alertKeyring(cfg)
	if err != nil {
		return err
	}
	if !kr.Configured() {
		log.Warn("OPENLOG_SECRETS_KEY is not set: alert notification channels cannot be created or tested")
	}
	var settings alert.ApdexSettingsFunc
	if apmSettings != nil {
		settings = apmSettings.List
	}
	srv.SetAlerts(alert.NewManager(alert.NewPGStore(pool), alert.ManagerOptions{
		Keys: kr, Sender: alertSender(cfg), PublicURL: cfg.Alert.PublicURL, MaxRulesPerOrg: cfg.Alert.MaxRulesPerOrg,
		Limits: alert.Limits{MaxSeries: cfg.Alert.MaxSeriesPerRule}, Delay: cfg.Alert.EvaluationDelay,
		QueryTimeout: cfg.Alert.QueryTimeout, DefaultApdexT: cfg.APM.DefaultApdexT, ApdexSettings: settings,
	}))
	return nil
}

// RunAlert runs the alert evaluator (rule leases, evaluation) and the notification dispatcher until ctx is done
// (openlog-alert, and openlog-allinone with OPENLOG_ALERT_ENABLED=true).
func RunAlert(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	log = log.With("component", "alert")
	if cfg.AuthMode != "postgres" {
		return errors.New("alerting requires OPENLOG_AUTH_MODE=postgres")
	}
	kr, err := alertKeyring(cfg)
	if err != nil {
		return err
	}
	if !kr.Configured() {
		log.Warn("OPENLOG_SECRETS_KEY is not set: notifications cannot be delivered (channel secrets cannot be decrypted)")
	}
	pool, err := OpenPostgres(ctx, cfg, "openlog-alert")
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		return err
	}
	adm.AddCheck("postgres", pool.Ping)
	conn, err := openClickHouse(ctx, cfg, cfg.ClickHouseDatabase, log)
	if err != nil {
		return err
	}
	defer conn.Close()
	db := query.New(conn, cfg.ClickHouseDatabase, cfg.Alert.QueryTimeout)
	adm.AddCheck("clickhouse", db.Ping)

	store := alert.NewPGStore(pool)
	instance := instanceID()
	a := cfg.Alert
	leases := alert.NewLeaseManager(store, alert.LeaseOptions{Instance: instance, Version: version.String(), TTL: a.LeaseTTL,
		RenewInterval: a.LeaseRenewInterval, Log: log.With("job", "alert-leases"), Registerer: adm.Registry()})
	apmSettings := apm.PGSettings{Pool: pool}
	ev := alert.NewEvaluator(store, leases, db, alert.EvaluatorOptions{
		Instance: instance, Delay: a.EvaluationDelay, MaxConcurrent: a.MaxConcurrentEvaluations, TenantMaxConcurrent: a.TenantMaxConcurrent,
		TenantPerMinute: a.TenantEvaluationsPerMinute, QueryTimeout: a.QueryTimeout, Limits: alert.Limits{MaxSeries: a.MaxSeriesPerRule},
		PublicURL: a.PublicURL, Log: log.With("job", "alert-evaluator"), Registerer: adm.Registry(),
		ApdexSettings: apmSettings.List, DefaultApdexT: cfg.APM.DefaultApdexT,
	})
	disp := alert.NewDispatcher(store, alertSender(cfg), kr, alert.DispatcherOptions{
		Instance: instance, Workers: a.DispatchWorkers, DeliveryTimeout: a.DeliveryTimeout, MaxAttempts: a.DeliveryMaxAttempts,
		Log: log.With("job", "alert-dispatcher"), Registerer: adm.Registry(),
	})

	// Leases outlive the evaluator: they are released only after running evaluations have committed.
	leaseCtx, stopLeases := context.WithCancel(context.WithoutCancel(ctx))
	leasesDone := make(chan struct{})
	go func() { defer close(leasesDone); leases.Run(leaseCtx) }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ev.Run(ctx) }()
	go func() { defer wg.Done(); disp.Run(ctx) }()
	log.Info("alert evaluator and dispatcher started", "instance_id", instance, "lease_ttl", a.LeaseTTL,
		"evaluation_delay", a.EvaluationDelay, "secrets_key", kr.CurrentKeyID())
	<-ctx.Done()
	wg.Wait()
	stopLeases()
	<-leasesDone
	return nil
}

// RotateAlertSecrets re-encrypts channel secrets with the current OPENLOG_SECRETS_KEY (openlog-alert rotate-secrets).
func RotateAlertSecrets(ctx context.Context, cfg config.Config, check bool, log *slog.Logger) (rotated, failed, pending int, err error) {
	kr, err := alertKeyring(cfg)
	if err != nil {
		return 0, 0, 0, err
	}
	pool, err := OpenPostgres(ctx, cfg, "openlog-alert-rotate")
	if err != nil {
		return 0, 0, 0, err
	}
	defer pool.Close()
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		return 0, 0, 0, err
	}
	return alert.NewPGStore(pool).RotateSecrets(ctx, kr, check)
}
