package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The client is tested against an httptest server, never a live openlog: what matters here is that the
// documented contract is spoken exactly — the Bearer key and the organization header on every request, the
// error shape decoded with its field path, and the statuses the provider branches on.

// newTestClient starts h as the API and returns a client pointed at it.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "ola_test", OrgID: "org-1", HTTPClient: srv.Client(), UserAgent: "tf-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNewValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no endpoint", Config{APIKey: "ola_x"}, "endpoint is required"},
		{"no key", Config{Endpoint: "https://openlog.example.com"}, "api_key is required"},
		{"not a URL", Config{Endpoint: "openlog.example.com", APIKey: "ola_x"}, "must be an http(s) URL"},
		{"wrong scheme", Config{Endpoint: "ftp://openlog.example.com", APIKey: "ola_x"}, "must be an http(s) URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New(%v) = %v, want an error containing %q", tt.cfg, err, tt.want)
			}
		})
	}
}

// A base URL that already carries /api/v1 is the common misconfiguration; the client trims it so requests do
// not end up at /api/v1/api/v1/....
func TestNewTrimsAPIPath(t *testing.T) {
	c, err := New(Config{Endpoint: "https://openlog.example.com/api/v1/", APIKey: "ola_x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.Endpoint(), "https://openlog.example.com"; got != want {
		t.Fatalf("Endpoint() = %q, want %q", got, want)
	}
}

func TestRequestCarriesAuthAndOrg(t *testing.T) {
	var got *http.Request
	var body []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","name":"x"}`))
	})
	if _, err := c.CreateAlertRule(context.Background(), AlertRule{Name: "x", Type: "log_match"}); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/v1/alerts/rules" {
		t.Errorf("request = %s %s, want POST /api/v1/alerts/rules", got.Method, got.URL.Path)
	}
	for header, want := range map[string]string{
		"Authorization":    "Bearer ola_test",
		"X-Openlog-Org-Id": "org-1",
		"Content-Type":     "application/json",
		"Accept":           "application/json",
		"User-Agent":       "tf-test",
	} {
		if got := got.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	// The write must not carry read-only fields back to the API.
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	for _, field := range []string{"id", "created_at", "updated_at", "created_by_email"} {
		if _, ok := sent[field]; ok {
			t.Errorf("create body carries read-only field %q", field)
		}
	}
}

// Without an organization the header is omitted entirely: the API then uses the key's own organization.
func TestRequestWithoutOrgOmitsHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["X-Openlog-Org-Id"]; ok {
			t.Errorf("X-Openlog-Org-Id was sent although no organization is configured")
		}
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: srv.URL, APIKey: "ola_x", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetSLO(context.Background(), "s1"); err != nil {
		t.Fatalf("GetSLO: %v", err)
	}
}

func TestErrorShape(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantCode  string
		wantField string
		wantMsg   string
	}{
		{
			name:      "validation error carries the field path",
			status:    http.StatusBadRequest,
			body:      `{"error":{"code":"invalid_argument","message":"condition.threshold: required"}}`,
			wantCode:  "invalid_argument",
			wantField: "condition.threshold",
			wantMsg:   "condition.threshold: required",
		},
		{
			name:      "indexed field path",
			status:    http.StatusBadRequest,
			body:      `{"error":{"code":"invalid_argument","message":"config.to[0]: invalid e-mail address"}}`,
			wantCode:  "invalid_argument",
			wantField: "config.to[0]",
			wantMsg:   "config.to[0]: invalid e-mail address",
		},
		{
			name:     "message without a field path",
			status:   http.StatusConflict,
			body:     `{"error":{"code":"failed_precondition","message":"conflicting change; reload and try again"}}`,
			wantCode: "failed_precondition",
			wantMsg:  "conflicting change; reload and try again",
		},
		{
			name:     "body that is not the error shape",
			status:   http.StatusBadGateway,
			body:     `<html>gateway</html>`,
			wantCode: "internal",
			wantMsg:  "Bad Gateway",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := c.GetAlertRule(context.Background(), "r1")
			e, ok := AsError(err)
			if !ok {
				t.Fatalf("GetAlertRule error = %v, want *Error", err)
			}
			if e.Status != tt.status || e.Code != tt.wantCode || e.Field != tt.wantField || e.Message != tt.wantMsg {
				t.Errorf("got status=%d code=%q field=%q message=%q, want status=%d code=%q field=%q message=%q",
					e.Status, e.Code, e.Field, e.Message, tt.status, tt.wantCode, tt.wantField, tt.wantMsg)
			}
			if !strings.Contains(e.Error(), "GET /api/v1/alerts/rules/r1") {
				t.Errorf("Error() = %q, want it to name the request", e.Error())
			}
		})
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		status                                 int
		notFound, conflict, invalid, forbidden bool
	}{
		{http.StatusNotFound, true, false, false, false},
		{http.StatusConflict, false, true, false, false},
		{http.StatusBadRequest, false, false, true, false},
		{http.StatusForbidden, false, false, false, true},
		{http.StatusInternalServerError, false, false, false, false},
	}
	for _, tt := range tests {
		e := &Error{Status: tt.status}
		if e.NotFound() != tt.notFound || e.Conflict() != tt.conflict || e.Invalid() != tt.invalid || e.Forbidden() != tt.forbidden {
			t.Errorf("status %d: NotFound=%v Conflict=%v Invalid=%v Forbidden=%v", tt.status,
				e.NotFound(), e.Conflict(), e.Invalid(), e.Forbidden())
		}
	}
}

// An update must carry the version that was read: it is how the API refuses a write against a stale copy
// (alerting.md §4) instead of overwriting a concurrent change.
func TestUpdateAlertRuleSendsVersion(t *testing.T) {
	var sent map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		_, _ = w.Write([]byte(`{"id":"r1","name":"x","version":8}`))
	})
	out, err := c.UpdateAlertRule(context.Background(), "r1", AlertRule{Name: "x", Type: "log_match"}, 7)
	if err != nil {
		t.Fatalf("UpdateAlertRule: %v", err)
	}
	if got := sent["version"]; got != float64(7) {
		t.Errorf("body version = %v, want 7", got)
	}
	if out.Version != 8 {
		t.Errorf("returned version = %d, want 8 (the API's answer, not the request)", out.Version)
	}
}

func TestUpdateDashboardSendsVersion(t *testing.T) {
	var sent map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		_, _ = w.Write([]byte(`{"id":"d1","name":"x","version":4}`))
	})
	if _, err := c.UpdateDashboard(context.Background(), "d1", Dashboard{Name: "x"}, 3); err != nil {
		t.Fatalf("UpdateDashboard: %v", err)
	}
	if got := sent["version"]; got != float64(3) {
		t.Errorf("body version = %v, want 3", got)
	}
}

// service_namespace and environment must survive as three distinct states: absent, "" and a value.
func TestSLONamespaceIsThreeValued(t *testing.T) {
	empty := ""
	prod := "prod"
	tests := []struct {
		name string
		in   *string
		want string
	}{
		{"aggregated", nil, `null`},
		{"the empty namespace", &empty, `""`},
		{"a namespace", &prod, `"prod"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				_, _ = w.Write([]byte(`{"id":"s1"}`))
			})
			if _, err := c.CreateSLO(context.Background(), SLO{Name: "x", ServiceNamespace: tt.in}); err != nil {
				t.Fatalf("CreateSLO: %v", err)
			}
			if !strings.Contains(string(body), `"service_namespace":`+tt.want) {
				t.Errorf("body = %s, want service_namespace %s", body, tt.want)
			}
		})
	}
}

// An availability SLO must not send latency_threshold_ms at all: the API rejects it for that SLI.
func TestSLOOmitsUnsetLatencyThreshold(t *testing.T) {
	var body []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	})
	if _, err := c.CreateSLO(context.Background(), SLO{Name: "x", SLIType: "availability"}); err != nil {
		t.Fatalf("CreateSLO: %v", err)
	}
	if strings.Contains(string(body), "latency_threshold_ms") {
		t.Errorf("body = %s, want no latency_threshold_ms", body)
	}
}

func TestDeleteExpectsNoContent(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteSLO(context.Background(), "s1"); err != nil {
		t.Fatalf("DeleteSLO: %v", err)
	}
}

// An id must not be able to escape its path segment.
func TestIDIsEscaped(t *testing.T) {
	var path string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":"x"}`))
	})
	_, _ = c.GetChannel(context.Background(), "../../secrets")
	if strings.Contains(path, "..") && !strings.Contains(path, "%2F") {
		t.Errorf("path = %q, want the id escaped into one segment", path)
	}
}

func TestFindChannelByName(t *testing.T) {
	list := `{"channels":[{"id":"c1","name":"ops"},{"id":"c2","name":"dev"},{"id":"c3","name":"dev"}]}`
	tests := []struct {
		name, lookup, wantID, wantErr string
	}{
		{name: "found", lookup: "ops", wantID: "c1"},
		{name: "missing", lookup: "nope", wantErr: `no notification channel named "nope"`},
		{name: "ambiguous", lookup: "dev", wantErr: `2 notification channels of this organization are named "dev"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(list))
			})
			ch, err := c.FindChannelByName(context.Background(), tt.lookup)
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("FindChannelByName: %v", err)
			case ch.ID != tt.wantID:
				t.Fatalf("id = %q, want %q", ch.ID, tt.wantID)
			}
		})
	}
}

// Channel secrets are write-only: the client sends them and reads back only the masked hints.
func TestChannelSecretsAreWriteOnly(t *testing.T) {
	var body []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"c1","name":"ops","type":"webhook",
			"secret_hints":{"url":"https://hooks…/•••f3a9","hmac_secret":"•••••• (set)"},
			"generated_secrets":{"hmac_secret":"deadbeef"}}`))
	})
	out, err := c.CreateChannel(context.Background(), Channel{
		Name: "ops", Type: "webhook", Secrets: map[string]string{"url": "https://hooks.example/x"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if !strings.Contains(string(body), `"secrets":{"url":"https://hooks.example/x"}`) {
		t.Errorf("body = %s, want the secrets sent", body)
	}
	if out.SecretHints["url"] == "" || out.GeneratedSecrets["hmac_secret"] != "deadbeef" {
		t.Errorf("hints = %v, generated = %v; want the masked hints and the generated secret", out.SecretHints, out.GeneratedSecrets)
	}
}
