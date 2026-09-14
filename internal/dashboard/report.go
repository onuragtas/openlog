package dashboard

import (
	"context"
	"net/mail"
	"slices"
	"strings"
	"time"
)

// Scheduled e-mail reports of dashboards (migrations/postgres/0055_dashboard_reports.sql, api.md "Dashboards" ›
// "Scheduled reports", D-087). The sending job is internal/dashboard/report.

// Report limits.
const (
	MaxReportsPerDashboard = 10
	MaxReportRecipients    = 20
)

// ReportInput creates or replaces a report schedule.
type ReportInput struct {
	Name       string              `json:"name"`
	Frequency  string              `json:"frequency"`
	Weekday    int                 `json:"weekday"`
	Hour       int                 `json:"hour"`
	Minute     int                 `json:"minute"`
	Timezone   string              `json:"timezone"`
	Recipients []string            `json:"recipients"`
	Language   string              `json:"language"`
	Range      string              `json:"range"`
	Variables  map[string][]string `json:"variables"`
	Enabled    *bool               `json:"enabled"`
}

// ReportRun is the outcome of one period.
type ReportRun struct {
	Period     string
	Status     string // running, sent, partial, failed, skipped
	Error      string
	Recipients int
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Report is a report schedule of a dashboard.
type Report struct {
	ID             string
	OrgID          string
	DashboardID    string
	Name           string
	Frequency      string // daily, weekly
	Weekday        int    // 0 = Sunday (weekly)
	Hour, Minute   int
	Timezone       string
	Recipients     []string
	Language       string // en, tr
	Range          string
	Variables      map[string][]string
	Enabled        bool
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastRun        *ReportRun
}

// ScheduledReport is an enabled report with what the sending job needs.
type ScheduledReport struct {
	Report
	TenantID string
}

// ReportStore persists report schedules and their runs (PGStore; optional for other stores).
type ReportStore interface {
	ListReports(ctx context.Context, orgID, dashboardID string) ([]Report, error)
	GetReport(ctx context.Context, orgID, dashboardID, id string) (*Report, error)
	// CreateReport inserts r unless the dashboard already has maxPerDashboard reports (invalid argument).
	CreateReport(ctx context.Context, r *Report, maxPerDashboard int) error
	UpdateReport(ctx context.Context, r *Report) error
	DeleteReport(ctx context.Context, orgID, dashboardID, id string) error
	// MemberEmails returns the e-mail addresses of the organization's members (lower case).
	MemberEmails(ctx context.Context, orgID string) ([]string, error)
	// EnabledReports returns the enabled reports of all organizations.
	EnabledReports(ctx context.Context, limit int) ([]ScheduledReport, error)
	// ClaimReportRun records the run of a period; false when the period was already claimed.
	ClaimReportRun(ctx context.Context, reportID, period string, now time.Time) (bool, error)
	FinishReportRun(ctx context.Context, reportID, period string, run ReportRun) error
}

// Location returns the schedule's time zone (UTC when it cannot be loaded).
func (r *Report) Location() *time.Location {
	if loc, err := time.LoadLocation(r.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

func (r *Report) at(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), r.Hour, r.Minute, 0, 0, day.Location())
}

func (r *Report) matches(day time.Time) bool {
	return r.Frequency != "weekly" || int(day.Weekday()) == r.Weekday
}

// Occurrence returns the latest scheduled send at or before now and its period (local date YYYY-MM-DD); ok=false
// when there is none in the last 8 days.
func (r *Report) Occurrence(now time.Time) (time.Time, string, bool) {
	local := now.In(r.Location())
	for i := 0; i <= 8; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()-i, 12, 0, 0, 0, local.Location())
		if !r.matches(day) {
			continue
		}
		if at := r.at(day); !at.After(now) {
			return at, day.Format(time.DateOnly), true
		}
	}
	return time.Time{}, "", false
}

// Next returns the next scheduled send after now.
func (r *Report) Next(now time.Time) time.Time {
	local := now.In(r.Location())
	for i := 0; i <= 8; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+i, 12, 0, 0, 0, local.Location())
		if !r.matches(day) {
			continue
		}
		if at := r.at(day); at.After(now) {
			return at
		}
	}
	return time.Time{}
}

// RangeDuration returns the report's time range.
func (r *Report) RangeDuration() time.Duration {
	d, _ := ParseRelativeRange(r.Range)
	return d
}

func (m *Manager) reportStore() (ReportStore, error) {
	rs, ok := m.store.(ReportStore)
	if !ok {
		return nil, ErrNotFound
	}
	return rs, nil
}

// Reports lists the report schedules of a dashboard.
func (m *Manager) Reports(ctx context.Context, orgID, id string, v Viewer) ([]Report, error) {
	rs, err := m.reportStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.canManageShares(d) {
		return nil, ErrForbidden
	}
	return rs.ListReports(ctx, orgID, d.ID)
}

// CreateReport validates and stores a report schedule (editors of the dashboard).
func (m *Manager) CreateReport(ctx context.Context, orgID, id string, in ReportInput, v Viewer) (*Report, error) {
	rs, err := m.reportStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(d) {
		return nil, ErrForbidden
	}
	r := &Report{ID: m.newID(), OrgID: orgID, DashboardID: d.ID, CreatedBy: v.UserID}
	if err := m.normalizeReport(ctx, rs, d, r, in); err != nil {
		return nil, err
	}
	if err := rs.CreateReport(ctx, r, MaxReportsPerDashboard); err != nil {
		return nil, err
	}
	return rs.GetReport(ctx, orgID, d.ID, r.ID)
}

// UpdateReport replaces a report schedule (editors of the dashboard, and admins for dashboards they can read).
func (m *Manager) UpdateReport(ctx context.Context, orgID, id, reportID string, in ReportInput, v Viewer) (*Report, error) {
	rs, err := m.reportStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.canManageShares(d) {
		return nil, ErrForbidden
	}
	if _, ok := canonicalID(reportID); !ok {
		return nil, ErrNotFound
	}
	r, err := rs.GetReport(ctx, orgID, d.ID, reportID)
	if err != nil {
		return nil, err
	}
	if err := m.normalizeReport(ctx, rs, d, r, in); err != nil {
		return nil, err
	}
	if err := rs.UpdateReport(ctx, r); err != nil {
		return nil, err
	}
	return rs.GetReport(ctx, orgID, d.ID, r.ID)
}

// DeleteReport removes a report schedule and returns it.
func (m *Manager) DeleteReport(ctx context.Context, orgID, id, reportID string, v Viewer) (*Report, error) {
	rs, err := m.reportStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.canManageShares(d) {
		return nil, ErrForbidden
	}
	if _, ok := canonicalID(reportID); !ok {
		return nil, ErrNotFound
	}
	r, err := rs.GetReport(ctx, orgID, d.ID, reportID)
	if err != nil {
		return nil, err
	}
	if err := rs.DeleteReport(ctx, orgID, d.ID, reportID); err != nil {
		return nil, err
	}
	return r, nil
}

func (m *Manager) normalizeReport(ctx context.Context, rs ReportStore, d *Dashboard, r *Report, in ReportInput) error {
	var err error
	if r.Name, err = checkText("name", in.Name, 0, 100); err != nil {
		return err
	}
	if strings.Contains(r.Name, "\n") {
		return invalid("name must be a single line")
	}
	switch in.Frequency {
	case "daily", "weekly":
	default:
		return invalid("frequency must be daily or weekly")
	}
	r.Frequency = in.Frequency
	if in.Weekday < 0 || in.Weekday > 6 {
		return invalid("weekday must be 0 (Sunday) to 6 (Saturday)")
	}
	if in.Hour < 0 || in.Hour > 23 || in.Minute < 0 || in.Minute > 59 {
		return invalid("hour must be 0-23 and minute 0-59")
	}
	r.Weekday, r.Hour, r.Minute = in.Weekday, in.Hour, in.Minute
	if r.Frequency == "daily" {
		r.Weekday = 1
	}
	r.Timezone = strings.TrimSpace(in.Timezone)
	if r.Timezone == "" {
		r.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil || len(r.Timezone) > 64 || strings.EqualFold(r.Timezone, "local") {
		return invalid("timezone must be an IANA time zone such as Europe/Istanbul")
	}
	switch in.Language {
	case "":
		r.Language = "en"
	case "en", "tr":
		r.Language = in.Language
	default:
		return invalid("language must be en or tr")
	}
	r.Range = in.Range
	if r.Range == "" {
		r.Range = map[string]string{"daily": "24h", "weekly": "7d"}[r.Frequency]
	}
	if _, ok := ParseRelativeRange(r.Range); !ok {
		return invalid("range must be a relative range such as 24h or 7d (at most 31 days)")
	}
	if r.Variables, err = lockedVariables(d, in.Variables); err != nil {
		return err
	}
	r.Enabled = in.Enabled == nil || *in.Enabled
	if r.Recipients, err = m.checkRecipients(ctx, rs, d, in.Recipients); err != nil {
		return err
	}
	return nil
}

// checkRecipients normalizes recipients: organization members or addresses in the organization's report domains;
// private dashboards only to their creator.
func (m *Manager) checkRecipients(ctx context.Context, rs ReportStore, d *Dashboard, in []string) ([]string, error) {
	if len(in) == 0 || len(in) > MaxReportRecipients {
		return nil, invalid("recipients: 1-%d e-mail addresses", MaxReportRecipients)
	}
	out := []string{}
	for i, raw := range in {
		a, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil || strings.ContainsAny(raw, "\r\n,;") || len(a.Address) > 320 {
			return nil, invalid("recipients[%d] is not a valid e-mail address", i)
		}
		addr := strings.ToLower(a.Address)
		if !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	allowed, err := RecipientChecker(ctx, rs, m.shareSettings(ctx, d.OrgID), d)
	if err != nil {
		return nil, err
	}
	for _, addr := range out {
		if !allowed(addr) {
			if d.Visibility == "private" {
				return nil, invalid("reports of a private dashboard can only be sent to its creator (%s is not allowed)", addr)
			}
			return nil, invalid("%s is neither a member of the organization nor in an allowed report domain", addr)
		}
	}
	return out, nil
}

func (m *Manager) shareSettings(ctx context.Context, orgID string) func() (Settings, error) {
	return func() (Settings, error) {
		ss, ok := m.store.(ShareStore)
		if !ok {
			return Settings{ReportDomains: []string{}}, nil
		}
		return ss.GetSettings(ctx, orgID)
	}
}

// RecipientChecker returns a function reporting whether a (lower-case) address may receive reports of d. It is used
// when a schedule is saved and again before every send.
func RecipientChecker(ctx context.Context, rs ReportStore, settings func() (Settings, error), d *Dashboard) (func(string) bool, error) {
	if d.Visibility == "private" {
		creator := strings.ToLower(d.CreatedByEmail)
		return func(addr string) bool { return creator != "" && addr == creator }, nil
	}
	members, err := rs.MemberEmails(ctx, d.OrgID)
	if err != nil {
		return nil, err
	}
	st, err := settings()
	if err != nil {
		return nil, err
	}
	return func(addr string) bool {
		if slices.Contains(members, addr) {
			return true
		}
		at := strings.LastIndexByte(addr, '@')
		return at > 0 && slices.Contains(st.ReportDomains, addr[at+1:])
	}, nil
}
