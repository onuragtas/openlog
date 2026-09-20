package synthetics

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the PostgreSQL Store and ScheduleStore (migrations/postgres/0090_synthetics.sql). Every write
// and its audit event are written in one transaction, like the SLO store.
type PGStore struct{ pool *pgxpool.Pool }

var (
	_ Store         = (*PGStore)(nil)
	_ ScheduleStore = (*PGStore)(nil)
)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const checkColumns = `c.id::text, c.org_id::text, c.name, c.type, c.enabled, c.url, c.method, c.headers,
	c.body, c.expected_status, c.assertion_type, c.assertion_path, c.assertion_value, c.timeout_ms,
	c.interval_seconds, c.locations, c.target, c.dns_record_type, c.dns_expected, c.tls_warning_days,
	coalesce(cu.email, ''), coalesce(uu.email, ''), c.created_at, c.updated_at`

const checkFrom = ` FROM synthetic_checks c
	LEFT JOIN users cu ON cu.id = c.created_by
	LEFT JOIN users uu ON uu.id = c.updated_by`

// scanCheck scans the checkColumns list plus any extra destinations appended to the select list.
func scanCheck(row pgx.Row, extra ...any) (*Check, error) {
	var (
		c        Check
		headers  []byte
		expected []int32
	)
	dest := []any{&c.ID, &c.OrgID, &c.Name, &c.Type, &c.Enabled, &c.URL, &c.Method, &headers, &c.Body,
		&expected, &c.AssertionType, &c.AssertionPath, &c.AssertionValue, &c.TimeoutMs, &c.IntervalSeconds,
		&c.Locations, &c.Target, &c.DNSRecordType, &c.DNSExpected, &c.TLSWarningDays,
		&c.CreatedByEmail, &c.UpdatedByEmail, &c.CreatedAt, &c.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	c.Headers = map[string]string{}
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &c.Headers); err != nil {
			return nil, err
		}
	}
	c.ExpectedStatus = make([]int, len(expected))
	for i, v := range expected {
		c.ExpectedStatus[i] = int(v)
	}
	if c.Locations == nil {
		c.Locations = []string{}
	}
	c.Status = []LocationStatus{}
	return &c, nil
}

func headersParam(in Input) ([]byte, error) {
	if in.Headers == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(in.Headers)
}

func expectedParam(in Input) []int32 {
	out := make([]int32, len(in.ExpectedStatus))
	for i, c := range in.ExpectedStatus {
		out[i] = int32(c)
	}
	return out
}

// expectedRecords is the dns_expected array parameter; a nil slice would be written as NULL, and the column
// is NOT NULL with an empty-array default.
func expectedRecords(in Input) []string {
	if in.DNSExpected == nil {
		return []string{}
	}
	return in.DNSExpected
}

func actorID(a Actor) any {
	if a.UserID == "" {
		return nil
	}
	return a.UserID
}

func auditDetails(in Input) []byte {
	b, _ := json.Marshal(map[string]any{"name": in.Name, "type": in.Type, "url": in.URL, "method": in.Method, "target": in.Target,
		"enabled": in.Enabled, "interval_seconds": in.IntervalSeconds, "timeout_ms": in.TimeoutMs,
		"locations": in.Locations, "assertion_type": in.AssertionType})
	return b
}

// keyID is the API key that made the change, or NULL for a signed-in user.
func keyID(a Actor) any {
	if a.APIKeyID == "" {
		return nil
	}
	return a.APIKeyID
}

func audit(ctx context.Context, tx pgx.Tx, orgID, action, id string, details []byte, actor Actor) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
		action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6, 'synthetic_check', $7, $8, $9)`, orgID, actorID(actor), actor.Email,
		keyID(actor), actor.APIKeyName, action, id, details, actor.IP)
	return err
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID string) ([]Check, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+checkColumns+checkFrom+`
		WHERE c.org_id = $1 ORDER BY lower(c.name), c.id LIMIT $2`, orgID, MaxPerOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		c, err := scanCheck(rows)
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

// withStatus fills the schedule rows of the listed checks.
func (s *PGStore) withStatus(ctx context.Context, list []Check) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	idx := map[string]int{}
	for i, c := range list {
		ids[i] = c.ID
		idx[c.ID] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT check_id::text, location, next_run_at, last_run_at, last_success,
		last_status_code, last_duration_ms, last_error_kind, last_error
		FROM synthetic_check_schedule WHERE check_id = ANY($1::uuid[]) ORDER BY location`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			st LocationStatus
		)
		if err := rows.Scan(&id, &st.Location, &st.NextRunAt, &st.LastRunAt, &st.LastSuccess,
			&st.LastStatusCode, &st.LastDurationMs, &st.LastErrorKind, &st.LastError); err != nil {
			return err
		}
		if i, ok := idx[id]; ok {
			list[i].Status = append(list[i].Status, st)
		}
	}
	return rows.Err()
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Check, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	c, err := scanCheck(s.pool.QueryRow(ctx, `SELECT `+checkColumns+checkFrom+` WHERE c.org_id = $1 AND c.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	list := []Check{*c}
	if err := s.withStatus(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// syncSchedule adds a schedule row for every location of the check and removes the rows of locations it no
// longer uses. A new row is due immediately, so a saved check reports its first result within one tick.
func syncSchedule(ctx context.Context, tx pgx.Tx, id string, locations []string, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO synthetic_check_schedule (check_id, location, next_run_at)
		SELECT $1::uuid, unnest($2::text[]), $3 ON CONFLICT (check_id, location) DO NOTHING`, id, locations, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM synthetic_check_schedule WHERE check_id = $1::uuid AND location <> ALL($2::text[])`, id, locations)
	return err
}

// Create implements Store. The count check and the insert are one statement; concurrent creates may exceed
// the limit by a few rows (like SLOs and saved views).
func (s *PGStore) Create(ctx context.Context, orgID string, in Input, actor Actor) (*Check, error) {
	headers, err := headersParam(in)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `INSERT INTO synthetic_checks (id, org_id, name, type, enabled, url, method, headers,
			body, expected_status, assertion_type, assertion_path, assertion_value, timeout_ms, interval_seconds,
			locations, target, dns_record_type, dns_expected, tls_warning_days, created_by, updated_by)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $21
			WHERE (SELECT count(*) FROM synthetic_checks WHERE org_id = $2) < $22
			RETURNING id::text`,
			id, orgID, in.Name, in.Type, in.Enabled, in.URL, in.Method, headers, in.Body, expectedParam(in),
			in.AssertionType, in.AssertionPath, in.AssertionValue, in.TimeoutMs, in.IntervalSeconds,
			in.Locations, in.Target, in.DNSRecordType, expectedRecords(in), in.TLSWarningDays,
			actorID(actor), MaxPerOrg).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLimit
		}
		if err != nil {
			return err
		}
		if err := syncSchedule(ctx, tx, id, in.Locations, now); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "synthetic_check.create", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Update implements Store.
func (s *PGStore) Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*Check, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	headers, err := headersParam(in)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE synthetic_checks SET name = $3, type = $4, enabled = $5, url = $6, method = $7,
			headers = $8, body = $9, expected_status = $10, assertion_type = $11, assertion_path = $12,
			assertion_value = $13, timeout_ms = $14, interval_seconds = $15, locations = $16, target = $17,
			dns_record_type = $18, dns_expected = $19, tls_warning_days = $20, updated_by = $21,
			updated_at = now() WHERE org_id = $1 AND id = $2`,
			orgID, id, in.Name, in.Type, in.Enabled, in.URL, in.Method, headers, in.Body, expectedParam(in),
			in.AssertionType, in.AssertionPath, in.AssertionValue, in.TimeoutMs, in.IntervalSeconds,
			in.Locations, in.Target, in.DNSRecordType, expectedRecords(in), in.TLSWarningDays, actorID(actor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := syncSchedule(ctx, tx, id, in.Locations, now); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "synthetic_check.update", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Delete implements Store. The schedule rows go with the check (ON DELETE CASCADE); the stored runs stay in
// ClickHouse until their TTL expires.
func (s *PGStore) Delete(ctx context.Context, orgID, id string, actor Actor) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM synthetic_checks WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"name": name})
		return audit(ctx, tx, orgID, "synthetic_check.delete", id, details, actor)
	})
}

// Claim implements ScheduleStore. Selecting the due rows, advancing their next_run_at and reading the
// definitions happen in one statement: FOR UPDATE ... SKIP LOCKED makes a second scheduler (a pod that still
// believes it is the leader) skip the rows this one took, so a check never runs twice per interval.
func (s *PGStore) Claim(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT s.check_id, s.location
			FROM synthetic_check_schedule s
			JOIN synthetic_checks c ON c.id = s.check_id
			WHERE c.enabled AND s.next_run_at <= $1
			ORDER BY s.next_run_at
			LIMIT $2
			FOR UPDATE OF s SKIP LOCKED
		), claimed AS (
			UPDATE synthetic_check_schedule s
			SET next_run_at = $1 + make_interval(secs => c.interval_seconds)
			FROM due d
			JOIN synthetic_checks c ON c.id = d.check_id
			WHERE s.check_id = d.check_id AND s.location = d.location
			RETURNING s.check_id, s.location
		)
		SELECT `+checkColumns+`, o.tenant_id, cl.location
		FROM claimed cl
		JOIN synthetic_checks c ON c.id = cl.check_id
		JOIN organizations o ON o.id = c.org_id
		LEFT JOIN users cu ON cu.id = c.created_by
		LEFT JOIN users uu ON uu.id = c.updated_by`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Due{}
	for rows.Next() {
		var tenant, location string
		c, err := scanCheck(rows, &tenant, &location)
		if err != nil {
			return nil, err
		}
		out = append(out, Due{Check: *c, TenantID: tenant, Location: location})
	}
	return out, rows.Err()
}

// Record implements ScheduleStore.
func (s *PGStore) Record(ctx context.Context, r Result) error {
	_, err := s.pool.Exec(ctx, `UPDATE synthetic_check_schedule SET last_run_at = $3, last_success = $4,
		last_status_code = $5, last_duration_ms = $6, last_error_kind = $7, last_error = $8
		WHERE check_id = $1::uuid AND location = $2`,
		r.CheckID, r.Location, r.At, r.Success, r.StatusCode, r.DurationMs, r.ErrorKind, r.Error)
	return err
}
