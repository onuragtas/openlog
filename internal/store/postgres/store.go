package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/tenant"
)

// Store implements auth.Store (and tenant.KeyStore) on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

var _ auth.Store = (*Store)(nil)

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// mapErr converts driver errors into auth sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505": // unique_violation
			return auth.ErrAlreadyExists
		case "23503", "22P02": // foreign_key_violation, invalid_text_representation (bad uuid)
			return auth.ErrNotFound
		}
	}
	return err
}

func validID(ids ...string) bool {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	return true
}

func ts(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// nullID renders an optional uuid parameter ("" = NULL); use with $n::uuid.
func nullID(id string) *string {
	if id == "" || !validID(id) {
		return nil
	}
	return &id
}

func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

// ---- organizations & users ----

func insertUser(ctx context.Context, q pgx.Tx, u *auth.User) error {
	var hash *string
	if u.PasswordHash != "" {
		hash = &u.PasswordHash
	}
	u.CreatedAt = ts(u.CreatedAt)
	// email_verified_at is written explicitly: NULL = unverified sign-up (the column default is for older binaries).
	return q.QueryRow(ctx, `INSERT INTO users (email, name, password_hash, created_at, updated_at, email_verified_at, locale)
		VALUES ($1, $2, $3, $4, $4, $5, $6) RETURNING id::text`, u.Email, u.Name, hash, u.CreatedAt, u.EmailVerifiedAt, u.Locale).Scan(&u.ID)
}

func (s *Store) CreateOrganization(ctx context.Context, org *auth.Organization, owner *auth.User) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if owner.ID == "" {
			if err := insertUser(ctx, tx, owner); err != nil {
				return err
			}
		}
		org.CreatedAt = ts(org.CreatedAt)
		if err := tx.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name, created_at, updated_at)
			VALUES ($1, $2, $3, $3) RETURNING id::text`, org.TenantID, org.Name, org.CreatedAt).Scan(&org.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES ($1, $2, 'owner', $3)`,
			org.ID, owner.ID, org.CreatedAt)
		return err
	})
}

const orgCols = `id::text, tenant_id, name, created_at, locale`

func scanOrg(row pgx.Row) (auth.Organization, error) {
	var o auth.Organization
	err := row.Scan(&o.ID, &o.TenantID, &o.Name, &o.CreatedAt, &o.Locale)
	return o, mapErr(err)
}

func (s *Store) GetOrganization(ctx context.Context, id string) (auth.Organization, error) {
	if !validID(id) {
		return auth.Organization{}, auth.ErrNotFound
	}
	return scanOrg(s.pool.QueryRow(ctx, `SELECT `+orgCols+` FROM organizations WHERE id = $1`, id))
}

func (s *Store) GetOrganizationByTenant(ctx context.Context, tenantID string) (auth.Organization, error) {
	return scanOrg(s.pool.QueryRow(ctx, `SELECT `+orgCols+` FROM organizations WHERE tenant_id = $1`, tenantID))
}

func (s *Store) UpdateOrganizationName(ctx context.Context, id, name string) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE organizations SET name = $2, updated_at = now() WHERE id = $1`, id, name))
}

// SetOrganizationLocale sets the default e-mail language (0060_language_preferences).
func (s *Store) SetOrganizationLocale(ctx context.Context, id, locale string) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE organizations SET locale = $2, updated_at = now() WHERE id = $1`, id, locale))
}

// SetUserLocale stores the user's language (0060_language_preferences).
func (s *Store) SetUserLocale(ctx context.Context, userID, locale string, explicit bool) error {
	if !validID(userID) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE users SET locale = $2, locale_explicit = $3, updated_at = now() WHERE id = $1`, userID, locale, explicit))
}

func (s *Store) CreateUser(ctx context.Context, u *auth.User) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return insertUser(ctx, tx, u) })
}

const userCols = `id::text, email, name, coalesce(password_hash, ''), created_at, last_login_at, disabled_at, email_verified_at, locale, locale_explicit`

func scanUser(row pgx.Row) (auth.User, error) {
	var u auth.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.LastLoginAt, &u.DisabledAt, &u.EmailVerifiedAt, &u.Locale, &u.LocaleExplicit)
	return u, mapErr(err)
}

func (s *Store) GetUser(ctx context.Context, id string) (auth.User, error) {
	if !validID(id) {
		return auth.User{}, auth.ErrNotFound
	}
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email))
}

func (s *Store) SetUserPassword(ctx context.Context, userID, hash string) error {
	if !validID(userID) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash))
}

func (s *Store) SetUserLastLogin(ctx context.Context, userID string, at time.Time) error {
	if !validID(userID) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, userID, at))
}

// SetUserEmail changes the address (users.email UNIQUE → ErrAlreadyExists).
func (s *Store) SetUserEmail(ctx context.Context, userID, email string) error {
	if !validID(userID) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE users SET email = $2, updated_at = now() WHERE id = $1`, userID, email))
}

// ---- memberships ----

func (s *Store) AddMember(ctx context.Context, orgID, userID string, role auth.Role) error {
	if !validID(orgID, userID) {
		return auth.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, $3)`, orgID, userID, string(role))
	return mapErr(err)
}

func (s *Store) GetMembership(ctx context.Context, orgID, userID string) (auth.Membership, error) {
	if !validID(orgID, userID) {
		return auth.Membership{}, auth.ErrNotFound
	}
	var m auth.Membership
	var role string
	err := s.pool.QueryRow(ctx, `SELECT o.id::text, o.tenant_id, o.name, o.created_at, m.role, m.created_at
		FROM memberships m JOIN organizations o ON o.id = m.org_id
		WHERE m.org_id = $1 AND m.user_id = $2 AND o.deleted_at IS NULL`, orgID, userID). // scheduled for deletion: no access (0070, D-107)
		Scan(&m.Org.ID, &m.Org.TenantID, &m.Org.Name, &m.Org.CreatedAt, &role, &m.CreatedAt)
	m.Role = auth.Role(role)
	return m, mapErr(err)
}

func (s *Store) ListMemberships(ctx context.Context, userID string) ([]auth.Membership, error) {
	if !validID(userID) {
		return []auth.Membership{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT o.id::text, o.tenant_id, o.name, o.created_at, m.role, m.created_at
		FROM memberships m JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = $1 AND o.deleted_at IS NULL ORDER BY m.created_at, o.name`, userID) // D-107
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.Membership, error) {
		var m auth.Membership
		var role string
		err := r.Scan(&m.Org.ID, &m.Org.TenantID, &m.Org.Name, &m.Org.CreatedAt, &role, &m.CreatedAt)
		m.Role = auth.Role(role)
		return m, err
	})
}

func (s *Store) ListMembers(ctx context.Context, orgID string) ([]auth.Member, error) {
	if !validID(orgID) {
		return []auth.Member{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, u.email, u.name, m.role, m.created_at
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.Member, error) {
		var m auth.Member
		var role string
		err := r.Scan(&m.UserID, &m.Email, &m.Name, &role, &m.JoinedAt)
		m.Role = auth.Role(role)
		return m, err
	})
}

// lockOwners locks the organization's owner memberships and returns their user ids,
// so concurrent demotions/removals cannot both see a second owner.
func lockOwners(ctx context.Context, tx pgx.Tx, orgID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text FROM memberships WHERE org_id = $1 AND role = 'owner' FOR UPDATE`, orgID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (s *Store) UpdateMemberRole(ctx context.Context, orgID, userID string, role auth.Role) error {
	if !validID(orgID, userID) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		owners, err := lockOwners(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if owners[userID] && role != auth.RoleOwner && len(owners) <= 1 {
			return auth.ErrLastOwner
		}
		return affected(tx.Exec(ctx, `UPDATE memberships SET role = $3 WHERE org_id = $1 AND user_id = $2`, orgID, userID, string(role)))
	})
}

func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) error {
	_, err := s.RemoveMemberWithCleanup(ctx, orgID, userID)
	return err
}

var _ auth.MemberRemover = (*Store)(nil)

// RemoveMemberWithCleanup deletes the membership and, in the same transaction, removes the user's address from the
// organization's dashboard report recipient lists; reports left without recipients are disabled (0062, D-096).
func (s *Store) RemoveMemberWithCleanup(ctx context.Context, orgID, userID string) (auth.MemberCleanup, error) {
	var out auth.MemberCleanup
	if !validID(orgID, userID) {
		return out, auth.ErrNotFound
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		out = auth.MemberCleanup{}
		owners, err := lockOwners(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if owners[userID] && len(owners) <= 1 {
			return auth.ErrLastOwner
		}
		if err := affected(tx.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE dashboard_reports r
			SET recipients = array_remove(r.recipients, lower(u.email)),
			    enabled = r.enabled AND cardinality(array_remove(r.recipients, lower(u.email))) > 0,
			    updated_at = now()
			FROM users u
			WHERE u.id = $2 AND r.org_id = $1 AND lower(u.email) = ANY (r.recipients)
			RETURNING r.id::text, cardinality(r.recipients) = 0`, orgID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var empty bool
			if err := rows.Scan(&id, &empty); err != nil {
				return err
			}
			out.ReportsUpdated = append(out.ReportsUpdated, id)
			if empty {
				out.ReportsDisabled = append(out.ReportsDisabled, id)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return auth.MemberCleanup{}, err
	}
	return out, nil
}

// ---- sessions ----

func (s *Store) CreateSession(ctx context.Context, sess *auth.Session) error {
	sess.CreatedAt = ts(sess.CreatedAt)
	method := sess.AuthMethod
	if method == "" {
		method = auth.MethodPassword
	}
	// auth_method, org_id and sso_connection_id: 0030_sso (SSO-bound sessions, D-077).
	err := s.pool.QueryRow(ctx, `INSERT INTO sessions (user_id, token_hash, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent,
			auth_method, org_id, sso_connection_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11::uuid) RETURNING id::text`,
		sess.UserID, sess.TokenHash, sess.CSRFToken, sess.CreatedAt, ts(sess.LastSeenAt), sess.ExpiresAt, sess.IP, sess.UserAgent,
		method, nullID(sess.OrgID), nullID(sess.ConnectionID)).Scan(&sess.ID)
	return mapErr(err)
}

const sessionCols = `s.id::text, s.user_id::text, s.token_hash, s.csrf_token, s.created_at, s.last_seen_at, s.expires_at, s.revoked_at, s.ip, s.user_agent,
	s.auth_method, coalesce(s.org_id::text, ''), coalesce(s.sso_connection_id::text, '')`

func scanSession(r pgx.Row, extra ...any) (auth.Session, error) {
	var x auth.Session
	dest := append([]any{&x.ID, &x.UserID, &x.TokenHash, &x.CSRFToken, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.RevokedAt, &x.IP, &x.UserAgent,
		&x.AuthMethod, &x.OrgID, &x.ConnectionID}, extra...)
	return x, r.Scan(dest...)
}

func (s *Store) GetSessionByTokenHash(ctx context.Context, hash []byte) (auth.Session, auth.User, error) {
	var u auth.User
	sess, err := scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+`, u.id::text, u.email, u.name, u.created_at, u.last_login_at, u.disabled_at, u.email_verified_at,
		       u.locale, u.locale_explicit
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = $1`, hash),
		&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.LastLoginAt, &u.DisabledAt, &u.EmailVerifiedAt, &u.Locale, &u.LocaleExplicit)
	return sess, u, mapErr(err)
}

func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1 AND last_seen_at < $2`, id, at)
	return mapErr(err)
}

func (s *Store) ListSessions(ctx context.Context, userID string, now time.Time) ([]auth.Session, error) {
	if !validID(userID) {
		return []auth.Session{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+sessionCols+` FROM sessions s
		WHERE s.user_id = $1 AND s.revoked_at IS NULL AND s.expires_at > $2
		ORDER BY s.created_at DESC LIMIT 500`, userID, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.Session, error) { return scanSession(r) })
}

func (s *Store) RevokeSession(ctx context.Context, userID, id string, at time.Time) error {
	if !validID(userID, id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = $3 WHERE id = $2 AND user_id = $1 AND revoked_at IS NULL`, userID, id, at))
}

func (s *Store) RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) error {
	if !validID(userID) {
		return auth.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = $3
		WHERE user_id = $1 AND revoked_at IS NULL AND ($2::uuid IS NULL OR id <> $2::uuid)`, userID, nullID(exceptID), at)
	return mapErr(err)
}

// ---- license keys ----

func (s *Store) CreateLicenseKey(ctx context.Context, k *auth.LicenseKey) error {
	k.CreatedAt = ts(k.CreatedAt)
	insert := `INSERT INTO license_keys (org_id, name, key_prefix, key_hash, custom, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7) RETURNING id::text`
	if len(k.LegacyHashes) == 0 {
		return mapErr(s.pool.QueryRow(ctx, insert, k.OrgID, k.Name, k.Prefix, k.Hash, k.Custom, nullID(k.CreatedBy), k.CreatedAt).Scan(&k.ID))
	}
	// The same value stored in an older hash format (before OPENLOG_KEY_HASH_SECRET or a rotation) counts as existing.
	// A concurrent insert of the same value is still caught by the unique key_hash.
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM license_keys WHERE key_hash = ANY($1::bytea[]))`, k.LegacyHashes).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return auth.ErrAlreadyExists
		}
		return tx.QueryRow(ctx, insert, k.OrgID, k.Name, k.Prefix, k.Hash, k.Custom, nullID(k.CreatedBy), k.CreatedAt).Scan(&k.ID)
	})
}

const licenseKeyCols = `k.id::text, k.org_id::text, k.name, k.key_prefix, k.key_hash, k.custom, coalesce(k.created_by::text, ''), k.created_at, k.last_used_at, k.revoked_at`

func scanLicenseKey(r pgx.Row, extra ...any) (auth.LicenseKey, error) {
	var k auth.LicenseKey
	dest := append([]any{&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.Hash, &k.Custom, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt}, extra...)
	return k, r.Scan(dest...)
}

func (s *Store) ListLicenseKeys(ctx context.Context, orgID string) ([]auth.LicenseKey, error) {
	if !validID(orgID) {
		return []auth.LicenseKey{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+licenseKeyCols+`, coalesce(u.email, '')
		FROM license_keys k LEFT JOIN users u ON u.id = k.created_by
		WHERE k.org_id = $1 ORDER BY k.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.LicenseKey, error) {
		var email string
		k, err := scanLicenseKey(r, &email)
		k.CreatedByEmail = email
		return k, err
	})
}

func (s *Store) RevokeLicenseKey(ctx context.Context, orgID, id, by string, at time.Time) (auth.LicenseKey, error) {
	if !validID(orgID, id) {
		return auth.LicenseKey{}, auth.ErrNotFound
	}
	k, err := scanLicenseKey(s.pool.QueryRow(ctx, `UPDATE license_keys k
		SET revoked_at = coalesce(k.revoked_at, $3), revoked_by = coalesce(k.revoked_by, $4::uuid)
		WHERE k.org_id = $1 AND k.id = $2 RETURNING `+licenseKeyCols, orgID, id, at, nullID(by)))
	return k, mapErr(err)
}

// LookupLicenseKey implements tenant.KeyStore. One statement finds the active key by any candidate hash
// (best candidate first) and rewrites a key found by an older-format hash to hashes[0] (D-044); the rewrite is
// skipped when another row already has that hash, so it can never fail the lookup with a unique violation.
// Without an active key, a license_key_tombstones row for a candidate yields tenant.ErrOrgDeleted (D-115) unless that
// hash still belongs to a key of an organization that is not deleted (a cancellation an older pod did not clean up).
func (s *Store) LookupLicenseKey(ctx context.Context, hashes [][]byte) (tenant.KeyInfo, error) {
	var info tenant.KeyInfo
	if len(hashes) == 0 {
		return info, tenant.ErrUnknownKey
	}
	var deleted bool
	err := s.pool.QueryRow(ctx, `WITH hit AS (
			SELECT k.id, k.key_hash, o.tenant_id FROM license_keys k JOIN organizations o ON o.id = k.org_id
			WHERE k.key_hash = ANY($1::bytea[]) AND k.revoked_at IS NULL AND o.deleted_at IS NULL
			ORDER BY array_position($1::bytea[], k.key_hash) LIMIT 1
		), rehash AS (
			UPDATE license_keys l SET key_hash = $2 FROM hit
			WHERE l.id = hit.id AND hit.key_hash <> $2 AND l.revoked_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM license_keys x WHERE x.key_hash = $2)
		)
		SELECT id::text, tenant_id, false FROM hit
		UNION ALL
		(SELECT '', '', true FROM license_key_tombstones t
			WHERE t.key_hash = ANY($1::bytea[]) AND NOT EXISTS (SELECT 1 FROM hit)
			  AND NOT EXISTS (SELECT 1 FROM license_keys x JOIN organizations xo ON xo.id = x.org_id
				WHERE x.key_hash = t.key_hash AND xo.deleted_at IS NULL)
			LIMIT 1)`, hashes, hashes[0]).Scan(&info.KeyID, &info.TenantID, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return info, tenant.ErrUnknownKey
	}
	if err == nil && deleted {
		return tenant.KeyInfo{}, tenant.ErrOrgDeleted
	}
	return info, err
}

// TouchLicenseKeys implements tenant.KeyStore. Rows used within the last
// minute are not rewritten.
func (s *Store) TouchLicenseKeys(ctx context.Context, ids []string, at time.Time) error {
	valid := ids[:0:0]
	for _, id := range ids {
		if validID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE license_keys SET last_used_at = $2::timestamptz
		WHERE id = ANY($1::uuid[]) AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, valid, at)
	return err
}

// ---- API keys ----

func (s *Store) CreateAPIKey(ctx context.Context, k *auth.APIKey) error {
	k.CreatedAt = ts(k.CreatedAt)
	if k.Scope == "" {
		k.Scope = "read"
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO api_keys (org_id, name, key_prefix, key_hash, scope, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8) RETURNING id::text`,
		k.OrgID, k.Name, k.Prefix, k.Hash, k.Scope, nullID(k.CreatedBy), k.CreatedAt, k.ExpiresAt).Scan(&k.ID)
	return mapErr(err)
}

const apiKeyCols = `k.id::text, k.org_id::text, k.name, k.key_prefix, k.key_hash, k.scope, coalesce(k.created_by::text, ''), k.created_at, k.last_used_at, k.expires_at, k.revoked_at`

func scanAPIKey(r pgx.Row, extra ...any) (auth.APIKey, error) {
	var k auth.APIKey
	dest := append([]any{&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.Hash, &k.Scope, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt}, extra...)
	return k, r.Scan(dest...)
}

func (s *Store) ListAPIKeys(ctx context.Context, orgID string) ([]auth.APIKey, error) {
	if !validID(orgID) {
		return []auth.APIKey{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyCols+`, coalesce(u.email, '')
		FROM api_keys k LEFT JOIN users u ON u.id = k.created_by
		WHERE k.org_id = $1 ORDER BY k.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.APIKey, error) {
		var email string
		k, err := scanAPIKey(r, &email)
		k.CreatedByEmail = email
		return k, err
	})
}

func (s *Store) GetAPIKey(ctx context.Context, orgID, id string) (auth.APIKey, error) {
	if !validID(orgID, id) {
		return auth.APIKey{}, auth.ErrNotFound
	}
	k, err := scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyCols+` FROM api_keys k WHERE k.org_id = $1 AND k.id = $2`, orgID, id))
	return k, mapErr(err)
}

func (s *Store) RevokeAPIKey(ctx context.Context, orgID, id, by string, at time.Time) (auth.APIKey, error) {
	if !validID(orgID, id) {
		return auth.APIKey{}, auth.ErrNotFound
	}
	k, err := scanAPIKey(s.pool.QueryRow(ctx, `UPDATE api_keys k
		SET revoked_at = coalesce(k.revoked_at, $3), revoked_by = coalesce(k.revoked_by, $4::uuid)
		WHERE k.org_id = $1 AND k.id = $2 RETURNING `+apiKeyCols, orgID, id, at, nullID(by)))
	return k, mapErr(err)
}

// LookupAPIKey returns the key with one of these hashes even when revoked or
// expired (the service checks usability); an active key found by an
// older-format hash is rewritten to hashes[0] in the same statement.
func (s *Store) LookupAPIKey(ctx context.Context, hashes [][]byte) (auth.APIKey, auth.Organization, error) {
	var o auth.Organization
	if len(hashes) == 0 {
		return auth.APIKey{}, o, auth.ErrNotFound
	}
	k, err := scanAPIKey(s.pool.QueryRow(ctx, `WITH hit AS (
			SELECT id, key_hash FROM api_keys WHERE key_hash = ANY($1::bytea[])
			ORDER BY array_position($1::bytea[], key_hash) LIMIT 1
		), rehash AS (
			UPDATE api_keys a SET key_hash = $2 FROM hit
			WHERE a.id = hit.id AND hit.key_hash <> $2 AND a.revoked_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM api_keys x WHERE x.key_hash = $2)
		)
		SELECT `+apiKeyCols+`, o.id::text, o.tenant_id, o.name, o.created_at
		FROM api_keys k JOIN hit ON hit.id = k.id JOIN organizations o ON o.id = k.org_id AND o.deleted_at IS NULL`, hashes, hashes[0]),
		&o.ID, &o.TenantID, &o.Name, &o.CreatedAt)
	return k, o, mapErr(err)
}

func (s *Store) RevokeAPIKeysCreatedBy(ctx context.Context, orgID, userID, by string, at time.Time) (int, error) {
	if !validID(orgID, userID) {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = $3, revoked_by = $4::uuid
		WHERE org_id = $1 AND created_by = $2 AND revoked_at IS NULL`, orgID, userID, at, nullID(by))
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2::timestamptz
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, at)
	return mapErr(err)
}

// ---- invitations ----

func (s *Store) CreateInvitation(ctx context.Context, inv *auth.Invitation) error {
	inv.CreatedAt = ts(inv.CreatedAt)
	return s.inTx(ctx, func(tx pgx.Tx) error {
		// An expired pending invitation must not block a new one (partial unique index).
		if _, err := tx.Exec(ctx, `UPDATE invitations SET revoked_at = $3
			WHERE org_id = $1 AND email = $2 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at <= $3`,
			inv.OrgID, inv.Email, inv.CreatedAt); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO invitations (org_id, email, role, token_hash, invited_by, created_at, expires_at, locale)
			VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8) RETURNING id::text`,
			inv.OrgID, inv.Email, string(inv.Role), inv.TokenHash, nullID(inv.InvitedBy), inv.CreatedAt, inv.ExpiresAt, inv.Locale).Scan(&inv.ID)
	})
}

const invitationCols = `i.id::text, i.org_id::text, i.email, i.role, i.token_hash, coalesce(i.invited_by::text, ''), i.created_at, i.expires_at, i.accepted_at, i.revoked_at, i.last_sent_at, i.send_count, i.locale`

func scanInvitation(r pgx.Row, extra ...any) (auth.Invitation, error) {
	var i auth.Invitation
	var role string
	dest := append([]any{&i.ID, &i.OrgID, &i.Email, &role, &i.TokenHash, &i.InvitedBy, &i.CreatedAt, &i.ExpiresAt, &i.AcceptedAt, &i.RevokedAt, &i.LastSentAt, &i.SendCount, &i.Locale}, extra...)
	err := r.Scan(dest...)
	i.Role = auth.Role(role)
	return i, err
}

func (s *Store) RenewInvitation(ctx context.Context, orgID, id string, tokenHash []byte, expiresAt time.Time) (auth.Invitation, error) {
	if !validID(orgID, id) {
		return auth.Invitation{}, auth.ErrNotFound
	}
	i, err := scanInvitation(s.pool.QueryRow(ctx, `UPDATE invitations i SET token_hash = $3, expires_at = $4
		WHERE i.org_id = $1 AND i.id = $2 AND i.accepted_at IS NULL AND i.revoked_at IS NULL RETURNING `+invitationCols,
		orgID, id, tokenHash, expiresAt))
	return i, mapErr(err)
}

func (s *Store) MarkInvitationSent(ctx context.Context, id string, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE invitations SET last_sent_at = $2, send_count = send_count + 1 WHERE id = $1`, id, at))
}

func (s *Store) ListInvitations(ctx context.Context, orgID string, now time.Time, includeExpired bool) ([]auth.Invitation, error) {
	if !validID(orgID) {
		return []auth.Invitation{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+invitationCols+`, coalesce(u.email, '')
		FROM invitations i LEFT JOIN users u ON u.id = i.invited_by
		WHERE i.org_id = $1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND ($3 OR i.expires_at > $2)
		ORDER BY i.created_at DESC LIMIT 1000`, orgID, now, includeExpired)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.Invitation, error) {
		var email string
		i, err := scanInvitation(r, &email)
		i.InvitedByEmail = email
		return i, err
	})
}

func (s *Store) RevokeInvitation(ctx context.Context, orgID, id string, at time.Time) (auth.Invitation, error) {
	if !validID(orgID, id) {
		return auth.Invitation{}, auth.ErrNotFound
	}
	i, err := scanInvitation(s.pool.QueryRow(ctx, `UPDATE invitations i SET revoked_at = coalesce(i.revoked_at, $3)
		WHERE i.org_id = $1 AND i.id = $2 AND i.accepted_at IS NULL RETURNING `+invitationCols, orgID, id, at))
	return i, mapErr(err)
}

func (s *Store) GetInvitationByTokenHash(ctx context.Context, hash []byte) (auth.Invitation, auth.Organization, error) {
	var o auth.Organization
	i, err := scanInvitation(s.pool.QueryRow(ctx, `SELECT `+invitationCols+`, o.id::text, o.tenant_id, o.name, o.created_at
		FROM invitations i JOIN organizations o ON o.id = i.org_id WHERE i.token_hash = $1`, hash),
		&o.ID, &o.TenantID, &o.Name, &o.CreatedAt)
	return i, o, mapErr(err)
}

func (s *Store) AcceptInvitation(ctx context.Context, invID string, user *auth.User, at time.Time) error {
	if !validID(invID) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var orgID, role string
		if err := tx.QueryRow(ctx, `SELECT org_id::text, role FROM invitations
			WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2 FOR UPDATE`, invID, at).
			Scan(&orgID, &role); err != nil {
			return err
		}
		if user.ID == "" {
			if err := insertUser(ctx, tx, user); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
			orgID, user.ID, role, at); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE invitations SET accepted_at = $2, accepted_by = $3 WHERE id = $1`, invID, at, user.ID)
		return err
	})
}

// ---- audit ----

func (s *Store) AddAuditEvent(ctx context.Context, e *auth.AuditEvent) error {
	details := []byte("{}")
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		details = b
	}
	e.CreatedAt = ts(e.CreatedAt)
	err := s.pool.QueryRow(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8, $9) RETURNING id`,
		nullID(e.OrgID), nullID(e.ActorUserID), e.ActorEmail, e.Action, e.TargetType, e.TargetID, string(details), e.IP, e.CreatedAt).Scan(&e.ID)
	return mapErr(err)
}

func (s *Store) ListAuditEvents(ctx context.Context, orgID string, f auth.AuditFilter) ([]auth.AuditEvent, error) {
	if !validID(orgID) {
		return []auth.AuditEvent{}, nil
	}
	// audit_log_org_idx (org_id, created_at DESC) serves the order, the time range and the keyset cursor.
	q := `SELECT id, org_id::text, coalesce(actor_user_id::text, ''), actor_email, action, target_type, target_id, details::text, ip, created_at
		FROM audit_log WHERE org_id = $1`
	args := []any{orgID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Actor != "" {
		q += ` AND strpos(lower(actor_email), lower(` + arg(f.Actor) + `::text)) > 0`
	}
	if f.Action != "" {
		q += ` AND starts_with(action, ` + arg(f.Action) + `::text)`
	}
	if !f.From.IsZero() {
		q += ` AND created_at >= ` + arg(f.From) + `::timestamptz`
	}
	if !f.To.IsZero() {
		q += ` AND created_at < ` + arg(f.To) + `::timestamptz`
	}
	if f.Before != nil {
		q += ` AND (created_at, id) < (` + arg(f.Before.CreatedAt) + `::timestamptz, ` + arg(f.Before.ID) + `::bigint)`
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ` + arg(limit) + `::int`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.AuditEvent, error) {
		var e auth.AuditEvent
		var details string
		if err := r.Scan(&e.ID, &e.OrgID, &e.ActorUserID, &e.ActorEmail, &e.Action, &e.TargetType, &e.TargetID, &details, &e.IP, &e.CreatedAt); err != nil {
			return e, err
		}
		_ = json.Unmarshal([]byte(details), &e.Details)
		return e, nil
	})
}

// ---- e-mail verification ----

func (s *Store) CreateEmailVerification(ctx context.Context, v *auth.EmailVerification) error {
	if !validID(v.UserID) {
		return auth.ErrNotFound
	}
	v.CreatedAt = ts(v.CreatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO email_verifications (user_id, email, token_hash, created_at, expires_at, locale)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, v.UserID, v.Email, v.TokenHash, v.CreatedAt, v.ExpiresAt, v.Locale).Scan(&v.ID))
}

func (s *Store) ConsumeEmailVerification(ctx context.Context, tokenHash []byte, at time.Time) (auth.User, error) {
	var u auth.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var userID, email string
		if err := tx.QueryRow(ctx, `UPDATE email_verifications SET used_at = $2
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2 RETURNING user_id::text, email`, tokenHash, at).Scan(&userID, &email); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET email_verified_at = coalesce(email_verified_at, $3), updated_at = now()
			WHERE id = $1 AND email = $2 RETURNING `+userCols, userID, email, at))
		return err
	})
	return u, err
}

// ---- login rate limiting ----

func (s *Store) CountLoginFailures(ctx context.Context, key []byte, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM login_failures WHERE key_hash = $1 AND attempted_at > $2`, key, since).Scan(&n)
	return n, err
}

func (s *Store) AddLoginFailure(ctx context.Context, key []byte, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO login_failures (key_hash, attempted_at) VALUES ($1, $2)`, key, at)
	return err
}

func (s *Store) ClearLoginFailures(ctx context.Context, key []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM login_failures WHERE key_hash = $1`, key)
	return err
}

// Cleanup deletes sessions that ended more than a day ago and login failures
// older than a day. Safe to run concurrently from several pods.
func (s *Store) Cleanup(ctx context.Context, now time.Time) (sessions, failures int64, err error) {
	cutoff := now.Add(-24 * time.Hour)
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1 OR revoked_at < $1`, cutoff)
	if err != nil {
		return 0, 0, err
	}
	sessions = tag.RowsAffected()
	tag, err = s.pool.Exec(ctx, `DELETE FROM login_failures WHERE attempted_at < $1`, cutoff)
	if err != nil {
		return sessions, 0, err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM email_verifications WHERE expires_at < $1 OR used_at < $1`, cutoff); err != nil {
		return sessions, tag.RowsAffected(), err
	}
	return sessions, tag.RowsAffected(), nil
}
