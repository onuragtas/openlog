package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/renderer"
)

// Report print view endpoints for openlog-renderer (docs/operations/reports.md, D-097).
//
// Security model: the caller is headless Chromium in openlog-renderer, loading the web app's /print/dashboard route.
// It authenticates with a render token ("Authorization: Bearer olrt_…"), never with a session or API key, and the
// authenticator of every other endpoint does not accept render tokens. A token is HMAC-SHA256-signed by the api
// leader with a key derived from OPENLOG_KEY_HASH_SECRET or OPENLOG_SECRETS_KEY (never given to the renderer), lives
// at most 10 minutes and names one organization, its tenant, one dashboard, one enabled scheduled report and the
// report period. The widget queries are the stored queries of that dashboard, with the report's variables and period
// and the organization's tenant scope and query limits; the request carries nothing else. Responses are the same
// redacted documents as share links (no query text, no execution statistics) with the same restrictive headers.

// SetRenderKey enables the render endpoints with the render token key (renderer.KeyFromSecret). Must be called
// before Run.
func (s *Server) SetRenderKey(key []byte) {
	s.renderKey = key
	s.srv.Handler = s.Handler()
}

func (s *Server) renderDashboardRoutes(mux *http.ServeMux) {
	if len(s.renderKey) == 0 {
		return
	}
	for _, rt := range []struct {
		pattern string
		h       sharedFunc
	}{
		{"GET /api/v1/render/dashboard", s.getSharedDashboard},
		{"GET /api/v1/render/dashboard/widgets/{widget_id}/result", s.getSharedWidgetResult},
	} {
		mux.Handle(rt.pattern, s.instrument(rt.pattern, s.renderHandler(rt.pattern, rt.h)))
	}
}

func (s *Server) renderHandler(route string, h sharedFunc) func(rec *statusRecorder, r *http.Request) {
	return func(rec *statusRecorder, r *http.Request) {
		publicShareHeaders(rec)
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(rec, &apiError{http.StatusUnauthorized, "unauthenticated", "a render token is required"})
			return
		}
		claims, err := renderer.VerifyToken(s.renderKey, strings.TrimSpace(token), s.now())
		if err != nil {
			writeError(rec, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid or expired render token"})
			return
		}
		d, rep, err := s.dashboards.OpenReportRender(r.Context(), claims.OrgID, claims.DashboardID, claims.ReportID)
		if err != nil {
			if errors.Is(err, dashboard.ErrNotFound) {
				writeError(rec, &apiError{http.StatusNotFound, "not_found", "the report or dashboard no longer exists"})
				return
			}
			s.log.Error("report render lookup failed", "route", route, "err", err)
			writeError(rec, &apiError{http.StatusInternalServerError, "internal", "internal error"})
			return
		}
		// A read-only view shaped like a share link: fixed report period, the report's variables, the token's tenant.
		sd := &dashboard.SharedDashboard{Dashboard: d, TenantID: claims.TenantID, Share: &dashboard.Share{
			OrgID: claims.OrgID, DashboardID: d.ID, From: time.UnixMilli(claims.From).UTC(), To: time.UnixMilli(claims.To).UTC(),
			Variables: rep.Variables, ExpiresAt: time.Unix(claims.Expires, 0).UTC(),
		}}
		if err := h(rec, r, sd); err != nil {
			ae := s.toAPIError(err)
			if ae.status >= 500 {
				s.log.Error("report render request failed", "route", route, "tenant_id", sd.TenantID, "err", err)
			}
			writeError(rec, ae)
		}
	}
}
