package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

// Billing metrics pushed daily.
const (
	MetricIngestGB = "ingest_gb"
	MetricHosts    = "hosts"
	MetricUsers    = "users"
)

// PushRecord is a billing_usage_pushes row.
type PushRecord struct {
	IdempotencyKey string
	OrgID          string
	Provider       string
	Metric         string
	Day            time.Time
	Quantity       float64
}

// PushStore is the bookkeeping of pushes (PGStore).
type PushStore interface {
	// ClaimPush inserts a pending record, or re-claims a failed one with fewer than maxAttempts attempts. It returns
	// false when the record was pushed already, is being pushed, or ran out of attempts.
	ClaimPush(ctx context.Context, r PushRecord, maxAttempts int) (bool, error)
	FinishPush(ctx context.Context, key string, pushErr error) error
}

// OrgSource lists organizations with billing ids and member counts (quota.PGStore).
type OrgSource interface {
	ListOrgPlans(ctx context.Context) ([]quota.OrgPlan, error)
	MemberCounts(ctx context.Context) (map[string]int64, error)
}

// DayUsage returns every tenant's usage of one UTC day (usage.Reader).
type DayUsage interface {
	DayAllTenants(ctx context.Context, day time.Time) (map[string]usage.DayTotals, error)
}

// PushJob pushes the previous days' usage of every organization connected to the provider.
type PushJob struct {
	Provider    Provider
	Orgs        OrgSource
	Usage       DayUsage
	Store       PushStore
	At          time.Duration // offset from 00:00 UTC of the daily run
	CatchUpDays int           // days before yesterday retried on every run (default 3)
	MaxAttempts int           // default 5
	Log         *slog.Logger
	Now         func() time.Time
}

// IdempotencyKey is the key of (org, day, metric).
func IdempotencyKey(orgID string, day time.Time, metric string) string {
	return fmt.Sprintf("openlog:%s:%s:%s", orgID, day.UTC().Format(time.DateOnly), metric)
}

// PushDay pushes one UTC day and returns the pushed and failed record counts.
func (j *PushJob) PushDay(ctx context.Context, day time.Time) (pushed, failed int, err error) {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	orgs, err := j.Orgs.ListOrgPlans(ctx)
	if err != nil {
		return 0, 0, err
	}
	members, err := j.Orgs.MemberCounts(ctx)
	if err != nil {
		return 0, 0, err
	}
	totals, err := j.Usage.DayAllTenants(ctx, day)
	if err != nil {
		return 0, 0, err
	}
	maxAttempts := j.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	for _, op := range orgs {
		if op.BillingCustomerID == "" || op.BillingProvider != j.Provider.Name() {
			continue
		}
		t := totals[op.TenantID]
		for _, m := range []struct {
			name string
			qty  float64
		}{
			{MetricIngestGB, float64(t.IngestBytes) / quota.GiB},
			{MetricHosts, float64(t.Hosts)},
			{MetricUsers, float64(members[op.OrgID])},
		} {
			rec := PushRecord{IdempotencyKey: IdempotencyKey(op.OrgID, day, m.name), OrgID: op.OrgID, Provider: j.Provider.Name(),
				Metric: m.name, Day: day, Quantity: m.qty}
			ok, err := j.Store.ClaimPush(ctx, rec, maxAttempts)
			if err != nil {
				return pushed, failed, err
			}
			if !ok {
				continue
			}
			perr := j.Provider.PushUsage(ctx, UsageRecord{CustomerID: op.BillingCustomerID, SubscriptionID: op.BillingSubscriptionID,
				Metric: m.name, Quantity: m.qty, Day: day, IdempotencyKey: rec.IdempotencyKey})
			if err := j.Store.FinishPush(ctx, rec.IdempotencyKey, perr); err != nil {
				return pushed, failed, err
			}
			if perr != nil {
				failed++
				if j.Log != nil {
					j.Log.Warn("billing usage push failed", "org_id", op.OrgID, "metric", m.name, "day", day.Format(time.DateOnly), "err", perr)
				}
			} else {
				pushed++
			}
		}
	}
	return pushed, failed, nil
}

// Run pushes yesterday and the catch-up days at start and then daily at At until ctx is done (leader task).
func (j *PushJob) Run(ctx context.Context) {
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	catchUp := j.CatchUpDays
	if catchUp <= 0 {
		catchUp = 3
	}
	for {
		today := now().UTC().Truncate(24 * time.Hour)
		for i := catchUp; i >= 1; i-- {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			p, f, err := j.PushDay(rctx, today.AddDate(0, 0, -i))
			cancel()
			if j.Log != nil {
				if err != nil && ctx.Err() == nil {
					j.Log.Warn("billing usage push run failed", "err", err)
				} else if p+f > 0 {
					j.Log.Info("billing usage pushed", "day", today.AddDate(0, 0, -i).Format(time.DateOnly), "pushed", p, "failed", f)
				}
			}
		}
		next := today.Add(j.At)
		if !next.After(now()) {
			next = next.AddDate(0, 0, 1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
	}
}
