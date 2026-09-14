package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/tenant"
)

// principalAuth authenticates every request as p (nil: 401).
type principalAuth struct{ p *auth.Principal }

func (a principalAuth) Authenticate(*http.Request) (*auth.Principal, error) {
	if a.p == nil {
		return nil, auth.ErrUnauthenticated
	}
	cp := *a.p
	return &cp, nil
}

var oqlNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func postJSON(h http.Handler, path, body string, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOQLQueryEndpoint(t *testing.T) {
	s, conn := newTestServer(t)
	s.now = func() time.Time { return oqlNow }
	h := s.Handler()
	for _, tc := range []struct {
		body, kind string
		series     int
	}{
		{`{"query": "SELECT count(*) FROM Log"}`, "single", 0},
		{`{"query": "SELECT count(*), average(duration.ms) FROM Transaction FACET service.name LIMIT 5"}`, "facets", 0},
		{`{"query": "SELECT count(*), max(value) FROM Metric TIMESERIES 5 minutes SINCE 1 hour ago"}`, "timeseries", 2},
		{`{"query": "SELECT count(*) FROM Log WHERE host.name IN ({{h}}) FACET service.name TIMESERIES", "variables": {"h": ["a", "b"]}, "from": 1789383600000, "to": "2026-09-14T12:00:00Z"}`, "timeseries", 0},
		{`{"query": "SELECT histogram(duration.ms, 100, 10) FROM Span"}`, "histogram", 0},
		{`{"query": "SELECT count(*) FROM Host COMPARE WITH 1 day ago", "variables": {"x": "y"}}`, "single", 0},
		{`{"query": "SELECT count(*) FROM Container FACET state"}`, "facets", 0},
	} {
		rec := postJSON(h, "/api/v1/query", tc.body, "key-a")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
		var res struct {
			Kind    string
			Rows    []json.RawMessage
			Series  []struct{ Points [][2]any }
			Buckets []json.RawMessage
			Compare *struct {
				OffsetSeconds int64 `json:"offset_seconds"`
			}
			Metadata struct {
				From          string
				BucketSeconds *int64 `json:"bucket_seconds"`
				Table         string
				Queries       int
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.Kind != tc.kind || len(res.Series) != tc.series || res.Metadata.From == "" || res.Rows == nil || res.Buckets == nil {
			t.Errorf("%s: %s", tc.body, rec.Body)
		}
		if tc.kind == "timeseries" && tc.series > 0 && (len(res.Series[0].Points) != 12 || *res.Metadata.BucketSeconds != 300 || res.Series[0].Points[0][1] != float64(0)) {
			t.Errorf("%s: timeseries points %s", tc.body, rec.Body)
		}
		if tc.kind == "histogram" && len(res.Buckets) != 10 {
			t.Errorf("histogram buckets %s", rec.Body)
		}
		if strings.Contains(tc.body, "COMPARE") && (res.Compare == nil || res.Compare.OffsetSeconds != 86400 || res.Metadata.Queries != 2) {
			t.Errorf("compare %s", rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("missing no-store")
		}
	}
	if len(conn.sql) < 7 {
		t.Fatalf("only %d statements", len(conn.sql))
	}
	for _, sql := range conn.sql {
		refs := tableRef.FindAllStringIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
		}
		for _, r := range refs {
			if !strings.HasPrefix(strings.TrimPrefix(sql[r[1]:], " FINAL"), " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
		if strings.ContainsAny(sql, "'\";") {
			t.Errorf("literal in SQL: %s", sql)
		}
	}
}

func TestOQLQueryErrors(t *testing.T) {
	s, conn := newTestServer(t)
	s.now = func() time.Time { return oqlNow }
	h := s.Handler()
	for _, tc := range []struct {
		body, msg string
		status    int
	}{
		{`{"query": ""}`, "query is required", 400},
		{`{"query": "SELECT count(*) FROM Logz"}`, "line 1, column 22: unknown event type", 400},
		{`{"query": "SELECT count(*) FROM Log\nWHERE tenant_id = 'x'"}`, "line 2, column 7: unknown attribute", 400},
		{`{"query": "SELECT count(*) FROM Log", "from": "2026-09-14T10:00:00Z"}`, "together", 400},
		{`{"query": "SELECT count(*) FROM Log", "from": "x", "to": "y"}`, "from:", 400},
		{`{"query": "SELECT count(*) FROM Log", "from": 1789387200000, "to": 1789383600000}`, "before", 400},
		{`{"query": "SELECT count(*) FROM Log", "from": 1, "to": 1789383600000}`, "400 days", 400},
		{`{"query": "SELECT count(*) FROM Log", "variables": {"h": 5}}`, "variables.h", 400},
		{`{"query": "SELECT count(*) FROM Log WHERE severity.number > {{n}}", "variables": {"n": ["1", "2"]}}`, "several values", 400},
		{`{"query": "SELECT count(*) FROM Log SINCE 90 days ago"}`, "at most 31 days", 400},
		{`{"query": "SELECT count(*) FROM Log FACET host.name LIMIT 1001"}`, "at most 1000", 400}, // capped by OPENLOG_API_MAX_ROWS
		{`not json`, "", 400},
	} {
		rec := postJSON(h, "/api/v1/query", tc.body, "key-a")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
	}
	if len(conn.sql) != 0 {
		t.Errorf("statements executed for invalid queries: %v", conn.sql)
	}
	if rec := postJSON(h, "/api/v1/query", `{"query": "SELECT count(*) FROM Log"}`, ""); rec.Code != 401 || len(conn.sql) != 0 {
		t.Errorf("unauthenticated: %d", rec.Code)
	}
}

func TestOQLRolesAndLimits(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.API{QueryTimeout: time.Second, MaxRows: 1000}
	body := `{"query": "SELECT count(*) FROM Log"}`
	for _, tc := range []struct {
		p      *auth.Principal
		status int
	}{
		{&auth.Principal{Kind: auth.KindSession, UserID: "u", OrgID: "o", TenantID: "t", Role: auth.RoleViewer}, 200},
		{&auth.Principal{Kind: auth.KindSession, UserID: "u", OrgID: "o", TenantID: "t", Role: auth.RoleMember}, 200},
		{&auth.Principal{Kind: auth.KindAPIKey, UserID: "u", OrgID: "o", TenantID: "t", Role: auth.RoleViewer}, 200},
		{&auth.Principal{Kind: auth.KindSession, UserID: "u"}, 403},
		{&auth.Principal{Kind: auth.KindSession, UserID: "u", OrgID: "o", TenantID: "t", Role: "unknown"}, 403},
	} {
		conn := &recordingConn{}
		s := New(cfg, query.New(conn, "openlog", time.Second), principalAuth{tc.p}, logger, nil)
		for _, path := range []string{"/api/v1/query", "/api/v1/query/validate"} {
			if rec := postJSON(s.Handler(), path, body, ""); rec.Code != tc.status {
				t.Errorf("%+v %s: %d %s", tc.p, path, rec.Code, rec.Body)
			}
		}
		if tc.status == 200 && (len(conn.sql) != 1 || !strings.Contains(conn.sql[0], "tenant_id = {tenant_id:String}")) {
			t.Errorf("statements %v", conn.sql)
		}
	}
	// Organization limits and timeouts map like every telemetry endpoint.
	res, _ := tenant.ParseStatic("key-a=tenant-a")
	for code, status := range map[int32]int{158: 422, 241: 422, 202: 429, 159: 504} {
		s := New(cfg, query.New(failingConn{err: &ch.Exception{Code: code, Message: "limit"}}, "openlog", time.Second),
			auth.StaticAuthenticator{Resolver: res}, logger, nil)
		if rec := postJSON(s.Handler(), "/api/v1/query", body, "key-a"); rec.Code != status {
			t.Errorf("code %d: %d %s", code, rec.Code, rec.Body)
		}
	}
}

func TestOQLValidateAndSchema(t *testing.T) {
	s, conn := newTestServer(t)
	s.now = func() time.Time { return oqlNow }
	h := s.Handler()
	rec := postJSON(h, "/api/v1/query/validate", `{"query": "SELECT count(*) FROM Log WHERE sevrity.number > 3 AND x = {{v}} FACET"}`, "key-a")
	var v struct {
		Valid  bool
		Errors []struct {
			Message              string
			Offset, Line, Column int
		}
		Variables []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if v.Valid || len(v.Errors) != 1 || v.Errors[0].Column != 70 || !strings.Contains(v.Errors[0].Message, "expected an attribute") {
		t.Errorf("validate %s", rec.Body)
	}
	rec = postJSON(h, "/api/v1/query/validate", `{"query": "SELECT count(*) FROM Log WHERE http.route = {{v}}"}`, "key-a")
	if !strings.Contains(rec.Body.String(), `"valid":true`) || !strings.Contains(rec.Body.String(), `"variables":["v"]`) || !strings.Contains(rec.Body.String(), `attributes['http.route']`) {
		t.Errorf("validate ok %s", rec.Body)
	}

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer key-a")
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		return r
	}
	rec = get("/api/v1/query/schema")
	var sch struct {
		EventTypes []struct {
			Name       string
			Attributes []struct{ Name string }
		} `json:"event_types"`
		Functions []struct{ Name string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sch); err != nil || rec.Code != 200 || len(sch.EventTypes) != 6 || len(sch.Functions) < 10 {
		t.Fatalf("schema %d %s", rec.Code, rec.Body)
	}
	if len(conn.sql) != 0 {
		t.Errorf("schema without event_type queried ClickHouse")
	}
	for et, statements := range map[string]int{"Log": 2, "Metric": 3, "Host": 1, "Container": 1} {
		conn.sql = nil
		if rec := get("/api/v1/query/schema?event_type=" + et); rec.Code != 200 {
			t.Errorf("%s: %d %s", et, rec.Code, rec.Body)
		}
		if len(conn.sql) != statements {
			t.Errorf("%s: %d statements %v", et, len(conn.sql), conn.sql)
		}
		for _, sql := range conn.sql {
			if !strings.Contains(sql, "WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped %s", sql)
			}
		}
	}
	if rec := get("/api/v1/query/schema?event_type=logs"); rec.Code != 400 {
		t.Errorf("bad event type: %d", rec.Code)
	}
}
