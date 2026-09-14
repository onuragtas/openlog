package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Usage configures usage metering, plans, quota enforcement and billing (docs/contracts/usage.md, D-079..D-081).
type Usage struct {
	// SaaSMode enables hard enforcement: ingest 429 over the plan's monthly ingest limit, per-tenant ingest rate
	// limits and per-tenant retention (OPENLOG_SAAS_MODE). Self-hosted default: false (metering and soft warnings only).
	SaaSMode bool
	// PlansJSON is the plan catalog (OPENLOG_PLANS inline, or the content of OPENLOG_PLANS_FILE). Empty: one
	// unlimited plan. Parsed by internal/quota.
	PlansJSON string
	// PlansFile is OPENLOG_PLANS_FILE (read at startup).
	PlansFile string
	// DefaultPlan is the plan of organizations without an assignment (OPENLOG_DEFAULT_PLAN; empty = first plan).
	DefaultPlan string
	// SuperadminEmails may assign plans and overrides to any organization (OPENLOG_SUPERADMIN_EMAILS).
	SuperadminEmails []string
	// EvaluationInterval is how often the api leader evaluates quotas (OPENLOG_USAGE_EVALUATION_INTERVAL).
	EvaluationInterval time.Duration
	// QueryCollection collects query compute from system.query_log (OPENLOG_USAGE_QUERY_COLLECTION_ENABLED).
	QueryCollection bool
	// NotifyThresholds are the percentages that trigger usage e-mails to owners (OPENLOG_USAGE_NOTIFY_THRESHOLDS).
	NotifyThresholds []int
	// QuotaRefreshInterval is how often ingest reloads tenant quota status (OPENLOG_QUOTA_REFRESH_INTERVAL).
	QuotaRefreshInterval time.Duration
	// QuotaIngestPods divides per-tenant rate limits among ingest pods; 0 = live ingest instances from
	// component_heartbeats (OPENLOG_QUOTA_INGEST_PODS).
	QuotaIngestPods int
	// QuotaBlockedRetryAfter is the Retry-After of ingest requests rejected because the monthly quota is used up
	// (OPENLOG_QUOTA_BLOCKED_RETRY_AFTER).
	QuotaBlockedRetryAfter time.Duration
	// RetentionEnabled runs the per-tenant retention deletions (OPENLOG_QUOTA_RETENTION_ENABLED; default SaaSMode).
	RetentionEnabled bool
	// RetentionMaxMutations bounds ALTER … DELETE mutations submitted per run (OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS).
	RetentionMaxMutations int
	// BillingProvider selects the billing integration: none (default) or noop (OPENLOG_BILLING_PROVIDER).
	BillingProvider string
	// BillingPushAt is the daily usage push time, HH:MM UTC (OPENLOG_BILLING_PUSH_AT).
	BillingPushAt string
}

func loadUsage(p *parser) Usage {
	saas := p.bool("OPENLOG_SAAS_MODE", false)
	u := Usage{
		SaaSMode:               saas,
		PlansJSON:              p.str("OPENLOG_PLANS", ""),
		PlansFile:              p.str("OPENLOG_PLANS_FILE", ""),
		DefaultPlan:            p.str("OPENLOG_DEFAULT_PLAN", ""),
		EvaluationInterval:     p.duration("OPENLOG_USAGE_EVALUATION_INTERVAL", time.Minute),
		QueryCollection:        p.bool("OPENLOG_USAGE_QUERY_COLLECTION_ENABLED", true),
		QuotaRefreshInterval:   p.duration("OPENLOG_QUOTA_REFRESH_INTERVAL", 30*time.Second),
		QuotaIngestPods:        int(p.int64("OPENLOG_QUOTA_INGEST_PODS", 0)),
		QuotaBlockedRetryAfter: p.duration("OPENLOG_QUOTA_BLOCKED_RETRY_AFTER", 5*time.Minute),
		RetentionEnabled:       p.bool("OPENLOG_QUOTA_RETENTION_ENABLED", saas),
		RetentionMaxMutations:  int(p.int64("OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS", 20)),
		BillingProvider:        p.str("OPENLOG_BILLING_PROVIDER", "none"),
		BillingPushAt:          p.str("OPENLOG_BILLING_PUSH_AT", "02:00"),
	}
	for _, e := range p.list("OPENLOG_SUPERADMIN_EMAILS", "") {
		u.SuperadminEmails = append(u.SuperadminEmails, strings.ToLower(e))
	}
	for _, s := range p.list("OPENLOG_USAGE_NOTIFY_THRESHOLDS", "80,100") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_USAGE_NOTIFY_THRESHOLDS: %q must be an integer percentage between 1 and 1000", s))
			continue
		}
		u.NotifyThresholds = append(u.NotifyThresholds, n)
	}
	return u
}

// IsSuperadmin reports whether email is in OPENLOG_SUPERADMIN_EMAILS (case-insensitive).
func (u Usage) IsSuperadmin(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, e := range u.SuperadminEmails {
		if e == email {
			return true
		}
	}
	return false
}

// BillingPushOffset returns OPENLOG_BILLING_PUSH_AT as the offset from 00:00 UTC.
func (u Usage) BillingPushOffset() time.Duration {
	t, err := time.Parse("15:04", u.BillingPushAt)
	if err != nil {
		return 2 * time.Hour
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

func (c Config) validateUsage() []error {
	var errs []error
	u := c.Usage
	if u.PlansJSON != "" && u.PlansFile != "" {
		errs = append(errs, errors.New("OPENLOG_PLANS and OPENLOG_PLANS_FILE are mutually exclusive"))
	}
	if u.EvaluationInterval < 10*time.Second || u.EvaluationInterval > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_USAGE_EVALUATION_INTERVAL: must be between 10s and 1h, got %s", u.EvaluationInterval))
	}
	if u.QuotaRefreshInterval < time.Second || u.QuotaRefreshInterval > 10*time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_QUOTA_REFRESH_INTERVAL: must be between 1s and 10m, got %s", u.QuotaRefreshInterval))
	}
	if u.QuotaIngestPods < 0 {
		errs = append(errs, errors.New("OPENLOG_QUOTA_INGEST_PODS must be >= 0"))
	}
	if u.QuotaBlockedRetryAfter < time.Second || u.QuotaBlockedRetryAfter > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_QUOTA_BLOCKED_RETRY_AFTER: must be between 1s and 24h, got %s", u.QuotaBlockedRetryAfter))
	}
	if u.RetentionMaxMutations < 1 || u.RetentionMaxMutations > 1000 {
		errs = append(errs, fmt.Errorf("OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS: must be between 1 and 1000, got %d", u.RetentionMaxMutations))
	}
	switch u.BillingProvider {
	case "none", "noop":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_BILLING_PROVIDER: must be none or noop, got %q (docs/operations/saas.md: adding a provider)", u.BillingProvider))
	}
	if _, err := time.Parse("15:04", u.BillingPushAt); err != nil {
		errs = append(errs, fmt.Errorf("OPENLOG_BILLING_PUSH_AT: must be HH:MM (UTC), got %q", u.BillingPushAt))
	}
	if u.SaaSMode && c.AuthMode != "postgres" {
		errs = append(errs, errors.New("OPENLOG_SAAS_MODE=true requires OPENLOG_AUTH_MODE=postgres"))
	}
	return errs
}
