package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/billing"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/ingest"
	"github.com/onuragtas/openlog/internal/mail"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/usage"
)

// leaderTask is a job run on the api leader only (startLeaderTasks).
type leaderTask struct {
	name string
	run  func(ctx context.Context)
}

// TTLOptions returns the TTL options of openlog-migrate: the configuration's plus, with per-tenant retention enabled
// (OPENLOG_QUOTA_RETENTION_ENABLED), the longest plan retention of the logs, traces and metrics tables (D-081).
func TTLOptions(cfg config.Config) (migrate.TTLOptions, error) {
	o := migrate.TTLOptionsFromConfig(cfg)
	if !cfg.Usage.RetentionEnabled {
		return o, nil
	}
	c, err := quota.LoadCatalog(cfg.Usage)
	if err != nil {
		return o, err
	}
	o.ClassRetentionDays = quota.TableRetentionDays(c)
	return o, nil
}

// startIngestQuota enables quota enforcement in ingest (OPENLOG_SAAS_MODE=true, postgres auth mode; D-080).
func startIngestQuota(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, svc *ingest.Service, reg prometheus.Registerer, log *slog.Logger) {
	if !cfg.Usage.SaaSMode || pool == nil {
		return
	}
	u := cfg.Usage
	lim := quota.NewIngestLimiter(quota.PGStore{Pool: pool}, quota.LimiterOptions{
		Refresh: u.QuotaRefreshInterval, Pods: u.QuotaIngestPods, BlockedRetryAfter: u.QuotaBlockedRetryAfter,
		Registerer: reg, Log: log.With("job", "ingest-quota"),
	})
	go lim.Run(ctx)
	svc.SetLimiter(ingestLimiter{lim})
	log.Info("SaaS mode: ingest quota enforcement enabled", "refresh", u.QuotaRefreshInterval, "pods", u.QuotaIngestPods)
}

type ingestLimiter struct{ l *quota.IngestLimiter }

func (a ingestLimiter) Allow(tenantID string, _ queue.Signal, bytes int) ingest.LimitDecision {
	d := a.l.Allow(tenantID, bytes)
	return ingest.LimitDecision{Allowed: d.Allowed, RetryAfter: d.RetryAfter, Message: d.Message}
}

// startUsageAPI enables the usage, plan and billing endpoints and returns the leader tasks: query compute collection,
// quota evaluation with owner notifications, per-tenant retention and the billing usage push (docs/contracts/usage.md).
func startUsageAPI(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, db *query.DB, srv *api.Server,
	reg prometheus.Registerer, log *slog.Logger) ([]leaderTask, error) {
	u := cfg.Usage
	catalog, err := quota.LoadCatalog(u)
	if err != nil {
		return nil, err
	}
	reader := &usage.Reader{Conn: conn, Database: cfg.ClickHouseDatabase}
	deps := api.UsageDeps{Reader: reader, Catalog: catalog, SaaS: u.SaaSMode, Superadmin: u.IsSuperadmin, Thresholds: u.NotifyThresholds}
	var tasks []leaderTask
	if u.QueryCollection {
		tasks = append(tasks, leaderTask{"usage-query-collection", (&usage.QueryCollector{Conn: conn, Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster, Log: log.With("job", "usage-query-collection")}).Run})
	}
	if pool == nil {
		srv.SetUsage(deps)
		return nil, nil // static mode: no leader, no plans in PostgreSQL
	}
	store := quota.PGStore{Pool: pool}
	deps.Store = store

	// Plan query limits (D-080): OPENLOG_QUERY_TENANT_LIMITS wins, then the plan's limits over the defaults.
	pl := &planQueryLimits{query: cfg.Query, catalog: catalog, store: store, log: log}
	pl.refresh(ctx)
	go pl.run(ctx)
	db.SetLimitsFunc("api", pl.limits)

	var mailer auth.Mailer
	if s := cfg.Alert.SMTP; s.Host != "" {
		m, err := mail.New(mail.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From,
			TLS: s.TLS, InsecureSkipVerify: s.InsecureSkipVerify})
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SMTP_*: %w", err)
		}
		mailer = m
	} else {
		log.Info("OPENLOG_SMTP_HOST is not set: usage threshold e-mails are disabled (banners still show)")
	}
	tasks = append(tasks, leaderTask{"usage-quota-evaluation", quota.NewEvaluator(catalog, store, reader, quota.EvaluatorOptions{
		SaaS: u.SaaSMode, Thresholds: u.NotifyThresholds, Interval: u.EvaluationInterval, Mailer: mailer, PublicURL: cfg.Alert.PublicURL,
		Log: log.With("job", "usage-quota-evaluation"), Registerer: reg,
	}).Run})
	if u.RetentionEnabled {
		tasks = append(tasks, leaderTask{"usage-tenant-retention", (&quota.RetentionJob{Conn: conn, Database: cfg.ClickHouseDatabase,
			Cluster: cfg.ClickHouseCluster, Catalog: catalog, Store: store, MaxMutations: u.RetentionMaxMutations,
			Log: log.With("job", "usage-tenant-retention")}).Run})
	}
	provider, err := billing.New(u.BillingProvider)
	if err != nil {
		return nil, err
	}
	if provider != nil {
		deps.Billing = provider
		tasks = append(tasks, leaderTask{"billing-usage-push", (&billing.PushJob{Provider: provider, Orgs: store, Usage: reader,
			Store: billing.PGStore{Pool: pool}, At: u.BillingPushOffset(), Log: log.With("job", "billing-usage-push")}).Run})
	}
	srv.SetUsage(deps)
	log.Info("usage metering enabled", "saas_mode", u.SaaSMode, "plans", catalog.PlanIDs(), "default_plan", catalog.Default,
		"tenant_retention", u.RetentionEnabled, "billing_provider", u.BillingProvider, "superadmins", len(u.SuperadminEmails))
	return tasks, nil
}

// planQueryLimits resolves the ClickHouse query limits of a tenant from its plan, refreshed every minute on every
// api pod.
type planQueryLimits struct {
	query   config.Query
	catalog *quota.Catalog
	store   quota.PGStore
	log     *slog.Logger

	mu     sync.RWMutex
	byTent map[string]quota.QueryLimits
}

func (p *planQueryLimits) refresh(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	orgs, err := p.store.ListOrgPlans(rctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Debug("cannot load plan query limits", "err", err)
		}
		return
	}
	m := make(map[string]quota.QueryLimits, len(orgs))
	for _, op := range orgs {
		if q := p.catalog.EffectivePlan(op).Limits.Query; q != (quota.QueryLimits{}) {
			m[op.TenantID] = q
		}
	}
	p.mu.Lock()
	p.byTent = m
	p.mu.Unlock()
}

func (p *planQueryLimits) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
			p.refresh(ctx)
		}
	}
}

func (p *planQueryLimits) limits(tenant string) config.QueryLimits {
	if l, ok := p.query.Tenants[tenant]; ok {
		return l
	}
	l := p.query.Defaults
	p.mu.RLock()
	q, ok := p.byTent[tenant]
	p.mu.RUnlock()
	if !ok {
		return l
	}
	if q.MaxMemoryUsage > 0 {
		l.MaxMemoryUsage = q.MaxMemoryUsage
	}
	if q.MaxRowsToRead > 0 {
		l.MaxRowsToRead = q.MaxRowsToRead
	}
	if q.MaxBytesToRead > 0 {
		l.MaxBytesToRead = q.MaxBytesToRead
	}
	return l
}
