package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Report schedules and runs (migrations/postgres/0055_dashboard_reports.sql).

var _ ReportStore = (*PGStore)(nil)

const reportSelect = `SELECT r.id::text, r.org_id::text, r.dashboard_id::text, r.name, r.frequency, r.weekday::int, r.hour::int, r.minute::int,
	       r.timezone, r.recipients, r.language, r.time_range, r.variables, r.enabled, coalesce(r.created_by::text, ''), coalesce(u.email, ''),
	       r.created_at, r.updated_at, lr.period, lr.status, lr.error, lr.recipients, lr.started_at, lr.finished_at`

const reportFrom = `
	FROM dashboard_reports r
	LEFT JOIN users u ON u.id = r.created_by
	LEFT JOIN LATERAL (SELECT period, status, error, recipients, started_at, finished_at FROM dashboard_report_runs
	                   WHERE report_id = r.id ORDER BY started_at DESC LIMIT 1) lr ON true`

func scanReport(row pgx.Row, extra ...any) (*Report, error) {
	var (
		r                    Report
		vars                 []byte
		period, status, errs *string
		recipients           *int32
		started, finished    *time.Time
	)
	dest := append([]any{&r.ID, &r.OrgID, &r.DashboardID, &r.Name, &r.Frequency, &r.Weekday, &r.Hour, &r.Minute, &r.Timezone, &r.Recipients,
		&r.Language, &r.Range, &vars, &r.Enabled, &r.CreatedBy, &r.CreatedByEmail, &r.CreatedAt, &r.UpdatedAt,
		&period, &status, &errs, &recipients, &started, &finished}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	r.Variables = map[string][]string{}
	if err := json.Unmarshal(vars, &r.Variables); err != nil {
		return nil, err
	}
	if r.Recipients == nil {
		r.Recipients = []string{}
	}
	if period != nil && status != nil && started != nil {
		run := &ReportRun{Period: *period, Status: *status, StartedAt: *started, FinishedAt: finished}
		if errs != nil {
			run.Error = *errs
		}
		if recipients != nil {
			run.Recipients = int(*recipients)
		}
		r.LastRun = run
	}
	return &r, nil
}

func (s *PGStore) ListReports(ctx context.Context, orgID, dashboardID string) ([]Report, error) {
	cid, ok := canonicalID(dashboardID)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, reportSelect+reportFrom+` WHERE r.org_id = $1 AND r.dashboard_id = $2 ORDER BY r.created_at, r.id`, orgID, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) GetReport(ctx context.Context, orgID, dashboardID, id string) (*Report, error) {
	cid, ok := canonicalID(dashboardID)
	rid, okReport := canonicalID(id)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg || !okReport {
		return nil, ErrNotFound
	}
	r, err := scanReport(s.pool.QueryRow(ctx, reportSelect+reportFrom+` WHERE r.org_id = $1 AND r.dashboard_id = $2 AND r.id = $3`, orgID, cid, rid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *PGStore) CreateReport(ctx context.Context, r *Report, maxPerDashboard int) error {
	vars, err := json.Marshal(r.Variables)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM dashboards WHERE org_id = $1 AND id = $2 FOR UPDATE`, r.OrgID, r.DashboardID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM dashboard_reports WHERE dashboard_id = $1`, r.DashboardID).Scan(&n); err != nil {
			return err
		}
		if n >= maxPerDashboard {
			return invalid("a dashboard has at most %d scheduled reports", maxPerDashboard)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO dashboard_reports (id, org_id, dashboard_id, name, frequency, weekday, hour, minute, timezone, recipients, language,
			                               time_range, variables, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14, $15)`,
			r.ID, r.OrgID, r.DashboardID, r.Name, r.Frequency, r.Weekday, r.Hour, r.Minute, r.Timezone, r.Recipients, r.Language,
			r.Range, string(vars), r.Enabled, nullID(r.CreatedBy))
		return err
	})
}

func (s *PGStore) UpdateReport(ctx context.Context, r *Report) error {
	vars, err := json.Marshal(r.Variables)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE dashboard_reports SET name = $4, frequency = $5, weekday = $6, hour = $7, minute = $8, timezone = $9, recipients = $10,
		       language = $11, time_range = $12, variables = $13::jsonb, enabled = $14, updated_at = now()
		WHERE org_id = $1 AND dashboard_id = $2 AND id = $3`,
		r.OrgID, r.DashboardID, r.ID, r.Name, r.Frequency, r.Weekday, r.Hour, r.Minute, r.Timezone, r.Recipients, r.Language, r.Range,
		string(vars), r.Enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteReport(ctx context.Context, orgID, dashboardID, id string) error {
	cid, ok := canonicalID(dashboardID)
	rid, okReport := canonicalID(id)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg || !okReport {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM dashboard_reports WHERE org_id = $1 AND dashboard_id = $2 AND id = $3`, orgID, cid, rid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) MemberEmails(ctx context.Context, orgID string) ([]string, error) {
	if _, ok := canonicalID(orgID); !ok {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT lower(u.email) FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PGStore) EnabledReports(ctx context.Context, limit int) ([]ScheduledReport, error) {
	rows, err := s.pool.Query(ctx, reportSelect+`, o.tenant_id`+reportFrom+`
		JOIN organizations o ON o.id = r.org_id
		WHERE r.enabled ORDER BY r.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduledReport{}
	for rows.Next() {
		var tenant string
		r, err := scanReport(rows, &tenant)
		if err != nil {
			return nil, err
		}
		out = append(out, ScheduledReport{Report: *r, TenantID: tenant})
	}
	return out, rows.Err()
}

func (s *PGStore) ClaimReportRun(ctx context.Context, reportID, period string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO dashboard_report_runs (report_id, period, started_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		reportID, period, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	// Runs are kept for 90 days.
	_, err = s.pool.Exec(ctx, `DELETE FROM dashboard_report_runs WHERE report_id = $1 AND started_at < $2::timestamptz - interval '90 days'`, reportID, now)
	return true, err
}

func (s *PGStore) FinishReportRun(ctx context.Context, reportID, period string, run ReportRun) error {
	msg := run.Error
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, err := s.pool.Exec(ctx, `UPDATE dashboard_report_runs SET status = $3, error = $4, recipients = $5, finished_at = $6
		WHERE report_id = $1 AND period = $2`, reportID, period, run.Status, msg, run.Recipients, run.FinishedAt)
	return err
}
