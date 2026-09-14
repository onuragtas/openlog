package api

import (
	"errors"
	"net/http"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
)

// Recommended alert templates (api.md "Alerting", alerting.md §2.8).

func (s *Server) alertTemplateRoutes(mux *http.ServeMux, route func(pattern string, access alertAccess, h alertFunc)) {
	route("GET /api/v1/alerts/templates", alertRead, s.listAlertTemplates)
	// Rendering may read telemetry (ratio thresholds): it runs through wrap like the rule preview.
	mux.Handle("POST /api/v1/alerts/templates/{id}/render", s.wrap("POST /api/v1/alerts/templates/{id}/render", s.renderAlertTemplate))
}

func (s *Server) listAlertTemplates(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"templates": alert.Templates(q.Get("category"), q.Get("integration"))})
	return nil
}

func (s *Server) renderAlertTemplate(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	if p, ok := auth.PrincipalFrom(r.Context()); !ok || alertAllowed(p, alertRead) != nil {
		return &apiError{http.StatusForbidden, "permission_denied", "your role does not allow reading alert rules"}
	}
	var in alert.TemplateRenderInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.alerts.RenderTemplate(r.Context(), sc, r.PathValue("id"), in)
	if err != nil {
		var (
			ve *alert.ValidationError
			pe *alert.PreconditionError
			le *alert.LimitError
		)
		switch {
		case errors.As(err, &ve):
			return &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()}
		case errors.As(err, &le):
			return &apiError{http.StatusBadRequest, "invalid_argument", le.Msg}
		case errors.As(err, &pe):
			return &apiError{http.StatusConflict, "failed_precondition", pe.Msg}
		case errors.Is(err, alert.ErrNotFound):
			return notFound("no such template")
		}
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
