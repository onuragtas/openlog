package operator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

// Abuse detection (SaaS mode, D-106): a leader job flags organizations with anomalous ingest for the operator console.
// Flags never suspend unless OPENLOG_SAAS_AUTO_SUSPEND=true.

// AbuseUsage is the usage data the detector reads (usage.Reader).
type AbuseUsage interface {
	IngestSince(ctx context.Context, from, to time.Time) (map[string]uint64, error)
	AllTenants(ctx context.Context, from, to time.Time) (map[string]usage.TenantPeriod, error)
}

// HoursPerMonth converts a monthly ingest limit into the hourly share (365 × 24 / 12).
const HoursPerMonth = 730

// AbuseDetector is the api leader job.
type AbuseDetector struct {
	Store   Store
	Plans   PlanLister
	Catalog *quota.Catalog
	Usage   AbuseUsage
	Config  config.SaaS
	Log     *slog.Logger
	Now     func() time.Time
	// Suspended is called after an automatic suspension (cache invalidation); may be nil.
	Suspended func(ctx context.Context)
}

// Finding is one flag the detector raises.
type Finding struct {
	OrgID   string
	Kind    string
	Details map[string]any
}

// AbuseInput is what Evaluate needs about one organization.
type AbuseInput struct {
	OrgID, TenantID string
	Plan            quota.Plan
	CreatedAt       time.Time
	HourIngestBytes uint64
	ActiveHosts     uint64
	SourceIPs       int
}

// EvaluateAbuse returns the findings for one organization at now under cfg.
func EvaluateAbuse(in AbuseInput, cfg config.SaaS, now time.Time) []Finding {
	var out []Finding
	if m := cfg.AbuseIngestMultiplier; m > 0 && in.Plan.Limits.IngestGBMonth > 0 {
		hourly := in.Plan.Limits.IngestGBMonth * quota.GiB / HoursPerMonth
		if threshold := hourly * m; float64(in.HourIngestBytes) > threshold {
			out = append(out, Finding{in.OrgID, FlagIngestSpike, map[string]any{"hour_ingest_bytes": in.HourIngestBytes,
				"threshold_bytes": int64(math.Round(threshold)), "multiplier": m, "plan_id": in.Plan.ID}})
		}
	}
	age := now.Sub(in.CreatedAt)
	if cfg.AbuseNewOrgHosts > 0 && age < time.Duration(cfg.AbuseNewOrgDays)*24*time.Hour && in.ActiveHosts > uint64(cfg.AbuseNewOrgHosts) {
		out = append(out, Finding{in.OrgID, FlagNewOrgHosts, map[string]any{"active_hosts": in.ActiveHosts, "threshold": cfg.AbuseNewOrgHosts,
			"org_age_hours": int64(age.Hours())}})
	}
	if cfg.AbuseSourceIPs > 0 && in.SourceIPs > cfg.AbuseSourceIPs {
		out = append(out, Finding{in.OrgID, FlagIngestSourceIPs, map[string]any{"distinct_source_ips": in.SourceIPs, "threshold": cfg.AbuseSourceIPs}})
	}
	return out
}

// OrgCreatedAt returns every organization's creation time.
func (s Store) OrgCreatedAt(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, created_at FROM organizations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// SourceIPCounts returns, per tenant, the largest distinct client address count of one instance in an hour >= since.
func (s Store) SourceIPCounts(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT tenant_id, max(distinct_ips) FROM ingest_source_counts WHERE hour >= $1 GROUP BY tenant_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM ingest_source_counts WHERE hour < now() - interval '48 hours'`)
	return out, err
}

// RunOnce evaluates every organization and returns the number of new flags.
func (d *AbuseDetector) RunOnce(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	orgs, err := d.Plans.ListOrgPlans(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	created, err := d.Store.OrgCreatedAt(ctx)
	if err != nil {
		return 0, err
	}
	hour := now.Truncate(time.Hour)
	prev, err := d.Usage.IngestSince(ctx, hour.Add(-time.Hour), hour)
	if err != nil {
		return 0, err
	}
	cur, err := d.Usage.IngestSince(ctx, hour, hour.Add(time.Hour))
	if err != nil {
		return 0, err
	}
	tenants, err := d.Usage.AllTenants(ctx, usage.PeriodOf(now).Start, hour.Add(time.Hour))
	if err != nil {
		return 0, err
	}
	ips, err := d.Store.SourceIPCounts(ctx, hour.Add(-time.Hour))
	if err != nil {
		return 0, err
	}
	raised := 0
	var errs []error
	for _, op := range orgs {
		in := AbuseInput{OrgID: op.OrgID, TenantID: op.TenantID, Plan: d.Catalog.EffectivePlan(op), CreatedAt: created[op.OrgID],
			HourIngestBytes: max(prev[op.TenantID], cur[op.TenantID]), ActiveHosts: tenants[op.TenantID].ActiveHosts, SourceIPs: ips[op.TenantID]}
		for _, f := range EvaluateAbuse(in, d.Config, now) {
			id, isNew, err := d.Store.RaiseFlag(ctx, f.OrgID, f.Kind, f.Details)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !isNew {
				continue
			}
			raised++
			log.Warn("organization flagged", "org_id", f.OrgID, "tenant_id", op.TenantID, "kind", f.Kind, "details", f.Details)
			if d.Config.AutoSuspend {
				l, err := d.Store.GetLifecycle(ctx, f.OrgID)
				if err != nil || l.Suspended {
					continue
				}
				if _, err := d.Store.Suspend(ctx, f.OrgID, "automatic suspension: abuse flag "+f.Kind, Actor{Email: "system:abuse-detector"}, true); err != nil {
					errs = append(errs, err)
					continue
				}
				_ = d.Store.MarkAutoSuspended(ctx, id)
				log.Warn("organization suspended automatically (OPENLOG_SAAS_AUTO_SUSPEND)", "org_id", f.OrgID, "kind", f.Kind)
				if d.Suspended != nil {
					d.Suspended(ctx)
				}
			}
		}
	}
	return raised, errors.Join(errs...)
}

// Run evaluates every Config.AbuseInterval until ctx is done (leader task).
func (d *AbuseDetector) Run(ctx context.Context) {
	runEvery(ctx, d.Config.AbuseInterval, d.Log, "abuse detection", func(ctx context.Context) error {
		_, err := d.RunOnce(ctx)
		return err
	})
}
