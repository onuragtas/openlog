package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the PostgreSQL Store, PingStore and SweepStore (migrations/postgres/0096_job_monitors.sql).
// Every write and its audit event are written in one transaction, like the synthetics store.
type PGStore struct{ pool *pgxpool.Pool }

var (
	_ Store      = (*PGStore)(nil)
	_ PingStore  = (*PGStore)(nil)
	_ SweepStore = (*PGStore)(nil)
)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const monitorColumns = `m.id::text, m.org_id::text, m.name, m.description, m.kind, m.cron, m.time_zone,
	m.interval_seconds, m.grace_seconds, m.enabled, m.tags, m.ping_token,
	coalesce(cu.email, ''), coalesce(uu.email, ''), m.created_at, m.updated_at,
	s.status, s.last_ping_at, s.last_started_at, s.last_finished_at, s.last_duration_ms, s.last_exit_code,
	s.last_message, s.expected_at, s.consecutive_failures`

const monitorFrom = ` FROM job_monitors m
	JOIN job_monitor_state s ON s.monitor_id = m.id
	LEFT JOIN users cu ON cu.id = m.created_by
	LEFT JOIN users uu ON uu.id = m.updated_by`

func scanMonitor(row pgx.Row, extra ...any) (*Monitor, error) {
	var m Monitor
	dest := []any{&m.ID, &m.OrgID, &m.Name, &m.Description, &m.Kind, &m.Cron, &m.TimeZone,
		&m.IntervalSeconds, &m.GraceSeconds, &m.Enabled, &m.Tags, &m.Token,
		&m.CreatedByEmail, &m.UpdatedByEmail, &m.CreatedAt, &m.UpdatedAt,
		&m.State.Status, &m.State.LastPingAt, &m.State.LastStartedAt, &m.State.LastFinishedAt,
		&m.State.LastDurationMs, &m.State.LastExitCode, &m.State.LastMessage, &m.State.ExpectedAt,
		&m.State.ConsecutiveFailures}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return &m, nil
}

func actorID(a Actor) any {
	if a.UserID == "" {
		return nil
	}
	return a.UserID
}

func keyID(a Actor) any {
	if a.APIKeyID == "" {
		return nil
	}
	return a.APIKeyID
}

func auditDetails(in Input) []byte {
	b, _ := json.Marshal(map[string]any{"name": in.Name, "kind": in.Kind, "cron": in.Cron,
		"time_zone": in.TimeZone, "interval_seconds": in.IntervalSeconds, "grace_seconds": in.GraceSeconds,
		"enabled": in.Enabled, "tags": in.Tags})
	return b
}

func audit(ctx context.Context, tx pgx.Tx, orgID, action, id string, details []byte, actor Actor) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
		action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6, 'job_monitor', $7, $8, $9)`, orgID, actorID(actor), actor.Email,
		keyID(actor), actor.APIKeyName, action, id, details, actor.IP)
	return err
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID string) ([]Monitor, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+monitorColumns+monitorFrom+`
		WHERE m.org_id = $1 ORDER BY lower(m.name), m.id LIMIT $2`, orgID, MaxPerOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Monitor{}
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Monitor, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	m, err := scanMonitor(s.pool.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+`
		WHERE m.org_id = $1 AND m.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// Create implements Store.
func (s *PGStore) Create(ctx context.Context, orgID string, in Input, actor Actor) (*Monitor, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	// A monitor that has never reported is due at its first scheduled time, so a job that is registered and
	// then never runs is noticed instead of waiting forever for a first ping to anchor the schedule.
	expected, err := in.NextExpected(now)
	if err != nil {
		return nil, invalid("cron", err.Error())
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `INSERT INTO job_monitors (id, org_id, name, description, kind, cron, time_zone,
			interval_seconds, grace_seconds, enabled, tags, ping_token, created_by, updated_by)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13
			WHERE (SELECT count(*) FROM job_monitors WHERE org_id = $2) < $14
			RETURNING id::text`,
			id, orgID, in.Name, in.Description, in.Kind, in.Cron, in.TimeZone, in.IntervalSeconds,
			in.GraceSeconds, in.Enabled, tagsParam(in), token, actorID(actor), MaxPerOrg).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLimit
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO job_monitor_state (monitor_id, expected_at) VALUES ($1, $2)`, id, expected); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "job_monitor.create", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Update implements Store. The token is left alone: the crontab that uses it must keep working.
func (s *PGStore) Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*Monitor, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	expected, err := in.NextExpected(time.Now().UTC())
	if err != nil {
		return nil, invalid("cron", err.Error())
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE job_monitors SET name = $3, description = $4, kind = $5, cron = $6,
			time_zone = $7, interval_seconds = $8, grace_seconds = $9, enabled = $10, tags = $11,
			updated_by = $12, updated_at = now() WHERE org_id = $1 AND id = $2`,
			orgID, id, in.Name, in.Description, in.Kind, in.Cron, in.TimeZone, in.IntervalSeconds,
			in.GraceSeconds, in.Enabled, tagsParam(in), actorID(actor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		// The schedule may have changed, so the expectation is recomputed from now. A run that is in flight
		// keeps its start time; only when the next one is due changes.
		if _, err := tx.Exec(ctx, `UPDATE job_monitor_state SET expected_at = $2 WHERE monitor_id = $1`, id, expected); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "job_monitor.update", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Delete implements Store.
func (s *PGStore) Delete(ctx context.Context, orgID, id string, actor Actor) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM job_monitors WHERE org_id = $1 AND id = $2`, orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, orgID, "job_monitor.delete", id, nil, actor)
	})
}

// Rotate implements Store: a new token for a monitor whose URL leaked. The old URL stops working at once,
// which is the point, so the crontab has to be updated with the new one.
func (s *PGStore) Rotate(ctx context.Context, orgID, id string, actor Actor) (*Monitor, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE job_monitors SET ping_token = $3, updated_by = $4, updated_at = now()
			WHERE org_id = $1 AND id = $2`, orgID, id, token, actorID(actor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, orgID, "job_monitor.rotate_token", id, nil, actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

func tagsParam(in Input) []string {
	if in.Tags == nil {
		return []string{}
	}
	return in.Tags
}

// ByToken implements PingStore.
func (s *PGStore) ByToken(ctx context.Context, token string) (Monitor, string, error) {
	token = NormalizeToken(token)
	if token == "" {
		return Monitor{}, "", ErrUnknownToken
	}
	var tenantID string
	m, err := scanMonitor(s.pool.QueryRow(ctx, `SELECT `+monitorColumns+`, o.tenant_id`+monitorFrom+`
		JOIN organizations o ON o.id = m.org_id WHERE m.ping_token = $1`, token), &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Monitor{}, "", ErrUnknownToken
	}
	if err != nil {
		return Monitor{}, "", err
	}
	return *m, tenantID, nil
}

// RecordPing implements PingStore: it applies the ping to the state and returns the run it concluded.
//
// The whole decision is made inside one transaction over the state row, because two pings of the same job
// (a start and a finish, or two racing cron hosts) must not interleave into a state that describes neither.
func (s *PGStore) RecordPing(ctx context.Context, monitorID string, p Ping) (*Run, *Monitor, error) {
	if _, err := uuid.Parse(monitorID); err != nil {
		return nil, nil, ErrNotFound
	}
	var (
		run *Run
		out *Monitor
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := scanMonitor(tx.QueryRow(ctx, `SELECT `+monitorColumns+monitorFrom+`
			WHERE m.id = $1 FOR UPDATE OF s`, monitorID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		st := m.State
		status := p.Status()
		at := p.At.UTC()
		late := at.Sub(st.ExpectedAt).Seconds()

		switch status {
		case StatusRunning:
			// A start ping while the previous run is still marked running past its grace: the previous run
			// never reported an end, which is an overrun and is worth a row of its own.
			if st.Status == StatusRunning && st.LastStartedAt != nil && m.Late(at) {
				run = &Run{MonitorID: m.ID, Name: m.Name, Status: StatusOverrun, StartedAt: *st.LastStartedAt,
					FinishedAt: at, DurationMs: float64(at.Sub(*st.LastStartedAt).Milliseconds()),
					LateSeconds: late, Source: p.Source}
			}
			st.Status = StatusRunning
			st.LastStartedAt = &at
		default:
			started := st.LastStartedAt
			duration := 0.0
			// The duration is only real when the start belongs to this run: a start recorded before the
			// previous finish is an old one, and pairing it would report a run that lasted a day.
			if started != nil && (st.LastFinishedAt == nil || started.After(*st.LastFinishedAt)) {
				duration = float64(at.Sub(*started).Milliseconds())
			} else {
				started = nil
			}
			run = &Run{MonitorID: m.ID, Name: m.Name, Status: status, FinishedAt: at,
				DurationMs: duration, ExitCode: p.ExitCode, Message: p.Message, LateSeconds: late,
				Source: p.Source}
			if started != nil {
				run.StartedAt = *started
			}
			st.Status = status
			st.LastFinishedAt = &at
			st.LastDurationMs = duration
			st.LastExitCode = p.ExitCode
			st.LastMessage = truncate(p.Message, MaxMessageBytes)
			if status == StatusSuccess {
				st.ConsecutiveFailures = 0
			} else {
				st.ConsecutiveFailures++
			}
		}
		st.LastPingAt = &at
		// The next expectation is anchored on this ping: a cron monitor's on the schedule, an interval
		// monitor's on the report it just made.
		next, err := m.Input.NextExpected(at)
		if err != nil {
			return err
		}
		st.ExpectedAt = next
		if err := writeState(ctx, tx, m.ID, st); err != nil {
			return err
		}
		m.State = st
		out = m
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return run, out, nil
}

func writeState(ctx context.Context, tx pgx.Tx, id string, st State) error {
	_, err := tx.Exec(ctx, `UPDATE job_monitor_state SET status = $2, last_ping_at = $3, last_started_at = $4,
		last_finished_at = $5, last_duration_ms = $6, last_exit_code = $7, last_message = $8,
		expected_at = $9, consecutive_failures = $10 WHERE monitor_id = $1`,
		id, st.Status, st.LastPingAt, st.LastStartedAt, st.LastFinishedAt, st.LastDurationMs,
		clampExit(st.LastExitCode), st.LastMessage, st.ExpectedAt, st.ConsecutiveFailures)
	return err
}

// ClaimOverdue implements SweepStore.
//
// Claiming and advancing happen in one statement (FOR UPDATE ... SKIP LOCKED, as in the synthetics
// scheduler), so two api pods never conclude the same missed run twice. The new expectation is computed in
// Go rather than SQL, because a cron expression in a time zone is not something to reimplement in SQL; the
// statement therefore claims the rows first and the caller writes their new expectation back.
func (s *PGStore) ClaimOverdue(ctx context.Context, now time.Time, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []Run
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+monitorColumns+`, o.tenant_id`+monitorFrom+`
			JOIN organizations o ON o.id = m.org_id
			WHERE m.enabled AND s.expected_at + make_interval(secs => m.grace_seconds) < $1
			ORDER BY s.expected_at
			LIMIT $2
			FOR UPDATE OF s SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		type claimed struct {
			m        Monitor
			tenantID string
		}
		var list []claimed
		for rows.Next() {
			var tenantID string
			m, err := scanMonitor(rows, &tenantID)
			if err != nil {
				rows.Close()
				return err
			}
			list = append(list, claimed{*m, tenantID})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range list {
			m := c.m
			st := m.State
			run := Run{MonitorID: m.ID, TenantID: c.tenantID, Name: m.Name, Status: StatusMissed,
				FinishedAt: now.UTC(), LateSeconds: now.Sub(st.ExpectedAt).Seconds()}
			// A run that started and never reported an end is an overrun, not a missing run: the job did
			// start, and saying so is the difference between "cron is broken" and "the job hung".
			if st.Status == StatusRunning && st.LastStartedAt != nil {
				run.Status = StatusOverrun
				run.StartedAt = *st.LastStartedAt
				run.DurationMs = float64(now.Sub(*st.LastStartedAt).Milliseconds())
			}
			st.Status = run.Status
			st.ConsecutiveFailures++
			next, err := m.Input.NextExpected(now)
			if err != nil {
				// A definition whose schedule no longer resolves would be claimed on every sweep; push it
				// out by a day so the sweep keeps moving and the monitor shows its state.
				next = now.Add(24 * time.Hour)
			}
			st.ExpectedAt = next
			if err := writeState(ctx, tx, m.ID, st); err != nil {
				return err
			}
			out = append(out, run)
		}
		return nil
	})
	return out, err
}
