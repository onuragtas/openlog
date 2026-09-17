package cloudconnect

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the PostgreSQL Store and ScheduleStore (migrations/postgres/0092_cloud_connections.sql). Every
// write and its audit event are written in one transaction, like the synthetics and SLO stores.
type PGStore struct{ pool *pgxpool.Pool }

var (
	_ Store         = (*PGStore)(nil)
	_ ScheduleStore = (*PGStore)(nil)
)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// connectionColumns never selects credentials_enc: only CredentialsEnc reads it, so a stray join cannot carry
// a ciphertext into a response.
const connectionColumns = `c.id::text, c.org_id::text, c.name, c.provider, c.ingest_mode, c.enabled,
	c.scopes, c.services, c.poll_interval_seconds, c.max_metrics_per_poll, c.max_api_calls_per_poll,
	(c.credentials_enc <> '') AS credentials_set, c.credentials_key_id,
	coalesce(cu.email, ''), coalesce(uu.email, ''), c.created_at, c.updated_at`

const connectionFrom = ` FROM cloud_connections c
	LEFT JOIN users cu ON cu.id = c.created_by
	LEFT JOIN users uu ON uu.id = c.updated_by`

// scanConnection scans the connectionColumns list plus any extra destinations appended to the select list.
func scanConnection(row pgx.Row, extra ...any) (*Connection, error) {
	var c Connection
	dest := []any{&c.ID, &c.OrgID, &c.Name, &c.Provider, &c.IngestMode, &c.Enabled, &c.Scopes, &c.Services,
		&c.PollIntervalSeconds, &c.MaxMetricsPerPoll, &c.MaxAPICallsPerPoll, &c.CredentialsSet,
		&c.CredentialsKeyID, &c.CreatedByEmail, &c.UpdatedByEmail, &c.CreatedAt, &c.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	if c.Services == nil {
		c.Services = []string{}
	}
	c.Status = []ScopeStatus{}
	return &c, nil
}

func actorID(a Actor) any {
	if a.UserID == "" {
		return nil
	}
	return a.UserID
}

// keyID is the API key that made the change, or NULL for a signed-in user.
func keyID(a Actor) any {
	if a.APIKeyID == "" {
		return nil
	}
	return a.APIKeyID
}

// auditDetails describes a change without any credential field: what was configured, never what was given.
func auditDetails(in Input) []byte {
	b, _ := json.Marshal(map[string]any{"name": in.Name, "provider": in.Provider, "ingest_mode": in.IngestMode,
		"enabled": in.Enabled, "scopes": in.Scopes, "services": in.Services,
		"poll_interval_seconds": in.PollIntervalSeconds, "max_metrics_per_poll": in.MaxMetricsPerPoll,
		"max_api_calls_per_poll": in.MaxAPICallsPerPoll, "credentials_changed": in.Credentials != nil})
	return b
}

func audit(ctx context.Context, tx pgx.Tx, orgID, action, id string, details []byte, actor Actor) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
		action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6, 'cloud_connection', $7, $8, $9)`, orgID, actorID(actor), actor.Email,
		keyID(actor), actor.APIKeyName, action, id, details, actor.IP)
	return err
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID string) ([]Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+connectionColumns+connectionFrom+`
		WHERE c.org_id = $1 ORDER BY lower(c.name), c.id LIMIT $2`, orgID, MaxPerOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.withStatus(ctx, out)
}

// withStatus fills the schedule rows of the listed connections.
func (s *PGStore) withStatus(ctx context.Context, list []Connection) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	idx := map[string]int{}
	for i, c := range list {
		ids[i] = c.ID
		idx[c.ID] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT connection_id::text, scope, next_run_at, last_run_at,
		coalesce(last_status, ''), last_error, last_metrics, last_api_calls, last_duration_ms, consecutive_errors
		FROM cloud_connection_schedule WHERE connection_id = ANY($1::uuid[]) ORDER BY scope`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			st ScopeStatus
		)
		if err := rows.Scan(&id, &st.Scope, &st.NextRunAt, &st.LastRunAt, &st.LastStatus, &st.LastError,
			&st.LastMetrics, &st.LastAPICalls, &st.LastDurationMs, &st.ConsecutiveErrors); err != nil {
			return err
		}
		if i, ok := idx[id]; ok {
			list[i].Status = append(list[i].Status, st)
		}
	}
	return rows.Err()
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Connection, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	c, err := scanConnection(s.pool.QueryRow(ctx, `SELECT `+connectionColumns+connectionFrom+
		` WHERE c.org_id = $1 AND c.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	list := []Connection{*c}
	if err := s.withStatus(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// CredentialsEnc implements Store.
func (s *PGStore) CredentialsEnc(ctx context.Context, orgID, id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", ErrNotFound
	}
	var enc string
	err := s.pool.QueryRow(ctx, `SELECT credentials_enc FROM cloud_connections WHERE org_id = $1 AND id = $2`,
		orgID, id).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if enc == "" {
		return "", ErrNoCredentials
	}
	return enc, nil
}

// syncSchedule adds a schedule row for every scope of the connection and removes the rows of scopes it no
// longer covers. A new row is due immediately, so a saved connection reports its first result within one tick.
// A push connection gets no rows at all: nothing is due for it.
func syncSchedule(ctx context.Context, tx pgx.Tx, id string, in Input, now time.Time) error {
	scopes := in.Scopes
	if !in.Polls() {
		scopes = nil
	}
	if len(scopes) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO cloud_connection_schedule (connection_id, scope, next_run_at)
			SELECT $1::uuid, unnest($2::text[]), $3 ON CONFLICT (connection_id, scope) DO NOTHING`, id, scopes, now); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM cloud_connection_schedule WHERE connection_id = $1::uuid AND scope <> ALL($2::text[])`,
		id, scopes)
	return err
}

// Create implements Store. The count check and the insert are one statement; concurrent creates may exceed
// the limit by a few rows (like synthetics and SLOs).
func (s *PGStore) Create(ctx context.Context, orgID, id string, in Input, credentialsEnc, keyIDValue string, actor Actor) (*Connection, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `INSERT INTO cloud_connections (id, org_id, name, provider, ingest_mode, enabled,
			credentials_enc, credentials_key_id, scopes, services, poll_interval_seconds, max_metrics_per_poll,
			max_api_calls_per_poll, created_by, updated_by)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14
			WHERE (SELECT count(*) FROM cloud_connections WHERE org_id = $2) < $15
			RETURNING id::text`,
			id, orgID, in.Name, in.Provider, in.IngestMode, in.Enabled, credentialsEnc, keyIDValue, in.Scopes,
			in.Services, in.PollIntervalSeconds, in.MaxMetricsPerPoll, in.MaxAPICallsPerPoll, actorID(actor), MaxPerOrg).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLimit
		}
		if err != nil {
			return err
		}
		if err := syncSchedule(ctx, tx, id, in, now); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "cloud_connection.create", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Update implements Store. A nil credentialsEnc keeps the stored credentials.
func (s *PGStore) Update(ctx context.Context, orgID, id string, in Input, credentialsEnc *string, keyIDValue string, actor Actor) (*Connection, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE cloud_connections SET name = $3, provider = $4, ingest_mode = $5,
			enabled = $6, scopes = $7, services = $8, poll_interval_seconds = $9, max_metrics_per_poll = $10,
			max_api_calls_per_poll = $11,
			credentials_enc = coalesce($12, credentials_enc),
			credentials_key_id = CASE WHEN $12 IS NULL THEN credentials_key_id ELSE $13 END,
			updated_by = $14, updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, id, in.Name, in.Provider, in.IngestMode, in.Enabled, in.Scopes, in.Services,
			in.PollIntervalSeconds, in.MaxMetricsPerPoll, in.MaxAPICallsPerPoll, credentialsEnc, keyIDValue, actorID(actor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := syncSchedule(ctx, tx, id, in, now); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "cloud_connection.update", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Delete implements Store. The schedule rows and the run history go with the connection (ON DELETE CASCADE);
// the metrics already written stay in ClickHouse until their TTL expires.
func (s *PGStore) Delete(ctx context.Context, orgID, id string, actor Actor) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name, provider string
		err := tx.QueryRow(ctx, `DELETE FROM cloud_connections WHERE org_id = $1 AND id = $2 RETURNING name, provider`,
			orgID, id).Scan(&name, &provider)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"name": name, "provider": provider})
		return audit(ctx, tx, orgID, "cloud_connection.delete", id, details, actor)
	})
}

// Runs implements Store.
func (s *PGStore) Runs(ctx context.Context, orgID, id, scope string, limit int) ([]Run, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > MaxRunsPerResponse {
		limit = MaxRunsPerResponse
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id, r.scope, r.started_at, r.duration_ms, r.status, r.metrics,
		r.api_calls, r.throttled, r.error, r.services
		FROM cloud_collection_runs r
		JOIN cloud_connections c ON c.id = r.connection_id
		WHERE c.org_id = $1 AND r.connection_id = $2 AND ($3 = '' OR r.scope = $3)
		ORDER BY r.started_at DESC, r.id DESC LIMIT $4`, orgID, id, scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var (
			r        Run
			services []byte
		)
		if err := rows.Scan(&r.ID, &r.Scope, &r.StartedAt, &r.DurationMs, &r.Status, &r.Metrics,
			&r.APICalls, &r.Throttled, &r.Error, &services); err != nil {
			return nil, err
		}
		r.Services = []ServiceRun{}
		if len(services) > 0 {
			if err := json.Unmarshal(services, &r.Services); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Claim implements ScheduleStore. Selecting the due rows, advancing their next_run_at and reading the
// definitions happen in one statement: FOR UPDATE ... SKIP LOCKED makes a second scheduler (a pod that still
// believes it is the leader) skip the rows this one took, so a scope is never polled twice per interval —
// which here also means the provider is never billed twice for the same poll.
func (s *PGStore) Claim(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT s.connection_id, s.scope
			FROM cloud_connection_schedule s
			JOIN cloud_connections c ON c.id = s.connection_id
			WHERE c.enabled AND c.ingest_mode = 'poll' AND c.credentials_enc <> '' AND s.next_run_at <= $1
			ORDER BY s.next_run_at
			LIMIT $2
			FOR UPDATE OF s SKIP LOCKED
		), claimed AS (
			UPDATE cloud_connection_schedule s
			SET next_run_at = $1 + make_interval(secs => c.poll_interval_seconds)
			FROM due d
			JOIN cloud_connections c ON c.id = d.connection_id
			WHERE s.connection_id = d.connection_id AND s.scope = d.scope
			RETURNING s.connection_id, s.scope, s.consecutive_errors
		)
		SELECT `+connectionColumns+`, o.tenant_id, cl.scope, cl.consecutive_errors, c.credentials_enc
		FROM claimed cl
		JOIN cloud_connections c ON c.id = cl.connection_id
		JOIN organizations o ON o.id = c.org_id
		LEFT JOIN users cu ON cu.id = c.created_by
		LEFT JOIN users uu ON uu.id = c.updated_by`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Due{}
	for rows.Next() {
		var (
			tenant, scope, enc string
			consecutive        int
		)
		c, err := scanConnection(rows, &tenant, &scope, &consecutive, &enc)
		if err != nil {
			return nil, err
		}
		// The scheduler backs a failing scope off from this count, so it travels with the claim.
		c.Status = []ScopeStatus{{Scope: scope, ConsecutiveErrors: consecutive}}
		out = append(out, Due{Connection: *c, TenantID: tenant, Scope: scope, CredentialsEnc: enc})
	}
	return out, rows.Err()
}

// Record implements ScheduleStore: the outcome on the schedule row (what the list shows) plus one history
// row, with the history pruned to MaxRunsPerConnection in the same transaction.
func (s *PGStore) Record(ctx context.Context, connectionID string, r Run, backoff time.Duration) error {
	services, err := json.Marshal(r.Services)
	if err != nil {
		return err
	}
	backoffSecs := int(backoff / time.Second)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE cloud_connection_schedule SET last_run_at = $3, last_status = $4,
			last_error = $5, last_metrics = $6, last_api_calls = $7, last_duration_ms = $8,
			consecutive_errors = CASE WHEN $4 = 'error' THEN consecutive_errors + 1 ELSE 0 END,
			next_run_at = CASE WHEN $9 > 0 THEN greatest(next_run_at, $3 + make_interval(secs => $9)) ELSE next_run_at END
			WHERE connection_id = $1::uuid AND scope = $2`,
			connectionID, r.Scope, r.StartedAt, r.Status, r.Error, r.Metrics, r.APICalls, r.DurationMs, backoffSecs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cloud_collection_runs (connection_id, scope, started_at, duration_ms,
			status, metrics, api_calls, throttled, error, services)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
			connectionID, r.Scope, r.StartedAt, r.DurationMs, r.Status, r.Metrics, r.APICalls, r.Throttled,
			r.Error, string(services)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM cloud_collection_runs
			WHERE connection_id = $1::uuid AND id NOT IN (
				SELECT id FROM cloud_collection_runs WHERE connection_id = $1::uuid
				ORDER BY started_at DESC, id DESC LIMIT $2)`, connectionID, MaxRunsPerConnection)
		return err
	})
}
