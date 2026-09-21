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
	"github.com/onuragtas/openlog/internal/synthetics"
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
	// Job monitoring registers nothing without its store, so without this its routes are invisible to the
	// coverage check below — the gap that lets a route reach production unexecuted.
	s.SetJobs(fakeJobStore{}, nil, nil)
	s.SetSLOs(fakeSLOStore{})
	// Seeded rather than empty: the results endpoint looks the check up before it queries, so an empty
	// store would 404 ahead of building any SQL — and the SQL is what this test exists to execute.
	synth := newFakeSyntheticStore()
	synth.checks["check-a"] = synthetics.Check{ID: "check-a", OrgID: "tenant-a"}
	synth.order = []string{"check-a"}
	s.SetSynthetics(synth)
	h := s.Handler()
	paths := []string{
		"/api/v1/logs?attr.log.file.path=/var/log/nginx/access.log&attr.openlog.discovery.id=nginx",
		"/api/v1/hosts",
		"/api/v1/hosts/h1",
		"/api/v1/metrics/names?host_id=h1",
		"/api/v1/metrics/correlate?from=2026-01-01T00:00:00Z&to=2026-01-01T00:30:00Z",
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
		// APM GA (apm_error_inbox.go, apm_deployments.go, apm_map_path.go)
		"/api/v1/apm/errors?status=unresolved,resolved&assignee=none&q=shard&sort=last_seen&environment=prod",
		"/api/v1/apm/errors?service=orders&namespace=shop",
		"/api/v1/apm/errors/groups/00000000000000ff/comments",
		"/api/v1/apm/services/orders/deployments?gap=10m",
		"/api/v1/apm/services/orders/deployments/compare?at=1757757600000&window=15m",
		"/api/v1/apm/map/path?service=orders&transaction=GET%20%2Forders&environment=prod",
		"/api/v1/apm/map?environment=prod&namespace=shop",
		"/api/v1/logs?service=orders&transaction=GET%20%2Forders&transaction_service=orders&span_id=00f067aa0ba902b7",
		"/api/v1/logs?cursor=" + encodeLogCursor(logPos{ts: 1757757600000000000, key: 42}, 2),
		// containers (containers.go)
		"/api/v1/containers?host_id=h1&compose_project=shop&compose_service=orders&state=running&q=ord",
		"/api/v1/containers/groups?host_id=h1",
		"/api/v1/containers/" + strings.Repeat("ab", 32),
		"/api/v1/containers/" + strings.Repeat("ab", 32) + "/timeseries",
		"/api/v1/containers/" + strings.Repeat("ab", 32) + "/services",
		"/api/v1/logs?container_id=" + strings.Repeat("ab", 32) + "&compose_service=orders&compose_project=shop&attr.log.iostream=stderr",
		// database monitoring (dbmon.go, D-138)
		"/api/v1/db/instances",
		"/api/v1/db/queries?instance=db1%3A5432&sort=avg&db=shop&q=orders",
		"/api/v1/db/queries/42?instance=db1%3A5432",
		"/api/v1/db/activity?instance=db1%3A5432",
		"/api/v1/db/sessions?instance=db1%3A5432&at=1757757600000",
		"/api/v1/db/lookup?db_system=postgresql&statement=SELECT%20%3F",
		// Service level objectives (slos.go, slo.md)
		"/api/v1/slos",
		"/api/v1/slos/22222222-2222-2222-2222-222222222222",
		"/api/v1/slos/22222222-2222-2222-2222-222222222222/results",
		// Synthetic checks (synthetics.go, D-132)
		"/api/v1/synthetics/checks",
		"/api/v1/synthetics/checks/check-a",
		"/api/v1/synthetics/checks/check-a/results",
		// Job monitoring (jobs.go, schema 0099_job_runs, D-141)
		"/api/v1/jobs/monitors",
		"/api/v1/jobs/monitors/11111111-1111-1111-1111-111111111111",
		"/api/v1/jobs/monitors/11111111-1111-1111-1111-111111111111/runs",
		// Vulnerabilities (vulnerabilities.go, schema 0100_host_vulns, D-142)
		"/api/v1/vulnerabilities",
		"/api/v1/vulnerabilities?severity=critical",
		"/api/v1/vulnerabilities/CVE-2026-0001",
		"/api/v1/hosts/h1/vulnerabilities",
		// Continuous profiling (profiles.go, schema 0095_profiles)
		"/api/v1/profiles/services",
		"/api/v1/profiles/flame?service=orders&type=cpu&environment=prod",
		"/api/v1/profiles/functions?service=orders&type=cpu&host=h1&sort=self",
		// Explorer field keys and values (fields.go, D-118)
		"/api/v1/fields/keys?signal=logs",
		"/api/v1/fields/values?signal=logs&key=service.name",
		// Metrics explorer (metrics.go, D-119)
		"/api/v1/metrics",
		// Language agent identity (apm_agents.go, D-124)
		"/api/v1/apm/agents",
		// OQL editor schema (oql.go)
		"/api/v1/query/schema",
		// Tail sampling policy (tailsampling.go, D-075)
		"/api/v1/apm/sampling",
		// Kubernetes (kubernetes.go, schema 0040–0042)
		"/api/v1/kubernetes/clusters",
		"/api/v1/kubernetes/clusters/c-1",
		"/api/v1/kubernetes/nodes",
		"/api/v1/kubernetes/workloads",
		"/api/v1/kubernetes/workloads/c-1/default/Deployment/web",
		"/api/v1/kubernetes/workloads/c-1/default/Deployment/web/timeseries",
		"/api/v1/kubernetes/pods",
		"/api/v1/kubernetes/pods/p-1",
		"/api/v1/kubernetes/pods/p-1/timeseries",
		"/api/v1/kubernetes/pods/p-1/events",
		"/api/v1/kubernetes/events",
		"/api/v1/apm/services/orders/kubernetes",
		// Real user monitoring (rum.go, D-136)
		"/api/v1/rum/apps",
		"/api/v1/rum/overview?app=shop-web",
		"/api/v1/rum/pages?app=shop-web",
		"/api/v1/rum/vitals?app=shop-web",
		"/api/v1/rum/sessions?app=shop-web",
		"/api/v1/rum/sessions/9f2c41b7a80d4e6fb35c1d8e07a4b620?app=shop-web",
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

	// The explorer and OQL query endpoints are POSTs, so the walk above cannot reach them — and a route
	// nothing calls has never had its SQL built. They run here, before the scoping check below, so their
	// statements are held to the same tenant predicate as every other read.
	posts := []struct{ path, body string }{
		{"/api/v1/logs/query", `{}`},
		{"/api/v1/logs/aggregate", `{}`},
		{"/api/v1/logs/patterns", `{}`},
		{"/api/v1/traces/query", `{}`},
		{"/api/v1/traces/aggregate", `{}`},
		{"/api/v1/metrics/query", `{"metric":"system.cpu.utilization"}`},
		{"/api/v1/metrics/exemplars", `{"metric":"http.server.request.duration","limit":10}`},
		{"/api/v1/query", `{"query":"SELECT count(*) FROM Log SINCE 1 hour ago"}`},
		{"/api/v1/query/validate", `{"query":"SELECT count(*) FROM Log SINCE 1 hour ago"}`},
		// An empty policy is a valid one (tailsampling.ParsePolicy): no rules, baseline ratio 0.
		{"/api/v1/apm/sampling/preview", `{"policy":{}}`},
	}
	beforePosts := len(conn.sql)
	for _, p := range posts {
		req := httptest.NewRequest(http.MethodPost, p.path, strings.NewReader(p.body))
		req.Header.Set("openlog-license-key", "key-a")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Errorf("POST %s: status %d %s", p.path, rec.Code, rec.Body)
		}
	}
	// A body the handler rejects returns 400 and runs nothing, which would still count as "walked" above —
	// coverage that proves nothing. Not every one of these must reach ClickHouse (validate only parses, and
	// a metric the fake does not know is a legitimate 404), but if none of them did, the bodies are wrong.
	if len(conn.sql) == beforePosts {
		t.Error("no POST endpoint reached a query: the request bodies are being rejected")
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

	// Every tenant-scoped route must be walked above. GET /api/v1/vulnerabilities shipped returning 500 for
	// every request — a query that could not even be built — because its route was missing from this list:
	// a route nobody calls here is a route whose SQL has never been constructed, let alone executed.
	//
	// What this does *not* cover, stated as a count rather than a shrug: twenty route-registering functions
	// return early unless their dependency is injected (accounts, alerts, SSO, operator, privacy, fleet,
	// usage, dashboards, costs, status page, cloud, saved views, source maps, browser keys, integration
	// settings, updates, vulnerability catalog, and the three wired above), and they hold 221 routes between
	// them. A bare test server has none, so tenantScopedRoutes never returns those patterns and the check
	// below cannot miss what was never registered.
	//
	// Jobs, SLOs and synthetics are wired in above because they were in that blind spot; job monitoring's
	// two read endpoints answered 500 the moment they were first walked. The other seventeen are still dark
	// here — but the fragment scan in internal/api/query covers the specific failure both known cases had
	// (a query that cannot be built) across the whole repository, registered or not.
	walkedPaths := make([]string, 0, len(paths)+len(posts))
	for _, p := range paths {
		walkedPaths = append(walkedPaths, "GET "+p)
	}
	for _, p := range posts {
		walkedPaths = append(walkedPaths, "POST "+p.path)
	}
	var uncovered []string
	for _, pattern := range s.tenantScopedRoutes() {
		if !walked(pattern, walkedPaths) && !knownUncovered[pattern] {
			uncovered = append(uncovered, pattern)
		}
	}
	if len(uncovered) > 0 {
		t.Errorf("tenant-scoped routes never walked by this test (add a path above, or knownUncovered):\n\t%s",
			strings.Join(uncovered, "\n\t"))
	}
	for pattern := range knownUncovered {
		if walked(pattern, walkedPaths) {
			t.Errorf("%s is covered now: remove it from knownUncovered (the list may shrink, never grow)", pattern)
		}
	}
}

// knownUncovered are tenant-scoped routes this test does not walk yet. It is a ratchet: entries may be
// removed as paths are added above, never added for a new route. Every line here is an endpoint whose query
// has never been built by anything — the state GET /api/v1/vulnerabilities was in when it shipped broken.
var knownUncovered = map[string]bool{}

// walked reports whether any walked request matches the route pattern. Both are "<METHOD> <path>", and a
// {param} segment of the pattern matches any non-empty segment: "GET /api/v1/hosts/{host_id}" is walked by
// "GET /api/v1/hosts/h1?x=1", but never by a POST to the same path.
func walked(pattern string, walkedPaths []string) bool {
	want := strings.Fields(pattern)
	if len(want) != 2 {
		return false
	}
	segs := strings.Split(strings.Trim(want[1], "/"), "/")
	for _, p := range walkedPaths {
		got := strings.Fields(p)
		if len(got) != 2 || got[0] != want[0] {
			continue
		}
		xs := strings.Split(strings.Trim(strings.SplitN(got[1], "?", 2)[0], "/"), "/")
		if len(xs) != len(segs) {
			continue
		}
		match := true
		for i, seg := range segs {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				match = match && xs[i] != ""
				continue
			}
			if seg != xs[i] {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
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
