package oql

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

var update = flag.Bool("update", false, "rewrite golden files")

var testNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func testScope(t testing.TB, tenant string) *query.Scope {
	t.Helper()
	sc, err := query.New(nil, "openlog", time.Second).Scope(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

var tableRef = regexp.MustCompile("`openlog`\\.(\\w+)")

// assertSafeSQL checks the security invariants of generated SQL: every table reference is immediately
// tenant-filtered, the tenant parameter is the scope's tenant, and no literal text (quotes, comments,
// statement separators) appears in the SQL.
func assertSafeSQL(t testing.TB, src, sql string, params map[string]string, tenant string) {
	t.Helper()
	refs := tableRef.FindAllStringIndex(sql, -1)
	if len(refs) == 0 {
		t.Fatalf("%s: no table reference in %s", src, sql)
	}
	for _, r := range refs {
		rest := strings.TrimPrefix(sql[r[1]:], " FINAL")
		if !strings.HasPrefix(rest, " WHERE (tenant_id = {tenant_id:String})") {
			t.Fatalf("%s: table reference not tenant-filtered: %s", src, sql)
		}
	}
	if params["tenant_id"] != tenant {
		t.Fatalf("%s: tenant param %q", src, params["tenant_id"])
	}
	for _, bad := range []string{"'", "\"", ";", "--", "/*", "`openlog`.`", "\\"} {
		if strings.Contains(sql, bad) {
			t.Fatalf("%s: SQL contains %q: %s", src, bad, sql)
		}
	}
	if n := strings.Count(sql, "tenant_id"); n != 2*len(refs) { // "tenant_id = {tenant_id:String}" per table
		t.Fatalf("%s: tenant_id appears %d times for %d tables: %s", src, n, len(refs), sql)
	}
}

type golden struct {
	name  string
	query string
	opt   Options
}

var goldens = []golden{
	{name: "log_count", query: "SELECT count(*) FROM Log"},
	{name: "log_facet", query: "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name SINCE 3 hours ago"},
	{name: "log_timeseries_facets", query: "SELECT count(*), uniqueCount(host.name) FROM Log WHERE message LIKE '%timeout%' FACET service.name, host.name TIMESERIES 5 minutes SINCE 1 day ago LIMIT 5"},
	{name: "log_map_attrs", query: "SELECT count(attributes['http.route']) FROM Log WHERE attributes['http.status'] >= 500 AND resource.k8s.namespace.name IN ('prod', 'staging') AND http.method = 'GET' FACET resource['cloud.region']"},
	{name: "log_is_null", query: "SELECT count(*) FROM Log WHERE trace.id IS NOT NULL AND attributes['user'] IS NULL AND severity.number IS NULL"},
	{name: "log_contains", query: "SELECT count(*) FROM Log WHERE message CONTAINS '50%_OFF' AND attributes['http.route'] NOT CONTAINS 'Health' AND contains = 'x'"},
	{name: "log_not_or", query: "SELECT count(*) FROM Log WHERE NOT (severity = 'DEBUG' OR severity = 'TRACE') AND service.name NOT IN ('a', 'b') AND message NOT LIKE 'health%'"},
	{name: "tx_percentile", query: "SELECT percentile(duration.ms, 50, 95, 99), average(duration.ms) AS 'avg ms' FROM Transaction WHERE service.name = 'checkout' AND error = false FACET transaction.name"},
	{name: "tx_rate_filter", query: "SELECT rate(count(*), 1 minute), filter(count(*), WHERE http.status_code >= 500) AS errors, filter(percentile(duration.ms, 95), WHERE http.status_code < 500) FROM Transaction TIMESERIES AUTO SINCE 6 hours ago"},
	{name: "tx_latest_earliest", query: "SELECT latest(transaction.name), earliest(duration), max(http.status_code) FROM Transaction"},
	{name: "span_histogram", query: "SELECT histogram(duration.ms, 1000, 20) FROM Span WHERE kind = 'server'"},
	{name: "span_compare", query: "SELECT count(*) FROM Span FACET status.code COMPARE WITH 1 week ago"},
	{name: "metric_raw", query: "SELECT average(value), max(value) FROM Metric WHERE metricName = 'system.cpu.utilization' FACET host.name TIMESERIES"},
	{name: "metric_rollup", query: "SELECT average(value), min(value), max(value), sum(value), count(*), latest(value), uniqueCount(host.id) FROM Metric WHERE metricName = 'system.cpu.utilization' AND attributes['state'] != 'idle' FACET host.id TIMESERIES SINCE 7 days ago"},
	{name: "metric_rollup_rate_filter", query: "SELECT rate(sum(value), 1 minute), filter(average(value), WHERE host.id = 'h1') FROM Metric WHERE metricName IN ('a', 'b') SINCE 2 days ago"},
	{name: "metric_rollup_disabled", query: "SELECT percentile(value, 95) FROM Metric WHERE metricName = 'x' SINCE 2 days ago"},
	{name: "host_count", query: "SELECT uniqueCount(host.id) FROM Host FACET os.type, resource.cloud.provider"},
	{name: "container_facets", query: "SELECT count(*), max(restarts) FROM Container WHERE state = 'running' AND attributes['com.docker.compose.version'] IS NOT NULL FACET compose.project TIMESERIES 1 hour SINCE 1 day ago"},
	{name: "variables_multi", query: "SELECT count(*) FROM Log WHERE host.name = {{host}} AND service.name IN ({{svc}}) AND severity = {{unset}}",
		opt: Options{Variables: map[string][]string{"host": {"web-1", "web-2"}, "svc": {"api"}}}},
	{name: "variables_all", query: "SELECT count(*) FROM Log WHERE host.name = {{host}} OR service.name = 'x'",
		opt: Options{Variables: map[string][]string{"host": {"*"}}}},
	{name: "time_override", query: "SELECT count(*) FROM Log SINCE 10 days ago",
		opt: Options{From: testNow.Add(-30 * time.Minute), To: testNow}},
	{name: "absolute_times", query: "SELECT count(*) FROM Span SINCE '2026-09-14 08:00:00' UNTIL 1789380000000 TIMESERIES 30 minutes"},
}

func TestGolden(t *testing.T) {
	sc := testScope(t, "tenant-a")
	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			opt := g.opt
			opt.Now = testNow
			p, err := Compile(g.query, opt)
			if err != nil {
				t.Fatalf("compile: %v", Describe(g.query, err))
			}
			sql, params, err := p.SQL(sc)
			if err != nil {
				t.Fatalf("sql: %v", err)
			}
			assertSafeSQL(t, g.query, sql, params, "tenant-a")
			var b strings.Builder
			fmt.Fprintf(&b, "-- %s\n-- kind=%s table=%s rollup=%v bucket=%s limit=%d from=%s to=%s\n%s\n", g.query, p.Kind, p.Table(), p.Rollup, p.Bucket, p.Limit,
				p.From.Format(time.RFC3339), p.To.Format(time.RFC3339), strings.ReplaceAll(sql, " WHERE ", "\nWHERE "))
			for _, k := range query.ParamNames(params) {
				fmt.Fprintf(&b, "-- %s = %s\n", k, params[k])
			}
			for _, c := range p.Columns {
				fmt.Fprintf(&b, "-- column %q %s %s zero=%v\n", c.Name, c.Function, c.Type, c.zeroFill)
			}
			path := filepath.Join("testdata", "golden", g.name+".sql")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/oql -run TestGolden -update)", err)
			}
			if string(want) != b.String() {
				t.Errorf("golden mismatch for %s\n--- got\n%s\n--- want\n%s", g.name, b.String(), want)
			}
		})
	}
}

func TestParseErrorsHavePositions(t *testing.T) {
	cases := []struct {
		query, msg string
		offset     int
	}{
		{"SELECT count(*) FROM Logs", "unknown event type", 21},
		{"SELECT count(*) FROM Log WHERE sevrity = 'x'", "", -1},
		{"SELECT count(*) FROM Log WHERE severity.number = 'abc'", "is a number", 49},
		{"SELECT count(* FROM Log", "expected ')'", 15},
		{"SELECT foo(x) FROM Log", "unknown function", 7},
		{"SELECT message FROM Log", "select items must be aggregates", 7},
		{"SELECT count(*) FROM Log WHERE", "expected an attribute", 30},
		{"SELECT count(*) FROM Log FACET host.name FACET x", "duplicate FACET", 41},
		{"SELECT sum(message) FROM Log", "needs a number attribute", 11},
		{"SELECT count(*) FROM Log WHERE message = 'unterminated", "unterminated string", 41},
		{"SELECT count(*) FROM Host WHERE foo = 'x'", "unknown attribute", 32},
		{"SELECT count(*) FROM Log SINCE 2 days ago UNTIL 3 days ago", "before its end", 31},
		{"SELECT count(*) FROM Log\nWHERE x == ", "expected a value", 36},
		{"SELECT percentile(duration.ms, 100) FROM Span", "between 0 and 100", 7},
		{"SELECT histogram(duration.ms, 10), count(*) FROM Span", "only select item", 7},
		{"SELECT rate(average(duration), 1 minute) FROM Span", "rate takes count", 12},
	}
	for _, c := range cases {
		_, err := Compile(c.query, Options{Now: testNow})
		if c.offset == -1 {
			// unknown attribute on an event type with attributes: warning, not error
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.query, err)
			}
			continue
		}
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%q: error %v (%T), want *Error", c.query, err, err)
			continue
		}
		if !strings.Contains(e.Msg, c.msg) || e.Offset != c.offset {
			t.Errorf("%q: got %q at %d, want %q at %d", c.query, e.Msg, e.Offset, c.msg, c.offset)
		}
	}
	d := Diagnose("SELECT count(*) FROM Log\nWHERE x == ", &Error{Msg: "m", Offset: 36})
	if d.Line != 2 || d.Column != 12 {
		t.Errorf("position %+v", d)
	}
}

func TestValidateWarningsAndVariables(t *testing.T) {
	v := Validate("SELECT count(*) FROM Log WHERE http.route = {{route}} FACET host.name", Options{Now: testNow})
	if !v.Valid || *v.Kind != "facets" || *v.EventType != "Log" || len(v.Variables) != 1 || v.Variables[0] != "route" {
		t.Fatalf("%+v", v)
	}
	if len(v.Warnings) != 1 || v.Warnings[0].Offset != 31 || !strings.Contains(v.Warnings[0].Message, "attributes['http.route']") {
		t.Fatalf("warnings %+v", v.Warnings)
	}
	v = Validate("SELECT count(*) FRM Log", Options{Now: testNow})
	if v.Valid || len(v.Errors) != 1 || v.Errors[0].Column != 17 || v.EventType != nil {
		t.Fatalf("%+v", v)
	}
}

func TestPlanChoices(t *testing.T) {
	cases := []struct {
		query  string
		kind   Kind
		bucket time.Duration
		rollup bool
		limit  int
	}{
		{"SELECT count(*) FROM Log TIMESERIES", KindTimeseries, 30 * time.Second, false, 10},
		{"SELECT count(*) FROM Log TIMESERIES SINCE 1 day ago", KindTimeseries, 5 * time.Minute, false, 10},
		{"SELECT count(*) FROM Log TIMESERIES SINCE 30 days ago LIMIT MAX", KindTimeseries, 3 * time.Hour, false, 50},
		{"SELECT average(value) FROM Metric TIMESERIES SINCE 7 hours ago", KindTimeseries, 5 * time.Minute, true, 10},
		{"SELECT average(value) FROM Metric TIMESERIES 90 seconds SINCE 7 hours ago", KindTimeseries, 90 * time.Second, false, 10},
		{"SELECT average(value) FROM Metric WHERE host.name = 'x' SINCE 7 hours ago", KindSingle, 0, false, 10},
		{"SELECT average(value) FROM Metric SINCE 90 days ago", KindSingle, 0, true, 10},
		{"SELECT count(*) FROM Log FACET host.name LIMIT MAX", KindFacets, 0, false, 2000},
	}
	for _, c := range cases {
		p, err := Compile(c.query, Options{Now: testNow})
		if err != nil {
			t.Errorf("%q: %v", c.query, err)
			continue
		}
		if p.Kind != c.kind || p.Bucket != c.bucket || p.Rollup != c.rollup || p.Limit != c.limit {
			t.Errorf("%q: kind %s bucket %s rollup %v limit %d", c.query, p.Kind, p.Bucket, p.Rollup, p.Limit)
		}
	}
}

func TestColumns(t *testing.T) {
	p, err := Compile("SELECT percentile(duration.ms, 50, 95), median(duration), filter(percentile(duration.ms, 99), WHERE error = true) AS slow, count(*) AS 'n', latest(name) FROM Span", Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range p.Columns {
		names = append(names, c.Name+"/"+c.Function+"/"+c.Type.String())
	}
	want := "percentile(duration.ms, 50)/percentile/number percentile(duration.ms, 95)/percentile/number median(duration)/median/number slow/filter/number n/count/number latest(name)/latest/string"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("columns\n got %s\nwant %s", got, want)
	}
}
