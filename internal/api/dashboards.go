package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
)

// Dashboards (docs/contracts/api.md "Dashboards"). Reads: any role and API keys (private dashboards: creator only).
// Writes: signed-in members and higher; changing or deleting another user's dashboard needs admin or owner.

// SetDashboards enables the dashboard endpoints (postgres auth mode). Must be called before Run.
func (s *Server) SetDashboards(m *dashboard.Manager) {
	s.dashboards = m
	s.srv.Handler = s.Handler()
}

type dashboardFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error

func dashboardViewer(p *auth.Principal) dashboard.Viewer {
	v := dashboard.Viewer{Admin: p.Role.AtLeast(auth.RoleAdmin)}
	if p.Kind == auth.KindSession {
		v.UserID = p.UserID
		v.CanWrite = p.Role.AtLeast(auth.RoleMember)
	}
	return v
}

func (s *Server) dashboardRoutes(mux *http.ServeMux) {
	if s.dashboards == nil {
		return
	}
	route := func(pattern string, h dashboardFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !p.HasOrg() {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"})
				return
			}
			if !p.Role.Can(auth.ActReadTelemetry) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "your role (" + string(p.Role) + ") does not allow this operation"})
				return
			}
			if err := h(rec, r, p, dashboardViewer(p)); err != nil {
				s.writeDashboardError(rec, pattern, p, err)
			}
		}))
	}
	route("GET /api/v1/dashboards", s.listDashboards)
	route("POST /api/v1/dashboards", s.createDashboard)
	route("POST /api/v1/dashboards/import", s.importDashboard)
	route("GET /api/v1/dashboards/{id}", s.getDashboard)
	route("PUT /api/v1/dashboards/{id}", s.updateDashboard)
	route("DELETE /api/v1/dashboards/{id}", s.deleteDashboard)
	route("POST /api/v1/dashboards/{id}/widgets", s.addDashboardWidget)
	route("POST /api/v1/dashboards/{id}/duplicate", s.duplicateDashboard)
	route("GET /api/v1/dashboards/{id}/export", s.exportDashboard)
	s.dashboardSharingRoutes(route) // dashboard_sharing.go: versions, settings, share links, reports (D-086, D-087)
	s.publicDashboardRoutes(mux)    // dashboard_public.go: unauthenticated share link endpoints
}

func (s *Server) writeDashboardError(w http.ResponseWriter, route string, p *auth.Principal, err error) {
	var ve *dashboard.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Msg})
	case errors.Is(err, dashboard.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "dashboard not found"})
	case errors.Is(err, dashboard.ErrConflict):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "the dashboard was changed by someone else; reload and try again"})
	case errors.Is(err, dashboard.ErrForbidden):
		msg := "only the creator of this dashboard or an admin can change it"
		switch {
		case p.Kind != auth.KindSession:
			msg = "this operation requires a signed-in user; API keys are read-only"
		case !p.Role.AtLeast(auth.RoleMember):
			msg = "your role (" + string(p.Role) + ") does not allow this operation"
		}
		writeError(w, &apiError{http.StatusForbidden, "permission_denied", msg})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) auditDashboard(r *http.Request, p *auth.Principal, action string, d *dashboard.Dashboard, details map[string]any) {
	if s.accounts == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["name"] = d.Name
	details["visibility"] = d.Visibility
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), action, "dashboard", d.ID, details)
}

// ---- response shapes ----

type dashboardJSON struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Description     string               `json:"description"`
	Visibility      string               `json:"visibility"`
	Version         int                  `json:"version"`
	Variables       []dashboard.Variable `json:"variables"`
	Pages           []dashboard.Page     `json:"pages"`
	CreatedByUserID *string              `json:"created_by_user_id"`
	CreatedByEmail  string               `json:"created_by_email"`
	CreatedAt       string               `json:"created_at"`
	UpdatedAt       string               `json:"updated_at"`
	CanEdit         bool                 `json:"can_edit"`
}

func dashboardResponse(d *dashboard.Dashboard, v dashboard.Viewer) dashboardJSON {
	out := dashboardJSON{ID: d.ID, Name: d.Name, Description: d.Description, Visibility: d.Visibility, Version: d.Version,
		Variables: d.Variables, Pages: d.Pages, CreatedByUserID: optString(d.CreatedBy), CreatedByEmail: d.CreatedByEmail,
		CreatedAt: formatTime(d.CreatedAt), UpdatedAt: formatTime(d.UpdatedAt), CanEdit: v.CanEdit(d)}
	if out.Variables == nil {
		out.Variables = []dashboard.Variable{}
	}
	if out.Pages == nil {
		out.Pages = []dashboard.Page{}
	}
	return out
}

type dashboardSummaryJSON struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Visibility     string `json:"visibility"`
	PageCount      int    `json:"page_count"`
	WidgetCount    int    `json:"widget_count"`
	CreatedByEmail string `json:"created_by_email"`
	UpdatedAt      string `json:"updated_at"`
	CanEdit        bool   `json:"can_edit"`
}

// ---- handlers ----

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	list, err := s.dashboards.List(r.Context(), p.OrgID, v, r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	out := make([]dashboardSummaryJSON, 0, len(list))
	for _, sm := range list {
		d := &dashboard.Dashboard{Visibility: sm.Visibility, CreatedBy: sm.CreatedBy}
		out = append(out, dashboardSummaryJSON{ID: sm.ID, Name: sm.Name, Description: sm.Description, Visibility: sm.Visibility,
			PageCount: sm.PageCount, WidgetCount: sm.WidgetCount, CreatedByEmail: sm.CreatedByEmail, UpdatedAt: formatTime(sm.UpdatedAt),
			CanEdit: v.CanEdit(d)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": out})
	return nil
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	d, err := s.dashboards.Get(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, dashboardResponse(d, v))
	return nil
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in dashboard.Input
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	d, err := s.dashboards.Create(r.Context(), p.OrgID, in, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.create", d, nil)
	writeJSON(w, http.StatusCreated, dashboardResponse(d, v))
	return nil
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in dashboard.Input
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	d, err := s.dashboards.Update(r.Context(), p.OrgID, r.PathValue("id"), in, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.update", d, map[string]any{"version": d.Version})
	writeJSON(w, http.StatusOK, dashboardResponse(d, v))
	return nil
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	d, err := s.dashboards.Delete(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.delete", d, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) addDashboardWidget(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in struct {
		PageID string            `json:"page_id"`
		Widget *dashboard.Widget `json:"widget"`
	}
	if err := decodeDashboardJSON(r, &in); err != nil {
		return err
	}
	if in.Widget == nil {
		return &dashboard.ValidationError{Msg: "widget is required"}
	}
	d, err := s.dashboards.AddWidget(r.Context(), p.OrgID, r.PathValue("id"), in.PageID, *in.Widget, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.update", d, map[string]any{"version": d.Version, "widget_added": true})
	writeJSON(w, http.StatusOK, dashboardResponse(d, v))
	return nil
}

func (s *Server) duplicateDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var in struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 {
		if err := decodeDashboardJSON(r, &in); err != nil {
			return err
		}
	}
	d, err := s.dashboards.Duplicate(r.Context(), p.OrgID, r.PathValue("id"), in.Name, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.duplicate", d, map[string]any{"source_id": r.PathValue("id")})
	writeJSON(w, http.StatusCreated, dashboardResponse(d, v))
	return nil
}

func (s *Server) exportDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	ex, err := s.dashboards.Export(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ex)
	return nil
}

func (s *Server) importDashboard(w http.ResponseWriter, r *http.Request, p *auth.Principal, v dashboard.Viewer) error {
	if !v.CanWrite {
		return dashboard.ErrForbidden
	}
	var ex dashboard.Export
	if err := decodeDashboardJSON(r, &ex); err != nil {
		return err
	}
	d, err := s.dashboards.Import(r.Context(), p.OrgID, ex, v)
	if err != nil {
		return err
	}
	s.auditDashboard(r, p, "dashboard.import", d, nil)
	writeJSON(w, http.StatusCreated, dashboardResponse(d, v))
	return nil
}

// maxDashboardBody bounds dashboard documents (up to 20 pages of 100 widgets with 8 KiB queries fit comfortably).
const maxDashboardBody = 4 << 20

// decodeDashboardJSON is decodeJSON with the dashboard body limit.
func decodeDashboardJSON(r *http.Request, v any) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		return badRequest("Content-Type must be application/json")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxDashboardBody)).Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}
