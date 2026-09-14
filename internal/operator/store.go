// Package operator implements SaaS operations (docs/operations/saas.md, D-105, D-106): the superadmin console's
// organization queries and actions (suspend, trials, notification reset, force logout, owner verification e-mails),
// owner-granted time-boxed support access with audited read-only support sessions, hard host and user limits,
// the trial lifecycle job and the abuse detector.
package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors.
var (
	ErrNotFound     = errors.New("not found")
	ErrNoAccess     = errors.New("the organization has not granted support access")
	ErrInvalidState = errors.New("invalid state")
)

// Actor is who performs an action (audit log).
type Actor struct {
	UserID string
	Email  string
	IP     string
}

// Store is the PostgreSQL persistence of SaaS operations.
type Store struct{ Pool *pgxpool.Pool }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func nullUUID(v string) any {
	if uuidRe.MatchString(v) {
		return v
	}
	return nil
}

// execer is satisfied by pgxpool.Pool and pgx.Tx.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// audit writes one audit_log row of the organization.
func audit(ctx context.Context, q execer, orgID string, a Actor, action, targetType, targetID string, details map[string]any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8)`, orgID, nullUUID(a.UserID), a.Email, action, targetType, targetID, string(b), a.IP)
	return err
}

// ---- organizations ----

// OrgFilter selects organizations of the operator console.
type OrgFilter struct {
	Query  string // name, tenant id or member e-mail substring (case-insensitive)
	PlanID string // effective plan
	State  string // "", active, suspended, trial, flagged
	// DefaultPlan is the plan of organizations without an assignment.
	DefaultPlan string
	Sort        string // created (newest first, default), name, ingest, members, last_ingest
	Limit       int
	Offset      int
}

// OrgSummary is one row of the organization list.
type OrgSummary struct {
	OrgID              string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	Name               string     `json:"name"`
	CreatedAt          time.Time  `json:"created_at"`
	PlanID             string     `json:"plan_id"`
	PlanAssigned       bool       `json:"plan_assigned"`
	State              string     `json:"state"` // active, suspended, trial
	SuspendedAt        *time.Time `json:"suspended_at"`
	SuspendReason      string     `json:"suspend_reason"`
	TrialPlanID        string     `json:"trial_plan_id"`
	TrialEndsAt        *time.Time `json:"trial_ends_at"`
	SupportAccessUntil *time.Time `json:"support_access_until"`
	Members            int64      `json:"members"`
	ActiveHosts        int64      `json:"active_hosts"`
	IngestBytes        int64      `json:"ingest_bytes"`
	QuotaLevel         string     `json:"quota_level"`
	LastIngestAt       *time.Time `json:"last_ingest_at"`
	OpenFlags          int64      `json:"open_flags"`
}

const orgSummarySelect = `SELECT o.id::text, o.tenant_id, o.name, o.created_at,
	coalesce(p.plan_id, $1::text), p.org_id IS NOT NULL,
	st.suspended_at, coalesce(st.suspend_reason, ''),
	CASE WHEN st.trial_ends_at IS NOT NULL AND st.trial_ended_at IS NULL THEN st.trial_plan_id ELSE '' END,
	CASE WHEN st.trial_ended_at IS NULL THEN st.trial_ends_at END,
	CASE WHEN st.support_access_until > now() THEN st.support_access_until END,
	(SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = o.id AND u.disabled_at IS NULL),
	coalesce((SELECT (e->>'used')::float8 FROM jsonb_array_elements(q.metrics) e WHERE e->>'metric' = 'hosts' LIMIT 1), 0)::bigint,
	coalesce(q.ingest_bytes, 0), coalesce(q.level, 'ok'),
	(SELECT max(k.last_used_at) FROM license_keys k WHERE k.org_id = o.id),
	(SELECT count(*) FROM abuse_flags f WHERE f.org_id = o.id AND f.status = 'open')
FROM organizations o
LEFT JOIN org_plans p ON p.org_id = o.id
LEFT JOIN org_saas_state st ON st.org_id = o.id
LEFT JOIN tenant_quota_status q ON q.tenant_id = o.tenant_id`

func scanSummary(row pgx.Row) (OrgSummary, error) {
	var s OrgSummary
	err := row.Scan(&s.OrgID, &s.TenantID, &s.Name, &s.CreatedAt, &s.PlanID, &s.PlanAssigned, &s.SuspendedAt, &s.SuspendReason,
		&s.TrialPlanID, &s.TrialEndsAt, &s.SupportAccessUntil, &s.Members, &s.ActiveHosts, &s.IngestBytes, &s.QuotaLevel,
		&s.LastIngestAt, &s.OpenFlags)
	switch {
	case s.SuspendedAt != nil:
		s.State = "suspended"
	case s.TrialEndsAt != nil:
		s.State = "trial"
	default:
		s.State = "active"
	}
	return s, err
}

// ListOrgs returns a page of organizations and the total number matching f.
func (s Store) ListOrgs(ctx context.Context, f OrgFilter) ([]OrgSummary, int64, error) {
	args := []any{f.DefaultPlan}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	var where []string
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q)) + "%"
		n := arg(like)
		where = append(where, `(lower(o.name) LIKE `+n+` OR o.tenant_id LIKE `+n+` OR o.id::text = `+arg(strings.ToLower(q))+
			` OR EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = o.id AND u.email LIKE `+n+`))`)
	}
	if f.PlanID != "" {
		where = append(where, `coalesce(p.plan_id, $1) = `+arg(f.PlanID))
	}
	switch f.State {
	case "":
	case "suspended":
		where = append(where, `st.suspended_at IS NOT NULL`)
	case "trial":
		where = append(where, `st.suspended_at IS NULL AND st.trial_ends_at IS NOT NULL AND st.trial_ended_at IS NULL`)
	case "active":
		where = append(where, `st.suspended_at IS NULL AND (st.trial_ends_at IS NULL OR st.trial_ended_at IS NOT NULL)`)
	case "flagged":
		where = append(where, `EXISTS (SELECT 1 FROM abuse_flags f WHERE f.org_id = o.id AND f.status = 'open')`)
	default:
		return nil, 0, fmt.Errorf("%w: state must be active, suspended, trial or flagged", ErrInvalidState)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	// The CTE types $1 (the default plan) even when no condition references it.
	if err := s.Pool.QueryRow(ctx, `WITH d AS (SELECT $1::text AS default_plan) SELECT count(*) FROM organizations o
		LEFT JOIN org_plans p ON p.org_id = o.id LEFT JOIN org_saas_state st ON st.org_id = o.id`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := " ORDER BY o.created_at DESC, o.id"
	switch f.Sort {
	case "", "created":
	case "name":
		order = " ORDER BY lower(o.name), o.id"
	case "ingest":
		order = " ORDER BY coalesce(q.ingest_bytes, 0) DESC, o.id"
	case "members":
		order = " ORDER BY 12 DESC, o.id"
	case "last_ingest":
		order = " ORDER BY 16 DESC NULLS LAST, o.id"
	default:
		return nil, 0, fmt.Errorf("%w: sort must be created, name, ingest, members or last_ingest", ErrInvalidState)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, orgSummarySelect+cond+order+" LIMIT "+arg(limit)+" OFFSET "+arg(max(f.Offset, 0)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []OrgSummary{}
	for rows.Next() {
		o, err := scanSummary(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	return out, total, rows.Err()
}

// resolveOrg maps an organization id or tenant id to the id.
func (s Store) resolveOrg(ctx context.Context, ref string) (string, error) {
	var id string
	var err error
	if uuidRe.MatchString(ref) {
		err = s.Pool.QueryRow(ctx, `SELECT id::text FROM organizations WHERE id = $1::uuid`, ref).Scan(&id)
	} else {
		err = s.Pool.QueryRow(ctx, `SELECT id::text FROM organizations WHERE tenant_id = $1`, ref).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// Member is a member of an organization in the operator console.
type Member struct {
	UserID        string     `json:"user_id"`
	Email         string     `json:"email"`
	Name          string     `json:"name"`
	Role          string     `json:"role"`
	JoinedAt      time.Time  `json:"joined_at"`
	LastLoginAt   *time.Time `json:"last_login_at"`
	EmailVerified bool       `json:"email_verified"`
	Disabled      bool       `json:"disabled"`
}

// SSOConnection summarizes a single sign-on connection (no configuration, no secrets).
type SSOConnection struct {
	ID         string `json:"id"`
	Protocol   string `json:"protocol"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Enforce    bool   `json:"enforce"`
	JITEnabled bool   `json:"jit_enabled"`
	LastTestOK bool   `json:"last_test_ok"`
}

// KeyCounts counts credentials of an organization (never their values).
type KeyCounts struct {
	LicenseKeysActive  int64      `json:"license_keys_active"`
	LicenseKeysRevoked int64      `json:"license_keys_revoked"`
	APIKeysActive      int64      `json:"api_keys_active"`
	SCIMTokensActive   int64      `json:"scim_tokens_active"`
	LastIngestAt       *time.Time `json:"last_ingest_at"`
}

// OrgDetail is the operator view of one organization.
type OrgDetail struct {
	OrgSummary
	// MemberList is named apart from OrgSummary.Members (the count), which an equal JSON name would shadow.
	MemberList         []Member         `json:"member_list"`
	PendingInvitations int64            `json:"pending_invitations"`
	Keys               KeyCounts        `json:"keys"`
	SSOConnections     []SSOConnection  `json:"sso_connections"`
	VerifiedDomains    int64            `json:"verified_domains"`
	Flags              []Flag           `json:"flags"`
	SupportSessions    []SupportSession `json:"support_sessions"`
	SupportGrantedBy   string           `json:"support_access_granted_by"`
}

// GetOrg returns the operator view of the organization with id or tenant id ref.
func (s Store) GetOrg(ctx context.Context, ref, defaultPlan string) (OrgDetail, error) {
	var d OrgDetail
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return d, err
	}
	d.OrgSummary, err = scanSummary(s.Pool.QueryRow(ctx, orgSummarySelect+` WHERE o.id = $2::uuid`, defaultPlan, id))
	if err != nil {
		return d, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT u.id::text, u.email, u.name, m.role, m.created_at, u.last_login_at, u.email_verified_at IS NOT NULL, u.disabled_at IS NOT NULL
		FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1::uuid
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, u.email LIMIT 500`, id)
	if err != nil {
		return d, err
	}
	d.MemberList = []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt, &m.LastLoginAt, &m.EmailVerified, &m.Disabled); err != nil {
			rows.Close()
			return d, err
		}
		d.MemberList = append(d.MemberList, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM invitations WHERE org_id = $1::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()),
		(SELECT count(*) FROM license_keys WHERE org_id = $1::uuid AND revoked_at IS NULL),
		(SELECT count(*) FROM license_keys WHERE org_id = $1::uuid AND revoked_at IS NOT NULL),
		(SELECT count(*) FROM api_keys WHERE org_id = $1::uuid AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())),
		(SELECT count(*) FROM scim_tokens WHERE org_id = $1::uuid AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())),
		(SELECT max(last_used_at) FROM license_keys WHERE org_id = $1::uuid),
		(SELECT count(*) FROM sso_domains WHERE org_id = $1::uuid AND verified_at IS NOT NULL),
		coalesce((SELECT u.email FROM org_saas_state st JOIN users u ON u.id = st.support_access_granted_by WHERE st.org_id = $1::uuid), '')`, id).
		Scan(&d.PendingInvitations, &d.Keys.LicenseKeysActive, &d.Keys.LicenseKeysRevoked, &d.Keys.APIKeysActive, &d.Keys.SCIMTokensActive,
			&d.Keys.LastIngestAt, &d.VerifiedDomains, &d.SupportGrantedBy)
	if err != nil {
		return d, err
	}
	rows, err = s.Pool.Query(ctx, `SELECT id::text, protocol, name, enabled, enforce, jit_enabled, last_test_ok FROM sso_connections
		WHERE org_id = $1::uuid ORDER BY created_at, id`, id)
	if err != nil {
		return d, err
	}
	d.SSOConnections = []SSOConnection{}
	for rows.Next() {
		var c SSOConnection
		if err := rows.Scan(&c.ID, &c.Protocol, &c.Name, &c.Enabled, &c.Enforce, &c.JITEnabled, &c.LastTestOK); err != nil {
			rows.Close()
			return d, err
		}
		d.SSOConnections = append(d.SSOConnections, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	if d.Flags, err = s.listFlags(ctx, `WHERE f.org_id = $1::uuid`, 20, id); err != nil {
		return d, err
	}
	if d.SupportSessions, err = s.listSupportSessions(ctx, id, 10); err != nil {
		return d, err
	}
	return d, nil
}

// AuditEntry is an audit log row shown in the operator console.
type AuditEntry struct {
	ID         int64          `json:"id"`
	ActorEmail string         `json:"actor_email"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

// RecentAudit returns the organization's newest audit events (no IP addresses).
func (s Store) RecentAudit(ctx context.Context, orgID string, limit int) ([]AuditEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, actor_email, action, target_type, target_id, details, created_at FROM audit_log
		WHERE org_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var details []byte
		if err := rows.Scan(&e.ID, &e.ActorEmail, &e.Action, &e.TargetType, &e.TargetID, &details, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(details, &e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}
