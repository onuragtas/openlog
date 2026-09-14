package operator

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Operator actions. Every action writes an audit event into the organization's audit log with the operator as actor
// and the reason in the details.

// Lifecycle is an organization's SaaS state (org_saas_state; zero value = active, no trial, no support access).
type Lifecycle struct {
	OrgID                  string     `json:"org_id"`
	TenantID               string     `json:"tenant_id"`
	Suspended              bool       `json:"suspended"`
	SuspendedAt            *time.Time `json:"suspended_at"`
	SuspendReason          string     `json:"suspend_reason"`
	TrialPlanID            string     `json:"trial_plan_id"`
	TrialStartedAt         *time.Time `json:"trial_started_at"`
	TrialEndsAt            *time.Time `json:"trial_ends_at"`
	TrialEndedAt           *time.Time `json:"trial_ended_at"`
	SupportAccessUntil     *time.Time `json:"support_access_until"`
	SupportAccessGrantedAt *time.Time `json:"support_access_granted_at"`
}

// TrialActive reports whether a trial is running at now.
func (l Lifecycle) TrialActive() bool { return l.TrialEndsAt != nil && l.TrialEndedAt == nil }

// SupportAccessActive reports whether support access is granted at now.
func (l Lifecycle) SupportAccessActive(now time.Time) bool {
	return l.SupportAccessUntil != nil && now.Before(*l.SupportAccessUntil)
}

// GetLifecycle returns the state of the organization with id or tenant id ref.
func (s Store) GetLifecycle(ctx context.Context, ref string) (Lifecycle, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return Lifecycle{}, err
	}
	var l Lifecycle
	err = s.Pool.QueryRow(ctx, `SELECT o.id::text, o.tenant_id, st.suspended_at, coalesce(st.suspend_reason, ''), coalesce(st.trial_plan_id, ''),
		st.trial_started_at, st.trial_ends_at, st.trial_ended_at, st.support_access_until, st.support_access_granted_at
		FROM organizations o LEFT JOIN org_saas_state st ON st.org_id = o.id WHERE o.id = $1::uuid`, id).
		Scan(&l.OrgID, &l.TenantID, &l.SuspendedAt, &l.SuspendReason, &l.TrialPlanID, &l.TrialStartedAt, &l.TrialEndsAt, &l.TrialEndedAt,
			&l.SupportAccessUntil, &l.SupportAccessGrantedAt)
	l.Suspended = l.SuspendedAt != nil
	return l, err
}

func (s Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}

const ensureState = `INSERT INTO org_saas_state (org_id) VALUES ($1::uuid) ON CONFLICT (org_id) DO NOTHING`

// Suspend suspends the organization (idempotent: an already suspended organization keeps its original time, the reason
// is updated). Audit org.suspend.
func (s Store) Suspend(ctx context.Context, ref, reason string, a Actor, auto bool) (Lifecycle, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return Lifecycle{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, ensureState, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE org_saas_state SET suspended_at = coalesce(suspended_at, now()), suspended_by = $2::uuid,
			suspend_reason = $3, updated_at = now() WHERE org_id = $1::uuid`, id, nullUUID(a.UserID), reason); err != nil {
			return err
		}
		return audit(ctx, tx, id, a, "org.suspend", "organization", id, map[string]any{"reason": reason, "automatic": auto})
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, id)
}

// Unsuspend lifts a suspension. Audit org.unsuspend.
func (s Store) Unsuspend(ctx context.Context, ref, reason string, a Actor) (Lifecycle, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return Lifecycle{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE org_saas_state SET suspended_at = NULL, suspended_by = NULL, suspend_reason = '', updated_at = now()
			WHERE org_id = $1::uuid AND suspended_at IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrInvalidState
		}
		return audit(ctx, tx, id, a, "org.unsuspend", "organization", id, map[string]any{"reason": reason})
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, id)
}

// StartTrial records a trial of planID ending at endsAt (the caller assigns the plan). Audit trial.start.
func (s Store) StartTrial(ctx context.Context, tx pgx.Tx, orgID, planID string, endsAt time.Time, reason string, a Actor) error {
	if _, err := tx.Exec(ctx, ensureState, orgID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE org_saas_state SET trial_plan_id = $2, trial_started_at = now(), trial_ends_at = $3, trial_ended_at = NULL,
		updated_at = now() WHERE org_id = $1::uuid`, orgID, planID, endsAt); err != nil {
		return err
	}
	return audit(ctx, tx, orgID, a, "trial.start", "organization", orgID, map[string]any{"plan_id": planID, "ends_at": endsAt.UTC(), "reason": reason})
}

// ExtendTrial moves the end of a running trial to endsAt. Audit trial.extend.
func (s Store) ExtendTrial(ctx context.Context, ref string, endsAt time.Time, reason string, a Actor) (Lifecycle, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return Lifecycle{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var prev time.Time
		err := tx.QueryRow(ctx, `SELECT trial_ends_at FROM org_saas_state WHERE org_id = $1::uuid AND trial_ends_at IS NOT NULL AND trial_ended_at IS NULL
			FOR UPDATE`, id).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE org_saas_state SET trial_ends_at = $2, updated_at = now() WHERE org_id = $1::uuid`, id, endsAt); err != nil {
			return err
		}
		return audit(ctx, tx, id, a, "trial.extend", "organization", id, map[string]any{"from": prev.UTC(), "to": endsAt.UTC(), "reason": reason})
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, id)
}

// ResetQuotaNotifications forgets the usage notifications of the billing period starting at periodStart so they can be
// sent again. Audit quota.notifications_reset.
func (s Store) ResetQuotaNotifications(ctx context.Context, ref string, periodStart time.Time, reason string, a Actor) (int64, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM usage_notifications WHERE org_id = $1::uuid AND period_start = $2`, id, periodStart.UTC().Format(time.DateOnly))
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return audit(ctx, tx, id, a, "quota.notifications_reset", "organization", id,
			map[string]any{"period_start": periodStart.UTC().Format(time.DateOnly), "deleted": n, "reason": reason})
	})
	return n, err
}

// ForceLogout revokes every active session of the organization's members (except the operator's own). Members of
// several organizations are signed out everywhere. Audit org.force_logout.
func (s Store) ForceLogout(ctx context.Context, ref, reason string, a Actor) (int64, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE revoked_at IS NULL AND expires_at > now()
			AND user_id IN (SELECT user_id FROM memberships WHERE org_id = $1::uuid) AND ($2::uuid IS NULL OR user_id <> $2::uuid)`, id, nullUUID(a.UserID))
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return audit(ctx, tx, id, a, "org.force_logout", "organization", id, map[string]any{"sessions_revoked": n, "reason": reason})
	})
	return n, err
}

// UnverifiedOwners returns the ids and e-mail addresses of enabled owners without a confirmed address.
func (s Store) UnverifiedOwners(ctx context.Context, ref string) (orgID string, owners []Member, err error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT u.id::text, u.email FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1::uuid AND m.role = 'owner' AND u.disabled_at IS NULL AND u.email_verified_at IS NULL ORDER BY u.email`, id)
	if err != nil {
		return id, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email); err != nil {
			return id, nil, err
		}
		owners = append(owners, m)
	}
	return id, owners, rows.Err()
}

// Audit writes an audit event of the organization (actions performed outside the store, e.g. e-mails).
func (s Store) Audit(ctx context.Context, orgID string, a Actor, action, targetType, targetID string, details map[string]any) error {
	return audit(ctx, s.Pool, orgID, a, action, targetType, targetID, details)
}

// SuspendedTenants returns the suspended organizations by tenant id and by org id (ingest and api caches).
func (s Store) SuspendedTenants(ctx context.Context) (byTenant map[string]string, byOrg map[string]string, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT o.id::text, o.tenant_id, st.suspend_reason FROM org_saas_state st JOIN organizations o ON o.id = st.org_id
		WHERE st.suspended_at IS NOT NULL`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byTenant, byOrg = map[string]string{}, map[string]string{}
	for rows.Next() {
		var id, tenant, reason string
		if err := rows.Scan(&id, &tenant, &reason); err != nil {
			return nil, nil, err
		}
		byTenant[tenant], byOrg[id] = reason, reason
	}
	return byTenant, byOrg, rows.Err()
}
