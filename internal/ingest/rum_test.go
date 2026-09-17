package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/otlputil"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/rum"
)

// POST /v1/rum (docs/contracts/rum.md §3). These cover the boundary that makes a public key safe: who is
// admitted, from where, how often, and what survives of what they sent.

// fakeRUMKeys is an in-memory rum.Keys: one known key, a settable rate-limit decision and a drop tally.
type fakeRUMKeys struct {
	key     rum.Key
	value   string
	allow   bool
	wait    time.Duration
	mu      sync.Mutex
	dropped map[string]int
}

func newFakeRUMKeys() *fakeRUMKeys {
	return &fakeRUMKeys{
		value: "olb_good",
		key: rum.Key{KeyID: "k1", TenantID: "tenant-a", ServiceName: "shop-web", Environment: "production",
			Origins: []string{"https://shop.example.com"}, RateLimitPerMinute: 6000, SampleRate: 1},
		allow:   true,
		dropped: map[string]int{},
	}
}

func (f *fakeRUMKeys) Resolve(_ context.Context, value, origin string) (rum.Key, error) {
	if value != f.value {
		return rum.Key{}, rum.ErrUnknownKey
	}
	if !rum.OriginAllowed(f.key.Origins, origin) {
		return rum.Key{}, rum.ErrOriginNotAllowed
	}
	return f.key, nil
}

func (f *fakeRUMKeys) Allow(_ rum.Key, _ int, _ time.Time) (bool, time.Duration) {
	return f.allow, f.wait
}

func (f *fakeRUMKeys) Rejected(reason string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped[reason] += n
}

func rumService(t *testing.T) (*Service, *fakeProducer, *fakeRUMKeys) {
	t.Helper()
	p := &fakeProducer{}
	s := newTestService(t, p, 10<<20)
	keys := newFakeRUMKeys()
	s.SetRUM(keys)
	return s, p, keys
}

// rumBody is one page view in OTLP/JSON, as the SDK sends it.
const rumBody = `{"resourceSpans":[{"resource":{"attributes":[
  {"key":"service.name","value":{"stringValue":"i-am-not-shop-web"}},
  {"key":"host.id","value":{"stringValue":"prod-db-1"}}
]},"scopeSpans":[{"spans":[{
  "traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7",
  "name":"pageview /orders/42","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000",
  "attributes":[
    {"key":"openlog.rum.event","value":{"stringValue":"page_view"}},
    {"key":"session.id","value":{"stringValue":"0123456789abcdef0123456789abcdef"}},
    {"key":"url.path","value":{"stringValue":"/orders/42"}}
  ]}]}]}]}`

func rumPost(h http.Handler, body, key, origin, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/rum", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if key != "" {
		req.Header.Set("openlog-browser-key", key)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRUMAcceptsAndForcesIdentity(t *testing.T) {
	s, p, _ := rumService(t)
	w := rumPost(s.HTTPHandler(), rumBody, "olb_good", "https://shop.example.com", ctJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://shop.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Accepted != 1 {
		t.Fatalf("body %s (%v)", w.Body.String(), err)
	}

	if len(p.msgs) != 1 {
		t.Fatalf("produced %d records, want 1", len(p.msgs))
	}
	msg := p.msgs[0]
	if msg.TenantID != "tenant-a" {
		t.Errorf("tenant = %q, want the key's tenant-a", msg.TenantID)
	}
	// RUM rides the ordinary traces topic: no RUM-specific processor path.
	if want := queue.Topic("openlog", queue.SignalTraces); msg.Topic != want {
		t.Errorf("topic = %q, want %q", msg.Topic, want)
	}
	var req coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(msg.Value, &req); err != nil {
		t.Fatal(err)
	}
	res := otlputil.AttrsToMap(req.GetResourceSpans()[0].GetResource().GetAttributes())
	if res["service.name"] != "shop-web" {
		t.Errorf("service.name = %q; the payload's own name must not win", res["service.name"])
	}
	if _, ok := res["host.id"]; ok {
		t.Error("host.id from a browser payload reached Kafka")
	}
}

func TestRUMRejectsUnknownKey(t *testing.T) {
	s, p, _ := rumService(t)
	w := rumPost(s.HTTPHandler(), rumBody, "olb_wrong", "https://shop.example.com", ctJSON)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(p.msgs) != 0 {
		t.Error("data was produced for an unknown key")
	}
}

func TestRUMRejectsMissingKey(t *testing.T) {
	s, _, _ := rumService(t)
	if w := rumPost(s.HTTPHandler(), rumBody, "", "https://shop.example.com", ctJSON); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRUMEnforcesOriginAllowlist(t *testing.T) {
	s, p, _ := rumService(t)
	w := rumPost(s.HTTPHandler(), rumBody, "olb_good", "https://evil.example.com", ctJSON)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// The reason is named: the usual cause is an operator who forgot a domain, and a bare 401 would send
	// them looking at the key instead of the allowlist.
	if !strings.Contains(w.Body.String(), "origin_not_allowed") {
		t.Errorf("body %q does not name the reason", w.Body.String())
	}
	if len(p.msgs) != 0 {
		t.Error("data was produced from a disallowed origin")
	}
}

func TestRUMRequiresAnOrigin(t *testing.T) {
	// A browser always sends Origin on a cross-origin POST; a request without one is not the traffic this
	// endpoint exists for.
	s, _, _ := rumService(t)
	if w := rumPost(s.HTTPHandler(), rumBody, "olb_good", "", ctJSON); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestRUMRateLimited(t *testing.T) {
	s, p, keys := rumService(t)
	keys.allow, keys.wait = false, 30*time.Second
	w := rumPost(s.HTTPHandler(), rumBody, "olb_good", "https://shop.example.com", ctJSON)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if len(p.msgs) != 0 {
		t.Error("data was produced past the rate limit")
	}
}

func TestRUMAcceptsBeaconContentType(t *testing.T) {
	// navigator.sendBeacon can only use a CORS-simple content type, and the key travels in the query
	// string because a beacon cannot set headers. Losing this loses the last batch of every visit.
	s, p, _ := rumService(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/rum?k=olb_good", strings.NewReader(rumBody))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", "https://shop.example.com")
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if len(p.msgs) != 1 {
		t.Fatalf("produced %d records, want 1", len(p.msgs))
	}
}

func TestRUMDropsNonRUMSpans(t *testing.T) {
	s, p, keys := rumService(t)
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{
      "traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","name":"SELECT users"}]}]}]}`
	w := rumPost(s.HTTPHandler(), body, "olb_good", "https://shop.example.com", ctJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// Nothing survived, so nothing is produced: the endpoint is not a general-purpose trace writer.
	if len(p.msgs) != 0 {
		t.Error("a non-RUM span reached Kafka")
	}
	if !strings.Contains(w.Body.String(), "not_rum_event") {
		t.Errorf("body %q does not report the rejection", w.Body.String())
	}
	keys.mu.Lock()
	defer keys.mu.Unlock()
	if keys.dropped[rum.ReasonNotRUM] != 1 {
		t.Errorf("dropped = %v, want one not_rum_event", keys.dropped)
	}
}

func TestRUMPreflight(t *testing.T) {
	s, _, _ := rumService(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/rum", nil)
	req.Header.Set("Origin", "https://shop.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,openlog-browser-key")
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	h := w.Header()
	if h.Get("Access-Control-Allow-Origin") != "https://shop.example.com" ||
		h.Get("Access-Control-Allow-Headers") != "content-type,openlog-browser-key" {
		t.Errorf("headers = %v", h)
	}
	// No credentials: authentication is by key, never by cookie.
	if h.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("the RUM endpoint must not allow credentials")
	}
}

func TestRUMConfigServesTheKeysSampleRate(t *testing.T) {
	s, _, keys := rumService(t)
	keys.key.SampleRate = 0.25
	req := httptest.NewRequest(http.MethodGet, "/v1/rum/config", nil)
	req.Header.Set("openlog-browser-key", "olb_good")
	req.Header.Set("Origin", "https://shop.example.com")
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out struct {
		ServiceName string  `json:"service_name"`
		SampleRate  float64 `json:"sample_rate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ServiceName != "shop-web" || out.SampleRate != 0.25 {
		t.Errorf("config = %+v", out)
	}
}

// TestRUMDisabled: without SetRUM the paths do not exist at all, so an installation that never creates a
// browser key exposes no public ingest path to probe.
func TestRUMDisabled(t *testing.T) {
	s := newTestService(t, &fakeProducer{}, 10<<20)
	if w := rumPost(s.HTTPHandler(), rumBody, "olb_good", "https://shop.example.com", ctJSON); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestRUMBodyLimit: the RUM bound is much smaller than the agent body limit.
func TestRUMBodyLimit(t *testing.T) {
	if got := rumBodyLimit(10 << 20); got != rumMaxBodyBytes {
		t.Errorf("limit = %d, want the RUM bound %d", got, rumMaxBodyBytes)
	}
	// A tighter operator setting still wins.
	if got := rumBodyLimit(1024); got != 1024 {
		t.Errorf("limit = %d, want the configured 1024", got)
	}
}
