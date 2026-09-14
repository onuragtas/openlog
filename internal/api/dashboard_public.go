package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/oql"
)

// Public read-only dashboard share links (docs/contracts/api.md "Dashboards" › "Share links", D-087).
//
// Security model: the unauthenticated endpoints take only the share token from the path. A token selects exactly one
// dashboard of one organization; it is not a credential for any other endpoint (the authenticator never accepts it).
// Widget queries are the stored queries of that dashboard (the client cannot send OQL, variables, filters or time
// ranges): they run with the organization's tenant scope and query limits, the share's time range and its locked
// variable values. Responses carry no organization, user or dashboard identifiers other than widget and page ids,
// no query text and no execution statistics. Requests are rate limited per client IP and per token, and failed token
// lookups per IP. Every response is no-store and carries a restrictive CSP.

type shareLimits struct {
	perIP, perToken, missesPerIP int
	window                       time.Duration
}

var defaultShareLimits = shareLimits{perIP: 600, perToken: 1200, missesPerIP: 30, window: time.Minute}

// shareLimiter is a fixed-window counter per key (process-local: limits apply per api pod).
type shareLimiter struct {
	mu      sync.Mutex
	limits  shareLimits
	windows map[string]*limitWindow
	now     func() time.Time
}

type limitWindow struct {
	start time.Time
	n     int
}

func newShareLimiter(l shareLimits) *shareLimiter {
	return &shareLimiter{limits: l, windows: map[string]*limitWindow{}, now: time.Now}
}

func (l *shareLimiter) window(key string) *limitWindow {
	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= l.limits.window {
		if len(l.windows) >= 50000 {
			for k, x := range l.windows {
				if now.Sub(x.start) >= l.limits.window {
					delete(l.windows, k)
				}
			}
		}
		w = &limitWindow{start: now}
		l.windows[key] = w
	}
	return w
}

// take counts a request and reports whether it is within limit.
func (l *shareLimiter) take(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.window(key)
	w.n++
	return w.n <= limit
}

// exceeded reports whether key already reached limit in the current window.
func (l *shareLimiter) exceeded(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.window(key).n >= limit
}

func publicShareHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

type sharedFunc func(w http.ResponseWriter, r *http.Request, sd *dashboard.SharedDashboard) error

func (s *Server) publicDashboardRoutes(mux *http.ServeMux) {
	if s.publicShares == nil {
		s.publicShares = newShareLimiter(defaultShareLimits)
	}
	for _, rt := range []struct {
		pattern string
		h       sharedFunc
	}{
		{"GET /api/v1/public/dashboards/{token}", s.getSharedDashboard},
		{"GET /api/v1/public/dashboards/{token}/widgets/{widget_id}/result", s.getSharedWidgetResult},
	} {
		mux.Handle(rt.pattern, s.instrument(rt.pattern, s.sharedHandler(rt.pattern, rt.h)))
	}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.accounts != nil {
		return s.accounts.Meta(r).IP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) sharedHandler(route string, h sharedFunc) func(rec *statusRecorder, r *http.Request) {
	tooMany := &apiError{http.StatusTooManyRequests, "resource_exhausted", "too many requests; retry later"}
	return func(rec *statusRecorder, r *http.Request) {
		publicShareHeaders(rec)
		lim := s.publicShares
		ip := s.clientIP(r)
		if !lim.take("ip:"+ip, lim.limits.perIP) || lim.exceeded("miss:"+ip, lim.limits.missesPerIP) {
			writeError(rec, tooMany)
			return
		}
		token := r.PathValue("token")
		sum := sha256.Sum256([]byte(token))
		if !lim.take("token:"+hex.EncodeToString(sum[:12]), lim.limits.perToken) {
			writeError(rec, tooMany)
			return
		}
		sd, err := s.dashboards.OpenShare(r.Context(), token)
		if err != nil {
			if errors.Is(err, dashboard.ErrNotFound) {
				lim.take("miss:"+ip, lim.limits.missesPerIP)
				writeError(rec, &apiError{http.StatusNotFound, "not_found", "this share link does not exist or is no longer valid"})
				return
			}
			s.log.Error("share link lookup failed", "route", route, "err", err)
			writeError(rec, &apiError{http.StatusInternalServerError, "internal", "internal error"})
			return
		}
		if sd.Audit && s.accounts != nil {
			s.accounts.Audit(r.Context(), &auth.Principal{OrgID: sd.Share.OrgID}, s.accounts.Meta(r), "dashboard.share.access", "dashboard",
				sd.Dashboard.ID, map[string]any{"share_id": sd.Share.ID, "label": sd.Share.Label, "name": sd.Dashboard.Name})
		}
		if err := h(rec, r, sd); err != nil {
			ae := s.toAPIError(err)
			if ae.status >= 500 {
				s.log.Error("shared dashboard request failed", "route", route, "tenant_id", sd.TenantID, "err", err)
			}
			writeError(rec, ae)
		}
	}
}

type sharedWidgetJSON struct {
	ID            string                  `json:"id"`
	Title         string                  `json:"title"`
	Visualization string                  `json:"visualization"`
	Layout        dashboard.Layout        `json:"layout"`
	Markdown      string                  `json:"markdown"`
	Unit          string                  `json:"unit"`
	Thresholds    []dashboard.Threshold   `json:"thresholds"`
	Options       dashboard.WidgetOptions `json:"options"`
}

type sharedPageJSON struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Widgets []sharedWidgetJSON `json:"widgets"`
}

type sharedVariableJSON struct {
	Name   string   `json:"name"`
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

type sharedDashboardJSON struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Pages       []sharedPageJSON     `json:"pages"`
	Variables   []sharedVariableJSON `json:"variables"`
	TimeRange   struct {
		Range *string `json:"range"`
		From  *string `json:"from"`
		To    *string `json:"to"`
	} `json:"time_range"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Server) getSharedDashboard(w http.ResponseWriter, _ *http.Request, sd *dashboard.SharedDashboard) error {
	d, sh := sd.Dashboard, sd.Share
	out := sharedDashboardJSON{Name: d.Name, Description: d.Description, Pages: []sharedPageJSON{}, Variables: []sharedVariableJSON{},
		ExpiresAt: formatTime(sh.ExpiresAt)}
	for _, p := range d.Pages {
		sp := sharedPageJSON{ID: p.ID, Name: p.Name, Widgets: []sharedWidgetJSON{}}
		for _, wd := range p.Widgets {
			th := wd.Thresholds
			if th == nil {
				th = []dashboard.Threshold{}
			}
			sp.Widgets = append(sp.Widgets, sharedWidgetJSON{ID: wd.ID, Title: wd.Title, Visualization: wd.Visualization, Layout: wd.Layout,
				Markdown: wd.Markdown, Unit: wd.Unit, Thresholds: th, Options: wd.Options})
		}
		out.Pages = append(out.Pages, sp)
	}
	for _, v := range d.Variables {
		vals := sh.Variables[v.Name]
		if vals == nil {
			vals = []string{}
		}
		out.Variables = append(out.Variables, sharedVariableJSON{Name: v.Name, Label: v.Label, Values: vals})
	}
	if sh.Range != "" {
		out.TimeRange.Range = optString(sh.Range)
	} else {
		out.TimeRange.From, out.TimeRange.To = optTimePtr(&sh.From), optTimePtr(&sh.To)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// shareScope is the only place besides wrap that creates a query scope (TestScopeOnlyCreatedInWrap): its tenant is the
// organization of a share link resolved from the token hash in PostgreSQL (dashboard.Manager.OpenShare), never a value
// of the request.
func (s *Server) shareScope(sd *dashboard.SharedDashboard) (*query.Scope, error) {
	return s.db.Scope(sd.TenantID)
}

func (s *Server) getSharedWidgetResult(w http.ResponseWriter, r *http.Request, sd *dashboard.SharedDashboard) error {
	id := r.PathValue("widget_id")
	var widget *dashboard.Widget
	for pi := range sd.Dashboard.Pages {
		for wi := range sd.Dashboard.Pages[pi].Widgets {
			if wd := &sd.Dashboard.Pages[pi].Widgets[wi]; strings.EqualFold(wd.ID, id) && wd.Visualization != "markdown" {
				widget = wd
			}
		}
	}
	if widget == nil {
		return notFound("widget not found")
	}
	now := s.now()
	from, to := sd.Share.TimeRange(now)
	p, err := oql.Compile(widget.Query, oql.Options{Now: now, From: from, To: to, Variables: sd.Share.Variables,
		MaxLimit: min(oql.MaxTableLimit, max(s.cfg.MaxRows, 1))})
	if err != nil {
		return &apiError{http.StatusUnprocessableEntity, "invalid_argument", "this widget's query cannot be run"}
	}
	sc, err := s.shareScope(sd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.QueryTimeout+2*time.Second)
	defer cancel()
	res, err := oql.Execute(ctx, sc, p)
	if err != nil {
		var oe *oql.Error
		if errors.As(err, &oe) {
			return &apiError{http.StatusUnprocessableEntity, "invalid_argument", "this widget's query cannot be run"}
		}
		return err
	}
	// No execution details: table names, rows/bytes read and warnings (which quote attribute names) stay private.
	res.Metadata.Table, res.Metadata.RowsRead, res.Metadata.BytesRead, res.Metadata.Warnings, res.Metadata.IgnoredFilters = "", 0, 0, []string{}, nil
	writeJSON(w, http.StatusOK, oqlResultJSON{Result: res, Metadata: oqlMetadataJSON{Metadata: res.Metadata,
		From: formatTime(res.Metadata.From), To: formatTime(res.Metadata.To)}})
	return nil
}
