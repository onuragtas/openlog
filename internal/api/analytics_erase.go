package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/onuragtas/openlog/internal/analytics"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/rum"
)

// Deleting one person's analytics data (analytics.md §7). A client cannot be trusted with a delete on a public key, so
// this takes an admin, and an API key is accepted because the caller is usually an application's backend answering a
// data subject request. The identifier never reaches the audit log: a record of an erasure must not undo it.

// SetAnalyticsErasures enables the erasure endpoints. Must be called before Handler.
func (s *Server) SetAnalyticsErasures(store analytics.Store) { s.erasures = store }

var erasureVisitorRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Server) analyticsEraseRoutes(mux *http.ServeMux) {
	if s.accounts == nil || s.erasures == nil {
		return
	}
	authed := func(pattern string, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if ae := authorize(p, auth.ActEraseAnalytics); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
	authed("POST /api/v1/analytics/erase", s.createAnalyticsErasure)
	authed("GET /api/v1/analytics/erase/{id}", s.getAnalyticsErasure)
}

func erasureResponse(e analytics.Erasure) map[string]any {
	status := "pending"
	if e.CompletedAt != nil {
		status = "submitted"
	}
	return map[string]any{
		"id": e.ID, "app": e.App, "status": status,
		"requested_at": formatTime(e.RequestedAt), "completed_at": optTime(e.CompletedAt),
		"note": "ClickHouse applies deletions in the background, shortly after they are submitted. " +
			"The identifier on raw spans expires with the 7-day trace retention.",
	}
}

func (s *Server) createAnalyticsErasure(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		App       string `json:"app"`
		VisitorID string `json:"visitor_id"`
		UserID    string `json:"user_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.App = strings.TrimSpace(in.App)
	in.UserID = strings.TrimSpace(in.UserID)
	if in.App == "" {
		return badRequest("app is required")
	}
	if (in.VisitorID == "") == (in.UserID == "") {
		return badRequest("exactly one of visitor_id and user_id is required")
	}
	if in.VisitorID != "" && !erasureVisitorRe.MatchString(in.VisitorID) {
		return badRequest("visitor_id must be 32 lower-case hex characters")
	}
	if len(in.UserID) > rum.MaxUserIDBytes {
		return badRequest("user_id must be at most %d bytes", rum.MaxUserIDBytes)
	}
	e, err := s.erasures.EnqueueErasure(r.Context(), analytics.Erasure{
		OrgID: p.OrgID, TenantID: p.TenantID, App: in.App, Actor: in.VisitorID + in.UserID,
	})
	if err != nil {
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "analytics.erase", "analytics_erasure", e.ID, map[string]any{"app": e.App})
	writeJSON(w, http.StatusAccepted, erasureResponse(e))
	return nil
}

func (s *Server) getAnalyticsErasure(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	e, err := s.erasures.GetErasure(r.Context(), p.OrgID, r.PathValue("id"))
	if errors.Is(err, analytics.ErrNotFound) {
		return &apiError{http.StatusNotFound, "not_found", "analytics erasure not found"}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, erasureResponse(e))
	return nil
}
