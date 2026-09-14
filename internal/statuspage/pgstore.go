package statuspage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/store/postgres"
)

// StateKey is the system_state document of the latest self-check.
const StateKey = "status_page"

// PGStore persists incidents and check counts (0071_status_page).
type PGStore struct {
	Pool *pgxpool.Pool
}

const incidentCols = `id::text, kind, title, status, impact, components, starts_at, ends_at, created_at, updated_at`

func scanIncident(row pgx.Row) (Incident, error) {
	var in Incident
	err := row.Scan(&in.ID, &in.Kind, &in.Title, &in.Status, &in.Impact, &in.Components, &in.StartsAt, &in.EndsAt, &in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, ErrNotFound
	}
	if in.Components == nil {
		in.Components = []string{}
	}
	in.Updates = []Update{}
	return in, err
}

func (s PGStore) withUpdates(ctx context.Context, list []Incident) ([]Incident, error) {
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]string, len(list))
	idx := map[string]int{}
	for i, in := range list {
		ids[i] = in.ID
		idx[in.ID] = i
	}
	rows, err := s.Pool.Query(ctx, `SELECT incident_id::text, id, status, message, created_at FROM status_incident_updates
		WHERE incident_id = ANY($1::uuid[]) ORDER BY created_at DESC, id DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var incID string
		var u Update
		if err := rows.Scan(&incID, &u.ID, &u.Status, &u.Message, &u.CreatedAt); err != nil {
			return nil, err
		}
		list[idx[incID]].Updates = append(list[idx[incID]].Updates, u)
	}
	return list, rows.Err()
}

func (s PGStore) query(ctx context.Context, sql string, args ...any) ([]Incident, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Incident, error) { return scanIncident(r) })
	if err != nil {
		return nil, err
	}
	return s.withUpdates(ctx, list)
}

// List returns incidents newest first (operators).
func (s PGStore) List(ctx context.Context, limit int) ([]Incident, error) {
	return s.query(ctx, `SELECT `+incidentCols+` FROM status_incidents ORDER BY starts_at DESC LIMIT $1`, limit)
}

// Public returns open incidents and maintenance plus those finished since `since`.
func (s PGStore) Public(ctx context.Context, since time.Time) ([]Incident, error) {
	return s.query(ctx, `SELECT `+incidentCols+` FROM status_incidents
		WHERE status NOT IN ('resolved', 'completed') OR coalesce(ends_at, updated_at) >= $1
		ORDER BY starts_at DESC LIMIT 200`, since)
}

// Get returns one incident with its updates.
func (s PGStore) Get(ctx context.Context, id string) (Incident, error) {
	list, err := s.query(ctx, `SELECT `+incidentCols+` FROM status_incidents WHERE id::text = $1`, id)
	if err != nil {
		return Incident{}, err
	}
	if len(list) == 0 {
		return Incident{}, ErrNotFound
	}
	return list[0], nil
}

// Create inserts a validated incident with its first update (message may be empty).
func (s PGStore) Create(ctx context.Context, in Incident, message, actorUserID string, now time.Time) (Incident, error) {
	var actor *string
	if actorUserID != "" {
		actor = &actorUserID
	}
	if in.EndsAt == nil && Finished(in.Status) {
		in.EndsAt = &now
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Incident{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO status_incidents (kind, title, status, impact, components, starts_at, ends_at, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid, $9, $9) RETURNING id::text`,
		in.Kind, in.Title, in.Status, in.Impact, in.Components, in.StartsAt, in.EndsAt, actor, now).Scan(&id); err != nil {
		return Incident{}, err
	}
	if message != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO status_incident_updates (incident_id, status, message, created_at) VALUES ($1, $2, $3, $4)`,
			id, in.Status, message, now); err != nil {
			return Incident{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// Update replaces the editable fields of a validated incident.
func (s PGStore) Update(ctx context.Context, in Incident, now time.Time) (Incident, error) {
	if in.EndsAt == nil && Finished(in.Status) {
		in.EndsAt = &now
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE status_incidents SET title = $2, status = $3, impact = $4, components = $5, starts_at = $6, ends_at = $7, updated_at = $8
		WHERE id::text = $1`, in.ID, in.Title, in.Status, in.Impact, in.Components, in.StartsAt, in.EndsAt, now)
	if err != nil {
		return Incident{}, err
	}
	if tag.RowsAffected() == 0 {
		return Incident{}, ErrNotFound
	}
	return s.Get(ctx, in.ID)
}

// AddUpdate appends a timeline entry and moves the incident to status.
func (s PGStore) AddUpdate(ctx context.Context, id, status, message string, now time.Time) (Incident, error) {
	in, err := s.Get(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	in.Status = status
	if err := in.Validate(); err != nil {
		return Incident{}, err
	}
	if err := ValidMessage(message); err != nil {
		return Incident{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Incident{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO status_incident_updates (incident_id, status, message, created_at) VALUES ($1, $2, $3, $4)`, id, status, message, now); err != nil {
		return Incident{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE status_incidents SET status = $2, updated_at = $3,
		ends_at = CASE WHEN $2 IN ('resolved', 'completed') THEN coalesce(ends_at, $3) ELSE ends_at END WHERE id = $1`, id, status, now); err != nil {
		return Incident{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// Delete removes an incident.
func (s PGStore) Delete(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM status_incidents WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// RecordCheck adds one check per component to today's counts and stores the snapshot.
func (s PGStore) RecordCheck(ctx context.Context, snap Snapshot) error {
	day := snap.CheckedAt.UTC().Format(time.DateOnly)
	for comp, st := range snap.Components {
		var op, deg, out int
		switch st {
		case Operational, Maintenance:
			op = 1
		case Degraded:
			deg = 1
		case PartialOutage, MajorOutage:
			out = 1
		default:
			continue
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO status_checks_daily (component, day, checks, operational, degraded, outage) VALUES ($1, $2::date, 1, $3, $4, $5)
			ON CONFLICT (component, day) DO UPDATE SET checks = status_checks_daily.checks + 1, operational = status_checks_daily.operational + $3,
				degraded = status_checks_daily.degraded + $4, outage = status_checks_daily.outage + $5`, comp, day, op, deg, out); err != nil {
			return err
		}
	}
	return postgres.PutSystemState(ctx, s.Pool, StateKey, snap)
}

// Prune deletes check counts older than 400 days.
func (s PGStore) Prune(ctx context.Context, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM status_checks_daily WHERE day < $1::date`, now.AddDate(0, 0, -400).Format(time.DateOnly))
	return err
}

// Snapshot returns the latest self-check (found=false before the first one).
func (s PGStore) Snapshot(ctx context.Context) (Snapshot, bool, error) {
	var snap Snapshot
	_, found, err := postgres.GetSystemState(ctx, s.Pool, StateKey, &snap)
	return snap, found, err
}

// Counts returns check counts per component and date since `since`.
func (s PGStore) Counts(ctx context.Context, since time.Time) (map[string]map[string]DayCounts, error) {
	rows, err := s.Pool.Query(ctx, `SELECT component, day::text, checks, operational, degraded, outage FROM status_checks_daily WHERE day >= $1::date`,
		since.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]DayCounts{}
	for rows.Next() {
		var comp, day string
		var c DayCounts
		if err := rows.Scan(&comp, &day, &c.Checks, &c.Operational, &c.Degraded, &c.Outage); err != nil {
			return nil, err
		}
		if out[comp] == nil {
			out[comp] = map[string]DayCounts{}
		}
		out[comp][day] = c
	}
	return out, rows.Err()
}
