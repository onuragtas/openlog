package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrOrgNotFound is returned for an unknown organization.
var ErrOrgNotFound = errors.New("organization not found")

// OrgPlan is an organization with its plan assignment (Assigned=false: the default plan, no row).
type OrgPlan struct {
	OrgID                 string
	TenantID              string
	OrgName               string
	Assigned              bool
	PlanID                string
	Overrides             Overrides
	BillingProvider       string
	BillingCustomerID     string
	BillingSubscriptionID string
	Note                  string
	UpdatedAt             *time.Time
	UpdatedByEmail        string
}

// Actor is who changes a plan (audit log).
type Actor struct {
	UserID string // empty for system changes (billing webhooks)
	Email  string
	IP     string
}

// StoredStatus is a tenant_quota_status row.
type StoredStatus struct {
	Status
	TenantID    string
	OrgID       string
	PeriodStart time.Time
	IngestBytes int64
	EvaluatedAt time.Time
}

// PGStore is the PostgreSQL persistence of plans, quota status, notifications and retention bookkeeping.
type PGStore struct{ Pool *pgxpool.Pool }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const orgPlanSelect = `SELECT o.id::text, o.tenant_id, o.name, p.org_id IS NOT NULL, coalesce(p.plan_id, ''), coalesce(p.overrides, '{}'::jsonb),
	coalesce(p.billing_provider, ''), coalesce(p.billing_customer_id, ''), coalesce(p.billing_subscription_id, ''), coalesce(p.note, ''),
	p.updated_at, coalesce(u.email, '')
FROM organizations o LEFT JOIN org_plans p ON p.org_id = o.id LEFT JOIN users u ON u.id = p.updated_by`

func scanOrgPlan(row pgx.Row) (OrgPlan, error) {
	var op OrgPlan
	var overrides []byte
	if err := row.Scan(&op.OrgID, &op.TenantID, &op.OrgName, &op.Assigned, &op.PlanID, &overrides, &op.BillingProvider,
		&op.BillingCustomerID, &op.BillingSubscriptionID, &op.Note, &op.UpdatedAt, &op.UpdatedByEmail); err != nil {
		return op, err
	}
	if len(overrides) > 0 {
		if err := json.Unmarshal(overrides, &op.Overrides); err != nil {
			return op, fmt.Errorf("org %s overrides: %w", op.OrgID, err)
		}
	}
	return op, nil
}

// FindOrgPlan returns the organization with id or tenant id ref and its assignment.
func (s PGStore) FindOrgPlan(ctx context.Context, ref string) (OrgPlan, error) {
	var row pgx.Row
	if uuidRe.MatchString(ref) {
		row = s.Pool.QueryRow(ctx, orgPlanSelect+` WHERE o.id = $1::uuid`, ref)
	} else {
		row = s.Pool.QueryRow(ctx, orgPlanSelect+` WHERE o.tenant_id = $1`, ref)
	}
	op, err := scanOrgPlan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return op, ErrOrgNotFound
	}
	return op, err
}

// ListOrgPlans returns every organization with its assignment.
func (s PGStore) ListOrgPlans(ctx context.Context) ([]OrgPlan, error) {
	rows, err := s.Pool.Query(ctx, orgPlanSelect+` ORDER BY o.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgPlan
	for rows.Next() {
		op, err := scanOrgPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// PutOrgPlan assigns op.PlanID and op.Overrides (and billing ids) to op.OrgID and writes audit event plan.update.
func (s PGStore) PutOrgPlan(ctx context.Context, op OrgPlan, actor Actor) error {
	overrides, err := json.Marshal(op.Overrides)
	if err != nil {
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
	tag, err := tx.Exec(ctx, `INSERT INTO org_plans (org_id, plan_id, overrides, billing_provider, billing_customer_id, billing_subscription_id, note, updated_by, updated_at)
		SELECT o.id, $2, $3::jsonb, $4, $5, $6, $7, $8::uuid, now() FROM organizations o WHERE o.id = $1::uuid
		ON CONFLICT (org_id) DO UPDATE SET plan_id = EXCLUDED.plan_id, overrides = EXCLUDED.overrides, billing_provider = EXCLUDED.billing_provider,
			billing_customer_id = EXCLUDED.billing_customer_id, billing_subscription_id = EXCLUDED.billing_subscription_id,
			note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		op.OrgID, op.PlanID, string(overrides), op.BillingProvider, op.BillingCustomerID, op.BillingSubscriptionID, op.Note, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOrgNotFound
	}
	details, _ := json.Marshal(map[string]any{"plan_id": op.PlanID, "overrides": op.Overrides, "billing_provider": op.BillingProvider,
		"billing_customer_id": op.BillingCustomerID, "billing_subscription_id": op.BillingSubscriptionID, "note": op.Note})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
		VALUES ($1::uuid, $2::uuid, $3, 'plan.update', 'organization', $4, $5::jsonb, $6)`,
		op.OrgID, actorID, actor.Email, op.OrgID, string(details), actor.IP); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FindOrgByBillingCustomer returns the organization connected to a provider customer.
func (s PGStore) FindOrgByBillingCustomer(ctx context.Context, provider, customerID string) (OrgPlan, error) {
	op, err := scanOrgPlan(s.Pool.QueryRow(ctx, orgPlanSelect+` WHERE p.billing_provider = $1 AND p.billing_customer_id = $2`, provider, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return op, ErrOrgNotFound
	}
	return op, err
}

// MemberCounts returns the number of members of every organization with members (by org id).
func (s PGStore) MemberCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT m.org_id::text, count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE u.disabled_at IS NULL GROUP BY m.org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Owners returns the e-mail addresses and e-mail languages of the organization's enabled owners. Language: the owner's
// preference, the organization's default, the language stored when the account was created (D-095).
func (s PGStore) Owners(ctx context.Context, orgID string) ([]Owner, error) {
	rows, err := s.Pool.Query(ctx, `SELECT u.email,
		       CASE WHEN u.locale_explicit AND u.locale <> '' THEN u.locale WHEN o.locale <> '' THEN o.locale ELSE u.locale END
		FROM memberships m JOIN users u ON u.id = m.user_id JOIN organizations o ON o.id = m.org_id
		WHERE m.org_id = $1::uuid AND m.role = 'owner' AND u.disabled_at IS NULL ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Owner
	for rows.Next() {
		var o Owner
		if err := rows.Scan(&o.Email, &o.Locale); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SaveStatuses upserts the evaluation of every organization in one transaction.
func (s PGStore) SaveStatuses(ctx context.Context, sts []StoredStatus) error {
	if len(sts) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, st := range sts {
		metrics, err := json.Marshal(st.Metrics)
		if err != nil {
			return err
		}
		b.Queue(`INSERT INTO tenant_quota_status (tenant_id, org_id, plan_id, period_start, level, ingest_bytes, ingest_limit_bytes, ingest_blocked,
				rate_bytes_per_second, burst_bytes, metrics, evaluated_at)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, now())
			ON CONFLICT (tenant_id) DO UPDATE SET org_id = EXCLUDED.org_id, plan_id = EXCLUDED.plan_id, period_start = EXCLUDED.period_start,
				level = EXCLUDED.level, ingest_bytes = EXCLUDED.ingest_bytes, ingest_limit_bytes = EXCLUDED.ingest_limit_bytes,
				ingest_blocked = EXCLUDED.ingest_blocked, rate_bytes_per_second = EXCLUDED.rate_bytes_per_second, burst_bytes = EXCLUDED.burst_bytes,
				metrics = EXCLUDED.metrics, evaluated_at = now()`,
			st.TenantID, st.OrgID, st.PlanID, st.PeriodStart.UTC().Format(time.DateOnly), string(st.Level), st.IngestBytes, st.IngestLimitBytes,
			st.IngestBlocked, st.RateBytesPerSecond, st.BurstBytes, string(metrics))
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return tx.SendBatch(ctx, b).Close()
	})
}

// GetStatus returns the stored evaluation of tenant.
func (s PGStore) GetStatus(ctx context.Context, tenantID string) (StoredStatus, bool, error) {
	var st StoredStatus
	var metrics []byte
	var level string
	err := s.Pool.QueryRow(ctx, `SELECT tenant_id, org_id::text, plan_id, period_start, level, ingest_bytes, ingest_limit_bytes, ingest_blocked,
		rate_bytes_per_second, burst_bytes, metrics, evaluated_at FROM tenant_quota_status WHERE tenant_id = $1`, tenantID).
		Scan(&st.TenantID, &st.OrgID, &st.PlanID, &st.PeriodStart, &level, &st.IngestBytes, &st.IngestLimitBytes, &st.IngestBlocked,
			&st.RateBytesPerSecond, &st.BurstBytes, &metrics, &st.EvaluatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	st.Level = Level(level)
	if err := json.Unmarshal(metrics, &st.Metrics); err != nil {
		return st, false, err
	}
	return st, true, nil
}

// LoadIngestStatuses implements StatusSource: statuses evaluated within the last hour (older rows belong to deleted
// or no longer evaluated tenants and are ignored).
func (s PGStore) LoadIngestStatuses(ctx context.Context) (map[string]IngestStatus, error) {
	rows, err := s.Pool.Query(ctx, `SELECT tenant_id, plan_id, ingest_blocked, ingest_bytes, ingest_limit_bytes, rate_bytes_per_second, burst_bytes, period_start
		FROM tenant_quota_status WHERE evaluated_at > now() - interval '1 hour' AND (ingest_blocked OR rate_bytes_per_second > 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IngestStatus{}
	for rows.Next() {
		var st IngestStatus
		if err := rows.Scan(&st.TenantID, &st.PlanID, &st.Blocked, &st.IngestBytes, &st.IngestLimitBytes, &st.RateBytesPerSecond, &st.BurstBytes, &st.PeriodStart); err != nil {
			return nil, err
		}
		out[st.TenantID] = st
	}
	return out, rows.Err()
}

// LiveIngestPods implements StatusSource from component_heartbeats (seen within 90 s).
func (s PGStore) LiveIngestPods(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM component_heartbeats
		WHERE component IN ('openlog-ingest', 'openlog-allinone') AND last_seen > now() - interval '90 seconds'`).Scan(&n)
	return n, err
}

// ClaimNotification records that the notification (org, period, metric, threshold) is being sent. It returns false
// when it was already claimed.
func (s PGStore) ClaimNotification(ctx context.Context, orgID string, periodStart time.Time, metric string, threshold int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO usage_notifications (org_id, period_start, metric, threshold) VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT DO NOTHING`, orgID, periodStart.UTC().Format(time.DateOnly), metric, threshold)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CompleteNotification stores the recipient count of a claimed notification, or releases the claim when sending
// failed (sent=false) so the next evaluation retries.
func (s PGStore) CompleteNotification(ctx context.Context, orgID string, periodStart time.Time, metric string, threshold, recipients int, sent bool) error {
	var err error
	if sent {
		_, err = s.Pool.Exec(ctx, `UPDATE usage_notifications SET recipients = $5, sent_at = now()
			WHERE org_id = $1::uuid AND period_start = $2 AND metric = $3 AND threshold = $4`, orgID, periodStart.UTC().Format(time.DateOnly), metric, threshold, recipients)
	} else {
		_, err = s.Pool.Exec(ctx, `DELETE FROM usage_notifications WHERE org_id = $1::uuid AND period_start = $2 AND metric = $3 AND threshold = $4`,
			orgID, periodStart.UTC().Format(time.DateOnly), metric, threshold)
	}
	return err
}

// RecentMutation reports whether a retention mutation for (table, partition, tenants) was submitted after since.
func (s PGStore) RecentMutation(ctx context.Context, table, partition, hash string, since time.Time) (bool, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM usage_retention_mutations WHERE table_name = $1 AND partition_id = $2 AND tenants_hash = $3
		AND submitted_at > $4`, table, partition, hash, since).Scan(&n)
	return n > 0, err
}

// RecordMutation stores a submitted retention mutation and prunes records older than 30 days.
func (s PGStore) RecordMutation(ctx context.Context, t RetentionTask) error {
	if _, err := s.Pool.Exec(ctx, `INSERT INTO usage_retention_mutations (table_name, partition_id, tenants_hash, retention_days, tenants, submitted_at)
		VALUES ($1, $2, $3, $4, $5, now()) ON CONFLICT (table_name, partition_id, tenants_hash) DO UPDATE SET submitted_at = now()`,
		t.Table, t.PartitionID, t.Hash, t.Days, t.Tenants); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM usage_retention_mutations WHERE submitted_at < now() - interval '30 days'`)
	return err
}

// validTenant mirrors organizations.tenant_id.
var validTenant = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func quoteTenants(ts []string) string {
	q := make([]string, len(ts))
	for i, t := range ts {
		q[i] = "'" + t + "'"
	}
	return strings.Join(q, ", ")
}
