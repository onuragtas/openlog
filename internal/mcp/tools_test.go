package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func newTestService(t *testing.T, api *fakeAPI) *Service {
	t.Helper()
	return NewService(testConfig(api.srv.URL), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

// callerCreds are the credentials of the caller in these tests; the configured key must not be used
// when the caller has one.
var callerCreds = Creds{Key: "ola_caller"}

// TestToolsAreTenantScoped calls every tool and verifies that each resulting API request carries the
// caller's own key and nothing that could select another tenant: the organization comes from the key
// (and at most the X-Openlog-Org-Id header), never from a path, query parameter or body field.
func TestToolsAreTenantScoped(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{"run_oql", func() error {
			_, err := s.runOQL(ctx, callerCreds, runOQLArgs{Query: "SELECT count(*) FROM Log"})
			return err
		}},
		{"oql_schema", func() error { _, err := s.oqlSchema(ctx, callerCreds, oqlSchemaArgs{EventType: "Log"}); return err }},
		{"list_services", func() error { _, err := s.listServices(ctx, callerCreds, listServicesArgs{Query: "ord"}); return err }},
		{"service_summary", func() error {
			_, err := s.serviceSummary(ctx, callerCreds, serviceSummaryArgs{Service: "orders"})
			return err
		}},
		{"list_incidents", func() error { _, err := s.listIncidents(ctx, callerCreds, listIncidentsArgs{}); return err }},
		{"get_incident", func() error { _, err := s.getIncident(ctx, callerCreds, getIncidentArgs{ID: "inc-1"}); return err }},
		{"search_logs", func() error { _, err := s.searchLogs(ctx, callerCreds, searchLogsArgs{Query: "error"}); return err }},
		{"list_slos", func() error { _, err := s.listSLOs(ctx, callerCreds, listSLOsArgs{}); return err }},
		{"slo_status", func() error { _, err := s.sloStatus(ctx, callerCreds, sloStatusArgs{ID: "slo-1"}); return err }},
	}
	for _, c := range calls {
		if err := c.call(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if len(api.requests) != len(calls)+1 { // service_summary reads overview and transactions
		t.Fatalf("%d requests for %d tools", len(api.requests), len(calls))
	}
	for i, r := range api.requests {
		if got := r.Header.Get("Authorization"); got != "Bearer ola_caller" {
			t.Errorf("request %d (%s): Authorization %q", i, r.URL.Path, got)
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			t.Errorf("request %d: path %s is outside the v1 API", i, r.URL.Path)
		}
		for _, forbidden := range []string{"tenant", "tenant_id", "org", "org_id", "organization"} {
			if r.URL.Query().Has(forbidden) {
				t.Errorf("request %d (%s): query selects a tenant with %q", i, r.URL.Path, forbidden)
			}
		}
		if body := api.bodies[i]; strings.Contains(body, "tenant") {
			t.Errorf("request %d (%s): body names a tenant: %s", i, r.URL.Path, body)
		}
	}
}

// The organization of a multi-organization key travels as the documented header only.
func TestToolsForwardOrgHeader(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	if _, err := s.listSLOs(context.Background(), Creds{Key: "ola_caller", OrgID: "org-3"}, listSLOsArgs{}); err != nil {
		t.Fatalf("list_slos: %v", err)
	}
	if got := api.last().Header.Get("X-Openlog-Org-Id"); got != "org-3" {
		t.Errorf("X-Openlog-Org-Id %q", got)
	}
}

func TestToolArgumentValidation(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
		want string
	}{
		{"run_oql without a query", func() error {
			_, err := s.runOQL(ctx, callerCreds, runOQLArgs{Query: "  "})
			return err
		}, "query is required"},
		{"run_oql with only from", func() error {
			_, err := s.runOQL(ctx, callerCreds, runOQLArgs{Query: "SELECT count(*) FROM Log", From: "2026-09-16T10:00:00Z"})
			return err
		}, "from and to must be given together"},
		{"run_oql with only to", func() error {
			_, err := s.runOQL(ctx, callerCreds, runOQLArgs{Query: "SELECT count(*) FROM Log", To: "2026-09-16T10:00:00Z"})
			return err
		}, "from and to must be given together"},
		{"service_summary without a service", func() error {
			_, err := s.serviceSummary(ctx, callerCreds, serviceSummaryArgs{})
			return err
		}, "service is required"},
		{"get_incident without an id", func() error {
			_, err := s.getIncident(ctx, callerCreds, getIncidentArgs{})
			return err
		}, "id is required"},
		{"slo_status without an id", func() error {
			_, err := s.sloStatus(ctx, callerCreds, sloStatusArgs{ID: " "})
			return err
		}, "id is required"},
		{"search_logs with a bad order", func() error {
			_, err := s.searchLogs(ctx, callerCreds, searchLogsArgs{Order: "descending"})
			return err
		}, "order must be asc or desc"},
		{"search_logs with an incomplete filter", func() error {
			_, err := s.searchLogs(ctx, callerCreds, searchLogsArgs{Filters: []logFilterArg{{Key: "service.name"}}})
			return err
		}, "key and op are required"},
	}
	before := len(api.requests)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
	// Invalid arguments are refused locally, so no query runs against the organization's data.
	if len(api.requests) != before {
		t.Errorf("%d requests sent for invalid arguments", len(api.requests)-before)
	}
}

func TestToolRequests(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		call      func(*Service) error
		wantPath  string
		wantQuery map[string]string
	}{
		{"oql schema", func(s *Service) error {
			_, err := s.oqlSchema(ctx, callerCreds, oqlSchemaArgs{EventType: "Metric"})
			return err
		}, "/api/v1/query/schema", map[string]string{"event_type": "Metric"}},
		{"services", func(s *Service) error {
			_, err := s.listServices(ctx, callerCreds, listServicesArgs{From: "1757757600000", To: "1757761200000", Namespace: "shop", Environment: "prod", Query: "ord"})
			return err
		}, "/api/v1/apm/services", map[string]string{"from": "1757757600000", "to": "1757761200000", "namespace": "shop", "environment": "prod", "q": "ord"}},
		{"incidents", func(s *Service) error {
			_, err := s.listIncidents(ctx, callerCreds, listIncidentsArgs{State: []string{"open", "acknowledged"}, Severity: "critical", RuleID: "r-1", Cursor: "c1", Limit: 5})
			return err
		}, "/api/v1/alerts/incidents", map[string]string{"state": "open,acknowledged", "severity": "critical", "rule_id": "r-1", "cursor": "c1", "limit": "5"}},
		{"incident by id", func(s *Service) error {
			_, err := s.getIncident(ctx, callerCreds, getIncidentArgs{ID: "inc 1/2"})
			return err
		}, "/api/v1/alerts/incidents/inc 1%2F2", nil},
		{"slos without a status", func(s *Service) error {
			no := false
			_, err := s.listSLOs(ctx, callerCreds, listSLOsArgs{Status: &no})
			return err
		}, "/api/v1/slos", map[string]string{"status": "false"}},
		{"slo results", func(s *Service) error {
			_, err := s.sloStatus(ctx, callerCreds, sloStatusArgs{ID: "slo-1", Step: "10m"})
			return err
		}, "/api/v1/slos/slo-1/results", map[string]string{"step": "10m"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI(t)
			s := newTestService(t, api)
			if err := c.call(s); err != nil {
				t.Fatalf("call: %v", err)
			}
			r := api.last()
			if r.URL.EscapedPath() != (&urlPath{c.wantPath}).escaped() {
				t.Errorf("path %q, want %q", r.URL.EscapedPath(), (&urlPath{c.wantPath}).escaped())
			}
			for k, want := range c.wantQuery {
				if got := r.URL.Query().Get(k); got != want {
					t.Errorf("query %s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

// urlPath compares an expected path with the escaping net/url applies.
type urlPath struct{ p string }

func (u *urlPath) escaped() string { return strings.ReplaceAll(u.p, " ", "%20") }

// list_slos without an argument keeps the documented default (the budget is included).
func TestListSLOsDefaultsToStatus(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	if _, err := s.listSLOs(context.Background(), callerCreds, listSLOsArgs{}); err != nil {
		t.Fatalf("list_slos: %v", err)
	}
	if got := api.last().URL.Query().Get("status"); got != "true" {
		t.Errorf("status %q, want true", got)
	}
}

// Every limit is capped by OPENLOG_MCP_MAX_ROWS, whatever the model asks for.
func TestLimitClamping(t *testing.T) {
	api := newFakeAPI(t)
	cfg := testConfig(api.srv.URL)
	cfg.MaxRows = 5
	s := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()

	if _, err := s.listIncidents(ctx, callerCreds, listIncidentsArgs{Limit: 1000}); err != nil {
		t.Fatalf("list_incidents: %v", err)
	}
	if got := api.last().URL.Query().Get("limit"); got != "5" {
		t.Errorf("incident limit %q, want 5", got)
	}
	if _, err := s.searchLogs(ctx, callerCreds, searchLogsArgs{Limit: 900}); err != nil {
		t.Fatalf("search_logs: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(api.bodies[len(api.bodies)-1]), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["limit"] != float64(5) {
		t.Errorf("log limit %v, want 5", body["limit"])
	}
	if _, err := s.serviceSummary(ctx, callerCreds, serviceSummaryArgs{Service: "orders", Limit: 400}); err != nil {
		t.Fatalf("service_summary: %v", err)
	}
	if got := api.last().URL.Query().Get("limit"); got != "5" {
		t.Errorf("transaction limit %q, want 5", got)
	}
}

func TestSearchLogsBody(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	_, err := s.searchLogs(context.Background(), callerCreds, searchLogsArgs{
		From: "2026-09-16T10:00:00Z", To: "2026-09-16T11:00:00Z", Query: "timeout", Order: "asc",
		Columns: []string{"attributes.http.route"},
		Filters: []logFilterArg{
			{Key: "service.name", Op: "=", Value: "checkout"},
			{Key: "http.status_code", Op: ">=", Value: "500"},
			{Key: "severity_text", Op: "in", Values: []string{"ERROR", "FATAL"}},
		},
	})
	if err != nil {
		t.Fatalf("search_logs: %v", err)
	}
	if api.last().URL.Path != "/api/v1/logs/query" {
		t.Errorf("path %s", api.last().URL.Path)
	}
	var body struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Q       string `json:"q"`
		Order   string `json:"order"`
		Columns []string
		Filters []struct {
			Key    string          `json:"key"`
			Op     string          `json:"op"`
			Value  json.RawMessage `json:"value"`
			Values []json.RawMessage
		} `json:"filters"`
	}
	if err := json.Unmarshal([]byte(api.bodies[0]), &body); err != nil {
		t.Fatalf("body %q: %v", api.bodies[0], err)
	}
	if body.From == "" || body.To == "" || body.Q != "timeout" || body.Order != "asc" {
		t.Errorf("body %+v", body)
	}
	if len(body.Filters) != 3 {
		t.Fatalf("%d filters", len(body.Filters))
	}
	// A string value stays a JSON string, a numeric one becomes a JSON number so numeric fields compare.
	if string(body.Filters[0].Value) != `"checkout"` {
		t.Errorf("string value %s", body.Filters[0].Value)
	}
	if string(body.Filters[1].Value) != `500` {
		t.Errorf("numeric value %s", body.Filters[1].Value)
	}
	if len(body.Filters[2].Values) != 2 || string(body.Filters[2].Values[0]) != `"ERROR"` {
		t.Errorf("values %v", body.Filters[2].Values)
	}
}

func TestJSONScalar(t *testing.T) {
	cases := map[string]string{
		"checkout": `"checkout"`, "500": `500`, "1.5": `1.5`, "true": `true`, "false": `false`,
		"": `""`, "5xx": `"5xx"`, `say "hi"`: `"say \"hi\""`,
	}
	for in, want := range cases {
		if got := string(jsonScalar(in)); got != want {
			t.Errorf("jsonScalar(%q) = %s, want %s", in, got, want)
		}
	}
}

// An API error reaches the tool caller with the API's own message, so a model can react to it.
func TestToolErrorMapping(t *testing.T) {
	api := newFakeAPI(t)
	api.status = http.StatusUnprocessableEntity
	api.body = `{"error":{"code":"resource_exhausted","message":"query exceeded the max_rows_to_read limit of this organization"}}`
	s := newTestService(t, api)
	_, err := s.runOQL(context.Background(), callerCreds, runOQLArgs{Query: "SELECT count(*) FROM Log"})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "max_rows_to_read") {
		t.Errorf("error %q", err)
	}
}

func TestRunOQLBody(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	_, err := s.runOQL(context.Background(), callerCreds, runOQLArgs{
		Query: "SELECT count(*) FROM Log WHERE service.name = {{svc}}",
		From:  "2026-09-16T10:00:00Z", To: "2026-09-16T11:00:00Z",
		Variables: map[string][]string{"svc": {"checkout", "orders"}},
	})
	if err != nil {
		t.Fatalf("run_oql: %v", err)
	}
	var body struct {
		Query     string              `json:"query"`
		From      string              `json:"from"`
		To        string              `json:"to"`
		Variables map[string][]string `json:"variables"`
	}
	if err := json.Unmarshal([]byte(api.bodies[0]), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Query == "" || body.From == "" || body.To == "" || len(body.Variables["svc"]) != 2 {
		t.Errorf("body %+v", body)
	}
}

// service_summary joins two reads of the same service into one answer.
func TestServiceSummaryJoinsReads(t *testing.T) {
	api := newFakeAPI(t)
	s := newTestService(t, api)
	raw, err := s.serviceSummary(context.Background(), callerCreds, serviceSummaryArgs{Service: "orders", Transaction: "GET /orders", Environment: "prod"})
	if err != nil {
		t.Fatalf("service_summary: %v", err)
	}
	if len(api.requests) != 2 {
		t.Fatalf("%d requests", len(api.requests))
	}
	if got := api.requests[0].URL.Path; got != "/api/v1/apm/services/orders/overview" {
		t.Errorf("first path %s", got)
	}
	if got := api.requests[0].URL.Query().Get("transaction"); got != "GET /orders" {
		t.Errorf("transaction %q", got)
	}
	// The transaction filter belongs to the overview only; the transaction list must stay unfiltered.
	if api.requests[1].URL.Path != "/api/v1/apm/services/orders/transactions" {
		t.Errorf("second path %s", api.requests[1].URL.Path)
	}
	if api.requests[1].URL.Query().Has("transaction") {
		t.Error("the transaction list is filtered by transaction")
	}
	if got := api.requests[1].URL.Query().Get("environment"); got != "prod" {
		t.Errorf("environment %q", got)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result: %v", err)
	}
	if out["service_name"] != "orders" || out["overview"] == nil || out["transactions"] == nil {
		t.Errorf("result %v", out)
	}
}
