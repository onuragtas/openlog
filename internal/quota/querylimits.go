package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/config"
)

// Per-organization query limits (docs/contracts/usage.md §4.5, migration 0051_org_query_limits).
//
// Precedence of a tenant's ClickHouse query limits, per setting, lowest to highest:
//
//	OPENLOG_QUERY_* defaults < plan limits.query (org_plans overrides applied, values > 0) < org_query_limits (non-NULL)
//	< OPENLOG_QUERY_TENANT_LIMITS (the operator's environment override replaces all three settings)

// Sources of an effective query limit.
const (
	QueryLimitSourceDefault      = "default"
	QueryLimitSourcePlan         = "plan"
	QueryLimitSourceOrganization = "organization"
	QueryLimitSourceEnvironment  = "environment"
)

// maxQueryLimit bounds stored values (ClickHouse settings are UInt64; keep them far from overflow).
const maxQueryLimit = int64(1) << 62

// OrgQueryLimits is an organization's query limit setting. nil fields inherit the plan or the defaults; 0 means openlog
// does not set the ClickHouse setting.
type OrgQueryLimits struct {
	MaxMemoryUsage *int64 `json:"max_memory_usage"`
	MaxRowsToRead  *int64 `json:"max_rows_to_read"`
	MaxBytesToRead *int64 `json:"max_bytes_to_read"`
}

// Empty reports whether no setting is set.
func (o OrgQueryLimits) Empty() bool {
	return o.MaxMemoryUsage == nil && o.MaxRowsToRead == nil && o.MaxBytesToRead == nil
}

// Validate checks the values (0 … 2^62).
func (o OrgQueryLimits) Validate() error {
	for name, v := range map[string]*int64{"max_memory_usage": o.MaxMemoryUsage, "max_rows_to_read": o.MaxRowsToRead, "max_bytes_to_read": o.MaxBytesToRead} {
		if v != nil && (*v < 0 || *v > maxQueryLimit) {
			return fmt.Errorf("%s must be between 0 and %d", name, maxQueryLimit)
		}
	}
	return nil
}

// StoredOrgQueryLimits is an org_query_limits row.
type StoredOrgQueryLimits struct {
	OrgQueryLimits
	UpdatedAt      time.Time
	UpdatedByEmail string
}

// QueryLimitSources names the layer each effective setting comes from.
type QueryLimitSources struct {
	MaxMemoryUsage string `json:"max_memory_usage"`
	MaxRowsToRead  string `json:"max_rows_to_read"`
	MaxBytesToRead string `json:"max_bytes_to_read"`
}

// ResolveQueryLimits applies the layers to defaults. env is the tenant's OPENLOG_QUERY_TENANT_LIMITS entry (nil: none).
func ResolveQueryLimits(defaults config.QueryLimits, plan QueryLimits, org OrgQueryLimits, env *config.QueryLimits) (config.QueryLimits, QueryLimitSources) {
	out, src := defaults, QueryLimitSources{QueryLimitSourceDefault, QueryLimitSourceDefault, QueryLimitSourceDefault}
	layer := func(v *int64, s *string, planV int64, orgV *int64, envV int64) {
		if planV > 0 {
			*v, *s = planV, QueryLimitSourcePlan
		}
		if orgV != nil {
			*v, *s = *orgV, QueryLimitSourceOrganization
		}
		if env != nil {
			*v, *s = envV, QueryLimitSourceEnvironment
		}
	}
	var envL config.QueryLimits
	if env != nil {
		envL = *env
	}
	layer(&out.MaxMemoryUsage, &src.MaxMemoryUsage, plan.MaxMemoryUsage, org.MaxMemoryUsage, envL.MaxMemoryUsage)
	layer(&out.MaxRowsToRead, &src.MaxRowsToRead, plan.MaxRowsToRead, org.MaxRowsToRead, envL.MaxRowsToRead)
	layer(&out.MaxBytesToRead, &src.MaxBytesToRead, plan.MaxBytesToRead, org.MaxBytesToRead, envL.MaxBytesToRead)
	return out, src
}

// GetOrgQueryLimits returns the organization's setting (found=false: no row).
func (s PGStore) GetOrgQueryLimits(ctx context.Context, orgID string) (StoredOrgQueryLimits, bool, error) {
	var l StoredOrgQueryLimits
	err := s.Pool.QueryRow(ctx, `SELECT l.max_memory_usage, l.max_rows_to_read, l.max_bytes_to_read, l.updated_at, coalesce(u.email, '')
		FROM org_query_limits l LEFT JOIN users u ON u.id = l.updated_by WHERE l.org_id = $1::uuid`, orgID).
		Scan(&l.MaxMemoryUsage, &l.MaxRowsToRead, &l.MaxBytesToRead, &l.UpdatedAt, &l.UpdatedByEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, false, nil
	}
	return l, err == nil, err
}

// PutOrgQueryLimits stores the setting (an empty one deletes the row) and writes audit event query_limits.update.
func (s PGStore) PutOrgQueryLimits(ctx context.Context, orgID string, l OrgQueryLimits, actor Actor) error {
	if err := l.Validate(); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID any
	if uuidRe.MatchString(actor.UserID) {
		actorID = actor.UserID
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrOrgNotFound
	}
	action := "query_limits.update"
	if l.Empty() {
		action = "query_limits.delete"
		if _, err := tx.Exec(ctx, `DELETE FROM org_query_limits WHERE org_id = $1::uuid`, orgID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO org_query_limits (org_id, max_memory_usage, max_rows_to_read, max_bytes_to_read, updated_by, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, now())
		ON CONFLICT (org_id) DO UPDATE SET max_memory_usage = EXCLUDED.max_memory_usage, max_rows_to_read = EXCLUDED.max_rows_to_read,
			max_bytes_to_read = EXCLUDED.max_bytes_to_read, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		orgID, l.MaxMemoryUsage, l.MaxRowsToRead, l.MaxBytesToRead, actorID); err != nil {
		return err
	}
	details, _ := json.Marshal(l)
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'organization', $1, $5::jsonb, $6)`,
		orgID, actorID, actor.Email, action, string(details), actor.IP); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListOrgQueryLimits returns every stored setting by tenant id.
func (s PGStore) ListOrgQueryLimits(ctx context.Context) (map[string]OrgQueryLimits, error) {
	rows, err := s.Pool.Query(ctx, `SELECT o.tenant_id, l.max_memory_usage, l.max_rows_to_read, l.max_bytes_to_read
		FROM org_query_limits l JOIN organizations o ON o.id = l.org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]OrgQueryLimits{}
	for rows.Next() {
		var tenant string
		var l OrgQueryLimits
		if err := rows.Scan(&tenant, &l.MaxMemoryUsage, &l.MaxRowsToRead, &l.MaxBytesToRead); err != nil {
			return nil, err
		}
		out[tenant] = l
	}
	return out, rows.Err()
}

// QueryLimitsStore is what QueryLimitsResolver reads (PGStore).
type QueryLimitsStore interface {
	ListOrgPlans(ctx context.Context) ([]OrgPlan, error)
	ListOrgQueryLimits(ctx context.Context) (map[string]OrgQueryLimits, error)
}

// DefaultQueryLimitsRefresh is how often every api/alert pod reloads plan assignments and organization settings.
const DefaultQueryLimitsRefresh = 30 * time.Second

// QueryLimitsResolver caches the plan and organization layers of every tenant and resolves the effective limits of
// each query without touching PostgreSQL (query.DB.SetLimitsFunc). Until the first successful refresh, and for
// tenants whose layers could not be loaded, the environment limits apply.
type QueryLimitsResolver struct {
	Query   config.Query
	Catalog *Catalog // nil: no plan layer
	Store   QueryLimitsStore
	// Refresh is the reload interval (default DefaultQueryLimitsRefresh).
	Refresh time.Duration
	Log     *slog.Logger

	mu   sync.RWMutex
	plan map[string]QueryLimits
	org  map[string]OrgQueryLimits
}

// Load reloads the layers from the store; on error the previous layers stay in use.
func (r *QueryLimitsResolver) Load(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	plan := map[string]QueryLimits{}
	if r.Catalog != nil {
		orgs, err := r.Store.ListOrgPlans(rctx)
		if err != nil {
			return fmt.Errorf("plan query limits: %w", err)
		}
		for _, op := range orgs {
			if q := r.Catalog.EffectivePlan(op).Limits.Query; q != (QueryLimits{}) {
				plan[op.TenantID] = q
			}
		}
	}
	org, err := r.Store.ListOrgQueryLimits(rctx)
	if err != nil {
		return fmt.Errorf("organization query limits: %w", err)
	}
	r.mu.Lock()
	r.plan, r.org = plan, org
	r.mu.Unlock()
	return nil
}

// Run reloads every Refresh until ctx is done.
func (r *QueryLimitsResolver) Run(ctx context.Context) {
	every := r.Refresh
	if every <= 0 {
		every = DefaultQueryLimitsRefresh
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Load(ctx); err != nil && ctx.Err() == nil && r.Log != nil {
				r.Log.Warn("cannot reload query limits; keeping the previous ones", "err", err)
			}
		}
	}
}

// Limits returns the effective limits of tenant (query.DB.SetLimitsFunc).
func (r *QueryLimitsResolver) Limits(tenant string) config.QueryLimits {
	r.mu.RLock()
	plan, org := r.plan[tenant], r.org[tenant]
	r.mu.RUnlock()
	var env *config.QueryLimits
	if l, ok := r.Query.Tenants[tenant]; ok {
		env = &l
	}
	l, _ := ResolveQueryLimits(r.Query.Defaults, plan, org, env)
	return l
}
