package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
)

// Dashboard version history, sharing settings, share links and scheduled reports (docs/contracts/api.md "Dashboards"
// › "Version history", "Sharing settings", "Share links", "Scheduled reports"; D-086, D-087). Registered by
// dashboardRoutes with its authentication and role checks.

func (s *Server) dashboardSharingRoutes(route func(pattern string, h dashboardFunc)) {
	route("GET /api/v1/dashboards/settings", s.getDashboardSettings)
	route("PUT /api/v1/dashboards/settings", s.updateDashboardSettings)
	route("GET /api/v1/dashboards/{id}/versions", s.listDashboardVersions)
	route("GET /api/v1/dashboards/{id}/versions/{version}", s.getDashboardVersion)
	route("POST /api/v1/dashboards/{id}/versions/{version}/restore", s.restoreDashboardVersion)
	route("GET /api/v1/dashboards/{id}/shares", s.listDashboardShares)
	route("POST /api/v1/dashboards/{id}/shares", s.createDashboardShare)
	route("DELETE /api/v1/dashboards/{id}/shares/{share_id}", s.revokeDashboardShare)
	route("GET /api/v1/dashboards/{id}/reports", s.listDashboardReports)
	route("POST /api/v1/dashboards/{id}/reports", s.createDashboardReport)
	route("PUT /api/v1/dashboards/{id}/reports/{report_id}", s.updateDashboardReport)
	route("DELETE /api/v1/dashboards/{id}/reports/{report_id}", s.deleteDashboardReport)
}

func optTimePtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func optInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

var errSharingDisabled = &apiError{http.StatusConflict, "failed_precondition",
	"share links are disabled for this organization; an admin or owner can enable them in the dashboard sharing settings"}

// ---- settings ----

type dashboardSettingsJSON struct {
	ShareLinksEnabled bool     `json:"share_links_enabled"`
	ReportDomains     []string `json:"report_domains"`
	UpdatedAt         *string  `json:"updated_at"`
	CanEdit           bool     `json:"can_edit"`
}

func settingsResponse(st dashboard.Settings, v dashboard.Viewer) dashboardSettingsJSON {
	// can_edit must report exactly what updateDashboardSettings enforces below; an admin API key
	// has no user id but may change these settings (D-133).
	out := dashboardSettingsJSON{ShareLinksEnabled: st.SharesEnabled, ReportDomains: st.ReportDomains, UpdatedAt: optTimePtr(&st.UpdatedAt),
		CanEdit: v.CanWrite && v.Admin}
	if out.ReportDomains == nil {
		out.ReportDomains = []string{}
	}
	return out
}

func (s *Server) getDashboardSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	st, err := s.dashboards.Settings(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, settingsResponse(st, v))
	return nil
}

func (s *Server) updateDashboardSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	var in struct {
		ShareLinksEnabled *bool    `json:"share_links_enabled"`
		ReportDomains     []string `json:"report_domains"`
	}
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	if in.ShareLinksEnabled == nil {
		return &dashboard.ValidationError{Msg: "share_links_enabled is required"}
	}
	if !v.Admin || !v.CanWrite {
		return &apiError{http.StatusForbidden, "permission_denied", "only admins and owners can change the dashboard sharing settings"}
	}
	st, err := s.dashboards.UpdateSettings(r.Context(), p.OrgID, dashboard.Settings{SharesEnabled: *in.ShareLinksEnabled, ReportDomains: in.ReportDomains}, v)
	if err != nil {
		return err
	}
	if s.accounts != nil {
		s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "dashboard.settings.update", "organization", p.OrgID,
			map[string]any{"share_links_enabled": st.SharesEnabled, "report_domains": st.ReportDomains})
	}
	writeJSON(w, http.StatusOK, settingsResponse(st, v))
	return nil
}

// ---- versions ----

type dashboardVersionJSON struct {
	Version      int     `json:"version"`
	AuthorUserID *string `json:"author_user_id"`
	AuthorEmail  string  `json:"author_email"`
	CreatedAt    string  `json:"created_at"`
	RestoredFrom *int    `json:"restored_from"`
	PageCount    int     `json:"page_count"`
	WidgetCount  int     `json:"widget_count"`
}

func dashboardVersionResponse(vi dashboard.VersionInfo) dashboardVersionJSON {
	return dashboardVersionJSON{Version: vi.Version, AuthorUserID: optString(vi.AuthorID), AuthorEmail: vi.AuthorEmail, CreatedAt: formatTime(vi.CreatedAt),
		RestoredFrom: optInt(vi.RestoredFrom), PageCount: vi.PageCount, WidgetCount: vi.WidgetCount}
}

type dashboardVersionDetailJSON struct {
	dashboardVersionJSON
	Document               dashboard.Snapshot `json:"document"`
	CurrentVersion         int                `json:"current_version"`
	PreviousVersion        *int               `json:"previous_version"`
	Changes                *dashboard.Diff    `json:"changes"`
	DifferencesFromCurrent dashboard.Diff     `json:"differences_from_current"`
}

func versionParam(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n <= 0 {
		return 0, dashboard.ErrNotFound
	}
	return n, nil
}

func (s *Server) listDashboardVersions(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	d, err := s.dashboards.Get(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	list, err := s.dashboards.Versions(r.Context(), p.OrgID, d.ID, v)
	if err != nil {
		return err
	}
	out := make([]dashboardVersionJSON, 0, len(list))
	for _, vi := range list {
		out = append(out, dashboardVersionResponse(vi))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out, "current_version": d.Version, "can_restore": v.CanEdit(d)})
	return nil
}

func (s *Server) getDashboardVersion(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	n, err := versionParam(r)
	if err != nil {
		return err
	}
	det, err := s.dashboards.VersionDetail(r.Context(), p.OrgID, r.PathValue("id"), n, v)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, dashboardVersionDetailJSON{dashboardVersionJSON: dashboardVersionResponse(det.VersionInfo), Document: det.Document,
		CurrentVersion: det.Current, PreviousVersion: optInt(det.Previous), Changes: det.Changes, DifferencesFromCurrent: det.FromCurrent})
	return nil
}

func (s *Server) restoreDashboardVersion(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	n, err := versionParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	if in.Version <= 0 {
		return &dashboard.ValidationError{Msg: "version is required (the current version of the dashboard that was read)"}
	}
	d, err := s.dashboards.Restore(r.Context(), p.OrgID, r.PathValue("id"), n, in.Version, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.restore", d, map[string]any{"version": d.Version, "restored_from": n})
	writeJSON(w, http.StatusOK, dashboardResponse(d, v))
	return nil
}

// ---- share links ----

type dashboardShareJSON struct {
	ID             string              `json:"id"`
	Label          string              `json:"label"`
	Range          *string             `json:"range"`
	From           *string             `json:"from"`
	To             *string             `json:"to"`
	Variables      map[string][]string `json:"variables"`
	CreatedByEmail string              `json:"created_by_email"`
	CreatedAt      string              `json:"created_at"`
	ExpiresAt      string              `json:"expires_at"`
	RevokedAt      *string             `json:"revoked_at"`
	LastUsedAt     *string             `json:"last_used_at"`
	UseCount       int64               `json:"use_count"`
	Active         bool                `json:"active"`
}

func (s *Server) shareResponse(sh *dashboard.Share) dashboardShareJSON {
	out := dashboardShareJSON{ID: sh.ID, Label: sh.Label, Range: optString(sh.Range), Variables: sh.Variables, CreatedByEmail: sh.CreatedByEmail,
		CreatedAt: formatTime(sh.CreatedAt), ExpiresAt: formatTime(sh.ExpiresAt), RevokedAt: optTimePtr(sh.RevokedAt),
		LastUsedAt: optTimePtr(sh.LastUsedAt), UseCount: sh.UseCount, Active: sh.Active(s.now())}
	if sh.Range == "" {
		out.From, out.To = optTimePtr(&sh.From), optTimePtr(&sh.To)
	}
	if out.Variables == nil {
		out.Variables = map[string][]string{}
	}
	return out
}

func (s *Server) listDashboardShares(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	list, err := s.dashboards.Shares(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	st, err := s.dashboards.Settings(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]dashboardShareJSON, 0, len(list))
	for i := range list {
		out = append(out, s.shareResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out, "share_links_enabled": st.SharesEnabled})
	return nil
}

func (s *Server) createDashboardShare(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in dashboard.ShareInput
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	sh, token, err := s.dashboards.CreateShare(r.Context(), p.OrgID, r.PathValue("id"), in, v)
	if errors.Is(err, dashboard.ErrSharingDisabled) {
		return errSharingDisabled
	}
	if err != nil {
		return err
	}
	if s.accounts != nil {
		s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "dashboard.share.create", "dashboard", sh.DashboardID,
			map[string]any{"share_id": sh.ID, "label": sh.Label, "expires_at": formatTime(sh.ExpiresAt), "range": sh.Range})
	}
	writeJSON(w, http.StatusCreated, struct {
		dashboardShareJSON
		Token string `json:"token"`
		Path  string `json:"path"`
	}{s.shareResponse(sh), token, "/shared/dashboards/" + token})
	return nil
}

func (s *Server) revokeDashboardShare(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	sh, err := s.dashboards.RevokeShare(r.Context(), p.OrgID, r.PathValue("id"), r.PathValue("share_id"), v)
	if err != nil {
		return err
	}
	if s.accounts != nil {
		s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "dashboard.share.revoke", "dashboard", sh.DashboardID,
			map[string]any{"share_id": sh.ID, "label": sh.Label})
	}
	writeJSON(w, http.StatusOK, s.shareResponse(sh))
	return nil
}

// ---- scheduled reports ----

type dashboardReportRunJSON struct {
	Period     string  `json:"period"`
	Status     string  `json:"status"`
	Error      string  `json:"error"`
	Recipients int     `json:"recipients"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

type dashboardReportJSON struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Frequency      string                  `json:"frequency"`
	Weekday        int                     `json:"weekday"`
	Hour           int                     `json:"hour"`
	Minute         int                     `json:"minute"`
	Timezone       string                  `json:"timezone"`
	Recipients     []string                `json:"recipients"`
	Language       string                  `json:"language"`
	Range          string                  `json:"range"`
	Variables      map[string][]string     `json:"variables"`
	Enabled        bool                    `json:"enabled"`
	CreatedByEmail string                  `json:"created_by_email"`
	CreatedAt      string                  `json:"created_at"`
	UpdatedAt      string                  `json:"updated_at"`
	NextRunAt      *string                 `json:"next_run_at"`
	LastRun        *dashboardReportRunJSON `json:"last_run"`
}

func (s *Server) reportResponse(rp *dashboard.Report) dashboardReportJSON {
	out := dashboardReportJSON{ID: rp.ID, Name: rp.Name, Frequency: rp.Frequency, Weekday: rp.Weekday, Hour: rp.Hour, Minute: rp.Minute,
		Timezone: rp.Timezone, Recipients: rp.Recipients, Language: rp.Language, Range: rp.Range, Variables: rp.Variables, Enabled: rp.Enabled,
		CreatedByEmail: rp.CreatedByEmail, CreatedAt: formatTime(rp.CreatedAt), UpdatedAt: formatTime(rp.UpdatedAt)}
	if out.Recipients == nil {
		out.Recipients = []string{}
	}
	if out.Variables == nil {
		out.Variables = map[string][]string{}
	}
	if rp.Enabled {
		next := rp.Next(s.now())
		out.NextRunAt = optTimePtr(&next)
	}
	if lr := rp.LastRun; lr != nil {
		out.LastRun = &dashboardReportRunJSON{Period: lr.Period, Status: lr.Status, Error: lr.Error, Recipients: lr.Recipients,
			StartedAt: formatTime(lr.StartedAt), FinishedAt: optTimePtr(lr.FinishedAt)}
	}
	return out
}

func (s *Server) auditReport(r *http.Request, p *auth.Principal, action string, rp *dashboard.Report) {
	if s.accounts == nil {
		return
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), action, "dashboard", rp.DashboardID,
		map[string]any{"report_id": rp.ID, "frequency": rp.Frequency, "recipients": rp.Recipients, "enabled": rp.Enabled})
}

func (s *Server) listDashboardReports(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	list, err := s.dashboards.Reports(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	out := make([]dashboardReportJSON, 0, len(list))
	for i := range list {
		out = append(out, s.reportResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
	return nil
}

func (s *Server) createDashboardReport(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in dashboard.ReportInput
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	rp, err := s.dashboards.CreateReport(r.Context(), p.OrgID, r.PathValue("id"), in, v)
	if err != nil {
		return err
	}
	s.auditReport(r, p, "dashboard.report.create", rp)
	writeJSON(w, http.StatusCreated, s.reportResponse(rp))
	return nil
}

func (s *Server) updateDashboardReport(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in dashboard.ReportInput
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	rp, err := s.dashboards.UpdateReport(r.Context(), p.OrgID, r.PathValue("id"), r.PathValue("report_id"), in, v)
	if err != nil {
		return err
	}
	s.auditReport(r, p, "dashboard.report.update", rp)
	writeJSON(w, http.StatusOK, s.reportResponse(rp))
	return nil
}

func (s *Server) deleteDashboardReport(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	rp, err := s.dashboards.DeleteReport(r.Context(), p.OrgID, r.PathValue("id"), r.PathValue("report_id"), v)
	if err != nil {
		return err
	}
	s.auditReport(r, p, "dashboard.report.delete", rp)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
