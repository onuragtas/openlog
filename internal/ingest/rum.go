package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/rum"
)

// Real user monitoring ingest (docs/contracts/rum.md §3, D-136).
//
// This is a separate endpoint from /v1/traces, and that separation is the whole security design. An ingest
// license key is a secret that authenticates every signal; a browser key is public and authenticates only
// this path, whose payloads are rewritten to what the key is allowed to say (rum.Sanitize) before anything
// reaches Kafka. A browser key presented to /v1/traces is simply an unknown license key, and a license key
// presented here is an unknown browser key: the two namespaces never meet.
//
// Everything downstream is unchanged — the sanitized request is produced to the ordinary traces topic, so
// the processor, the trace model and the APM error inbox handle browser spans with no RUM-specific code.

// RUM paths. Kept as constants because cors.go must recognize them to stand aside.
const (
	rumPath       = "/v1/rum"
	rumConfigPath = "/v1/rum/config"
)

func isRUMPath(p string) bool { return p == rumPath || p == rumConfigPath }

// RUMKeys resolves browser keys and enforces their rate limit (internal/rum.Keys).
type RUMKeys interface {
	Resolve(ctx context.Context, value string, sc rum.Scope) (rum.Key, error)
	Allow(key rum.Key, n int, now time.Time) (bool, time.Duration)
	Rejected(reason string, n int)
}

// SetRUM enables POST /v1/rum. Must be called before Run.
func (s *Service) SetRUM(k RUMKeys) {
	s.rum = k
	s.httpSrv.Handler = s.HTTPHandler()
}

// rumKeyHeader is the dedicated header. The key may also arrive as the query parameter "k", because
// navigator.sendBeacon — the only send that survives a page being closed — cannot set request headers.
// Putting a credential in a URL is normally wrong; here the value is public by construction, so the cost is
// that it appears in access logs rather than that it leaks. Operators are told so in rum.md §3.1.
const (
	rumKeyHeader = "openlog-browser-key"
	rumKeyParam  = "k"
	// rumAppIDHeader carries a mobile key's application identifier. A mobile application has no Origin —
	// it is a binary on a device, not a page on a site — so it declares its own package name or bundle id.
	// That declaration is **self-reported and unforgeable by nobody**: it narrows casual reuse of a key
	// lifted from one app's build, and is not a defence against a program (rum.md §3.6).
	rumAppIDHeader = "openlog-app-id"
)

// rumMaxBodyBytes bounds a RUM request independently of OPENLOG_INGEST_MAX_BODY_BYTES: the agent limit (10
// MiB) is sized for a fleet's batched export, and nothing a browser legitimately sends comes close to this.
const rumMaxBodyBytes = 512 << 10

func (s *Service) rumRoutes(mux *http.ServeMux) {
	mux.Handle("POST "+rumPath, http.HandlerFunc(s.rumExport))
	mux.Handle("OPTIONS "+rumPath, http.HandlerFunc(s.rumPreflight))
	mux.Handle("GET "+rumConfigPath, http.HandlerFunc(s.rumConfig))
	mux.Handle("OPTIONS "+rumConfigPath, http.HandlerFunc(s.rumPreflight))
}

// rumPreflight answers the CORS preflight. It echoes the requested origin without looking at the key,
// because a preflight authorizes nothing by itself: the POST that follows resolves the key and checks the
// origin against that key's allowlist server-side. Answering permissively here keeps a wrong answer from
// looking like a network failure in the browser console, where the real reason (401/403 with a JSON body)
// would otherwise be invisible.
func (s *Service) rumPreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if s.rum == nil || origin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h := w.Header()
	h.Add("Vary", "Origin")
	h.Add("Vary", "Access-Control-Request-Headers")
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
		h.Set("Access-Control-Allow-Headers", req)
	} else {
		h.Set("Access-Control-Allow-Headers", "Content-Type, "+rumKeyHeader+", "+rumAppIDHeader)
	}
	h.Set("Access-Control-Max-Age", "7200")
	w.WriteHeader(http.StatusNoContent)
}

// rumCORS sets the response headers of an accepted cross-origin request. It is called only after the key and
// its origin allowlist have been checked, so the echoed origin is one this key is configured for.
func rumCORS(w http.ResponseWriter, origin string) {
	if origin == "" {
		return
	}
	h := w.Header()
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Expose-Headers", "Retry-After")
}

// rumError writes a JSON error. The body is deliberately plain JSON rather than a google.rpc.Status: the
// consumer is a browser SDK, not an OTLP exporter, and it should be able to log something readable.
func (s *Service) rumError(w http.ResponseWriter, origin string, code int, reason, msg string) {
	rumCORS(w, origin)
	s.m.requests.WithLabelValues("rum", "http", strconv.Itoa(code)).Inc()
	w.Header().Set("Content-Type", ctJSON)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": reason, "message": msg}})
}

// rumKeyFrom extracts the browser key from the header or the query parameter.
func rumKeyFrom(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get(rumKeyHeader)); k != "" {
		return k
	}
	return strings.TrimSpace(r.URL.Query().Get(rumKeyParam))
}

// rumResolve authenticates the request and answers the error itself when it fails. The second result says
// whether the caller may continue.
func (s *Service) rumResolve(w http.ResponseWriter, r *http.Request) (rum.Key, string, bool) {
	origin := r.Header.Get("Origin")
	key, err := s.rum.Resolve(r.Context(), rumKeyFrom(r), rum.Scope{
		Origin: origin, AppID: strings.TrimSpace(r.Header.Get(rumAppIDHeader)),
	})
	switch {
	case err == nil:
		return key, origin, true
	case errors.Is(err, rum.ErrOriginNotAllowed):
		// Named explicitly: the overwhelmingly common cause is a real operator who forgot to add a domain,
		// and a generic 401 would send them looking at the key instead of the allowlist.
		s.rumError(w, origin, http.StatusForbidden, "origin_not_allowed",
			"this browser key is not allowed for origin "+origin+"; add it to the key's origin list")
	case errors.Is(err, rum.ErrAppNotAllowed):
		// Distinct from the origin message for the same reason that one exists: the likely cause is an
		// operator who shipped a new application without adding it to the key, not an attack.
		s.rumError(w, origin, http.StatusForbidden, "app_not_allowed",
			"this mobile key is not allowed for this application; add its id to the key's application list")
	case errors.Is(err, rum.ErrUnavailable):
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
		s.rumError(w, origin, http.StatusServiceUnavailable, "unavailable", "authentication temporarily unavailable, retry later")
	default:
		s.rumError(w, origin, http.StatusUnauthorized, "unauthenticated", "invalid or missing browser key")
	}
	return rum.Key{}, origin, false
}

// rumConfig serves the SDK's runtime configuration. The sample rate lives on the key so it can be changed
// without redeploying the web application; the SDK falls back to its compiled-in options when this call
// fails, so a page never depends on it to start reporting.
func (s *Service) rumConfig(w http.ResponseWriter, r *http.Request) {
	if s.rum == nil {
		http.NotFound(w, r)
		return
	}
	key, origin, ok := s.rumResolve(w, r)
	if !ok {
		return
	}
	rumCORS(w, origin)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", ctJSON)
	s.m.requests.WithLabelValues("rum", "http", "200").Inc()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service_name": key.ServiceName,
		"environment":  key.Environment,
		"sample_rate":  key.SampleRate,
	})
}

// rumExport accepts one batch of RUM events as OTLP/JSON.
//
// Content types: application/json is what fetch() sends. text/plain is accepted because a Blob posted by
// navigator.sendBeacon can only use a CORS-simple content type — application/json would make the beacon
// preflight, and a preflight cannot be answered while the page is unloading, so the last batch of every
// visit would be lost. The body is parsed as OTLP/JSON either way; the content type is a transport detail,
// never a trust signal.
func (s *Service) rumExport(w http.ResponseWriter, r *http.Request) {
	if s.rum == nil {
		http.NotFound(w, r)
		return
	}
	key, origin, ok := s.rumResolve(w, r)
	if !ok {
		return
	}
	if s.gate != nil { // SaaS organization state (gate.go, D-105)
		s.gate.ObserveSource(key.TenantID, r.RemoteAddr)
		if _, suspended := s.gate.Suspended(key.TenantID); suspended {
			s.rumError(w, origin, http.StatusForbidden, ReasonOrgSuspended, suspendedMessage)
			return
		}
	}

	body, err := s.readBodyLimit(r, rumBodyLimit(s.cfg.MaxBodyBytes))
	if err != nil {
		if errors.Is(err, errTooLarge) {
			s.rumError(w, origin, http.StatusRequestEntityTooLarge, "too_large",
				"request body exceeds "+strconv.Itoa(rumMaxBodyBytes)+" bytes")
			return
		}
		s.rumError(w, origin, http.StatusBadRequest, "invalid_argument", "cannot read body: "+err.Error())
		return
	}

	req := &coltrace.ExportTraceServiceRequest{}
	if err := unmarshalOTLPJSON(body, req); err != nil {
		s.rumError(w, origin, http.StatusBadRequest, "invalid_argument", "cannot decode request: "+err.Error())
		return
	}

	// Validate and rewrite before anything else looks at the content (rum.Sanitize).
	res := rum.Sanitize(req, key)
	for reason, n := range res.Dropped {
		s.rum.Rejected(reason, n)
	}
	// Malformed span ids are the same class of problem here as on /v1/traces; reuse the same filter so a
	// browser cannot write a row the trace view then cannot open.
	badIDs := filterSpans(req)
	s.rum.Rejected("invalid_span_id", int(badIDs))

	kept := countSpans(req)
	if kept > 0 {
		if allowed, wait := s.rum.Allow(key, kept, s.now()); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
			s.rumError(w, origin, http.StatusTooManyRequests, "rate_limited",
				"this browser key is over its rate limit; retry later")
			return
		}
	}

	if kept > 0 {
		p := prepared{key: otlputil.TracesPartitionKey(key.TenantID, req), msg: req, changed: true}
		if err := s.export(r.Context(), queue.SignalTraces, key.TenantID, p, nil); err != nil {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
			s.rumError(w, origin, http.StatusServiceUnavailable, "unavailable", "backend temporarily unavailable, retry later")
			return
		}
	}

	rumCORS(w, origin)
	w.Header().Set("Content-Type", ctJSON)
	s.m.requests.WithLabelValues("rum", "http", "200").Inc()
	out := map[string]any{"accepted": kept}
	if dropped := res.DroppedTotal() + int(badIDs); dropped > 0 {
		out["rejected"] = dropped
		if msg := res.Message(); msg != "" {
			out["message"] = msg
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// countSpans is the number of spans left after sanitizing.

// rumBodyLimit is the smaller of the RUM limit and the operator's global one, so lowering
// OPENLOG_INGEST_MAX_BODY_BYTES still tightens this endpoint.
func rumBodyLimit(configured int64) int64 {
	if configured > 0 && configured < rumMaxBodyBytes {
		return configured
	}
	return rumMaxBodyBytes
}

func countSpans(req *coltrace.ExportTraceServiceRequest) int {
	n := 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			n += len(ss.GetSpans())
		}
	}
	return n
}
