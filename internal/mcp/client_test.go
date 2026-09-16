package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAPI records the requests it receives and answers with a fixed status and body.
type fakeAPI struct {
	srv      *httptest.Server
	requests []*http.Request
	bodies   []string
	status   int
	body     string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: http.StatusOK, body: `{"ok": true}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.bodies = append(f.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) last() *http.Request {
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func testConfig(apiURL string) Config {
	return Config{Transport: TransportStdio, APIURL: apiURL, APIKey: "ola_configured", Timeout: 5 * time.Second, MaxRows: defaultMaxRows}
}

func TestClientSendsCredentials(t *testing.T) {
	api := newFakeAPI(t)
	c := NewClient(testConfig(api.srv.URL))
	if _, err := c.Get(context.Background(), Creds{Key: "ola_caller", OrgID: "org-7"}, "/api/v1/slos", url.Values{"status": {"true"}}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	r := api.last()
	if got := r.Header.Get("Authorization"); got != "Bearer ola_caller" {
		t.Errorf("Authorization %q", got)
	}
	if got := r.Header.Get("X-Openlog-Org-Id"); got != "org-7" {
		t.Errorf("X-Openlog-Org-Id %q", got)
	}
	if r.URL.Path != "/api/v1/slos" || r.URL.Query().Get("status") != "true" {
		t.Errorf("url %s", r.URL)
	}
}

// Creds falls back to the configured key only when the caller has none of its own.
func TestClientCredsFallback(t *testing.T) {
	c := NewClient(Config{APIURL: "http://x", APIKey: "ola_configured", OrgID: "org-default"})
	if cr := c.Creds("", ""); cr.Key != "ola_configured" || cr.OrgID != "org-default" {
		t.Errorf("fallback: %+v", cr)
	}
	if cr := c.Creds("ola_caller", "org-caller"); cr.Key != "ola_caller" || cr.OrgID != "org-caller" {
		t.Errorf("caller credentials: %+v", cr)
	}
	// A caller's key never inherits the configured organization silently... it uses the configured one
	// only when the caller named none.
	if cr := c.Creds("ola_caller", ""); cr.Key != "ola_caller" || cr.OrgID != "org-default" {
		t.Errorf("caller key with the default org: %+v", cr)
	}
	empty := NewClient(Config{APIURL: "http://x"})
	if cr := empty.Creds("", ""); cr.Key != "" {
		t.Errorf("no key configured: %+v", cr)
	}
}

// Without a key nothing is sent: an unauthenticated request must never reach the API.
func TestClientRefusesWithoutKey(t *testing.T) {
	api := newFakeAPI(t)
	c := NewClient(Config{APIURL: api.srv.URL, Timeout: time.Second})
	_, err := c.Get(context.Background(), c.Creds("", ""), "/api/v1/slos", nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("error %v", err)
	}
	if len(api.requests) != 0 {
		t.Errorf("%d requests sent without a key", len(api.requests))
	}
}

func TestClientPostSendsJSON(t *testing.T) {
	api := newFakeAPI(t)
	c := NewClient(testConfig(api.srv.URL))
	if _, err := c.Post(context.Background(), Creds{Key: "k"}, "/api/v1/query", map[string]any{"query": "SELECT count(*) FROM Log"}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got := api.last().Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q", got)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(api.bodies[0]), &sent); err != nil {
		t.Fatalf("body %q: %v", api.bodies[0], err)
	}
	if sent["query"] != "SELECT count(*) FROM Log" {
		t.Errorf("body %v", sent)
	}
}

func TestClientErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
		wantMsg  string
	}{
		{"unauthenticated", 401, `{"error":{"code":"unauthenticated","message":"invalid or missing credentials"}}`,
			"unauthenticated", "check OPENLOG_MCP_API_KEY"},
		{"permission denied", 403, `{"error":{"code":"permission_denied","message":"your role does not allow reading telemetry"}}`,
			"permission_denied", "check the role of the API key"},
		{"query limit", 422, `{"error":{"code":"resource_exhausted","message":"query exceeded the max_rows_to_read limit"}}`,
			"resource_exhausted", "max_rows_to_read"},
		{"timeout", 504, `{"error":{"code":"timeout","message":"query timed out"}}`, "timeout", "query timed out"},
		{"not found", 404, `{"error":{"code":"not_found","message":"slo not found"}}`, "not_found", "slo not found"},
		{"body without the error shape", 500, `oops`, "internal", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.status, api.body = c.status, c.body
			client := NewClient(testConfig(api.srv.URL))
			_, err := client.Get(context.Background(), Creds{Key: "k"}, "/api/v1/slos", nil)
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("error %v is not an APIError", err)
			}
			if ae.Status != c.status || ae.Code != c.wantCode {
				t.Errorf("status %d, code %q", ae.Status, ae.Code)
			}
			if c.wantMsg != "" && !strings.Contains(ae.Error(), c.wantMsg) {
				t.Errorf("message %q does not contain %q", ae.Error(), c.wantMsg)
			}
		})
	}
}

// A 200 that is not JSON usually means the URL points at something other than the API.
func TestClientRejectsNonJSON(t *testing.T) {
	api := newFakeAPI(t)
	api.body = "<html>the web UI</html>"
	c := NewClient(testConfig(api.srv.URL))
	_, err := c.Get(context.Background(), Creds{Key: "k"}, "/api/v1/slos", nil)
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("error %v", err)
	}
}

func TestClientPing(t *testing.T) {
	api := newFakeAPI(t)
	c := NewClient(testConfig(api.srv.URL))
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if api.last().URL.Path != "/api/v1/auth/config" {
		t.Errorf("ping path %s", api.last().URL.Path)
	}
	api.status = http.StatusServiceUnavailable
	if err := c.Ping(context.Background()); err == nil {
		t.Error("no error for 503")
	}
}
