package fleet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/intsettings"
)

// PGStore implements Store on PostgreSQL (migrations/postgres/0002_fleet.sql).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func validUUID(ids ...string) bool {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	return true
}

func nullUUID(id string) *string {
	if id == "" || !validUUID(id) {
		return nil
	}
	return &id
}

func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func mapPGErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

// ---- policy ----

const policyCols = `p.mode, p.channel, p.target, p.pinned_version, p.waves, p.wave_soak_minutes, p.halt_failure_rate,
	p.maintenance_windows::text, p.updated_at, coalesce(u.email, ''), p.php_agent::text, p.java_agent::text`

func scanPolicy(row pgx.Row) (StoredPolicy, error) {
	var (
		sp        StoredPolicy
		waves     []int32
		windows   string
		updated   time.Time
		php, java string
	)
	err := row.Scan(&sp.Mode, &sp.Channel, &sp.Target, &sp.PinnedVersion, &waves, &sp.WaveSoakMinutes, &sp.HaltFailureRate,
		&windows, &updated, &sp.UpdatedByEmail, &php, &java)
	if err != nil {
		return sp, err
	}
	sp.Waves = ints(waves)
	sp.MaintenanceWindows = []Window{}
	_ = json.Unmarshal([]byte(windows), &sp.MaintenanceWindows)
	sp.UpdatedAt = &updated
	sp.PHPAgent = DefaultPHPAgentPolicy()
	var stored PHPAgentPolicy
	if json.Unmarshal([]byte(php), &stored) == nil && stored.Mode != "" { // '{}' = never changed (0045)
		if n, err := stored.Normalize(); err == nil {
			n.ChangedAt = stored.ChangedAt
			sp.PHPAgent = n
		}
	}
	sp.JavaAgent = DefaultJavaAgentPolicy()
	var storedJava JavaAgentPolicy
	if json.Unmarshal([]byte(java), &storedJava) == nil && storedJava.Mode != "" { // '{}' = never changed (0086)
		if n, err := storedJava.Normalize(); err == nil {
			n.ChangedAt = storedJava.ChangedAt
			sp.JavaAgent = n
		}
	}
	return sp, nil
}

func ints(v []int32) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}

func int32s(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

func (s *PGStore) GetPolicy(ctx context.Context, orgID string) (StoredPolicy, error) {
	if !validUUID(orgID) {
		return StoredPolicy{}, ErrNotFound
	}
	sp, err := scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyCols+` FROM agent_update_policies p
		LEFT JOIN users u ON u.id = p.updated_by WHERE p.org_id = $1`, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPolicy{Policy: DefaultPolicy(), IsDefault: true}, nil
	}
	return sp, err
}

func (s *PGStore) PutPolicy(ctx context.Context, orgID string, p Policy, userID string, at time.Time) error {
	windows, err := json.Marshal(p.MaintenanceWindows)
	if err != nil {
		return err
	}
	php, err := json.Marshal(p.PHPAgent)
	if err != nil {
		return err
	}
	java, err := json.Marshal(p.JavaAgent)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO agent_update_policies
		(org_id, mode, channel, target, pinned_version, waves, wave_soak_minutes, halt_failure_rate, maintenance_windows, updated_at, updated_by, php_agent, java_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11::uuid, $12::jsonb, $13::jsonb)
		ON CONFLICT (org_id) DO UPDATE SET mode = EXCLUDED.mode, channel = EXCLUDED.channel, target = EXCLUDED.target,
			pinned_version = EXCLUDED.pinned_version, waves = EXCLUDED.waves, wave_soak_minutes = EXCLUDED.wave_soak_minutes,
			halt_failure_rate = EXCLUDED.halt_failure_rate, maintenance_windows = EXCLUDED.maintenance_windows,
			updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by, php_agent = EXCLUDED.php_agent,
			java_agent = EXCLUDED.java_agent`,
		orgID, p.Mode, p.Channel, p.Target, p.PinnedVersion, int32s(p.Waves), p.WaveSoakMinutes, p.HaltFailureRate,
		string(windows), at, nullUUID(userID), string(php), string(java))
	return mapPGErr(err)
}

// ---- Java agent overrides (0086) ----

func (s *PGStore) ListJavaOverrides(ctx context.Context, orgID string) (map[string]JavaOverride, error) {
	out := map[string]JavaOverride{}
	if !validUUID(orgID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT host_id, mode, updated_at FROM agent_java_host_overrides WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o JavaOverride
		if err := rows.Scan(&o.HostID, &o.Mode, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out[o.HostID] = o
	}
	return out, rows.Err()
}

func (s *PGStore) PutJavaOverride(ctx context.Context, orgID string, o JavaOverride, userID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_java_host_overrides (org_id, host_id, mode, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5::uuid)
		ON CONFLICT (org_id, host_id) DO UPDATE SET mode = EXCLUDED.mode, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		orgID, o.HostID, o.Mode, o.UpdatedAt, nullUUID(userID))
	return mapPGErr(err)
}

func (s *PGStore) DeleteJavaOverride(ctx context.Context, orgID, hostID string) (bool, error) {
	if !validUUID(orgID) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_java_host_overrides WHERE org_id = $1 AND host_id = $2`, orgID, hostID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- PHP agent overrides (0045) ----

func (s *PGStore) ListPHPOverrides(ctx context.Context, orgID string) (map[string]PHPOverride, error) {
	out := map[string]PHPOverride{}
	if !validUUID(orgID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT host_id, mode, updated_at FROM agent_php_host_overrides WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o PHPOverride
		if err := rows.Scan(&o.HostID, &o.Mode, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out[o.HostID] = o
	}
	return out, rows.Err()
}

func (s *PGStore) PutPHPOverride(ctx context.Context, orgID string, o PHPOverride, userID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_php_host_overrides (org_id, host_id, mode, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5::uuid)
		ON CONFLICT (org_id, host_id) DO UPDATE SET mode = EXCLUDED.mode, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		orgID, o.HostID, o.Mode, o.UpdatedAt, nullUUID(userID))
	return mapPGErr(err)
}

func (s *PGStore) DeletePHPOverride(ctx context.Context, orgID, hostID string) (bool, error) {
	if !validUUID(orgID) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_php_host_overrides WHERE org_id = $1 AND host_id = $2`, orgID, hostID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- overrides ----

func (s *PGStore) ListOverrides(ctx context.Context, orgID string) (map[string]Override, error) {
	out := map[string]Override{}
	if !validUUID(orgID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT host_id, action, coalesce(version, ''), updated_at
		FROM agent_update_host_overrides WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o Override
		if err := rows.Scan(&o.HostID, &o.Action, &o.Version, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out[o.HostID] = o
	}
	return out, rows.Err()
}

func (s *PGStore) PutOverride(ctx context.Context, orgID string, o Override, userID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_update_host_overrides (org_id, host_id, action, version, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6::uuid)
		ON CONFLICT (org_id, host_id) DO UPDATE SET action = EXCLUDED.action, version = EXCLUDED.version,
			updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		orgID, o.HostID, o.Action, nullText(o.Version), o.UpdatedAt, nullUUID(userID))
	return mapPGErr(err)
}

func (s *PGStore) DeleteOverride(ctx context.Context, orgID, hostID string) (bool, error) {
	if !validUUID(orgID) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_update_host_overrides WHERE org_id = $1 AND host_id = $2`, orgID, hostID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- hosts ----

// UpsertHosts writes one batch with a single statement. Reports of tenants without an
// organization are skipped by the join.
func (s *PGStore) UpsertHosts(ctx context.Context, recs []HostRecord) error {
	if len(recs) == 0 {
		return nil
	}
	n := len(recs)
	var (
		tenant, hostID, name, agentName, version, commit, goos, arch, method = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		state, from, to, errMsg, hash, intRev                                = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		capable                                                              = make([]bool, n)
		changed                                                              = make([]*time.Time, n)
		syncAt                                                               = make([]time.Time, n)
		rollout                                                              = make([]*string, n)
		php, phpAccess, java                                                 = make([]*string, n), make([]*string, n), make([]*string, n)
	)
	for i, r := range recs {
		h := r.Report
		if h.JavaAgent != nil {
			if b, err := json.Marshal(h.JavaAgent); err == nil {
				s := string(b)
				java[i] = &s
			}
		}
		if h.PHPAgent != nil {
			if b, err := json.Marshal(h.PHPAgent); err == nil {
				s := string(b)
				php[i] = &s
			}
		}
		if h.PHPAccess != nil {
			if b, err := json.Marshal(h.PHPAccess); err == nil {
				s := string(b)
				phpAccess[i] = &s
			}
		}
		tenant[i], hostID[i], name[i], agentName[i], version[i], commit[i] = r.TenantID, h.HostID, h.HostName, h.AgentName, h.Version, h.Commit
		goos[i], arch[i], method[i], capable[i] = h.OS, h.Arch, h.InstallMethod, h.UpdateCapable
		state[i], from[i], to[i], errMsg[i], hash[i], intRev[i] = h.UpdateState, h.UpdateFrom, h.UpdateTo, h.UpdateError, h.ConfigHash, h.IntegrationsConfigRevision
		if !h.UpdateChangedAt.IsZero() {
			t := h.UpdateChangedAt
			changed[i] = &t
		}
		syncAt[i] = r.SyncAt
		rollout[i] = nullUUID(r.RolloutID)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_hosts (org_id, host_id, host_name, agent_name, agent_version, agent_commit,
			agent_os, agent_arch, install_method, update_capable, update_state, update_from, update_to, update_error,
			update_changed_at, config_hash, first_seen_at, last_sync_at, rollout_id, integrations_config_revision, php_agent, php_access,
			java_agent)
		SELECT o.id, r.host_id, r.host_name, r.agent_name, r.version, r.commit, r.os, r.arch, r.method, r.capable, r.state,
			r.from_v, r.to_v, r.err, r.changed, r.hash, r.sync_at, r.sync_at, r.rollout::uuid, r.int_rev, r.php::jsonb, r.php_access::jsonb,
			r.java::jsonb
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[],
			$10::bool[], $11::text[], $12::text[], $13::text[], $14::text[], $15::timestamptz[], $16::text[], $17::timestamptz[], $18::text[],
			$19::text[], $20::text[], $21::text[], $22::text[])
			AS r(tenant_id, host_id, host_name, agent_name, version, commit, os, arch, method, capable, state, from_v, to_v, err,
			     changed, hash, sync_at, rollout, int_rev, php, php_access, java)
		JOIN organizations o ON o.tenant_id = r.tenant_id
		ON CONFLICT (org_id, host_id) DO UPDATE SET host_name = EXCLUDED.host_name, agent_name = EXCLUDED.agent_name,
			agent_version = EXCLUDED.agent_version, agent_commit = EXCLUDED.agent_commit, agent_os = EXCLUDED.agent_os,
			agent_arch = EXCLUDED.agent_arch, install_method = EXCLUDED.install_method, update_capable = EXCLUDED.update_capable,
			update_state = EXCLUDED.update_state, update_from = EXCLUDED.update_from, update_to = EXCLUDED.update_to,
			update_error = EXCLUDED.update_error, update_changed_at = EXCLUDED.update_changed_at, config_hash = EXCLUDED.config_hash,
			last_sync_at = GREATEST(agent_hosts.last_sync_at, EXCLUDED.last_sync_at),
			rollout_id = COALESCE(EXCLUDED.rollout_id, agent_hosts.rollout_id),
			integrations_config_revision = EXCLUDED.integrations_config_revision, php_agent = EXCLUDED.php_agent,
			php_access = EXCLUDED.php_access, java_agent = EXCLUDED.java_agent`,
		tenant, hostID, name, agentName, version, commit, goos, arch, method, capable, state, from, to, errMsg, changed, hash, syncAt, rollout, intRev, php,
		phpAccess, java)
	return err
}

const hostCols = `org_id::text, host_id, host_name, agent_name, agent_version, agent_commit, agent_os, agent_arch, install_method,
	update_capable, update_state, update_from, update_to, update_error, update_changed_at, config_hash, first_seen_at, last_sync_at,
	coalesce(rollout_id::text, ''), integrations_config_revision, coalesce(php_agent::text, ''), coalesce(php_access::text, ''),
	coalesce(java_agent::text, '')`

func scanHost(row pgx.Row) (Host, error) {
	var (
		h                    Host
		changed              *time.Time
		php, phpAccess, java string
	)
	err := row.Scan(&h.OrgID, &h.HostID, &h.HostName, &h.AgentName, &h.Version, &h.Commit, &h.OS, &h.Arch, &h.InstallMethod,
		&h.UpdateCapable, &h.UpdateState, &h.UpdateFrom, &h.UpdateTo, &h.UpdateError, &changed, &h.ConfigHash, &h.FirstSeenAt,
		&h.LastSyncAt, &h.RolloutID, &h.IntegrationsConfigRevision, &php, &phpAccess, &java)
	if changed != nil {
		h.UpdateChangedAt = *changed
	}
	h.PHPAgent = ParsePHPAgentReport([]byte(php))
	h.PHPAccess = ParsePHPAccessReport([]byte(phpAccess))
	h.JavaAgent = ParseJavaAgentReport([]byte(java))
	return h, err
}

func collectHosts(rows pgx.Rows) ([]Host, error) {
	defer rows.Close()
	out := []Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *PGStore) GetHost(ctx context.Context, orgID, hostID string) (Host, error) {
	if !validUUID(orgID) {
		return Host{}, ErrNotFound
	}
	h, err := scanHost(s.pool.QueryRow(ctx, `SELECT `+hostCols+` FROM agent_hosts WHERE org_id = $1 AND host_id = $2`, orgID, hostID))
	return h, mapPGErr(err)
}

type hostCursor struct {
	Name string `json:"n"`
	ID   string `json:"i"`
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ErrBadCursor is returned for an invalid pagination cursor.
var ErrBadCursor = &ValidationError{Msg: "invalid cursor"}

func (s *PGStore) ListHosts(ctx context.Context, orgID string, f HostFilter) ([]Host, string, error) {
	if !validUUID(orgID) {
		return []Host{}, "", nil
	}
	var cur hostCursor
	hasCursor := f.Cursor != ""
	if hasCursor {
		b, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil || json.Unmarshal(b, &cur) != nil {
			return nil, "", ErrBadCursor
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT `+hostCols+` FROM agent_hosts
		WHERE org_id = $1
		  AND ($2 = '' OR agent_version = $2)
		  AND ($3 = '' OR update_state = $3)
		  AND ($4 = '' OR host_name ILIKE '%' || $4 || '%' OR host_id ILIKE '%' || $4 || '%')
		  AND (NOT $5 OR (host_name, host_id) > ($6, $7))
		ORDER BY host_name, host_id LIMIT $8`,
		orgID, f.Version, f.State, escapeLike(f.Query), hasCursor, cur.Name, cur.ID, f.Limit+1)
	if err != nil {
		return nil, "", err
	}
	hosts, err := collectHosts(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(hosts) > f.Limit {
		hosts = hosts[:f.Limit]
		last := hosts[len(hosts)-1]
		b, _ := json.Marshal(hostCursor{Name: last.HostName, ID: last.HostID})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return hosts, next, nil
}

func (s *PGStore) AllHosts(ctx context.Context, orgID string, since time.Time) ([]Host, error) {
	if !validUUID(orgID) {
		return []Host{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+hostCols+` FROM agent_hosts WHERE org_id = $1 AND last_sync_at >= $2`, orgID, since)
	if err != nil {
		return nil, err
	}
	return collectHosts(rows)
}

func (s *PGStore) HostGroups(ctx context.Context, orgID string, since time.Time) ([]HostGroup, error) {
	if !validUUID(orgID) {
		return []HostGroup{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT agent_version, update_capable, install_method, update_state, count(*),
			count(*) FILTER (WHERE last_sync_at >= $2)
		FROM agent_hosts WHERE org_id = $1 GROUP BY 1, 2, 3, 4`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostGroup{}
	for rows.Next() {
		var g HostGroup
		if err := rows.Scan(&g.Version, &g.UpdateCapable, &g.InstallMethod, &g.UpdateState, &g.Hosts, &g.RecentlySeen); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *PGStore) FleetOrgs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.id::text FROM organizations o
		WHERE EXISTS (SELECT 1 FROM agent_hosts h WHERE h.org_id = o.id)
		   OR EXISTS (SELECT 1 FROM agent_rollouts r WHERE r.org_id = o.id AND r.state IN ('active', 'paused', 'halted'))`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ---- rollouts ----

const rolloutCols = `r.id::text, r.org_id::text, r.action, coalesce(r.from_version, ''), coalesce(r.to_version, ''), r.targets::text,
	r.waves, r.current_wave, r.wave_started_at, r.wave_soak_minutes, r.halt_failure_rate, r.state, r.state_reason,
	r.hosts_pending, r.hosts_attempted, r.hosts_succeeded, r.hosts_failed, r.hosts_rolled_back, r.ack_attempted, r.ack_failed,
	coalesce(r.created_by::text, ''), coalesce(u.email, ''), r.created_at, r.updated_at, r.ended_at`

const rolloutFrom = ` FROM agent_rollouts r LEFT JOIN users u ON u.id = r.created_by `

func scanRollout(row pgx.Row) (Rollout, error) {
	var (
		r       Rollout
		targets string
		waves   []int32
	)
	err := row.Scan(&r.ID, &r.OrgID, &r.Action, &r.FromVersion, &r.ToVersion, &targets, &waves, &r.CurrentWave, &r.WaveStartedAt,
		&r.WaveSoakMinutes, &r.HaltFailureRate, &r.State, &r.StateReason, &r.Counters.Pending, &r.Counters.Attempted,
		&r.Counters.Succeeded, &r.Counters.Failed, &r.Counters.RolledBack, &r.AckAttempted, &r.AckFailed, &r.CreatedBy,
		&r.CreatedByEmail, &r.CreatedAt, &r.UpdatedAt, &r.EndedAt)
	if err != nil {
		return r, err
	}
	r.Waves = ints(waves)
	r.Targets = map[string]string{}
	_ = json.Unmarshal([]byte(targets), &r.Targets)
	return r, nil
}

func (s *PGStore) ListRollouts(ctx context.Context, orgID string, limit int) ([]Rollout, error) {
	if !validUUID(orgID) {
		return []Rollout{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+rolloutCols+rolloutFrom+`WHERE r.org_id = $1 ORDER BY r.created_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rollout{}
	for rows.Next() {
		r, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PGStore) GetRollout(ctx context.Context, orgID, id string) (Rollout, error) {
	if !validUUID(orgID, id) {
		return Rollout{}, ErrNotFound
	}
	r, err := scanRollout(s.pool.QueryRow(ctx, `SELECT `+rolloutCols+rolloutFrom+`WHERE r.org_id = $1 AND r.id = $2`, orgID, id))
	return r, mapPGErr(err)
}

func (s *PGStore) CurrentRollout(ctx context.Context, orgID string) (*Rollout, error) {
	if !validUUID(orgID) {
		return nil, nil
	}
	r, err := scanRollout(s.pool.QueryRow(ctx, `SELECT `+rolloutCols+rolloutFrom+`WHERE r.org_id = $1 AND r.state <> 'superseded'
		ORDER BY r.created_at DESC LIMIT 1`, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PGStore) CreateRollout(ctx context.Context, r *Rollout, supersedeReason string) error {
	targets, err := json.Marshal(nonNilTargets(r.Targets))
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := r.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_rollouts SET state = 'superseded', state_reason = $2, ended_at = $3, updated_at = $3
		WHERE org_id = $1 AND state IN ('active', 'paused', 'halted')`, r.OrgID, supersedeReason, now); err != nil {
		return mapPGErr(err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO agent_rollouts (org_id, action, from_version, to_version, targets, waves, current_wave,
			wave_started_at, wave_soak_minutes, halt_failure_rate, state, state_reason, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $13::uuid, $14, $14) RETURNING id::text`,
		r.OrgID, r.Action, nullText(r.FromVersion), nullText(r.ToVersion), string(targets), int32s(r.Waves), r.CurrentWave,
		r.WaveStartedAt, r.WaveSoakMinutes, r.HaltFailureRate, r.State, r.StateReason, nullUUID(r.CreatedBy), now).Scan(&r.ID)
	if err != nil {
		return mapPGErr(err)
	}
	r.CreatedAt, r.UpdatedAt = now, now
	return mapPGErr(tx.Commit(ctx))
}

func nonNilTargets(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *PGStore) UpdateRollout(ctx context.Context, r *Rollout, expectState string) error {
	if !validUUID(r.OrgID, r.ID) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_rollouts SET current_wave = $3, wave_started_at = $4, state = $5, state_reason = $6,
			hosts_pending = $7, hosts_attempted = $8, hosts_succeeded = $9, hosts_failed = $10, hosts_rolled_back = $11,
			ack_attempted = $12, ack_failed = $13, ended_at = $14, updated_at = $15
		WHERE org_id = $1 AND id = $2 AND state = $16`,
		r.OrgID, r.ID, r.CurrentWave, r.WaveStartedAt, r.State, r.StateReason, r.Counters.Pending, r.Counters.Attempted,
		r.Counters.Succeeded, r.Counters.Failed, r.Counters.RolledBack, r.AckAttempted, r.AckFailed, r.EndedAt, r.UpdatedAt, expectState)
	if err != nil {
		return mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// ---- org state (ingest) ----

func (s *PGStore) LoadOrgState(ctx context.Context, tenantID string) (OrgState, error) {
	var st OrgState
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM organizations WHERE tenant_id = $1`, tenantID).Scan(&st.OrgID); err != nil {
		return st, mapPGErr(err)
	}
	sp, err := s.GetPolicy(ctx, st.OrgID)
	if err != nil {
		return st, fmt.Errorf("policy: %w", err)
	}
	st.Policy = sp.Policy
	if st.Overrides, err = s.ListOverrides(ctx, st.OrgID); err != nil {
		return st, fmt.Errorf("overrides: %w", err)
	}
	if st.Rollout, err = s.CurrentRollout(ctx, st.OrgID); err != nil {
		return st, fmt.Errorf("rollout: %w", err)
	}
	if st.PHPOverrides, err = s.ListPHPOverrides(ctx, st.OrgID); err != nil {
		return st, fmt.Errorf("php agent overrides: %w", err)
	}
	if st.JavaOverrides, err = s.ListJavaOverrides(ctx, st.OrgID); err != nil {
		return st, fmt.Errorf("java agent overrides: %w", err)
	}
	st.Integrations, err = intsettings.NewPGStore(s.pool).ListSettings(ctx, st.OrgID)
	switch {
	case err == nil:
		st.IntegrationsLoaded = true
	case intsettings.IsUndefinedTable(err):
		st.Integrations = nil // 0008_integration_settings not applied yet: updates keep working
	default:
		return st, fmt.Errorf("integration settings: %w", err)
	}
	return st, nil
}

// ---- audit ----

func (s *PGStore) AddAudit(ctx context.Context, e AuditEntry) error {
	details := []byte("{}")
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		details = b
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8, $9)`,
		nullUUID(e.OrgID), nullUUID(e.ActorUserID), e.ActorEmail, e.Action, e.TargetType, e.TargetID, string(details), e.IP, at)
	return err
}
