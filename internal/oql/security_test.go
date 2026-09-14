package oql

import (
	"strings"
	"testing"
	"time"
)

// Injection attempts must either fail to compile or produce SQL whose only user-controlled parts are parameters.
func TestInjectionAttemptsAreParameterized(t *testing.T) {
	sc := testScope(t, "tenant-a")
	payloads := []string{
		`x' OR 1=1 --`,
		`x\' OR tenant_id != \'`,
		`'); DROP TABLE openlog.logs; --`,
		`x" UNION ALL SELECT * FROM system.users --`,
		"x`) FROM openlog.logs_local /*",
		`{tenant_id:String}`,
		`\N`,
	}
	for _, pl := range payloads {
		lit := "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(pl) + "'"
		queries := []string{
			"SELECT count(*) FROM Log WHERE message = " + lit,
			"SELECT count(*) FROM Log WHERE attributes[" + lit + "] = " + lit,
			"SELECT count(*) FROM Log WHERE service.name IN (" + lit + ", 'b') FACET attributes[" + lit + "]",
			"SELECT count(*) FROM Log WHERE message LIKE " + lit + " TIMESERIES FACET host.name",
			"SELECT count(*) AS " + lit + " FROM Span",
		}
		for _, q := range queries {
			p, err := Compile(q, Options{Now: testNow})
			if err != nil {
				t.Fatalf("%s: %v", q, Describe(q, err))
			}
			sql, params, err := p.SQL(sc)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			assertSafeSQL(t, q, sql, params, "tenant-a")
			if pl != "{tenant_id:String}" && strings.Contains(sql, pl) {
				t.Fatalf("payload copied into SQL: %s", sql)
			}
			found := strings.Contains(q, "AS ") // aliases never reach SQL
			for _, v := range params {
				if strings.Contains(v, pl) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: payload not bound as a parameter: %v", q, params)
			}
		}
	}
	// Variable values are parameters too.
	p, err := Compile("SELECT count(*) FROM Log WHERE host.name IN ({{h}}) AND message = {{m}}", Options{Now: testNow,
		Variables: map[string][]string{"h": {"a') OR 1=1 --", "b"}, "m": {"x' OR '1"}}})
	if err != nil {
		t.Fatal(err)
	}
	sql, params, err := p.SQL(sc)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeSQL(t, "variables", sql, params, "tenant-a")
}

func TestTenantCannotBeEscaped(t *testing.T) {
	for _, q := range []string{
		"SELECT count(*) FROM Log WHERE tenant_id = 'tenant-b'",
		"SELECT count(*) FROM Log WHERE `tenant_id` != ''",
		"SELECT count(*) FROM Log FACET tenant_id",
		"SELECT uniqueCount(tenant_id) FROM Span",
		"SELECT count(*) FROM Host WHERE _internal = 'x'",
		"SELECT count(*) FROM logs_local",
		"SELECT count(*) FROM system.tables",
		"SELECT count(*) FROM Log, Span",
		"SELECT count(*) FROM (SELECT * FROM Log)",
		"SELECT count(*) FROM Log; SELECT count(*) FROM Span",
		"SELECT count(*) FROM Log WHERE host.name IN (SELECT host_id FROM hosts)",
		"SELECT sleep(3) FROM Log",
		"SELECT remote('x', 'y') FROM Log",
		"SELECT count(*) FROM Log SETTINGS max_memory_usage = 0",
		"SELECT count(*) FROM Log FORMAT JSON",
		"SELECT count(*) FROM Log WHERE message = 'a' UNION ALL SELECT count(*) FROM Log",
	} {
		if _, err := Compile(q, Options{Now: testNow}); err == nil {
			t.Errorf("accepted: %s", q)
		} else if _, ok := err.(*Error); !ok {
			t.Errorf("%s: error without position %T %v", q, err, err)
		}
	}
	// Map keys named like the tenant column stay map lookups under the tenant predicate.
	sc := testScope(t, "tenant-a")
	p, err := Compile("SELECT count(*) FROM Log WHERE attributes['tenant_id'] = 'tenant-b' OR resource.tenant_id = 'tenant-b'", Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	sql, params, err := p.SQL(sc)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeSQL(t, "map tenant key", sql, params, "tenant-a")
	if !strings.Contains(sql, "WHERE (tenant_id = {tenant_id:String}) AND (timestamp >=") {
		t.Errorf("tenant predicate is not first: %s", sql)
	}
	if strings.Contains(sql, "OR (tenant_id") {
		t.Errorf("unexpected tenant reference: %s", sql)
	}
}

func TestLimits(t *testing.T) {
	long := "SELECT count(*) FROM Log WHERE message = '" + strings.Repeat("a", MaxQueryBytes) + "'"
	deep := "SELECT count(*) FROM Log WHERE " + strings.Repeat("(", 500) + "message = 'x'" + strings.Repeat(")", 500)
	nots := "SELECT count(*) FROM Log WHERE " + strings.Repeat("NOT ", 100) + "message = 'x'"
	nested := "SELECT " + strings.Repeat("filter(", 30) + "count(*)" + strings.Repeat(", WHERE message = 'x')", 30) + " FROM Log"
	manyIn := "SELECT count(*) FROM Log WHERE host.name IN ('" + strings.Repeat("a', '", 500) + "a')"
	manyPreds := "SELECT count(*) FROM Log WHERE " + strings.TrimSuffix(strings.Repeat("message = 'x' OR ", 101), " OR ")
	manyItems := "SELECT " + strings.TrimSuffix(strings.Repeat("count(*), ", 21), ", ") + " FROM Log"
	manyLevels := "SELECT percentile(duration.ms, 1,2,3,4,5,6,7,8,9,10,11) FROM Span"
	cases := map[string]string{
		long:       "longer than",
		deep:       "nested more than",
		nots:       "nested more than",
		nested:     "nested more than",
		manyIn:     "at most 500 values",
		manyPreds:  "at most 100 conditions",
		manyItems:  "at most 20 select items",
		manyLevels: "at most 10 percentile levels",
		"SELECT count(*) FROM Log SINCE 45 days ago":                                "at most 31 days",
		"SELECT count(*) FROM Log SINCE 0":                                          "more than 400 days ago",
		"SELECT count(*) FROM Log SINCE 10 years ago":                               "more than 400 days ago",
		"SELECT count(*) FROM Log SINCE '1999-01-01'":                               "more than 400 days ago",
		"SELECT average(value) FROM Metric WHERE host.name = 'x' SINCE 60 days ago": "1-minute rollup",
		"SELECT average(value) FROM Metric SINCE 500 days ago":                      "more than 400 days ago",
		"SELECT count(*) FROM Log TIMESERIES 10 seconds SINCE 1 day ago":            "buckets (at most 1000)",
		"SELECT count(*) FROM Log TIMESERIES 1 second":                              "at least 10 seconds",
		"SELECT count(*) FROM Log FACET host.name LIMIT 5000":                       "at most 2000",
		"SELECT count(*) FROM Log FACET host.name TIMESERIES LIMIT 51":              "at most 50",
		"SELECT percentile(message.length, 1,2,3,4,5,6,7,8,9) , percentile(duration, 1,2,3,4,5,6,7,8,9), percentile(x, 1,2,3,4,5,6,7,8,9), percentile(y, 1,2,3,4) FROM Log": "at most 30 result columns",
		"SELECT count(*), count(*), count(*), count(*), count(*), count(*), count(*), count(*), count(*), count(*), count(*) FROM Log FACET host.name TIMESERIES LIMIT 50":  "too many series",
		"SELECT count(*) FROM Log COMPARE WITH 2 years ago":                              "COMPARE WITH must be between",
		"SELECT count(*) FROM Log WHERE host.name = {{" + strings.Repeat("v", 70) + "}}": "invalid variable name",
	}
	for q, want := range cases {
		_, err := Compile(q, Options{Now: testNow})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.120s: got %v, want %q", q, err, want)
		}
	}
	// Variable with several values in an ordering comparison fails at translation.
	p, err := Compile("SELECT count(*) FROM Log WHERE severity.number > {{n}}", Options{Now: testNow, Variables: map[string][]string{"n": {"1", "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.SQL(testScope(t, "t")); err == nil || !strings.Contains(err.Error(), "several values") {
		t.Errorf("multi-value ordering: %v", err)
	}
	// A variable value that is not a number for a number attribute.
	p, _ = Compile("SELECT count(*) FROM Log WHERE severity.number = {{n}}", Options{Now: testNow, Variables: map[string][]string{"n": {"abc"}}})
	if _, _, err := p.SQL(testScope(t, "t")); err == nil {
		t.Error("non-numeric variable accepted for a number attribute")
	}
	// Time override is subject to the same caps.
	if _, err := Compile("SELECT count(*) FROM Log", Options{Now: testNow, From: testNow.Add(-40 * 24 * time.Hour), To: testNow}); err == nil {
		t.Error("override range not capped")
	}
}
