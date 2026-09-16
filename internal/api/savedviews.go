package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/savedview"
)

// Saved explorer views (docs/contracts/api.md "Saved views", D-118). Reads: any role and API keys (private views:
// creator only). Writes: signed-in members and higher; changing another user's org-wide view needs admin or owner.

// SetSavedViews enables the saved view endpoints (postgres auth mode). Must be called before Run.
func (s *Server) SetSavedViews(m *savedview.Manager) {
	s.savedViews = m
	s.srv.Handler = s.Handler()
}

type savedViewFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error

func (s *Server) savedViewRoutes(mux *http.ServeMux) {
	if s.savedViews == nil {
		return
	}
	route := func(pattern string, h savedViewFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if ae := authorize(p, auth.ActReadTelemetry); ae != nil {
				writeError(rec, ae)
				return
			}
			// UserID stays empty for API keys: a key owns nothing, so it edits a view
			// only through the admin rule.
			v := savedview.Viewer{UserID: p.UserID, Admin: allowed(p, auth.ActManageSavedViews),
				CanWrite: allowed(p, auth.ActWriteSavedViews)}
			if err := h(rec, r, p, v); err != nil {
				s.writeSavedViewError(rec, pattern, p, err)
			}
		}))
	}
	route("GET /api/v1/saved-views", s.listSavedViews)
	route("POST /api/v1/saved-views", s.createSavedView)
	route("GET /api/v1/saved-views/{id}", s.getSavedView)
	route("PUT /api/v1/saved-views/{id}", s.updateSavedView)
	route("DELETE /api/v1/saved-views/{id}", s.deleteSavedView)
}

func (s *Server) writeSavedViewError(w http.ResponseWriter, route string, p *auth.Principal, err error) {
	var ve *savedview.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Msg})
	case errors.Is(err, savedview.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "saved view not found"})
	case errors.Is(err, savedview.ErrLimit):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "the organization already has the maximum number of saved views (500); delete some first"})
	case errors.Is(err, savedview.ErrForbidden):
		// The store refused: either the principal may not write at all (the gate says why),
		// or it may write but does not own this view.
		if ae := authorize(p, auth.ActWriteSavedViews); ae != nil {
			writeError(w, ae)
			return
		}
		writeError(w, &apiError{http.StatusForbidden, "permission_denied",
			"only the creator of this saved view or an admin can change it"})
	default:
		s.writeAccountError(w, route, err)
	}
}

type savedViewJSON struct {
	ID              string          `json:"id"`
	Signal          string          `json:"signal"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Visibility      string          `json:"visibility"`
	State           json.RawMessage `json:"state"`
	CreatedByUserID *string         `json:"created_by_user_id"`
	CreatedByEmail  string          `json:"created_by_email"`
	CanEdit         bool            `json:"can_edit"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func toSavedViewJSON(x *savedview.View, v savedview.Viewer) savedViewJSON {
	out := savedViewJSON{ID: x.ID, Signal: x.Signal, Name: x.Name, Description: x.Description, Visibility: x.Visibility,
		State: x.State, CreatedByEmail: x.CreatedByEmail, CanEdit: v.CanEdit(x), CreatedAt: formatTime(x.CreatedAt), UpdatedAt: formatTime(x.UpdatedAt)}
	if x.CreatedBy != "" {
		id := x.CreatedBy
		out.CreatedByUserID = &id
	}
	if len(out.State) == 0 {
		out.State = json.RawMessage("{}")
	}
	return out
}

func (s *Server) auditSavedView(r *http.Request, p *auth.Principal, action string, x *savedview.View) {
	if s.accounts == nil {
		return
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), action, "saved_view", x.ID,
		map[string]any{"name": x.Name, "signal": x.Signal, "visibility": x.Visibility})
}

func (s *Server) listSavedViews(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error {
	views, err := s.savedViews.List(r.Context(), p.OrgID, v, r.URL.Query().Get("signal"))
	if err != nil {
		return err
	}
	out := make([]savedViewJSON, 0, len(views))
	for i := range views {
		out = append(out, toSavedViewJSON(&views[i], v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": out})
	return nil
}

func (s *Server) getSavedView(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error {
	x, err := s.savedViews.Get(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSavedViewJSON(x, v))
	return nil
}

func decodeSavedViewInput(r *http.Request) (savedview.Input, error) {
	var in savedview.Input
	if err := decodeJSON(r, &in); err != nil {
		return in, err
	}
	return in, nil
}

func (s *Server) createSavedView(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error {
	in, err := decodeSavedViewInput(r)
	if err != nil {
		return err
	}
	x, err := s.savedViews.Create(r.Context(), p.OrgID, v, in)
	if err != nil {
		return err
	}
	s.auditSavedView(r, p, "saved_view.create", x)
	writeJSON(w, http.StatusCreated, toSavedViewJSON(x, v))
	return nil
}

func (s *Server) updateSavedView(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error {
	in, err := decodeSavedViewInput(r)
	if err != nil {
		return err
	}
	x, err := s.savedViews.Update(r.Context(), p.OrgID, r.PathValue("id"), v, in)
	if err != nil {
		return err
	}
	s.auditSavedView(r, p, "saved_view.update", x)
	writeJSON(w, http.StatusOK, toSavedViewJSON(x, v))
	return nil
}

func (s *Server) deleteSavedView(w http.ResponseWriter, r *http.Request, p *auth.Principal, v savedview.Viewer) error {
	x, err := s.savedViews.Delete(r.Context(), p.OrgID, r.PathValue("id"), v)
	if err != nil {
		return err
	}
	s.auditSavedView(r, p, "saved_view.delete", x)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
