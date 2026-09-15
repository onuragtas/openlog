//go:build integration

// Integration tests of OQL against a single-node ClickHouse cluster "openlog" (embedded Keeper,
// deploy/compose/clickhouse/openlog-cluster.xml, users openlog/openlog with access management): the schema is
// migrated, data of two tenants is seeded into the local tables, and OQL results (run as a read-only user through
// internal/api/query) are compared with hand-written SQL.
//
//	OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19000 go test -tags integration -count=1 -run Integration ./internal/oql
package oql_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/oql"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/schema"
)

type chEnv struct {
	writer  clickhouse.Conn
	db      *query.DB // read-only user
	tenantA string
	tenantB string
	now     time.Time
}

func openCH(t *testing.T, user, password string) clickhouse.Conn {
	t.Helper()
	addr := os.Getenv("OPENLOG_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("OPENLOG_TEST_CLICKHOUSE_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: user, Password: password}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Connections live for the whole test binary (shared by every test).
	return conn
}

var sharedEnv *chEnv

func setup(t *testing.T) *chEnv {
	t.Helper()
	if sharedEnv != nil {
		return sharedEnv
	}
	ctx := context.Background()
	w := openCH(t, "openlog", "openlog")
	ms, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, w, ms, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE USER IF NOT EXISTS oql_ro IDENTIFIED WITH plaintext_password BY 'ro' SETTINGS readonly = 2",
		"GRANT SELECT ON openlog.* TO oql_ro",
	} {
		if err := w.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	sfx := fmt.Sprint(time.Now().UnixNano())
	e := &chEnv{writer: w, tenantA: "oql-a-" + sfx, tenantB: "oql-b-" + sfx, now: time.Now().UTC().Truncate(10 * time.Minute)}
	ro := openCH(t, "oql_ro", "ro")
	e.db = query.New(ro, "openlog", 30*time.Second)
	e.db.SetLimits("api", config.Query{})
	seed(t, e)
	sharedEnv = e
	return e
}

func seed(t *testing.T, e *chEnv) {
	t.Helper()
	ctx := context.Background()
	batch := func(sql string, rows func(add func(args ...any))) {
		b, err := e.writer.PrepareBatch(ctx, sql)
		if err != nil {
			t.Fatal(err)
		}
		rows(func(args ...any) {
			if err := b.Append(args...); err != nil {
				t.Fatal(err)
			}
		})
		if err := b.Send(); err != nil {
			t.Fatal(err)
		}
	}
	sevs := []struct {
		text string
		num  uint8
	}{{"INFO", 9}, {"WARN", 13}, {"ERROR", 17}}
	batch("INSERT INTO openlog.logs_local (tenant_id, timestamp, service_name, host_id, host_name, severity_text, severity_number, body, resource_attributes, attributes)", func(add func(...any)) {
		for tn, tenant := range []string{e.tenantA, e.tenantB} {
			for i := 0; i < 2400+tn*300; i++ {
				ts := e.now.Add(-time.Duration(i*3+1) * time.Second) // last 2 hours (+)
				svc := []string{"api", "web", "worker"}[i%3]
				host := fmt.Sprintf("h%d", i%4)
				sv := sevs[(i/7)%3]
				status := "200"
				if i%10 == 0 {
					status = "500"
				}
				attrs := map[string]string{"http.status": status, "http.route": fmt.Sprintf("/r%d", i%5)}
				if i%6 == 0 {
					delete(attrs, "http.route")
				}
				add(tenant, ts, svc, host, host+".local", sv.text, sv.num, fmt.Sprintf("line %d timeout=%v", i, i%9 == 0),
					map[string]string{"k8s.namespace.name": []string{"prod", "dev"}[i%2]}, attrs)
			}
		}
	})
	batch("INSERT INTO openlog.spans_local (tenant_id, timestamp, duration_ns, trace_id, span_id, name, kind, status_code, service_name, host_id, attributes, resource_attributes, is_entry, transaction_type, transaction_name, http_status_code, is_error, sample_weight)", func(add func(...any)) {
		for tn, tenant := range []string{e.tenantA, e.tenantB} {
			for i := 0; i < 1500+tn*100; i++ {
				ts := e.now.Add(-time.Duration(i*4+2) * time.Second)
				entry := i%3 != 0
				code := uint16(200)
				if i%8 == 0 {
					code = 503
				}
				kind := "server"
				if !entry {
					kind = "client"
				}
				add(tenant, ts, uint64((i%97+1)*1_300_000), fmt.Sprintf("%032x", i), fmt.Sprintf("%016x", i), "span"+fmt.Sprint(i%4), kind, "unset",
					[]string{"checkout", "cart"}[i%2], fmt.Sprintf("h%d", i%3), map[string]string{"db.system": "pg"}, map[string]string{}, entry, "web",
					fmt.Sprintf("GET /t%d", i%6), code, code >= 500, 1.0)
			}
		}
	})
	batch("INSERT INTO openlog.metrics_local (tenant_id, metric_name, metric_type, temporality, is_monotonic, unit, service_name, host_id, host_name, series_id, resource_attributes, attributes, timestamp, value)", func(add func(...any)) {
		for tn, tenant := range []string{e.tenantA, e.tenantB} {
			for i := 0; i < 10*60*2; i++ { // 10 hours, every 30s
				ts := e.now.Add(-time.Duration(i*30+5) * time.Second)
				for h := 0; h < 2; h++ {
					host := fmt.Sprintf("h%d", h)
					v := float64((i*7+h*13)%100) / 100
					add(tenant, "system.cpu.utilization", "gauge", "unspecified", false, "1", "", host, host+".local", uint64(h*2+i%2+1),
						map[string]string{}, map[string]string{"state": []string{"user", "system"}[i%2]}, ts, v+float64(tn))
				}
			}
			// containers (containers_mv)
			for c := 0; c < 5; c++ {
				cid := fmt.Sprintf("%064x", c+1)
				attrs := map[string]string{"container.id": cid, "container.name": fmt.Sprintf("c%d", c),
					"docker.compose.project": []string{"shop", "blog"}[c%2], "openlog.container.state": "running"}
				add(tenant, "openlog.container.status", "gauge", "unspecified", false, "", "", "h0", "h0.local", uint64(100+c),
					map[string]string{}, attrs, e.now.Add(-time.Duration(c+1)*time.Minute), 1.0)
				add(tenant, "container.restarts", "sum", "cumulative", true, "", "", "h0", "h0.local", uint64(200+c),
					map[string]string{}, attrs, e.now.Add(-time.Duration(c+1)*time.Minute), float64(c*2))
			}
		}
	})
	batch("INSERT INTO openlog.hosts_local (tenant_id, host_id, host_name, os_type, os_description, arch, agent_name, agent_version, resource_attributes, last_seen)", func(add func(...any)) {
		for tn, tenant := range []string{e.tenantA, e.tenantB} {
			for h := 0; h < 4+tn; h++ {
				add(tenant, fmt.Sprintf("h%d", h), fmt.Sprintf("h%d.local", h), []string{"linux", "darwin"}[h%2], "", "amd64", "openlog", "1.0",
					map[string]string{"cloud.provider": "aws"}, e.now.Add(-time.Duration(h)*time.Minute))
			}
		}
	})
}

// ---- helpers ----

func (e *chEnv) run(t *testing.T, tenant, src string, opt oql.Options) *oql.Result {
	t.Helper()
	if opt.Now.IsZero() {
		opt.Now = e.now
	}
	p, err := oql.Compile(src, opt)
	if err != nil {
		t.Fatalf("%s: %s", src, oql.Describe(src, err))
	}
	sc, err := e.db.Scope(tenant)
	if err != nil {
		t.Fatal(err)
	}
	res, err := oql.Execute(context.Background(), sc, p)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return res
}

// rows runs hand-written SQL and returns every row as strings (numbers formatted with %g).
func (e *chEnv) rows(t *testing.T, sql string) [][]string {
	t.Helper()
	rs, err := e.writer.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rs.Close()
	var out [][]string
	types := rs.ColumnTypes()
	for rs.Next() {
		dest := make([]any, len(types))
		for i := range dest {
			dest[i] = new(string)
		}
		vals := make([]any, len(types))
		for i, ct := range types {
			switch ct.DatabaseTypeName() {
			case "Float64":
				vals[i] = new(float64)
			case "UInt64":
				vals[i] = new(uint64)
			case "Int64":
				vals[i] = new(int64)
			default:
				vals[i] = new(string)
			}
		}
		if err := rs.Scan(vals...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			switch x := v.(type) {
			case *float64:
				row[i] = fmtNum(*x)
			case *uint64:
				row[i] = fmtNum(float64(*x))
			case *int64:
				row[i] = fmt.Sprint(*x)
			case *string:
				row[i] = *x
			}
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func fmtNum(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	return fmt.Sprintf("%.6g", f)
}

func val(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case float64:
		return fmtNum(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// resultRows flattens rows as facets followed by values.
func resultRows(res *oql.Result) [][]string {
	var out [][]string
	for _, r := range res.Rows {
		row := append([]string{}, r.Facets...)
		for _, v := range r.Values {
			row = append(row, val(v))
		}
		out = append(out, row)
	}
	return out
}

func sameRows(t *testing.T, name string, got, want [][]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s:\n got %v\nwant %v", name, got, want)
	}
}

func ns(ts time.Time) string { return fmt.Sprint(ts.UnixNano()) }

func rangeSQL(col string, from, to time.Time) string {
	return fmt.Sprintf("%s >= fromUnixTimestamp64Nano(%s) AND %s < fromUnixTimestamp64Nano(%s)", col, ns(from), col, ns(to))
}

// ---- tests ----

func TestIntegrationResultsMatchSQL(t *testing.T) {
	e := setup(t)
	from, to := e.now.Add(-3*time.Hour), e.now
	opt := oql.Options{From: from, To: to}
	A := e.tenantA
	logs := fmt.Sprintf("FROM openlog.logs WHERE tenant_id = '%s' AND %s", A, rangeSQL("timestamp", from, to))
	txs := fmt.Sprintf("FROM openlog.spans WHERE tenant_id = '%s' AND %s AND is_entry", A, rangeSQL("timestamp", from, to))

	cases := []struct {
		name, query, sql string
	}{
		{"count", "SELECT count(*) FROM Log",
			"SELECT count() " + logs},
		{"facets", "SELECT count(*), average(severity.number), uniqueCount(host.name) FROM Log WHERE severity != 'INFO' FACET service.name",
			"SELECT service_name, count() AS c, avg(severity_number), uniqExact(host_name) " + logs + " AND severity_text != 'INFO' GROUP BY service_name ORDER BY c DESC, service_name"},
		{"two facets limit", "SELECT count(*) FROM Log FACET service.name, host.name LIMIT 5",
			"SELECT service_name, host_name, count() AS c " + logs + " GROUP BY service_name, host_name ORDER BY c DESC, service_name, host_name LIMIT 5"},
		{"map attributes", "SELECT count(*), count(http.route) FROM Log WHERE attributes['http.status'] >= 500 AND resource.k8s.namespace.name IN ('prod') FACET attributes['http.route']",
			"SELECT attributes['http.route'] AS r, count() AS c, countIf(mapContains(attributes, 'http.route')) " + logs +
				" AND toFloat64OrNull(attributes['http.status']) >= 500 AND resource_attributes['k8s.namespace.name'] IN ('prod') GROUP BY r ORDER BY c DESC, r"},
		{"like not null", "SELECT count(*) FROM Log WHERE message LIKE '%timeout=true%' AND attributes['http.route'] IS NULL",
			"SELECT count() " + logs + " AND body LIKE '%timeout=true%' AND NOT mapContains(attributes, 'http.route')"},
		// CONTAINS (D-122): case-insensitive and literal, like the explorers' contains.
		{"contains", "SELECT count(*) FROM Log WHERE message CONTAINS 'TIMEOUT=' AND message NOT CONTAINS '%'",
			"SELECT count() " + logs + " AND positionCaseInsensitiveUTF8(body, 'timeout=') > 0 AND position(body, '%') = 0"},
		{"not or", "SELECT count(*) FROM Log WHERE NOT (service.name = 'api' OR host.name IN ('h1.local', 'h2.local'))",
			"SELECT count() " + logs + " AND NOT (service_name = 'api' OR host_name IN ('h1.local', 'h2.local'))"},
		{"transactions", "SELECT count(*), percentile(duration.ms, 50, 90), max(duration) FROM Transaction WHERE service.name = 'checkout' FACET name",
			"SELECT transaction_name, count() AS c, quantile(0.5)(duration_ns / 1e6), quantile(0.9)(duration_ns / 1e6), max(duration_ns / 1e9) " + txs +
				" AND service_name = 'checkout' GROUP BY transaction_name ORDER BY c DESC, transaction_name"},
		{"filter rate latest", "SELECT filter(count(*), WHERE http.status_code >= 500), rate(count(*), 1 minute), filter(average(duration.ms), WHERE error = true), latest(name) FROM Transaction",
			fmt.Sprintf("SELECT countIf(http_status_code >= 500), count() * 60 / %d, avgIf(duration_ns / 1e6, is_error = true), argMax(transaction_name, timestamp) ", int(to.Sub(from).Seconds())) + txs},
		{"spans kind", "SELECT count(*) FROM Span WHERE kind = 'client' FACET service.name",
			fmt.Sprintf("SELECT service_name, count() AS c FROM openlog.spans WHERE tenant_id = '%s' AND %s AND toString(kind) = 'client' GROUP BY service_name ORDER BY c DESC, service_name", A, rangeSQL("timestamp", from, to))},
		{"hosts", "SELECT count(*), uniqueCount(host.name) FROM Host WHERE resource.cloud.provider = 'aws' FACET os.type",
			fmt.Sprintf("SELECT os_type, count() AS c, uniqExact(host_name) FROM openlog.hosts FINAL WHERE tenant_id = '%s' AND %s GROUP BY os_type ORDER BY c DESC, os_type", A, rangeSQL("last_seen", from, to))},
	}
	for _, c := range cases {
		sameRows(t, c.name, resultRows(e.run(t, A, c.query, opt)), e.rows(t, c.sql))
	}

	// Containers: merged per container from the aggregating table.
	res := e.run(t, A, "SELECT count(*), max(restarts) FROM Container WHERE state = 'running' FACET compose.project", oql.Options{})
	sameRows(t, "containers", resultRows(res), [][]string{{"shop", "3", "8"}, {"blog", "2", "6"}})

	// Timeseries with facets: buckets and zero fill.
	res = e.run(t, A, "SELECT count(*), max(severity.number) FROM Log FACET service.name TIMESERIES 10 minutes LIMIT 2", opt)
	want := e.rows(t, "SELECT service_name, toString(toUnixTimestamp64Milli(toDateTime64(toStartOfInterval(timestamp, INTERVAL 10 MINUTE), 3))), count(), toFloat64(max(severity_number)) "+logs+
		" AND service_name IN (SELECT service_name "+logs+" GROUP BY service_name ORDER BY count() DESC, service_name LIMIT 2) GROUP BY 1, 2 ORDER BY 1, 2")
	var got [][]string
	if len(res.Series) != 4 || *res.Metadata.BucketSeconds != 600 || len(res.Series[0].Points) != 18 {
		t.Fatalf("timeseries shape: %d series, bucket %v, points %d", len(res.Series), res.Metadata.BucketSeconds, len(res.Series[0].Points))
	}
	byKey := map[string][]string{}
	for _, s := range res.Series {
		for _, p := range s.Points {
			k := s.Facets[0] + "|" + fmt.Sprint(p[0])
			byKey[k] = append(byKey[k], val(p[1]))
		}
	}
	for k, vs := range byKey {
		if vs[0] == "0" && vs[1] == "null" {
			continue // empty bucket: count zero-filled, max null
		}
		parts := strings.SplitN(k, "|", 2)
		got = append(got, append([]string{parts[0], parts[1]}, vs...))
	}
	sort.Slice(got, func(i, j int) bool { return got[i][0]+got[i][1] < got[j][0]+got[j][1] })
	sameRows(t, "timeseries", got, want)

	// Histogram.
	res = e.run(t, A, "SELECT histogram(duration.ms, 130, 10) FROM Span", opt)
	hw := e.rows(t, fmt.Sprintf("SELECT toString(toInt64(floor(duration_ns / 1e6 / 13))) AS b, count() FROM openlog.spans WHERE tenant_id = '%s' AND %s AND duration_ns / 1e6 < 130 GROUP BY b ORDER BY toInt64(b)", A, rangeSQL("timestamp", from, to)))
	var hg [][]string
	for i, b := range res.Buckets {
		if b.Count > 0 {
			hg = append(hg, []string{fmt.Sprint(i), fmtNum(b.Count)})
		}
	}
	sameRows(t, "histogram", hg, hw)

	// COMPARE WITH.
	res = e.run(t, A, "SELECT count(*) FROM Log COMPARE WITH 1 hour ago", oql.Options{From: e.now.Add(-time.Hour), To: e.now})
	prev := e.rows(t, fmt.Sprintf("SELECT count() FROM openlog.logs WHERE tenant_id = '%s' AND %s", A, rangeSQL("timestamp", e.now.Add(-2*time.Hour), e.now.Add(-time.Hour))))
	if res.Compare == nil || val(res.Compare.Rows[0].Values[0]) != prev[0][0] || res.Metadata.Queries != 2 || res.Metadata.RowsRead == 0 {
		t.Errorf("compare %+v want %v", res.Compare, prev)
	}
}

func TestIntegrationMetricRollup(t *testing.T) {
	e := setup(t)
	from, to := e.now.Add(-9*time.Hour), e.now
	q := "SELECT average(value), max(value), min(value), count(*), sum(value) FROM Metric WHERE metricName = 'system.cpu.utilization' AND attributes['state'] = 'user' FACET host.id"
	roll := e.run(t, e.tenantA, q, oql.Options{From: from, To: to})
	raw := e.run(t, e.tenantA, q, oql.Options{From: from, To: to, NoRollup: true})
	if !roll.Metadata.Rollup || roll.Metadata.Table != "metrics_1m" || raw.Metadata.Rollup {
		t.Fatalf("rollup selection: %v %v", roll.Metadata, raw.Metadata)
	}
	// Minute-aligned range: the rollup equals raw data points of whole minutes.
	sameRows(t, "rollup vs raw", resultRows(roll), resultRows(raw))
	want := e.rows(t, fmt.Sprintf("SELECT host_id, avg(value) AS a, max(value), min(value), count(), sum(value) FROM openlog.metrics WHERE tenant_id = '%s' AND metric_name = 'system.cpu.utilization' AND attributes['state'] = 'user' AND %s GROUP BY host_id ORDER BY a DESC, host_id", e.tenantA, rangeSQL("timestamp", from, to)))
	sameRows(t, "rollup vs SQL", resultRows(roll), want)

	ts := e.run(t, e.tenantA, "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' TIMESERIES SINCE 9 hours ago", oql.Options{})
	if !ts.Metadata.Rollup || *ts.Metadata.BucketSeconds != 300 || len(ts.Series) != 1 {
		t.Errorf("rollup timeseries metadata %+v", ts.Metadata)
	}
	// The other tenant's values (offset by 1) never appear.
	for _, s := range ts.Series {
		for _, p := range s.Points {
			if f, ok := p[1].(float64); ok && f >= 1 {
				t.Fatalf("tenant B value in tenant A result: %v", p)
			}
		}
	}
}

func TestIntegrationTenantIsolationAndLimits(t *testing.T) {
	e := setup(t)
	opt := oql.Options{From: e.now.Add(-3 * time.Hour), To: e.now}
	for _, tn := range []string{e.tenantA, e.tenantB} {
		res := e.run(t, tn, "SELECT count(*) FROM Log WHERE attributes['tenant_id'] IS NULL OR attributes['tenant_id'] IS NOT NULL", opt)
		want := e.rows(t, fmt.Sprintf("SELECT count() FROM openlog.logs WHERE tenant_id = '%s' AND %s", tn, rangeSQL("timestamp", opt.From, opt.To)))
		sameRows(t, "isolation "+tn, resultRows(res), want)
	}
	a := e.run(t, e.tenantA, "SELECT count(*) FROM Log", opt)
	b := e.run(t, e.tenantB, "SELECT count(*) FROM Log", opt)
	if val(a.Rows[0].Values[0]) == val(b.Rows[0].Values[0]) {
		t.Errorf("tenants have the same count: %v", a.Rows)
	}

	// The read-only user cannot write.
	sc, _ := e.db.Scope(e.tenantA)
	_ = sc
	ro := openCH(t, "oql_ro", "ro")
	if err := ro.Exec(context.Background(), "INSERT INTO openlog.logs_local (tenant_id) VALUES ('x')"); err == nil {
		t.Error("read-only user could insert")
	}

	// Organization limits stop expensive queries with a LimitError.
	limited := query.New(ro, "openlog", 30*time.Second)
	limited.SetLimits("api", config.Query{Defaults: config.QueryLimits{MaxRowsToRead: 10}})
	lsc, _ := limited.Scope(e.tenantA)
	p, err := oql.Compile("SELECT count(*) FROM Log FACET host.name", oql.Options{Now: e.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oql.Execute(context.Background(), lsc, p); err == nil {
		t.Error("max_rows_to_read not enforced")
	} else if le, ok := query.AsLimitError(err); !ok || le.Limit != "max_rows_to_read" {
		t.Errorf("limit error: %v", err)
	}
}

func TestIntegrationWindowsAndSamples(t *testing.T) {
	e := setup(t)
	sc, _ := e.db.Scope(e.tenantA)
	p, err := oql.Compile("SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name", oql.Options{Now: e.now, NoRollup: true, From: e.now.Add(-time.Hour), To: e.now, DefaultLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	step, window := time.Minute, 5*time.Minute
	origin := e.now.Add(-30 * time.Minute)
	series, err := oql.ExecuteWindows(context.Background(), sc, p, origin, step, window, 30, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 {
		t.Fatalf("%d series", len(series))
	}
	for _, s := range series {
		for _, j := range []int{0, 7, 29} {
			end := origin.Add(time.Duration(j+1) * step)
			want := e.rows(t, fmt.Sprintf("SELECT count() FROM openlog.logs WHERE tenant_id = '%s' AND service_name = '%s' AND severity_text = 'ERROR' AND %s",
				e.tenantA, s.Facets[0], rangeSQL("timestamp", end.Add(-window), end)))
			if fmtNum(s.Values[j]) != want[0][0] {
				t.Errorf("%s window %d: %v want %v", s.Facets[0], j, s.Values[j], want[0][0])
			}
		}
	}
	smp, err := oql.SampleKeys(context.Background(), sc, "Log", e.now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(smp.AttributeKeys, ",") != "http.status,http.route" || strings.Join(smp.ResourceKeys, ",") != "k8s.namespace.name" {
		t.Errorf("samples %+v", smp)
	}
	msmp, err := oql.SampleKeys(context.Background(), sc, "Metric", e.now)
	if err != nil || len(msmp.MetricNames) != 3 {
		t.Errorf("metric samples %+v %v", msmp, err)
	}
}
