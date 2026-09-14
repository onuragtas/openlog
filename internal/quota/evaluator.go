package quota

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/auth"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
	"github.com/onuragtas/openlog/internal/usage"
)

// EvalStore is the persistence the evaluator needs (PGStore).
type EvalStore interface {
	ListOrgPlans(ctx context.Context) ([]OrgPlan, error)
	MemberCounts(ctx context.Context) (map[string]int64, error)
	SaveStatuses(ctx context.Context, sts []StoredStatus) error
	Owners(ctx context.Context, orgID string) ([]Owner, error)
	ClaimNotification(ctx context.Context, orgID string, periodStart time.Time, metric string, threshold int) (bool, error)
	CompleteNotification(ctx context.Context, orgID string, periodStart time.Time, metric string, threshold, recipients int, sent bool) error
}

// UsageSource returns every tenant's period usage (usage.Reader).
type UsageSource interface {
	AllTenants(ctx context.Context, from, to time.Time) (map[string]usage.TenantPeriod, error)
}

// EvaluatorOptions configure Evaluator.
type EvaluatorOptions struct {
	SaaS       bool
	Thresholds []int // notification thresholds in percent (80, 100)
	Interval   time.Duration
	// Mailer sends usage e-mails to owners; nil disables them (banners still show).
	Mailer     auth.Mailer
	PublicURL  string
	Log        *slog.Logger
	Now        func() time.Time
	Registerer prometheus.Registerer
}

// Evaluator is the api leader job that evaluates every organization's usage against its plan, stores the result in
// tenant_quota_status (read by ingest pods and the UI banner) and e-mails owners when a threshold is crossed.
type Evaluator struct {
	catalog *Catalog
	store   EvalStore
	usage   UsageSource
	o       EvaluatorOptions
	runs    *prometheus.CounterVec
}

// NewEvaluator creates the job.
func NewEvaluator(c *Catalog, store EvalStore, u UsageSource, o EvaluatorOptions) *Evaluator {
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	e := &Evaluator{catalog: c, store: store, usage: u, o: o, runs: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "openlog_quota_evaluations_total", Help: "Quota evaluation runs of the api leader by result.",
	}, []string{"result"})}
	if o.Registerer != nil {
		o.Registerer.MustRegister(e.runs)
	}
	return e
}

// EffectivePlan returns the plan of an assignment with its overrides applied.
func (c *Catalog) EffectivePlan(op OrgPlan) Plan {
	return op.Overrides.Apply(c.Resolve(op.PlanID))
}

// RunOnce evaluates all organizations and returns the stored statuses.
func (e *Evaluator) RunOnce(ctx context.Context) ([]StoredStatus, error) {
	now := e.o.Now().UTC()
	period := usage.PeriodOf(now)
	orgs, err := e.store.ListOrgPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	members, err := e.store.MemberCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("member counts: %w", err)
	}
	tenants, err := e.usage.AllTenants(ctx, period.Start, now.Add(time.Hour).Truncate(time.Hour))
	if err != nil {
		return nil, fmt.Errorf("tenant usage: %w", err)
	}
	warn := WarnPercent(e.o.Thresholds)
	out := make([]StoredStatus, 0, len(orgs))
	for _, op := range orgs {
		plan := e.catalog.EffectivePlan(op)
		tu := tenants[op.TenantID]
		u := Usage{IngestBytes: clampInt64(tu.IngestBytes), ActiveHosts: clampInt64(tu.ActiveHosts), Users: members[op.OrgID]}
		st := Evaluate(plan, u, e.o.SaaS, warn)
		out = append(out, StoredStatus{Status: st, TenantID: op.TenantID, OrgID: op.OrgID, PeriodStart: period.Start, IngestBytes: u.IngestBytes})
	}
	if err := e.store.SaveStatuses(ctx, out); err != nil {
		return nil, fmt.Errorf("save quota status: %w", err)
	}
	if e.o.Mailer != nil {
		for i, op := range orgs {
			e.notify(ctx, op, e.catalog.EffectivePlan(op), out[i], period)
		}
	}
	return out, nil
}

func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// notify e-mails the owners once per (period, metric, threshold) crossed.
func (e *Evaluator) notify(ctx context.Context, op OrgPlan, plan Plan, st StoredStatus, period usage.Period) {
	for _, m := range st.Metrics {
		crossed := CrossedThresholds(m, e.o.Thresholds)
		if len(crossed) == 0 {
			continue
		}
		// Only the highest crossed threshold is mailed; lower ones are claimed silently (no 80 % mail after 100 %).
		top := crossed[len(crossed)-1]
		for _, t := range crossed[:len(crossed)-1] {
			if _, err := e.store.ClaimNotification(ctx, op.OrgID, period.Start, m.Metric, t); err != nil {
				e.o.Log.Warn("usage notification claim failed", "org_id", op.OrgID, "err", err)
				return
			}
		}
		claimed, err := e.store.ClaimNotification(ctx, op.OrgID, period.Start, m.Metric, top)
		if err != nil {
			e.o.Log.Warn("usage notification claim failed", "org_id", op.OrgID, "err", err)
			return
		}
		if !claimed {
			continue
		}
		owners, err := e.store.Owners(ctx, op.OrgID)
		sent := 0
		if err == nil {
			for _, to := range owners {
				mail := UsageMail(op, plan, m, top, period, e.o.PublicURL, e.o.SaaS, to.Locale)
				mail.To = to.Email
				if serr := e.o.Mailer.Send(ctx, mail); serr != nil {
					e.o.Log.Warn("usage notification e-mail failed", "org_id", op.OrgID, "metric", m.Metric, "err", serr)
					continue
				}
				sent++
			}
		}
		if cerr := e.store.CompleteNotification(ctx, op.OrgID, period.Start, m.Metric, top, sent, sent > 0 || (err == nil && len(owners) == 0)); cerr != nil {
			e.o.Log.Warn("usage notification bookkeeping failed", "org_id", op.OrgID, "err", cerr)
		}
		if sent > 0 {
			e.o.Log.Info("usage notification sent", "org_id", op.OrgID, "metric", m.Metric, "threshold", top, "recipients", sent)
		}
	}
}

// MetricLabel is the human-readable name of a quota metric.
func MetricLabel(metric string) string {
	switch metric {
	case MetricIngestBytes:
		return "Data ingest"
	case MetricHosts:
		return "Hosts"
	case MetricUsers:
		return "Users"
	}
	return metric
}

// FormatMetricValue renders a metric value (GiB for ingest).
func FormatMetricValue(metric string, v float64) string {
	if metric == MetricIngestBytes {
		return fmt.Sprintf("%.2f GiB", v/GiB)
	}
	return fmt.Sprintf("%.0f", v)
}

// Owner is a recipient of usage notifications.
type Owner struct {
	Email  string
	Locale string // users.locale ("" = English)
}

// UsageMail builds the owner notification for metric m crossing threshold percent, in locale (internal/mail/templates).
func UsageMail(op OrgPlan, plan Plan, m MetricStatus, threshold int, period usage.Period, publicURL string, saas bool, locale string) auth.Mail {
	d := mailtemplates.UsageData{OrgName: op.OrgName, TenantID: op.TenantID, Metric: m.Metric,
		Used: FormatMetricValue(m.Metric, m.Used), Limit: FormatMetricValue(m.Metric, m.Limit), Percent: m.Percent, Threshold: threshold,
		PlanName: plan.Name, Period: period.ID(), PeriodEnd: period.End.Format(time.DateOnly), Exceeded: threshold >= 100,
		HardBlock:    m.Metric == MetricIngestBytes && threshold >= 100 && saas && plan.Enforcement.HardIngestLimit,
		BlockPercent: 100 + plan.Enforcement.GracePercent, GracePercent: plan.Enforcement.GracePercent}
	if publicURL != "" {
		d.Link = strings.TrimRight(publicURL, "/") + "/settings/usage"
	}
	msg := mailtemplates.Usage(locale, d)
	return auth.Mail{Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}
}

// Run evaluates every Interval until ctx is done (leader task).
func (e *Evaluator) Run(ctx context.Context) {
	for {
		rctx, cancel := context.WithTimeout(ctx, max(e.o.Interval, time.Minute))
		sts, err := e.RunOnce(rctx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			e.runs.WithLabelValues("error").Inc()
			e.o.Log.Warn("quota evaluation failed", "err", err)
		case err == nil:
			e.runs.WithLabelValues("ok").Inc()
			blocked := 0
			for _, s := range sts {
				if s.IngestBlocked {
					blocked++
				}
			}
			e.o.Log.Debug("quota evaluation", "organizations", len(sts), "ingest_blocked", blocked)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.o.Interval):
		}
	}
}
