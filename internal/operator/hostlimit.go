package operator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

// Hard host limits (SaaS mode, D-105). The api leader writes, for every tenant whose effective plan limits hosts, the
// limit and the hosts active on the current or previous UTC day (the window of the hosts metric) into
// tenant_host_limits / tenant_known_hosts. Ingest pods load both (HostLimiter): known hosts always keep reporting (also
// above the limit after a downgrade); a host id not known yet is admitted only while known + newly admitted hosts of
// this pod are below the limit, otherwise its data is rejected with 429 quota_exceeded.

// HostLimitState is the host limit of one tenant as loaded by ingest.
type HostLimitState struct {
	Limit int64
	Known map[string]struct{}
}

// LoadHostLimits returns the host limits evaluated within the last hour with their known hosts.
func (s Store) LoadHostLimits(ctx context.Context) (map[string]*HostLimitState, error) {
	rows, err := s.Pool.Query(ctx, `SELECT l.tenant_id, l.host_limit, k.host_id FROM tenant_host_limits l
		LEFT JOIN tenant_known_hosts k ON k.tenant_id = l.tenant_id WHERE l.evaluated_at > now() - interval '1 hour'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*HostLimitState{}
	for rows.Next() {
		var tenant string
		var limit int64
		var host *string
		if err := rows.Scan(&tenant, &limit, &host); err != nil {
			return nil, err
		}
		st := out[tenant]
		if st == nil {
			st = &HostLimitState{Limit: limit, Known: map[string]struct{}{}}
			out[tenant] = st
		}
		if host != nil {
			st.Known[*host] = struct{}{}
		}
	}
	return out, rows.Err()
}

// SyncHostLimits replaces the host limits with limits (tenant -> limit) and merges the active hosts; hosts not seen
// since minDay and tenants without a limit are removed.
func (s Store) SyncHostLimits(ctx context.Context, limits map[string]int64, hosts map[string][]usage.HostSeen, minDay time.Time) error {
	tenants := make([]string, 0, len(limits))
	lims := make([]int64, 0, len(limits))
	active := make([]int64, 0, len(limits))
	var kt, kh []string
	var kd []time.Time
	for t, l := range limits {
		tenants, lims, active = append(tenants, t), append(lims, l), append(active, int64(len(hosts[t])))
		for _, h := range hosts[t] {
			if h.HostID == "" || len(h.HostID) > 512 {
				continue
			}
			kt, kh, kd = append(kt, t), append(kh, h.HostID), append(kd, h.LastSeen)
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM tenant_host_limits WHERE NOT (tenant_id = ANY($1::text[]))`, tenants); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_host_limits (tenant_id, host_limit, active_hosts, evaluated_at)
			SELECT t, l, a, now() FROM unnest($1::text[], $2::bigint[], $3::bigint[]) AS u(t, l, a) JOIN organizations o ON o.tenant_id = u.t
			ON CONFLICT (tenant_id) DO UPDATE SET host_limit = EXCLUDED.host_limit, active_hosts = EXCLUDED.active_hosts, evaluated_at = now()`,
			tenants, lims, active); err != nil {
			return err
		}
		if len(kt) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO tenant_known_hosts (tenant_id, host_id, last_seen)
				SELECT t, h, d FROM unnest($1::text[], $2::text[], $3::date[]) AS u(t, h, d) JOIN organizations o ON o.tenant_id = u.t
				ON CONFLICT (tenant_id, host_id) DO UPDATE SET last_seen = greatest(tenant_known_hosts.last_seen, EXCLUDED.last_seen)`,
				kt, kh, kd); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM tenant_known_hosts WHERE last_seen < $1 OR NOT (tenant_id = ANY($2::text[]))`,
			minDay.UTC().Format(time.DateOnly), tenants)
		return err
	})
}

// HostSyncUsage returns active host ids (usage.Reader).
type HostSyncUsage interface {
	ActiveHostIDs(ctx context.Context, tenants []string, at time.Time) (map[string][]usage.HostSeen, error)
}

// PlanLister lists organizations with their plan assignment (quota.PGStore).
type PlanLister interface {
	ListOrgPlans(ctx context.Context) ([]quota.OrgPlan, error)
}

// HostSync is the api leader job writing host limits (SaaS mode).
type HostSync struct {
	Catalog  *quota.Catalog
	Plans    PlanLister
	Usage    HostSyncUsage
	Store    Store
	Interval time.Duration
	Log      *slog.Logger
	Now      func() time.Time
}

// RunOnce synchronizes the host limits and returns the number of limited tenants.
func (j *HostSync) RunOnce(ctx context.Context) (int, error) {
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	orgs, err := j.Plans.ListOrgPlans(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	limits := map[string]int64{}
	var tenants []string
	for _, op := range orgs {
		if l := j.Catalog.EffectivePlan(op).Limits.Hosts; l > 0 {
			limits[op.TenantID] = l
			tenants = append(tenants, op.TenantID)
		}
	}
	t := now().UTC()
	hosts, err := j.Usage.ActiveHostIDs(ctx, tenants, t)
	if err != nil {
		return 0, err
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return len(limits), j.Store.SyncHostLimits(ctx, limits, hosts, day.AddDate(0, 0, -1))
}

// Run synchronizes every Interval until ctx is done (leader task).
func (j *HostSync) Run(ctx context.Context) {
	runEvery(ctx, j.Interval, j.Log, "host limit sync", func(ctx context.Context) error {
		_, err := j.RunOnce(ctx)
		return err
	})
}

// runEvery runs fn now and then every interval until ctx is done, logging failures.
func runEvery(ctx context.Context, interval time.Duration, log *slog.Logger, what string, fn func(ctx context.Context) error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if interval <= 0 {
		interval = time.Minute
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, max(interval, 2*time.Minute))
		err := fn(rctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn(what+" failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
