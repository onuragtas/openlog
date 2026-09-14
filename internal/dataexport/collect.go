package dataexport

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// document is one JSON file of the archive: SQL returns either a single jsonb value (single) or one jsonb value per
// row (written as a JSON array without holding the rows in memory). Every query selects explicit columns: secrets,
// key hashes, tokens and encrypted values are never exported.
type document struct {
	name   string
	single bool
	sql    string
}

// OrgDocuments are the PostgreSQL files of an organization export ($1 = organization id).
var OrgDocuments = []document{
	{"organization.json", true, `SELECT jsonb_build_object('id', id, 'tenant_id', tenant_id, 'name', name, 'email_language', locale,
		'created_at', created_at, 'updated_at', updated_at) FROM organizations WHERE id::text = $1`},
	{"members.json", false, `SELECT jsonb_build_object('user_id', u.id, 'email', u.email, 'name', u.name, 'role', m.role, 'joined_at', m.created_at,
		'last_login_at', u.last_login_at) FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id::text = $1 ORDER BY u.email`},
	{"invitations.json", false, `SELECT jsonb_build_object('id', i.id, 'email', i.email, 'role', i.role, 'invited_by', coalesce(u.email, ''),
		'created_at', i.created_at, 'expires_at', i.expires_at, 'accepted_at', i.accepted_at, 'revoked_at', i.revoked_at)
		FROM invitations i LEFT JOIN users u ON u.id = i.invited_by WHERE i.org_id::text = $1 ORDER BY i.created_at`},
	{"license_keys.json", false, `SELECT jsonb_build_object('id', k.id, 'name', k.name, 'prefix', k.key_prefix, 'custom', k.custom,
		'created_by', coalesce(u.email, ''), 'created_at', k.created_at, 'last_used_at', k.last_used_at, 'revoked_at', k.revoked_at)
		FROM license_keys k LEFT JOIN users u ON u.id = k.created_by WHERE k.org_id::text = $1 ORDER BY k.created_at`},
	{"api_keys.json", false, `SELECT jsonb_build_object('id', k.id, 'name', k.name, 'prefix', k.key_prefix, 'scope', k.scope,
		'created_by', coalesce(u.email, ''), 'created_at', k.created_at, 'last_used_at', k.last_used_at, 'expires_at', k.expires_at, 'revoked_at', k.revoked_at)
		FROM api_keys k LEFT JOIN users u ON u.id = k.created_by WHERE k.org_id::text = $1 ORDER BY k.created_at`},
	{"dashboards.json", false, `SELECT jsonb_build_object('id', d.id, 'name', d.name, 'description', d.description, 'visibility', d.visibility,
		'variables', d.variables, 'created_by', coalesce(u.email, ''), 'created_at', d.created_at, 'updated_at', d.updated_at,
		'pages', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', p.id, 'name', p.name, 'widgets',
			(SELECT coalesce(jsonb_agg(jsonb_build_object('id', w.id, 'title', w.title, 'visualization', w.visualization, 'x', w.x, 'y', w.y,
				'w', w.w, 'h', w.h, 'query', w.query, 'markdown', w.markdown, 'unit', w.unit, 'thresholds', w.thresholds, 'options', w.options)
				ORDER BY w.position), '[]'::jsonb) FROM dashboard_widgets w WHERE w.page_id = p.id)) ORDER BY p.position), '[]'::jsonb)
			FROM dashboard_pages p WHERE p.dashboard_id = d.id))
		FROM dashboards d LEFT JOIN users u ON u.id = d.created_by WHERE d.org_id::text = $1 ORDER BY d.name`},
	{"dashboard_reports.json", false, `SELECT jsonb_build_object('id', r.id, 'dashboard_id', r.dashboard_id, 'name', r.name, 'frequency', r.frequency,
		'weekday', r.weekday, 'hour', r.hour, 'minute', r.minute, 'timezone', r.timezone, 'recipients', r.recipients, 'language', r.language,
		'time_range', r.time_range, 'variables', r.variables, 'enabled', r.enabled, 'created_at', r.created_at, 'updated_at', r.updated_at)
		FROM dashboard_reports r WHERE r.org_id::text = $1 ORDER BY r.created_at`},
	{"alert_rules.json", false, `SELECT jsonb_build_object('id', r.id, 'name', r.name, 'description', r.description, 'type', r.type,
		'severity', r.severity, 'enabled', r.enabled, 'interval_seconds', r.interval_seconds, 'for_seconds', r.for_seconds,
		'recovery_for_seconds', r.recovery_for_seconds, 'condition', r.condition, 'renotify_interval_seconds', r.renotify_interval_seconds,
		'flapping', r.flapping, 'runbook_url', r.runbook_url, 'labels', r.labels, 'created_by', coalesce(u.email, ''),
		'created_at', r.created_at, 'updated_at', r.updated_at,
		'channel_ids', (SELECT coalesce(jsonb_agg(rc.channel_id), '[]'::jsonb) FROM alert_rule_channels rc WHERE rc.rule_id = r.id))
		FROM alert_rules r LEFT JOIN users u ON u.id = r.created_by WHERE r.org_id::text = $1 ORDER BY r.name`},
	// Channel secrets (webhook URLs, tokens, SMTP passwords) stay out; secret_hints are the masked forms the API shows.
	{"alert_channels.json", false, `SELECT jsonb_build_object('id', c.id, 'name', c.name, 'type', c.type, 'enabled', c.enabled, 'config', c.config,
		'secret_hints', c.secret_hints, 'created_at', c.created_at, 'updated_at', c.updated_at)
		FROM alert_channels c WHERE c.org_id::text = $1 ORDER BY c.name`},
	{"alert_mutes.json", false, `SELECT to_jsonb(m) - 'org_id' - 'created_by' FROM alert_mutes m WHERE m.org_id::text = $1 ORDER BY m.created_at`},
	{"alert_incidents.json", false, `SELECT to_jsonb(i) - 'org_id' - 'acknowledged_by' - 'resolved_by' FROM alert_incidents i
		WHERE i.org_id::text = $1 ORDER BY i.id LIMIT 100000`},
	// Client secrets and SP private keys are encrypted columns that are not selected.
	{"sso.json", true, `SELECT jsonb_build_object(
		'connections', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', c.id, 'protocol', c.protocol, 'name', c.name, 'enabled', c.enabled,
			'config', c.config, 'email_attribute', c.email_attribute, 'name_attribute', c.name_attribute, 'groups_attribute', c.groups_attribute,
			'jit_enabled', c.jit_enabled, 'default_role', c.default_role, 'session_max_age_seconds', c.session_max_age_seconds, 'enforce', c.enforce,
			'allow_external_invitations', c.allow_external_invitations, 'created_at', c.created_at, 'updated_at', c.updated_at) ORDER BY c.created_at), '[]'::jsonb)
			FROM sso_connections c WHERE c.org_id::text = $1),
		'domains', (SELECT coalesce(jsonb_agg(jsonb_build_object('domain', d.domain, 'connection_id', d.connection_id, 'verified_at', d.verified_at,
			'verification_method', d.verification_method, 'created_at', d.created_at) ORDER BY d.domain), '[]'::jsonb) FROM sso_domains d WHERE d.org_id::text = $1),
		'role_mappings', (SELECT coalesce(jsonb_agg(jsonb_build_object('group', r.group_name, 'role', r.role, 'connection_id', r.connection_id)
			ORDER BY r.group_name), '[]'::jsonb) FROM sso_role_mappings r WHERE r.org_id::text = $1),
		'scim_tokens', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', t.id, 'name', t.name, 'prefix', t.key_prefix, 'created_at', t.created_at,
			'last_used_at', t.last_used_at, 'expires_at', t.expires_at, 'revoked_at', t.revoked_at) ORDER BY t.created_at), '[]'::jsonb)
			FROM scim_tokens t WHERE t.org_id::text = $1))`},
	{"plan.json", true, `SELECT jsonb_build_object(
		'plan', (SELECT to_jsonb(p) - 'org_id' - 'updated_by' FROM org_plans p WHERE p.org_id::text = $1),
		'query_limits', (SELECT to_jsonb(q) - 'org_id' - 'updated_by' FROM org_query_limits q WHERE q.org_id::text = $1),
		'tail_sampling_policy', (SELECT to_jsonb(t) - 'org_id' - 'updated_by' FROM tail_sampling_policies t WHERE t.org_id::text = $1))`},
	{"audit_log.json", false, `SELECT jsonb_build_object('id', a.id, 'actor_email', a.actor_email, 'action', a.action, 'target_type', a.target_type,
		'target_id', a.target_id, 'details', a.details, 'ip', a.ip, 'created_at', a.created_at)
		FROM audit_log a WHERE a.org_id::text = $1 ORDER BY a.created_at, a.id`},
}

// UserDocuments are the files of a personal export ($1 = user id).
var UserDocuments = []document{
	{"profile.json", true, `SELECT jsonb_build_object('id', id, 'email', email, 'name', name, 'language', CASE WHEN locale_explicit THEN locale ELSE 'auto' END,
		'request_language', locale, 'has_password', password_hash IS NOT NULL, 'email_verified_at', email_verified_at, 'created_at', created_at,
		'updated_at', updated_at, 'last_login_at', last_login_at, 'disabled_at', disabled_at) FROM users WHERE id::text = $1`},
	{"memberships.json", false, `SELECT jsonb_build_object('organization_id', o.id, 'organization_name', o.name, 'tenant_id', o.tenant_id,
		'role', m.role, 'joined_at', m.created_at) FROM memberships m JOIN organizations o ON o.id = m.org_id WHERE m.user_id::text = $1 ORDER BY m.created_at`},
	// Session metadata only: token hashes and CSRF tokens are never exported.
	{"sessions.json", false, `SELECT jsonb_build_object('id', s.id, 'created_at', s.created_at, 'last_seen_at', s.last_seen_at, 'expires_at', s.expires_at,
		'revoked_at', s.revoked_at, 'ip', s.ip, 'user_agent', s.user_agent, 'auth_method', s.auth_method, 'organization_id', s.org_id)
		FROM sessions s WHERE s.user_id::text = $1 ORDER BY s.created_at`},
	{"api_keys.json", false, `SELECT jsonb_build_object('id', k.id, 'organization_id', k.org_id, 'name', k.name, 'prefix', k.key_prefix,
		'created_at', k.created_at, 'last_used_at', k.last_used_at, 'expires_at', k.expires_at, 'revoked_at', k.revoked_at)
		FROM api_keys k WHERE k.created_by::text = $1 ORDER BY k.created_at`},
	{"invitations.json", false, `SELECT jsonb_build_object('organization_id', i.org_id, 'email', i.email, 'role', i.role, 'created_at', i.created_at,
		'expires_at', i.expires_at, 'accepted_at', i.accepted_at, 'revoked_at', i.revoked_at)
		FROM invitations i JOIN users u ON u.email = i.email WHERE u.id::text = $1 ORDER BY i.created_at`},
	{"sso_identities.json", true, `SELECT jsonb_build_object(
		'scim_users', (SELECT coalesce(jsonb_agg(jsonb_build_object('organization_id', s.org_id, 'user_name', s.user_name, 'external_id', s.external_id,
			'active', s.active, 'display_name', s.display_name, 'given_name', s.given_name, 'family_name', s.family_name,
			'created_at', s.created_at, 'updated_at', s.updated_at)), '[]'::jsonb) FROM scim_users s WHERE s.user_id::text = $1),
		'sso_sessions', (SELECT coalesce(jsonb_agg(jsonb_build_object('connection_id', x.connection_id, 'organization_id', x.org_id, 'subject', x.subject,
			'created_at', x.created_at)), '[]'::jsonb) FROM sso_sessions x WHERE x.user_id::text = $1))`},
	{"comments.json", false, `SELECT jsonb_build_object('organization_id', c.org_id, 'body', c.body, 'created_at', c.created_at)
		FROM apm_error_group_comments c WHERE c.author_user_id::text = $1 ORDER BY c.created_at`},
	{"audit_log.json", false, `SELECT jsonb_build_object('organization_id', a.org_id, 'actor_email', a.actor_email, 'action', a.action,
		'target_type', a.target_type, 'target_id', a.target_id, 'details', a.details, 'ip', a.ip, 'created_at', a.created_at)
		FROM audit_log a WHERE a.actor_user_id::text = $1 OR (a.target_type = 'user' AND a.target_id = $1) ORDER BY a.created_at, a.id`},
}

// writeDocuments writes docs into zw and returns the file names.
func writeDocuments(ctx context.Context, pool *pgxpool.Pool, zw *zip.Writer, prefix string, docs []document, arg string, now time.Time) ([]string, error) {
	var names []string
	for _, d := range docs {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: prefix + d.name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, err
		}
		if err := writeDocument(ctx, pool, w, d, arg); err != nil {
			return nil, fmt.Errorf("%s: %w", d.name, err)
		}
		names = append(names, prefix+d.name)
	}
	return names, nil
}

func writeDocument(ctx context.Context, pool *pgxpool.Pool, w io.Writer, d document, arg string) error {
	if d.single {
		var raw []byte
		err := pool.QueryRow(ctx, d.sql, arg).Scan(&raw)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if raw == nil {
			raw = []byte("null")
		}
		_, err = w.Write(append(raw, '\n'))
		return err
	}
	rows, err := pool.Query(ctx, d.sql, arg)
	if err != nil {
		return err
	}
	defer rows.Close()
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	first := true
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		sep := ",\n"
		if first {
			sep, first = "\n", false
		}
		if _, err := io.WriteString(w, sep); err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n]\n")
	return err
}
