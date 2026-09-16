// Package api implements the query and management API (docs/contracts/api.md).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/savedview"
	"github.com/onuragtas/openlog/internal/slo"
	"github.com/onuragtas/openlog/internal/synthetics"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
)

// Server is the API HTTP server.
type Server struct {
	cfg      config.API
	db       *query.DB
	authn    auth.Authenticator
	accounts *auth.Service // nil in OPENLOG_AUTH_MODE=static
	log      *slog.Logger
	now      func() time.Time
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	ui       http.Handler
	srv      *http.Server
	versions VersionSource   // nil: GET /api/v1/version reports the build only
	updates  updatereq.Queue // nil: no update requests (updates.go; static auth mode)
	checkNow func(ctx context.Context) error
	fleet    *fleet.Manager // nil: no fleet endpoints (static auth mode)
	apm      *apmState      // APM settings (apm.go); nil: default Apdex T
	alerts   *alert.Manager // nil: no alerting endpoints (alerts.go; static auth mode)
	// nil: no integration settings endpoints (intsettings.go; static auth mode)
	intSettings *intsettings.Manager
	dashboards  *dashboard.Manager // nil: no dashboard endpoints (dashboards.go; static auth mode)
	// rate limits of the public dashboard share link endpoints (dashboard_public.go, D-087)
	publicShares *shareLimiter
	// tail sampling policy endpoints (tailsampling.go, D-075)
	tailSampling *tailSamplingState
	// usage, plan and billing endpoints (usage.go, D-079..D-081); nil: none
	usage *UsageDeps
	// single sign-on, domain verification and SCIM endpoints (sso.go, D-077, D-078); nil: none
	sso ssoService
	// public endpoints and flags of GET /api/v1/onboarding ("Add data" page, onboarding.go); nil: derived from requests
	onboarding *OnboardingConfig
	// render token signing key of the internal report render endpoints (dashboard_render.go, D-097); nil: none
	renderKey []byte
	// SaaS operator console, suspension read-only mode and support sessions (operator.go, D-105, D-106); nil: none
	saas *saasState
	// data exports, account and organization deletion (privacy.go, D-107); nil: none
	privacy *PrivacyDeps
	// public status page and its incidents (statuspage.go, D-108); nil: none
	statusPage *StatusPageDeps
	// saved explorer views (savedviews.go, D-118); nil: none
	savedViews *savedview.Manager
	// service level objectives and their error budgets (slos.go, slo.md); nil: none (static auth mode)
	slos slo.Store
	// scheduled outside-in checks (synthetics.go, D-132); nil: none (static auth mode)
	synthetics synthetics.Store
	// verified release catalog of the language agent version comparison (apm_agents.go, D-124); nil: statuses unknown
	agentReleases func() *catalog.Snapshot
}

// SetUI mounts h (the embedded web UI) at "/" for every non-/api path.
// Must be called before Run.
func (s *Server) SetUI(h http.Handler) {
	s.ui = h
	s.srv.Handler = s.Handler()
}

// SetAccounts enables user authentication and the management endpoints
// (OPENLOG_AUTH_MODE=postgres). Must be called before Run.
func (s *Server) SetAccounts(svc *auth.Service) {
	s.accounts = svc
	s.srv.Handler = s.Handler()
}

// New creates the API server. authn authenticates every /api request; reg may be nil.
func New(cfg config.API, db *query.DB, authn auth.Authenticator, log *slog.Logger, reg prometheus.Registerer) *Server {
	s := &Server{
		cfg: cfg, db: db, authn: authn, log: log, now: time.Now,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_api_requests_total", Help: "API requests by route and status.",
		}, []string{"route", "code"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "openlog_api_request_duration_seconds", Help: "API request latency by route.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}, []string{"route"}),
	}
	if reg != nil {
		reg.MustRegister(s.requests, s.latency)
	}
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	return s
}

// handlerFunc is a telemetry handler bound to a tenant scope.
type handlerFunc func(w http.ResponseWriter, r *http.Request, sc *query.Scope) error

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, h handlerFunc) {
		mux.Handle(pattern, s.wrap(pattern, h))
	}
	route("GET /api/v1/hosts", s.listHosts)
	route("GET /api/v1/hosts/{host_id}", s.getHost)
	route("GET /api/v1/metrics/names", s.metricNames)
	route("GET /api/v1/hosts/{host_id}/metrics", s.hostMetrics)
	route("GET /api/v1/hosts/{host_id}/inventory", s.hostInventory)
	route("GET /api/v1/hosts/{host_id}/services", s.hostServices)
	route("GET /api/v1/inventory/search", s.inventorySearch)
	route("GET /api/v1/logs", s.listLogs)
	route("GET /api/v1/traces/{trace_id}", s.getTrace)
	s.explorerRoutes(mux) // fields.go, logsquery.go, metricsexplorer.go, savedviews.go (D-118, D-119)
	s.apmRoutes(mux)
	s.containerRoutes(mux)
	s.kubernetesRoutes(mux) // kubernetes.go
	s.accountRoutes(mux)
	s.versionRoutes(mux)
	s.updateRoutes(mux)
	s.fleetRoutes(mux)
	s.alertRoutes(mux)
	s.integrationSettingsRoutes(mux)
	s.oqlRoutes(mux)          // oql.go
	s.dashboardRoutes(mux)    // dashboards.go
	s.tailSamplingRoutes(mux) // tailsampling.go (D-075)
	s.usageRoutes(mux)        // usage.go (D-079..D-081)
	s.ssoRoutes(mux)          // sso.go: single sign-on, domains, SCIM (D-077, D-078)
	s.onboardingRoutes(mux)   // onboarding.go: "Add data" install command inputs
	s.operatorRoutes(mux)     // operator.go: SaaS operator console, lifecycle, support access (D-105, D-106)
	s.privacyRoutes(mux)      // privacy.go: data exports, account and organization deletion (D-107)
	s.statusPageRoutes(mux)   // statuspage.go: public status page and incidents (D-108)
	s.sloRoutes(mux)          // slos.go: service level objectives, error budgets and burn rates
	s.syntheticsRoutes(mux)   // synthetics.go: scheduled outside-in checks (D-132)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &apiError{http.StatusNotFound, "not_found", "no such endpoint"})
	})
	if s.ui != nil {
		mux.Handle("/", s.ui)
	}
	return version.Middleware(mux)
}

// instrument records metrics and recovers panics for one route.
func (s *Server) instrument(routeLabel string, fn func(w *statusRecorder, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic in handler", "route", routeLabel, "panic", fmt.Sprint(p))
				writeError(rec, &apiError{http.StatusInternalServerError, "internal", "internal error"})
			}
			s.requests.WithLabelValues(routeLabel, strconv.Itoa(rec.code)).Inc()
			s.latency.WithLabelValues(routeLabel).Observe(time.Since(start).Seconds())
		}()
		fn(rec, r)
	})
}

// authenticate runs the authenticator and stores the principal in the
// request context. It writes the error response and returns nil on failure.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Principal, *http.Request) {
	p, err := s.authn.Authenticate(r)
	if err != nil {
		writeError(w, s.toAPIError(err))
		return nil, r
	}
	if s.saas != nil { // support sessions and suspended organizations (operator_access.go, D-105)
		if p, r, err = s.saasGate(r, p); err != nil {
			writeError(w, s.toAPIError(err))
			return nil, r
		}
	}
	return p, r.WithContext(auth.WithPrincipal(r.Context(), p))
}

// wrap authenticates the request, creates the tenant scope and maps errors.
// The tenant comes only from the authenticated principal: no header, query
// parameter or path segment can select it.
func (s *Server) wrap(pattern string, h handlerFunc) http.Handler {
	return s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
		p, r := s.authenticate(rec, r)
		if p == nil {
			return
		}
		if ae := authorize(p, auth.ActReadTelemetry); ae != nil {
			writeError(rec, ae)
			return
		}
		sc, err := s.db.Scope(p.TenantID)
		if err != nil {
			writeError(rec, &apiError{http.StatusInternalServerError, "internal", "internal error"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.QueryTimeout+2*time.Second)
		defer cancel()
		if err := h(rec, r.WithContext(ctx), sc); err != nil {
			ae := s.toAPIError(err)
			if ae.status >= 500 {
				s.log.Error("api request failed", "route", pattern, "tenant_id", p.TenantID, "err", err)
			}
			writeError(rec, ae)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, "invalid_argument", fmt.Sprintf(format, args...)}
}

func notFound(msg string) error { return &apiError{http.StatusNotFound, "not_found", msg} }

// authStatus maps auth error codes to HTTP statuses.
var authStatus = map[auth.Code]int{
	auth.CodeInvalidArgument:    http.StatusBadRequest,
	auth.CodeUnauthenticated:    http.StatusUnauthorized,
	auth.CodePermissionDenied:   http.StatusForbidden,
	auth.CodeNotFound:           http.StatusNotFound,
	auth.CodeAlreadyExists:      http.StatusConflict,
	auth.CodeFailedPrecondition: http.StatusConflict,
	auth.CodeResourceExhausted:  http.StatusTooManyRequests,
	auth.CodeUnavailable:        http.StatusServiceUnavailable,
	auth.CodeQuotaExceeded:      http.StatusForbidden, // plan users limit (SaaS mode, D-105)
}

func (s *Server) toAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var au *auth.Error
	if errors.As(err, &au) {
		status, ok := authStatus[au.Code]
		if !ok {
			return &apiError{http.StatusInternalServerError, "internal", "internal error"}
		}
		msg := au.Message
		if msg == "" {
			msg = strings.ReplaceAll(string(au.Code), "_", " ")
		}
		return &apiError{status, string(au.Code), msg}
	}
	if le, ok := query.AsLimitError(err); ok { // per-tenant limits (D-047)
		if le.Retryable {
			return &apiError{http.StatusTooManyRequests, "resource_exhausted", "too many queries are running for this organization; retry later"}
		}
		return &apiError{http.StatusUnprocessableEntity, "resource_exhausted",
			"query exceeded the " + le.Limit + " limit of this organization; narrow the time range or add filters"}
	}
	if se, ok := query.AsStorageError(err); ok { // cold (S3) parts unreadable (query/storage.go, D-066)
		s.log.Warn("clickhouse storage error", "code", se.Code, "err", err)
		return &apiError{http.StatusServiceUnavailable, "storage_unavailable",
			"part of the requested data is on storage that cannot be read right now; retry later or query a more recent time range"}
	}
	var ex *ch.Exception
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ex) && (ex.Code == 159 || ex.Code == 160)) {
		return &apiError{http.StatusGatewayTimeout, "timeout", "query timed out"}
	}
	if errors.Is(err, query.ErrInvalid) {
		return &apiError{http.StatusInternalServerError, "internal", "internal error"}
	}
	return &apiError{http.StatusInternalServerError, "internal", "internal error"}
}

func writeError(w http.ResponseWriter, e *apiError) {
	if e.status == http.StatusTooManyRequests || e.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
	}
	body := map[string]any{"code": e.code, "message": e.message}
	if e.code == "storage_unavailable" {
		body["retryable"] = true
	}
	writeJSON(w, e.status, map[string]any{"error": body})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, addr string, shutdownTimeout time.Duration) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.log.Info("api listening", "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = s.srv.Shutdown(sctx)
	return err
}

// ---- parameter helpers ----

const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime accepts RFC3339 or unix milliseconds.
func parseTime(v string) (time.Time, error) {
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q (use RFC3339 or unix milliseconds)", v)
	}
	return t.UTC(), nil
}

// timeRange parses from/to with defaults now-1h..now.
func (s *Server) timeRange(r *http.Request) (time.Time, time.Time, error) {
	now := s.now().UTC()
	from, to := now.Add(-time.Hour), now
	var err error
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = parseTime(v); err != nil {
			return from, to, badRequest("from: %v", err)
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = parseTime(v); err != nil {
			return from, to, badRequest("to: %v", err)
		}
	}
	if !from.Before(to) {
		return from, to, badRequest("from must be before to")
	}
	return from, to, nil
}

// limit parses limit with default 100, capped by OPENLOG_API_MAX_ROWS.
func (s *Server) limit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return min(100, s.cfg.MaxRows), nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, badRequest("limit must be a positive integer")
	}
	return min(n, s.cfg.MaxRows), nil
}

// jsonData renders a stored JSON body; non-JSON strings are returned as strings.
func jsonData(s string) json.RawMessage {
	if s != "" && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
