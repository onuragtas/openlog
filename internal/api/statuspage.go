package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/statuspage"
)

// Public status page (docs/contracts/api.md "Status page", D-108).
//
//	GET    /api/v1/status                                      public, cacheable (no tenant data)
//	GET    /api/v1/admin/status/incidents?limit=               incidents and maintenance (superadmin)
//	POST   /api/v1/admin/status/incidents                      create
//	PATCH  /api/v1/admin/status/incidents/{id}                 edit
//	POST   /api/v1/admin/status/incidents/{id}/updates         {"status", "message"}: timeline entry
//	DELETE /api/v1/admin/status/incidents/{id}

// StatusPageAPI builds the public page (statuspage.Service).
type StatusPageAPI interface {
	Page(ctx context.Context) (statuspage.Page, error)
	Invalidate()
}

// StatusIncidentStore manages incidents (statuspage.PGStore).
type StatusIncidentStore interface {
	List(ctx context.Context, limit int) ([]statuspage.Incident, error)
	Get(ctx context.Context, id string) (statuspage.Incident, error)
	Create(ctx context.Context, in statuspage.Incident, message, actorUserID string, now time.Time) (statuspage.Incident, error)
	Update(ctx context.Context, in statuspage.Incident, now time.Time) (statuspage.Incident, error)
	AddUpdate(ctx context.Context, id, status, message string, now time.Time) (statuspage.Incident, error)
	Delete(ctx context.Context, id string) error
}

// StatusPageDeps enables the status page endpoints (OPENLOG_STATUS_PAGE_ENABLED).
type StatusPageDeps struct {
	Service StatusPageAPI
	Store   StatusIncidentStore
}

// SetStatusPage enables the status page endpoints. Must be called before Run.
func (s *Server) SetStatusPage(d StatusPageDeps) {
	s.statusPage = &d
	s.srv.Handler = s.Handler()
}

func (s *Server) statusPageRoutes(mux *http.ServeMux) {
	if s.statusPage == nil {
		return
	}
	mux.Handle("GET /api/v1/status", s.instrument("GET /api/v1/status", func(rec *statusRecorder, r *http.Request) {
		page, err := s.statusPage.Service.Page(r.Context())
		if err != nil {
			noStore(rec)
			s.log.Error("status page failed", "err", err)
			writeError(rec, &apiError{http.StatusServiceUnavailable, "unavailable", "status is temporarily unavailable"})
			return
		}
		// Identical for every viewer: shared caches and other origins (status widgets) may use it.
		rec.Header().Set("Cache-Control", "public, max-age=30")
		rec.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(rec, http.StatusOK, page)
	}))
	if s.accounts == nil || s.statusPage.Store == nil {
		return
	}
	super := func(pattern string, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !s.isSuperadmin(p) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "only openlog operators (OPENLOG_SUPERADMIN_EMAILS) can manage the status page"})
				return
			}
			if err := h(rec, r, p); err != nil {
				var inv *statuspage.InvalidError
				switch {
				case errors.As(err, &inv):
					err = badRequest("%s", inv.Msg)
				case errors.Is(err, statuspage.ErrNotFound):
					err = notFound("incident not found")
				}
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
	super("GET /api/v1/admin/status/incidents", s.listStatusIncidents)
	super("POST /api/v1/admin/status/incidents", s.createStatusIncident)
	super("PATCH /api/v1/admin/status/incidents/{id}", s.updateStatusIncident)
	super("POST /api/v1/admin/status/incidents/{id}/updates", s.addStatusIncidentUpdate)
	super("DELETE /api/v1/admin/status/incidents/{id}", s.deleteStatusIncident)
}

type incidentInput struct {
	Kind       *string   `json:"kind"`
	Title      *string   `json:"title"`
	Status     *string   `json:"status"`
	Impact     *string   `json:"impact"`
	Components *[]string `json:"components"`
	StartsAt   *string   `json:"starts_at"`
	EndsAt     *string   `json:"ends_at"` // "" clears
	Message    string    `json:"message"`
}

func (in incidentInput) apply(dst *statuspage.Incident) error {
	if in.Title != nil {
		dst.Title = *in.Title
	}
	if in.Status != nil {
		dst.Status = *in.Status
	}
	if in.Impact != nil {
		dst.Impact = *in.Impact
	}
	if in.Components != nil {
		dst.Components = *in.Components
	}
	if in.StartsAt != nil {
		t, err := parseTime(*in.StartsAt)
		if err != nil {
			return badRequest("starts_at: %v", err)
		}
		dst.StartsAt = t
	}
	if in.EndsAt != nil {
		if *in.EndsAt == "" {
			dst.EndsAt = nil
		} else {
			t, err := parseTime(*in.EndsAt)
			if err != nil {
				return badRequest("ends_at: %v", err)
			}
			dst.EndsAt = &t
		}
	}
	return dst.Validate()
}

func (s *Server) statusAudit(r *http.Request, p *auth.Principal, action string, in statuspage.Incident) {
	s.installationAudit(r.Context(), r, p.UserID, p.Email, action, "status_incident", in.ID, map[string]any{"kind": in.Kind, "title": in.Title, "status": in.Status})
	s.statusPage.Service.Invalidate()
}

func (s *Server) listStatusIncidents(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	limit, err := queryLimit(r, 100, 500)
	if err != nil {
		return err
	}
	list, err := s.statusPage.Store.List(r.Context(), limit)
	if err != nil {
		return err
	}
	if list == nil {
		list = []statuspage.Incident{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": list, "components": statuspage.Components})
	return nil
}

func (s *Server) createStatusIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in incidentInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	inc := statuspage.Incident{StartsAt: s.now().UTC(), Components: []string{}}
	if in.Kind != nil {
		inc.Kind = *in.Kind
	}
	if err := in.apply(&inc); err != nil {
		return err
	}
	if in.Message != "" {
		if err := statuspage.ValidMessage(in.Message); err != nil {
			return err
		}
	}
	created, err := s.statusPage.Store.Create(r.Context(), inc, in.Message, p.UserID, s.now())
	if err != nil {
		return err
	}
	s.statusAudit(r, p, "status.incident_create", created)
	writeJSON(w, http.StatusCreated, map[string]any{"incident": created})
	return nil
}

func (s *Server) updateStatusIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in incidentInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Kind != nil {
		return badRequest("kind cannot be changed")
	}
	cur, err := s.statusPage.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := in.apply(&cur); err != nil {
		return err
	}
	updated, err := s.statusPage.Store.Update(r.Context(), cur, s.now())
	if err != nil {
		return err
	}
	s.statusAudit(r, p, "status.incident_update", updated)
	writeJSON(w, http.StatusOK, map[string]any{"incident": updated})
	return nil
}

func (s *Server) addStatusIncidentUpdate(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	updated, err := s.statusPage.Store.AddUpdate(r.Context(), r.PathValue("id"), in.Status, in.Message, s.now())
	if err != nil {
		return err
	}
	s.statusAudit(r, p, "status.incident_update", updated)
	writeJSON(w, http.StatusOK, map[string]any{"incident": updated})
	return nil
}

func (s *Server) deleteStatusIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id := r.PathValue("id")
	cur, err := s.statusPage.Store.Get(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.statusPage.Store.Delete(r.Context(), id); err != nil {
		return err
	}
	s.statusAudit(r, p, "status.incident_delete", cur)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
