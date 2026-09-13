package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/tenant"
)

// recordingConn captures every SQL statement and returns empty result sets.
type recordingConn struct {
	driver.Conn
	mu  sync.Mutex
	sql []string
	// hostKnown makes statements on the hosts table return one row (host "h1").
	hostKnown bool
}

func (c *recordingConn) Query(_ context.Context, sql string, _ ...any) (driver.Rows, error) {
	c.mu.Lock()
	c.sql = append(c.sql, sql)
	known := c.hostKnown
	c.mu.Unlock()
	if known {
		for _, m := range tableRef.FindAllStringSubmatch(sql, -1) {
			if m[1] == "hosts" {
				return &hostRow{}, nil
			}
		}
	}
	return emptyRows{}, nil
}

// hostRow is a one-row result whose first column is the host id "h1".
type hostRow struct {
	driver.Rows
	done bool
}

func (r *hostRow) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}

func (r *hostRow) Scan(dest ...any) error {
	if p, ok := dest[0].(*string); ok {
		*p = "h1"
	}
	return nil
}

func (*hostRow) Close() error { return nil }
func (*hostRow) Err() error   { return nil }

type emptyRows struct{ driver.Rows }

func (emptyRows) Next() bool   { return false }
func (emptyRows) Close() error { return nil }
func (emptyRows) Err() error   { return nil }

func newTestServer(t *testing.T) (*Server, *recordingConn) {
	t.Helper()
	conn := &recordingConn{}
	res, _ := tenant.ParseStatic("key-a=tenant-a")
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), auth.StaticAuthenticator{Resolver: res},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return s, conn
}

var tableRef = regexp.MustCompile("`openlog`\\.(\\w+)( FINAL)?")

// TestEveryEndpointIsTenantScoped issues a request to every endpoint and
// verifies that every table reference in every executed statement is
// immediately followed by the bound tenant predicate.
func TestEveryEndpointIsTenantScoped(t *testing.T) {
	s, conn := newTestServer(t)
	conn.hostKnown = true // host endpoints run their data queries after the existence check
	h := s.Handler()
	paths := []string{
		"/api/v1/logs?attr.log.file.path=/var/log/nginx/access.log&attr.openlog.discovery.id=nginx",
		"/api/v1/hosts",
		"/api/v1/hosts/h1",
		"/api/v1/metrics/names?host_id=h1",
		"/api/v1/hosts/h1/metrics?name=system.cpu.utilization",
		"/api/v1/hosts/h1/metrics?name=postgresql.table.size&agg=last&resource.openlog.discovery.id=postgresql&resource.openlog.discovery.instance=%2Fusr%2Fbin%2Fpostgres&group_by=resource.postgresql.table.name",
		"/api/v1/hosts/h1/inventory?category=package",
		"/api/v1/hosts/h1/services",
		"/api/v1/inventory/search?category=package&q=ssl",
		"/api/v1/logs?host_id=h1&service=api&q=err&severity_min=ERROR&trace_id=5b8efff798038103d269b633813fc60c",
		"/api/v1/traces/5b8efff798038103d269b633813fc60c",
		// APM (apm.go)
		"/api/v1/apm/services?environment=prod&q=ord",
		"/api/v1/apm/services/orders?namespace=shop&environment=prod",
		"/api/v1/apm/services/orders/overview?transaction=GET%20%2Forders%2F%7Bid%7D&type=web",
		"/api/v1/apm/services/orders/transactions?sort=slowest",
		"/api/v1/apm/services/orders/transaction?name=GET%20%2Forders%2F%7Bid%7D",
		"/api/v1/apm/services/orders/errors",
		"/api/v1/apm/services/orders/errors/00000000000000ff",
		"/api/v1/apm/services/orders/databases?sort=calls&db_system=postgresql",
		"/api/v1/apm/services/orders/hosts",
		"/api/v1/apm/services/orders/settings",
		"/api/v1/apm/hosts/h1/services",
		"/api/v1/apm/map?service=orders&environment=prod",
		"/api/v1/apm/traces?service=orders&transaction=x&min_duration_ms=10&max_duration_ms=99&error=true&attr.http.route=%2Fx&sort=duration",
		"/api/v1/apm/traces",
		"/api/v1/apm/services/orders/containers?environment=prod",
		// containers (containers.go)
		"/api/v1/containers?host_id=h1&compose_project=shop&compose_service=orders&state=running&q=ord",
		"/api/v1/containers/groups?host_id=h1",
		"/api/v1/containers/" + strings.Repeat("ab", 32),
		"/api/v1/containers/" + strings.Repeat("ab", 32) + "/timeseries",
		"/api/v1/containers/" + strings.Repeat("ab", 32) + "/services",
		"/api/v1/logs?container_id=" + strings.Repeat("ab", 32) + "&compose_service=orders&compose_project=shop&attr.log.iostream=stderr",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("openlog-license-key", "key-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Errorf("%s: status %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(conn.sql) < len(paths) {
		t.Fatalf("only %d statements executed", len(conn.sql))
	}
	for _, sql := range conn.sql {
		refs := tableRef.FindAllStringIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
		}
		for _, r := range refs {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
	}
}

func TestAuthAndErrors(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	do := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/api/v1/hosts", "")
	if rec.Code != 401 {
		t.Errorf("no key: %d", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "unauthenticated" {
		t.Errorf("error body %s", rec.Body)
	}
	if len(conn.sql) != 0 {
		t.Error("query executed without authentication")
	}
	if rec := do("/api/v1/hosts?limit=abc", "key-a"); rec.Code != 400 {
		t.Errorf("bad limit: %d", rec.Code)
	}
	if rec := do("/api/v1/hosts/h1/metrics", "key-a"); rec.Code != 400 {
		t.Errorf("missing name: %d", rec.Code)
	}
	if rec := do("/api/v1/traces/xyz", "key-a"); rec.Code != 400 {
		t.Errorf("bad trace id: %d", rec.Code)
	}
	if rec := do("/api/v1/traces/5b8efff798038103d269b633813fc60c", "key-a"); rec.Code != 404 {
		t.Errorf("missing trace: %d", rec.Code)
	}
	if rec := do("/api/v1/hosts/nope", "key-a"); rec.Code != 404 {
		t.Errorf("missing host: %d", rec.Code)
	}
	if rec := do("/api/v1/inventory/search", "key-a"); rec.Code != 400 {
		t.Errorf("search without category: %d", rec.Code)
	}
	if rec := do("/api/v1/logs?from=2026-01-02T00:00:00Z&to=1000", "key-a"); rec.Code != 400 {
		t.Errorf("from after to: %d", rec.Code)
	}
}

func TestChooseStep(t *testing.T) {
	base := time.Unix(0, 0)
	cases := []struct {
		explicit string
		rng      time.Duration
		rollup   bool
		want     time.Duration
		err      bool
	}{
		{"", time.Hour, false, 20 * time.Second, false},
		{"", 5 * time.Minute, false, 10 * time.Second, false},
		{"", 24 * time.Hour, true, 5 * time.Minute, false},
		{"", 7 * time.Hour, true, 2 * time.Minute, false},
		{"15s", time.Hour, false, 20 * time.Second, false},
		{"90s", time.Hour, true, 2 * time.Minute, false},
		{"5s", time.Hour, false, 0, true},
		{"x", time.Hour, false, 0, true},
	}
	for _, c := range cases {
		got, err := chooseStep(c.explicit, base, base.Add(c.rng), c.rollup)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("chooseStep(%q, %v, %v) = %v, %v; want %v", c.explicit, c.rng, c.rollup, got, err, c.want)
		}
	}
	if formatStep(90*time.Second) != "90s" || formatStep(time.Hour) != "3600s" {
		t.Errorf("formatStep %s", formatStep(90*time.Second))
	}
}

func TestParseTime(t *testing.T) {
	a, err := parseTime("1757757600000")
	b, err2 := parseTime("2025-09-13T10:00:00Z")
	if err != nil || err2 != nil || !a.Equal(b) {
		t.Errorf("%v %v %v %v", a, b, err, err2)
	}
	if formatTime(a) != "2025-09-13T10:00:00.000000000Z" {
		t.Errorf("formatTime %s", formatTime(a))
	}
}
