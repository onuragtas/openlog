package alert

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Holiday calendars (migrations/postgres/0015_alert_holiday_calendars.sql, alerting.md §5.2).

const calendarColumns = `c.id::text, c.org_id::text, c.name, c.description, c.dates, COALESCE(c.created_by::text, ''), COALESCE(u.email, ''),
	c.created_at, c.updated_at,
	(SELECT count(*) FROM alert_mutes m WHERE m.org_id = c.org_id AND m.schedule->'holiday_calendar_ids' ? c.id::text)`

const calendarFrom = ` FROM alert_holiday_calendars c LEFT JOIN users u ON u.id = c.created_by`

func scanCalendar(row pgx.Row) (*HolidayCalendar, error) {
	c := &HolidayCalendar{}
	if err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.Description, &c.Dates, &c.CreatedBy, &c.CreatedByEmail, &c.CreatedAt, &c.UpdatedAt,
		&c.MuteCount); err != nil {
		return nil, mapPGErr(err)
	}
	if c.Dates == nil {
		c.Dates = []string{}
	}
	return c, nil
}

func (s *PGStore) ListHolidayCalendars(ctx context.Context, orgID string) ([]HolidayCalendar, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+calendarColumns+calendarFrom+` WHERE c.org_id = $1 ORDER BY c.name, c.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HolidayCalendar{}
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *PGStore) GetHolidayCalendar(ctx context.Context, orgID, id string) (*HolidayCalendar, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	return scanCalendar(s.pool.QueryRow(ctx, `SELECT `+calendarColumns+calendarFrom+` WHERE c.org_id = $1 AND c.id = $2`, orgID, id))
}

func (s *PGStore) CreateHolidayCalendar(ctx context.Context, orgID string, v *ValidHolidayCalendar, actor Actor) (*HolidayCalendar, error) {
	id := uuid.NewString()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO alert_holiday_calendars (id, org_id, name, description, dates, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, orgID, v.Name, v.Description, v.Dates, nullID(actor.UserID))
	audit(b, orgID, actor, "alert.holiday_calendar.create", "alert_holiday_calendar", id, map[string]any{"name": v.Name, "dates": len(v.Dates)})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetHolidayCalendar(ctx, orgID, id)
}

func (s *PGStore) UpdateHolidayCalendar(ctx context.Context, orgID, id string, v *ValidHolidayCalendar, actor Actor) (*HolidayCalendar, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `UPDATE alert_holiday_calendars SET name = $3, description = $4, dates = $5, updated_at = now()
		WHERE org_id = $1 AND id = $2`, orgID, id, v.Name, v.Description, v.Dates)
	if err != nil {
		return nil, mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.holiday_calendar.update", "alert_holiday_calendar", id, map[string]any{"name": v.Name, "dates": len(v.Dates)})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetHolidayCalendar(ctx, orgID, id)
}

// DeleteHolidayCalendar refuses to delete a calendar that mutes still reference (409 failed_precondition).
func (s *PGStore) DeleteHolidayCalendar(ctx context.Context, orgID, id string, actor Actor) error {
	if !ValidUUID(id) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var used int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_mutes WHERE org_id = $1 AND schedule->'holiday_calendar_ids' ? $2`, orgID, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return &PreconditionError{Msg: "the holiday calendar is used by recurring mutes; remove it from them first"}
	}
	var name string
	if err := tx.QueryRow(ctx, `DELETE FROM alert_holiday_calendars WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name); err != nil {
		return mapPGErr(err)
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.holiday_calendar.delete", "alert_holiday_calendar", id, map[string]any{"name": name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HolidayCalendarDates returns id → dates for the calendars of the organization among ids (unknown ids are absent).
func (s *PGStore) HolidayCalendarDates(ctx context.Context, orgID string, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, dates FROM alert_holiday_calendars WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, mapPGErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id    string
			dates []string
		)
		if err := rows.Scan(&id, &dates); err != nil {
			return nil, err
		}
		out[id] = dates
	}
	return out, rows.Err()
}

// attachMuteHolidays loads the holiday calendars referenced by recurring mutes (of any organizations, each mute
// only sees calendars of its own organization) and sets their dates on the schedules.
func (s *PGStore) attachMuteHolidays(ctx context.Context, mutes []Mute) error {
	var ids []string
	seen := map[string]bool{}
	for _, m := range mutes {
		if m.Schedule == nil {
			continue
		}
		for _, id := range m.Schedule.HolidayCalendarIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, org_id::text, dates FROM alert_holiday_calendars WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	byOrg := map[string]map[string][]string{}
	for rows.Next() {
		var (
			id, org string
			dates   []string
		)
		if err := rows.Scan(&id, &org, &dates); err != nil {
			return err
		}
		if byOrg[org] == nil {
			byOrg[org] = map[string][]string{}
		}
		byOrg[org][id] = dates
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range mutes {
		AttachHolidays(mutes[i:i+1], byOrg[mutes[i].OrgID])
	}
	return nil
}

// nowUTC is the store clock for tests that need a fixed time (unused in production paths).
var _ = time.Now
