package operator

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Support access (D-105): an owner grants openlog support access for 24 hours or 7 days; while it is active a
// superadmin may open read-only support sessions bound to their own session. Access ends when the grant expires or is
// revoked (which also ends open support sessions). Every support request is audited in the organization's log.

// Support access durations an owner can grant.
var SupportAccessDurations = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

// GrantSupportAccess allows support access until now + d. Audit support_access.grant.
func (s Store) GrantSupportAccess(ctx context.Context, orgID string, d time.Duration, a Actor) (Lifecycle, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, ensureState, orgID); err != nil {
			return err
		}
		var until time.Time
		if err := tx.QueryRow(ctx, `UPDATE org_saas_state SET support_access_until = now() + $2::interval, support_access_granted_by = $3::uuid,
			support_access_granted_at = now(), updated_at = now() WHERE org_id = $1::uuid RETURNING support_access_until`,
			orgID, d.String(), nullUUID(a.UserID)).Scan(&until); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, a, "support_access.grant", "organization", orgID, map[string]any{"until": until.UTC(), "duration": d.String()})
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, orgID)
}

// RevokeSupportAccess ends support access and every open support session. Audit support_access.revoke.
func (s Store) RevokeSupportAccess(ctx context.Context, orgID string, a Actor) (Lifecycle, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE org_saas_state SET support_access_until = NULL, updated_at = now() WHERE org_id = $1::uuid`, orgID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE support_sessions SET ended_at = now() WHERE org_id = $1::uuid AND ended_at IS NULL AND expires_at > now()`, orgID)
		if err != nil {
			return err
		}
		return audit(ctx, tx, orgID, a, "support_access.revoke", "organization", orgID, map[string]any{"support_sessions_ended": tag.RowsAffected()})
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, orgID)
}

// SupportSession is a read-only support view of an operator.
type SupportSession struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"org_id"`
	OrgName       string     `json:"org_name"`
	TenantID      string     `json:"tenant_id"`
	OperatorEmail string     `json:"operator_email"`
	Reason        string     `json:"reason"`
	StartedAt     time.Time  `json:"started_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	EndedAt       *time.Time `json:"ended_at"`
	operatorID    string
	authSessionID string
}

// StartSupportSession opens a support session of the operator into the organization; it requires active support
// access and lasts at most ttl (and never beyond the grant). Audit support.session_start.
func (s Store) StartSupportSession(ctx context.Context, ref, reason, authSessionID string, ttl time.Duration, a Actor) (SupportSession, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return SupportSession{}, err
	}
	var ss SupportSession
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var until *time.Time
		if err := tx.QueryRow(ctx, `SELECT st.support_access_until FROM organizations o LEFT JOIN org_saas_state st ON st.org_id = o.id
			WHERE o.id = $1::uuid`, id).Scan(&until); err != nil {
			return err
		}
		if until == nil || !time.Now().Before(*until) {
			return ErrNoAccess
		}
		expires := time.Now().Add(ttl)
		if until.Before(expires) {
			expires = *until
		}
		if err := tx.QueryRow(ctx, `INSERT INTO support_sessions (org_id, operator_user_id, operator_email, auth_session_id, reason, expires_at)
			VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6) RETURNING id::text`, id, a.UserID, a.Email, authSessionID, reason, expires).Scan(&ss.ID); err != nil {
			return err
		}
		return audit(ctx, tx, id, a, "support.session_start", "support_session", ss.ID, map[string]any{"reason": reason, "expires_at": expires.UTC()})
	})
	if err != nil {
		return SupportSession{}, err
	}
	return s.getSupportSession(ctx, ss.ID)
}

func (s Store) getSupportSession(ctx context.Context, id string) (SupportSession, error) {
	var ss SupportSession
	if !uuidRe.MatchString(id) {
		return ss, ErrNotFound
	}
	err := s.Pool.QueryRow(ctx, `SELECT ss.id::text, ss.org_id::text, o.name, o.tenant_id, ss.operator_email, ss.reason, ss.started_at, ss.expires_at,
		ss.ended_at, ss.operator_user_id::text, ss.auth_session_id::text
		FROM support_sessions ss JOIN organizations o ON o.id = ss.org_id WHERE ss.id = $1::uuid`, id).
		Scan(&ss.ID, &ss.OrgID, &ss.OrgName, &ss.TenantID, &ss.OperatorEmail, &ss.Reason, &ss.StartedAt, &ss.ExpiresAt, &ss.EndedAt,
			&ss.operatorID, &ss.authSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ss, ErrNotFound
	}
	return ss, err
}

// ResolveSupportSession returns the support session id if it belongs to the operator's user and auth session, is not
// ended or expired and the organization's support access is still active.
func (s Store) ResolveSupportSession(ctx context.Context, id, userID, authSessionID string, now time.Time) (SupportSession, error) {
	ss, err := s.getSupportSession(ctx, id)
	if err != nil {
		return ss, err
	}
	if ss.operatorID != userID || ss.authSessionID != authSessionID || ss.EndedAt != nil || !now.Before(ss.ExpiresAt) {
		return ss, ErrNoAccess
	}
	var until *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT support_access_until FROM org_saas_state WHERE org_id = $1::uuid`, ss.OrgID).Scan(&until); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return ss, err
	}
	if until == nil || !now.Before(*until) {
		return ss, ErrNoAccess
	}
	return ss, nil
}

// EndSupportSession ends the operator's support session. Audit support.session_end.
func (s Store) EndSupportSession(ctx context.Context, id string, a Actor) (SupportSession, error) {
	ss, err := s.getSupportSession(ctx, id)
	if err != nil {
		return ss, err
	}
	if ss.operatorID != a.UserID {
		return ss, ErrNotFound
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE support_sessions SET ended_at = now() WHERE id = $1::uuid AND ended_at IS NULL`, id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return audit(ctx, tx, ss.OrgID, a, "support.session_end", "support_session", id, nil)
	})
	if err != nil {
		return ss, err
	}
	return s.getSupportSession(ctx, id)
}

// ListOperatorSupportSessions returns the operator's open support sessions.
func (s Store) ListOperatorSupportSessions(ctx context.Context, userID string) ([]SupportSession, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ss.id::text, ss.org_id::text, o.name, o.tenant_id, ss.operator_email, ss.reason, ss.started_at, ss.expires_at, ss.ended_at
		FROM support_sessions ss JOIN organizations o ON o.id = ss.org_id
		WHERE ss.operator_user_id = $1::uuid AND ss.ended_at IS NULL AND ss.expires_at > now() ORDER BY ss.started_at DESC LIMIT 50`, nullUUID(userID))
	if err != nil {
		return nil, err
	}
	return scanSupportSessions(rows)
}

func (s Store) listSupportSessions(ctx context.Context, orgID string, limit int) ([]SupportSession, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ss.id::text, ss.org_id::text, o.name, o.tenant_id, ss.operator_email, ss.reason, ss.started_at, ss.expires_at, ss.ended_at
		FROM support_sessions ss JOIN organizations o ON o.id = ss.org_id WHERE ss.org_id = $1::uuid ORDER BY ss.started_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	return scanSupportSessions(rows)
}

func scanSupportSessions(rows pgx.Rows) ([]SupportSession, error) {
	defer rows.Close()
	out := []SupportSession{}
	for rows.Next() {
		var ss SupportSession
		if err := rows.Scan(&ss.ID, &ss.OrgID, &ss.OrgName, &ss.TenantID, &ss.OperatorEmail, &ss.Reason, &ss.StartedAt, &ss.ExpiresAt, &ss.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}
