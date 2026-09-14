package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

// SSOStore implements sso.Store on PostgreSQL (migration 0030_sso, postgres.md "Single sign-on and SCIM").
type SSOStore struct {
	pool *pgxpool.Pool
}

var _ sso.Store = (*SSOStore)(nil)

// NewSSOStore wraps a pool.
func NewSSOStore(pool *pgxpool.Pool) *SSOStore { return &SSOStore{pool: pool} }

func (s *SSOStore) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return (&Store{pool: s.pool}).inTx(ctx, fn)
}

func jsonOrEmpty(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- connections ----

type connConfig struct {
	OIDC                    *sso.OIDCConfig `json:"oidc,omitempty"`
	SAML                    *sso.SAMLConfig `json:"saml,omitempty"`
	LogoutRedirectAllowlist []string        `json:"logout_redirect_allowlist,omitempty"`
}

const connCols = `id::text, org_id::text, protocol, name, enabled, config::text, secret_enc, sp_key_enc, email_attribute, name_attribute,
	groups_attribute, jit_enabled, default_role, session_max_age_seconds, enforce, break_glass_user_ids::text[], config_version,
	tested_version, last_test_at, last_test_ok, last_test_error, last_test_details::text, coalesce(created_by::text, ''),
	coalesce(updated_by::text, ''), created_at, updated_at, allow_external_invitations, idp_refreshed_at, idp_refresh_ok,
	idp_refresh_error, idp_refresh_failures, idp_next_refresh_at, idp_cache::text`

// connOrder puts the default (oldest) connection first.
const connOrder = ` ORDER BY created_at, id`

func scanConn(row pgx.Row) (sso.Connection, error) {
	var c sso.Connection
	var protocol, role, cfg, details, cache string
	var maxAge int64
	err := row.Scan(&c.ID, &c.OrgID, &protocol, &c.Name, &c.Enabled, &cfg, &c.SecretEnc, &c.SPKeyEnc, &c.EmailAttribute, &c.NameAttribute,
		&c.GroupsAttribute, &c.JITEnabled, &role, &maxAge, &c.Enforce, &c.BreakGlassUserIDs, &c.ConfigVersion,
		&c.TestedVersion, &c.LastTestAt, &c.LastTestOK, &c.LastTestError, &details, &c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt,
		&c.AllowExternalInvitations, &c.Refresh.RefreshedAt, &c.Refresh.OK, &c.Refresh.Error, &c.Refresh.Failures, &c.Refresh.NextAt, &cache)
	if err != nil {
		return c, mapErr(err)
	}
	c.Protocol, c.DefaultRole, c.SessionMaxAge = sso.Protocol(protocol), auth.Role(role), time.Duration(maxAge)*time.Second
	var cc connConfig
	if err := json.Unmarshal([]byte(cfg), &cc); err != nil {
		return c, err
	}
	c.OIDC, c.SAML, c.LogoutRedirectAllowlist = cc.OIDC, cc.SAML, cc.LogoutRedirectAllowlist
	_ = json.Unmarshal([]byte(details), &c.LastTestDetails)
	_ = json.Unmarshal([]byte(cache), &c.Refresh.Cache)
	if c.BreakGlassUserIDs == nil {
		c.BreakGlassUserIDs = []string{}
	}
	return c, nil
}

func scanConns(rows pgx.Rows, err error) ([]sso.Connection, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.Connection, error) { return scanConn(r) })
}

func connArgs(c *sso.Connection) ([]any, error) {
	cfg, err := jsonOrEmpty(connConfig{OIDC: c.OIDC, SAML: c.SAML, LogoutRedirectAllowlist: c.LogoutRedirectAllowlist})
	if err != nil {
		return nil, err
	}
	ids := c.BreakGlassUserIDs
	if ids == nil {
		ids = []string{}
	}
	return []any{c.ID, c.OrgID, string(c.Protocol), c.Name, c.Enabled, cfg, c.SecretEnc, c.SPKeyEnc, c.EmailAttribute, c.NameAttribute,
		c.GroupsAttribute, c.JITEnabled, string(c.DefaultRole), int64(c.SessionMaxAge / time.Second), c.Enforce, ids, c.ConfigVersion,
		nullID(c.CreatedBy), nullID(c.UpdatedBy), c.AllowExternalInvitations}, nil
}

func (s *SSOStore) CreateConnection(ctx context.Context, c *sso.Connection) error {
	if c.ID == "" || !validID(c.ID) {
		return errors.New("sso: connection id must be a uuid")
	}
	if !validID(c.OrgID) {
		return auth.ErrNotFound
	}
	args, err := connArgs(c)
	if err != nil {
		return err
	}
	c.CreatedAt = ts(c.CreatedAt)
	args = append(args, c.CreatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO sso_connections (id, org_id, protocol, name, enabled, config, secret_enc, sp_key_enc,
			email_attribute, name_attribute, groups_attribute, jit_enabled, default_role, session_max_age_seconds, enforce, break_glass_user_ids,
			config_version, created_by, updated_by, allow_external_invitations, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::uuid[], $17, $18::uuid, $19::uuid, $20, $21, $21)
		RETURNING updated_at`, args...).Scan(&c.UpdatedAt))
}

func (s *SSOStore) ListConnections(ctx context.Context, orgID string) ([]sso.Connection, error) {
	if !validID(orgID) {
		return []sso.Connection{}, nil
	}
	return scanConns(s.pool.Query(ctx, `SELECT `+connCols+` FROM sso_connections WHERE org_id = $1`+connOrder, orgID))
}

func (s *SSOStore) GetConnection(ctx context.Context, orgID, id string) (sso.Connection, error) {
	if !validID(orgID) || (id != "" && !validID(id)) {
		return sso.Connection{}, auth.ErrNotFound
	}
	if id == "" {
		return scanConn(s.pool.QueryRow(ctx, `SELECT `+connCols+` FROM sso_connections WHERE org_id = $1`+connOrder+` LIMIT 1`, orgID))
	}
	return scanConn(s.pool.QueryRow(ctx, `SELECT `+connCols+` FROM sso_connections WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *SSOStore) GetConnectionByID(ctx context.Context, id string) (sso.Connection, error) {
	if !validID(id) {
		return sso.Connection{}, auth.ErrNotFound
	}
	return scanConn(s.pool.QueryRow(ctx, `SELECT `+connCols+` FROM sso_connections WHERE id = $1`, id))
}

func (s *SSOStore) UpdateConnection(ctx context.Context, c *sso.Connection) error {
	if !validID(c.ID) {
		return auth.ErrNotFound
	}
	args, err := connArgs(c)
	if err != nil {
		return err
	}
	args = append(args, ts(c.UpdatedAt))
	return affected(s.pool.Exec(ctx, `UPDATE sso_connections SET protocol = $3, name = $4, enabled = $5, config = $6::jsonb,
			secret_enc = $7, sp_key_enc = $8, email_attribute = $9, name_attribute = $10, groups_attribute = $11, jit_enabled = $12,
			default_role = $13, session_max_age_seconds = $14, enforce = $15, break_glass_user_ids = $16::uuid[], config_version = $17,
			updated_by = $19::uuid, allow_external_invitations = $20, updated_at = $21
		WHERE id = $1 AND org_id = $2 AND $18::uuid IS NOT DISTINCT FROM $18::uuid`, args...))
}

func (s *SSOStore) DeleteConnection(ctx context.Context, orgID, id string) (sso.Connection, error) {
	if !validID(orgID, id) {
		return sso.Connection{}, auth.ErrNotFound
	}
	return scanConn(s.pool.QueryRow(ctx, `DELETE FROM sso_connections WHERE org_id = $1 AND id = $2 RETURNING `+connCols, orgID, id))
}

func (s *SSOStore) RecordTest(ctx context.Context, id string, version int, ok bool, errMsg string, details map[string]any, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	d, err := jsonOrEmpty(details)
	if err != nil {
		return err
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_connections SET last_test_at = $2, last_test_ok = $3, last_test_error = $4,
			last_test_details = $5::jsonb,
			tested_version = CASE WHEN $3 AND config_version = $6 THEN $6 ELSE tested_version END
		WHERE id = $1`, id, at, ok, errMsg, d, version))
}

// GetOrgPolicy reads the session's connection and the routed connection of the user's e-mail domain in one
// statement (every session-authenticated request).
func (s *SSOStore) GetOrgPolicy(ctx context.Context, orgID, connectionID, emailDomain string) (sso.OrgPolicy, error) {
	var p sso.OrgPolicy
	if !validID(orgID) {
		return p, nil
	}
	if connectionID != "" && !validID(connectionID) {
		connectionID = ""
	}
	var maxAge int64
	err := s.pool.QueryRow(ctx, `SELECT coalesce(sc.id::text, ''), coalesce(sc.enabled, false), coalesce(sc.session_max_age_seconds, 0),
			d.id IS NOT NULL, coalesce(r.enabled AND r.enforce, false), coalesce(r.break_glass_user_ids::text[], '{}')
		FROM (SELECT 1) AS one
		LEFT JOIN sso_connections sc ON $2 <> '' AND sc.org_id = $1 AND sc.id::text = $2
		LEFT JOIN sso_domains d ON $3 <> '' AND d.org_id = $1 AND d.domain = $3 AND d.verified_at IS NOT NULL
		LEFT JOIN LATERAL (
			SELECT c.enabled, c.enforce, c.break_glass_user_ids FROM sso_connections c
			WHERE d.id IS NOT NULL AND c.org_id = $1 AND c.id = coalesce(d.connection_id,
				(SELECT x.id FROM sso_connections x WHERE x.org_id = $1 ORDER BY x.created_at, x.id LIMIT 1))
		) r ON true`, orgID, connectionID, emailDomain).
		Scan(&p.ConnectionID, &p.Enabled, &maxAge, &p.DomainVerified, &p.Enforce, &p.BreakGlassUserIDs)
	p.SessionMaxAge = time.Duration(maxAge) * time.Second
	return p, err
}

func (s *SSOStore) ConnectionForDomain(ctx context.Context, domain string) (sso.Connection, sso.Domain, error) {
	d, err := s.FindVerifiedDomain(ctx, domain)
	if err != nil {
		return sso.Connection{}, d, err
	}
	c, err := s.GetConnection(ctx, d.OrgID, d.ConnectionID)
	return c, d, err
}

// ---- background refresh ----

func (s *SSOStore) RecordRefresh(ctx context.Context, id string, ok bool, errMsg string, cache *sso.IdPCache, next, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	var cacheJSON *string
	if ok && cache != nil {
		b, err := json.Marshal(cache)
		if err != nil {
			return err
		}
		v := string(b)
		cacheJSON = &v
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_connections SET idp_refreshed_at = $2, idp_refresh_ok = $3, idp_refresh_error = $4,
			idp_refresh_failures = CASE WHEN $3 THEN 0 ELSE idp_refresh_failures + 1 END, idp_next_refresh_at = $5,
			idp_cache = coalesce($6::jsonb, idp_cache)
		WHERE id = $1`, id, at, ok, errMsg, next, cacheJSON))
}

func (s *SSOStore) ListRefreshDue(ctx context.Context, now time.Time, limit int) ([]sso.Connection, error) {
	return scanConns(s.pool.Query(ctx, `SELECT `+connCols+` FROM sso_connections
		WHERE enabled AND (idp_next_refresh_at IS NULL OR idp_next_refresh_at <= $1)
		ORDER BY idp_next_refresh_at NULLS FIRST, id LIMIT $2`, now, limit))
}

func (s *SSOStore) UpdateSAMLMetadata(ctx context.Context, id string, version int, cfg sso.SAMLConfig) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_connections SET config = jsonb_set(config, '{saml}', $3::jsonb)
		WHERE id = $1 AND config_version = $2 AND protocol = 'saml'`, id, version, string(b)))
}

func (s *SSOStore) CountRefreshFailing(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sso_connections WHERE enabled AND idp_refresh_ok = false`).Scan(&n)
	return n, err
}

// ---- SSO sessions (single logout) ----

const ssoSessionCols = `x.session_id::text, x.connection_id::text, x.org_id::text, x.user_id::text, x.subject, x.name_id_format,
	x.name_qualifier, x.sp_name_qualifier, x.session_index, x.id_token_enc, x.created_at`

func scanSSOSession(r pgx.Row) (sso.SSOSession, error) {
	var x sso.SSOSession
	err := r.Scan(&x.SessionID, &x.ConnectionID, &x.OrgID, &x.UserID, &x.Subject, &x.NameIDFormat, &x.NameQualifier, &x.SPNameQualifier,
		&x.SessionIndex, &x.IDTokenEnc, &x.CreatedAt)
	return x, mapErr(err)
}

func (s *SSOStore) CreateSSOSession(ctx context.Context, x *sso.SSOSession) error {
	if !validID(x.SessionID, x.ConnectionID, x.OrgID, x.UserID) {
		return auth.ErrNotFound
	}
	x.CreatedAt = ts(x.CreatedAt)
	_, err := s.pool.Exec(ctx, `INSERT INTO sso_sessions (session_id, connection_id, org_id, user_id, subject, name_id_format, name_qualifier,
			sp_name_qualifier, session_index, id_token_enc, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, x.SessionID, x.ConnectionID, x.OrgID, x.UserID, x.Subject, x.NameIDFormat,
		x.NameQualifier, x.SPNameQualifier, x.SessionIndex, x.IDTokenEnc, x.CreatedAt)
	return mapErr(err)
}

func (s *SSOStore) GetSSOSession(ctx context.Context, sessionID string) (sso.SSOSession, error) {
	if !validID(sessionID) {
		return sso.SSOSession{}, auth.ErrNotFound
	}
	return scanSSOSession(s.pool.QueryRow(ctx, `SELECT `+ssoSessionCols+` FROM sso_sessions x WHERE x.session_id = $1`, sessionID))
}

func (s *SSOStore) ListSSOSessions(ctx context.Context, connectionID, subject, userID string, now time.Time) ([]sso.SSOSession, error) {
	if !validID(connectionID) || (userID != "" && !validID(userID)) || (subject == "" && userID == "") {
		return []sso.SSOSession{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+ssoSessionCols+` FROM sso_sessions x JOIN sessions s ON s.id = x.session_id
		WHERE x.connection_id = $1 AND ($2 = '' OR x.subject = $2) AND ($3 = '' OR x.user_id::text = $3)
		  AND s.revoked_at IS NULL AND s.expires_at > $4
		ORDER BY x.created_at`, connectionID, subject, userID, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.SSOSession, error) { return scanSSOSession(r) })
}

// ---- domains ----

const domainCols = `id::text, org_id::text, domain, dns_token, email_token_hash, email_address, email_expires_at, verified_at,
	verification_method, last_checked_at, coalesce(connection_id::text, ''), coalesce(created_by::text, ''), created_at`

func scanDomain(row pgx.Row) (sso.Domain, error) {
	var d sso.Domain
	err := row.Scan(&d.ID, &d.OrgID, &d.Domain, &d.DNSToken, &d.EmailTokenHash, &d.EmailAddress, &d.EmailExpiresAt, &d.VerifiedAt,
		&d.VerificationMethod, &d.LastCheckedAt, &d.ConnectionID, &d.CreatedBy, &d.CreatedAt)
	return d, mapErr(err)
}

func (s *SSOStore) CreateDomain(ctx context.Context, d *sso.Domain) error {
	if !validID(d.OrgID) || (d.ConnectionID != "" && !validID(d.ConnectionID)) {
		return auth.ErrNotFound
	}
	d.CreatedAt = ts(d.CreatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO sso_domains (org_id, domain, dns_token, created_by, created_at, connection_id)
		VALUES ($1, $2, $3, $4::uuid, $5, $6::uuid) RETURNING id::text`, d.OrgID, d.Domain, d.DNSToken, nullID(d.CreatedBy), d.CreatedAt,
		nullID(d.ConnectionID)).Scan(&d.ID))
}

func (s *SSOStore) ListDomains(ctx context.Context, orgID string) ([]sso.Domain, error) {
	if !validID(orgID) {
		return []sso.Domain{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+domainCols+` FROM sso_domains WHERE org_id = $1 ORDER BY domain`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.Domain, error) { return scanDomain(r) })
}

func (s *SSOStore) GetDomain(ctx context.Context, orgID, id string) (sso.Domain, error) {
	if !validID(orgID, id) {
		return sso.Domain{}, auth.ErrNotFound
	}
	return scanDomain(s.pool.QueryRow(ctx, `SELECT `+domainCols+` FROM sso_domains WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *SSOStore) DeleteDomain(ctx context.Context, orgID, id string) (sso.Domain, error) {
	if !validID(orgID, id) {
		return sso.Domain{}, auth.ErrNotFound
	}
	return scanDomain(s.pool.QueryRow(ctx, `DELETE FROM sso_domains WHERE org_id = $1 AND id = $2 RETURNING `+domainCols, orgID, id))
}

func (s *SSOStore) MarkDomainVerified(ctx context.Context, orgID, id, method string, at time.Time) (sso.Domain, error) {
	if !validID(orgID, id) {
		return sso.Domain{}, auth.ErrNotFound
	}
	// A second organization verifying the same domain violates sso_domains_verified_uniq → ErrAlreadyExists.
	return scanDomain(s.pool.QueryRow(ctx, `UPDATE sso_domains SET verified_at = coalesce(verified_at, $3),
			verification_method = CASE WHEN verified_at IS NULL THEN $4 ELSE verification_method END,
			email_token_hash = NULL, email_expires_at = NULL
		WHERE org_id = $1 AND id = $2 RETURNING `+domainCols, orgID, id, at, method))
}

func (s *SSOStore) SetDomainChecked(ctx context.Context, id string, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_domains SET last_checked_at = $2 WHERE id = $1`, id, at))
}

func (s *SSOStore) SetDomainEmailToken(ctx context.Context, orgID, id string, hash []byte, address string, expires time.Time) error {
	if !validID(orgID, id) {
		return auth.ErrNotFound
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_domains SET email_token_hash = $3, email_address = $4, email_expires_at = $5
		WHERE org_id = $1 AND id = $2`, orgID, id, hash, address, expires))
}

func (s *SSOStore) ConsumeDomainEmailToken(ctx context.Context, hash []byte, at time.Time) (sso.Domain, error) {
	return scanDomain(s.pool.QueryRow(ctx, `UPDATE sso_domains SET verified_at = coalesce(verified_at, $2),
			verification_method = CASE WHEN verified_at IS NULL THEN 'email' ELSE verification_method END,
			email_token_hash = NULL, email_expires_at = NULL
		WHERE email_token_hash = $1 AND email_expires_at > $2 RETURNING `+domainCols, hash, at))
}

func (s *SSOStore) FindVerifiedDomain(ctx context.Context, domain string) (sso.Domain, error) {
	return scanDomain(s.pool.QueryRow(ctx, `SELECT `+domainCols+` FROM sso_domains WHERE domain = $1 AND verified_at IS NOT NULL`, domain))
}

func (s *SSOStore) SetDomainConnection(ctx context.Context, orgID, id, connectionID string) (sso.Domain, error) {
	if !validID(orgID, id) || (connectionID != "" && !validID(connectionID)) {
		return sso.Domain{}, auth.ErrNotFound
	}
	return scanDomain(s.pool.QueryRow(ctx, `UPDATE sso_domains SET connection_id = $3::uuid
		WHERE org_id = $1 AND id = $2
		  AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM sso_connections c WHERE c.id = $3::uuid AND c.org_id = $1))
		RETURNING `+domainCols, orgID, id, nullID(connectionID)))
}

// ---- role mappings ----

func (s *SSOStore) ListRoleMappings(ctx context.Context, orgID, connectionID string) ([]sso.RoleMapping, error) {
	if !validID(orgID) || (connectionID != "" && !validID(connectionID)) {
		return []sso.RoleMapping{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT group_name, role FROM sso_role_mappings
		WHERE org_id = $1 AND connection_id IS NOT DISTINCT FROM $2::uuid ORDER BY group_name`, orgID, nullID(connectionID))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.RoleMapping, error) {
		var m sso.RoleMapping
		var role string
		err := r.Scan(&m.Group, &role)
		m.Role = auth.Role(role)
		return m, err
	})
}

func (s *SSOStore) ReplaceRoleMappings(ctx context.Context, orgID, connectionID string, ms []sso.RoleMapping) error {
	if !validID(orgID) || (connectionID != "" && !validID(connectionID)) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if connectionID != "" {
			if err := affected(tx.Exec(ctx, `SELECT 1 FROM sso_connections WHERE id = $1 AND org_id = $2 FOR UPDATE`, connectionID, orgID)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sso_role_mappings WHERE org_id = $1 AND connection_id IS NOT DISTINCT FROM $2::uuid`,
			orgID, nullID(connectionID)); err != nil {
			return err
		}
		for _, m := range ms {
			if _, err := tx.Exec(ctx, `INSERT INTO sso_role_mappings (org_id, connection_id, group_name, role) VALUES ($1, $2::uuid, $3, $4)`,
				orgID, nullID(connectionID), m.Group, string(m.Role)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- login states & replay cache ----

const stateCols = `id::text, state_hash, binding_hash, org_id::text, connection_id::text, purpose, idp_initiated, nonce, pkce_verifier,
	saml_request_id, redirect_to, coalesce(actor_user_id::text, ''), config_version, result::text, created_at, expires_at, consumed_at`

func scanState(row pgx.Row) (sso.LoginState, error) {
	var st sso.LoginState
	var result *string
	err := row.Scan(&st.ID, &st.StateHash, &st.BindingHash, &st.OrgID, &st.ConnectionID, &st.Purpose, &st.IdPInitiated, &st.Nonce,
		&st.PKCEVerifier, &st.SAMLRequestID, &st.RedirectTo, &st.ActorUserID, &st.ConfigVersion, &result, &st.CreatedAt, &st.ExpiresAt, &st.ConsumedAt)
	if err != nil {
		return st, mapErr(err)
	}
	if result != nil {
		var id sso.Identity
		if err := json.Unmarshal([]byte(*result), &id); err != nil {
			return st, err
		}
		st.Result = &id
	}
	return st, nil
}

func (s *SSOStore) CreateLoginState(ctx context.Context, st *sso.LoginState) error {
	var result *string
	if st.Result != nil {
		b, err := json.Marshal(st.Result)
		if err != nil {
			return err
		}
		v := string(b)
		result = &v
	}
	st.CreatedAt = ts(st.CreatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO sso_login_states (state_hash, binding_hash, org_id, connection_id, purpose, idp_initiated,
			nonce, pkce_verifier, saml_request_id, redirect_to, actor_user_id, config_version, result, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid, $12, $13::jsonb, $14, $15) RETURNING id::text`,
		st.StateHash, st.BindingHash, st.OrgID, st.ConnectionID, st.Purpose, st.IdPInitiated, st.Nonce, st.PKCEVerifier, st.SAMLRequestID,
		st.RedirectTo, nullID(st.ActorUserID), st.ConfigVersion, result, st.CreatedAt, st.ExpiresAt).Scan(&st.ID))
}

func (s *SSOStore) GetLoginState(ctx context.Context, hash []byte, now time.Time) (sso.LoginState, error) {
	return scanState(s.pool.QueryRow(ctx, `SELECT `+stateCols+` FROM sso_login_states
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2`, hash, now))
}

func (s *SSOStore) SetLoginResult(ctx context.Context, id string, result sso.Identity, now time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return affected(s.pool.Exec(ctx, `UPDATE sso_login_states SET result = $2::jsonb
		WHERE id = $1 AND result IS NULL AND consumed_at IS NULL AND expires_at > $3`, id, string(b), now))
}

func (s *SSOStore) ConsumeLoginState(ctx context.Context, hash []byte, now time.Time) (sso.LoginState, error) {
	return scanState(s.pool.QueryRow(ctx, `UPDATE sso_login_states SET consumed_at = $2
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2 RETURNING `+stateCols, hash, now))
}

func (s *SSOStore) RecordAssertion(ctx context.Context, connectionID, assertionID string, expires time.Time) (bool, error) {
	if !validID(connectionID) {
		return false, auth.ErrNotFound
	}
	// An id whose entry has already expired (cleanup not run yet) is accepted again.
	tag, err := s.pool.Exec(ctx, `INSERT INTO sso_saml_assertions (connection_id, assertion_id, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (connection_id, assertion_id) DO UPDATE SET expires_at = EXCLUDED.expires_at
		WHERE sso_saml_assertions.expires_at < now()`, connectionID, assertionID, expires)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SSOStore) Cleanup(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sso_login_states WHERE expires_at < $1`, now.Add(-time.Hour)); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM sso_saml_assertions WHERE expires_at < $1`, now); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE sso_domains SET email_token_hash = NULL, email_expires_at = NULL
		WHERE email_expires_at < $1`, now)
	return err
}

// ---- SCIM tokens ----

const scimTokenCols = `t.id::text, t.org_id::text, t.name, t.key_prefix, t.key_hash, coalesce(t.created_by::text, ''), t.created_at,
	t.last_used_at, t.expires_at, t.revoked_at`

func scanSCIMToken(r pgx.Row, extra ...any) (sso.SCIMToken, error) {
	var t sso.SCIMToken
	dest := append([]any{&t.ID, &t.OrgID, &t.Name, &t.Prefix, &t.Hash, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt}, extra...)
	return t, r.Scan(dest...)
}

func (s *SSOStore) CreateSCIMToken(ctx context.Context, t *sso.SCIMToken) error {
	if !validID(t.OrgID) {
		return auth.ErrNotFound
	}
	t.CreatedAt = ts(t.CreatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO scim_tokens (org_id, name, key_prefix, key_hash, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7) RETURNING id::text`,
		t.OrgID, t.Name, t.Prefix, t.Hash, nullID(t.CreatedBy), t.CreatedAt, t.ExpiresAt).Scan(&t.ID))
}

func (s *SSOStore) ListSCIMTokens(ctx context.Context, orgID string) ([]sso.SCIMToken, error) {
	if !validID(orgID) {
		return []sso.SCIMToken{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+scimTokenCols+`, coalesce(u.email, '')
		FROM scim_tokens t LEFT JOIN users u ON u.id = t.created_by WHERE t.org_id = $1 ORDER BY t.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.SCIMToken, error) {
		var email string
		t, err := scanSCIMToken(r, &email)
		t.CreatedByEmail = email
		return t, err
	})
}

func (s *SSOStore) RevokeSCIMToken(ctx context.Context, orgID, id string, at time.Time) (sso.SCIMToken, error) {
	if !validID(orgID, id) {
		return sso.SCIMToken{}, auth.ErrNotFound
	}
	t, err := scanSCIMToken(s.pool.QueryRow(ctx, `UPDATE scim_tokens t SET revoked_at = coalesce(t.revoked_at, $3)
		WHERE t.org_id = $1 AND t.id = $2 RETURNING `+scimTokenCols, orgID, id, at))
	return t, mapErr(err)
}

// LookupSCIMToken finds a token by any candidate hash and rewrites an active token found by an older-format hash
// (like LookupAPIKey, D-044).
func (s *SSOStore) LookupSCIMToken(ctx context.Context, hashes [][]byte) (sso.SCIMToken, auth.Organization, error) {
	var o auth.Organization
	if len(hashes) == 0 {
		return sso.SCIMToken{}, o, auth.ErrNotFound
	}
	t, err := scanSCIMToken(s.pool.QueryRow(ctx, `WITH hit AS (
			SELECT id, key_hash FROM scim_tokens WHERE key_hash = ANY($1::bytea[])
			ORDER BY array_position($1::bytea[], key_hash) LIMIT 1
		), rehash AS (
			UPDATE scim_tokens x SET key_hash = $2 FROM hit
			WHERE x.id = hit.id AND hit.key_hash <> $2 AND x.revoked_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM scim_tokens y WHERE y.key_hash = $2)
		)
		SELECT `+scimTokenCols+`, o.id::text, o.tenant_id, o.name, o.created_at
		FROM scim_tokens t JOIN hit ON hit.id = t.id JOIN organizations o ON o.id = t.org_id`, hashes, hashes[0]),
		&o.ID, &o.TenantID, &o.Name, &o.CreatedAt)
	return t, o, mapErr(err)
}

func (s *SSOStore) TouchSCIMToken(ctx context.Context, id string, at time.Time) error {
	if !validID(id) {
		return auth.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `UPDATE scim_tokens SET last_used_at = $2::timestamptz
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, at)
	return mapErr(err)
}

// ---- SCIM users ----

const scimUserCols = `org_id::text, user_id::text, user_name, external_id, active, display_name, given_name, family_name, created_at, updated_at`

func scanSCIMUser(r pgx.Row) (sso.SCIMUser, error) {
	var u sso.SCIMUser
	err := r.Scan(&u.OrgID, &u.UserID, &u.UserName, &u.ExternalID, &u.Active, &u.DisplayName, &u.GivenName, &u.FamilyName, &u.CreatedAt, &u.UpdatedAt)
	return u, mapErr(err)
}

func (s *SSOStore) CreateSCIMUser(ctx context.Context, u *sso.SCIMUser) error {
	if !validID(u.OrgID, u.UserID) {
		return auth.ErrNotFound
	}
	u.CreatedAt = ts(u.CreatedAt)
	u.UpdatedAt = u.CreatedAt
	_, err := s.pool.Exec(ctx, `INSERT INTO scim_users (org_id, user_id, user_name, external_id, active, display_name, given_name, family_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`, u.OrgID, u.UserID, u.UserName, u.ExternalID, u.Active, u.DisplayName, u.GivenName, u.FamilyName, u.CreatedAt)
	return mapErr(err)
}

func (s *SSOStore) GetSCIMUser(ctx context.Context, orgID, userID string) (sso.SCIMUser, error) {
	if !validID(orgID, userID) {
		return sso.SCIMUser{}, auth.ErrNotFound
	}
	return scanSCIMUser(s.pool.QueryRow(ctx, `SELECT `+scimUserCols+` FROM scim_users WHERE org_id = $1 AND user_id = $2`, orgID, userID))
}

func (s *SSOStore) ListSCIMUsers(ctx context.Context, orgID string, f sso.SCIMFilter, offset, limit int) ([]sso.SCIMUser, int, error) {
	if !validID(orgID) {
		return []sso.SCIMUser{}, 0, nil
	}
	where := ` FROM scim_users WHERE org_id = $1 AND ($2 = '' OR lower(user_name) = lower($2)) AND ($3 = '' OR external_id = $3)`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+where, orgID, f.UserName, f.ExternalID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+scimUserCols+where+` ORDER BY created_at, user_id OFFSET $4 LIMIT $5`,
		orgID, f.UserName, f.ExternalID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.SCIMUser, error) { return scanSCIMUser(r) })
	return out, total, err
}

func (s *SSOStore) UpdateSCIMUser(ctx context.Context, u *sso.SCIMUser) error {
	if !validID(u.OrgID, u.UserID) {
		return auth.ErrNotFound
	}
	return mapErr(s.pool.QueryRow(ctx, `UPDATE scim_users SET user_name = $3, external_id = $4, active = $5, display_name = $6,
			given_name = $7, family_name = $8, updated_at = now()
		WHERE org_id = $1 AND user_id = $2 RETURNING created_at, updated_at`,
		u.OrgID, u.UserID, u.UserName, u.ExternalID, u.Active, u.DisplayName, u.GivenName, u.FamilyName).Scan(&u.CreatedAt, &u.UpdatedAt))
}

func (s *SSOStore) DeleteSCIMUser(ctx context.Context, orgID, userID string) error {
	if !validID(orgID, userID) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM scim_group_members m USING scim_groups g
			WHERE m.group_id = g.id AND g.org_id = $1 AND m.user_id = $2`, orgID, userID); err != nil {
			return err
		}
		return affected(tx.Exec(ctx, `DELETE FROM scim_users WHERE org_id = $1 AND user_id = $2`, orgID, userID))
	})
}

// ---- SCIM groups ----

const scimGroupCols = `g.id::text, g.org_id::text, g.display_name, g.external_id, g.created_at, g.updated_at,
	array(SELECT m.user_id::text FROM scim_group_members m WHERE m.group_id = g.id ORDER BY m.user_id)`

func scanSCIMGroup(r pgx.Row) (sso.SCIMGroup, error) {
	var g sso.SCIMGroup
	err := r.Scan(&g.ID, &g.OrgID, &g.DisplayName, &g.ExternalID, &g.CreatedAt, &g.UpdatedAt, &g.Members)
	if g.Members == nil {
		g.Members = []string{}
	}
	return g, mapErr(err)
}

func setMembers(ctx context.Context, tx pgx.Tx, groupID string, members []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM scim_group_members WHERE group_id = $1`, groupID); err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO scim_group_members (group_id, user_id) SELECT $1, u FROM unnest($2::uuid[]) AS u ON CONFLICT DO NOTHING`, groupID, members)
	return err
}

func (s *SSOStore) CreateSCIMGroup(ctx context.Context, g *sso.SCIMGroup) error {
	if !validID(g.OrgID) || !validID(g.Members...) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO scim_groups (org_id, display_name, external_id) VALUES ($1, $2, $3)
			RETURNING id::text, created_at, updated_at`, g.OrgID, g.DisplayName, g.ExternalID).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return err
		}
		return setMembers(ctx, tx, g.ID, g.Members)
	})
}

func (s *SSOStore) GetSCIMGroup(ctx context.Context, orgID, id string) (sso.SCIMGroup, error) {
	if !validID(orgID, id) {
		return sso.SCIMGroup{}, auth.ErrNotFound
	}
	return scanSCIMGroup(s.pool.QueryRow(ctx, `SELECT `+scimGroupCols+` FROM scim_groups g WHERE g.org_id = $1 AND g.id = $2`, orgID, id))
}

func (s *SSOStore) ListSCIMGroups(ctx context.Context, orgID string, f sso.SCIMFilter, offset, limit int) ([]sso.SCIMGroup, int, error) {
	if !validID(orgID) {
		return []sso.SCIMGroup{}, 0, nil
	}
	where := ` FROM scim_groups g WHERE g.org_id = $1 AND ($2 = '' OR lower(g.display_name) = lower($2)) AND ($3 = '' OR g.external_id = $3)`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+where, orgID, f.DisplayName, f.ExternalID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+scimGroupCols+where+` ORDER BY g.created_at, g.id OFFSET $4 LIMIT $5`,
		orgID, f.DisplayName, f.ExternalID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sso.SCIMGroup, error) { return scanSCIMGroup(r) })
	return out, total, err
}

func (s *SSOStore) UpdateSCIMGroup(ctx context.Context, g *sso.SCIMGroup, members []string) error {
	if !validID(g.OrgID, g.ID) || !validID(members...) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE scim_groups SET display_name = $3, external_id = $4, updated_at = now()
			WHERE org_id = $1 AND id = $2`, g.OrgID, g.ID, g.DisplayName, g.ExternalID)); err != nil {
			return err
		}
		if members != nil {
			if err := setMembers(ctx, tx, g.ID, members); err != nil {
				return err
			}
		}
		ng, err := scanSCIMGroup(tx.QueryRow(ctx, `SELECT `+scimGroupCols+` FROM scim_groups g WHERE g.id = $1`, g.ID))
		if err == nil {
			*g = ng
		}
		return err
	})
}

func (s *SSOStore) AddSCIMGroupMembers(ctx context.Context, orgID, groupID string, userIDs []string) error {
	if !validID(orgID, groupID) || !validID(userIDs...) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE scim_groups SET updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, groupID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO scim_group_members (group_id, user_id) SELECT $1, u FROM unnest($2::uuid[]) AS u ON CONFLICT DO NOTHING`, groupID, userIDs)
		return err
	})
}

func (s *SSOStore) RemoveSCIMGroupMembers(ctx context.Context, orgID, groupID string, userIDs []string) error {
	if !validID(orgID, groupID) {
		return auth.ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE scim_groups SET updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, groupID)); err != nil {
			return err
		}
		valid := userIDs[:0:0]
		for _, id := range userIDs {
			if validID(id) {
				valid = append(valid, id)
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM scim_group_members WHERE group_id = $1 AND user_id = ANY($2::uuid[])`, groupID, valid)
		return err
	})
}

func (s *SSOStore) DeleteSCIMGroup(ctx context.Context, orgID, id string) (sso.SCIMGroup, error) {
	if !validID(orgID, id) {
		return sso.SCIMGroup{}, auth.ErrNotFound
	}
	g, err := s.GetSCIMGroup(ctx, orgID, id)
	if err != nil {
		return g, err
	}
	return g, affected(s.pool.Exec(ctx, `DELETE FROM scim_groups WHERE org_id = $1 AND id = $2`, orgID, id))
}

func (s *SSOStore) SCIMGroupNames(ctx context.Context, orgID, userID string) ([]string, error) {
	if !validID(orgID, userID) {
		return []string{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT g.display_name FROM scim_groups g JOIN scim_group_members m ON m.group_id = g.id
		WHERE g.org_id = $1 AND m.user_id = $2 ORDER BY g.display_name`, orgID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
