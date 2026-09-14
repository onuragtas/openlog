package oql

import (
	"errors"
	"testing"
)

// FuzzCompile checks that arbitrary input never panics, that errors carry positions inside the query, and that
// every accepted query renders SQL satisfying the security invariants (assertSafeSQL).
//
//	go test ./internal/oql -run '^$' -fuzz FuzzCompile -fuzztime 60s
func FuzzCompile(f *testing.F) {
	for _, g := range goldens {
		f.Add(g.query)
	}
	for _, s := range []string{
		"SELECT count(*) FROM Log WHERE a = 'x'' OR 1=1 --'",
		"SELECT filter(count(*), WHERE `weird key` = {{v}}) FROM Span FACET resource['a]'] TIMESERIES 1 minute",
		"select COUNT(*) from log where x not in (1, -2.5e3, true, 'a') since 5minutes ago until now limit max compare with 1 day ago",
		"SELECT histogram(attributes['x'], 1e3, 7) FROM Log WHERE NOT (a IS NULL OR b IS NOT NULL)",
		"SELECT rate(filter(sum(value), WHERE metricName LIKE 'sys%'), 1 second) FROM Metric SINCE 3 days ago TIMESERIES 1 hour",
		"SELECT latest(state), max(restarts) FROM Container FACET host.name, compose.service",
		"SELECT count(*) FROM Log -- comment\n// another\nWHERE message = \"dq \\\" \\n\"",
	} {
		f.Add(s)
	}
	sc := testScope(f, "tenant-f")
	f.Fuzz(func(t *testing.T, src string) {
		v := Validate(src, Options{Now: testNow})
		for _, d := range append(v.Errors, v.Warnings...) {
			if d.Offset < 0 || d.Offset > len(src) || d.Offset+d.Length > len(src)+1 || d.Line < 1 || d.Column < 1 {
				t.Fatalf("diagnostic out of range %+v for %q", d, src)
			}
		}
		p, err := Compile(src, Options{Now: testNow, Variables: map[string][]string{"v": {"x'--", "y"}}})
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error without position: %v", err)
			}
			return
		}
		sql, params, err := p.SQL(sc)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("build error without position: %v for %q", err, src)
			}
			return
		}
		assertSafeSQL(t, src, sql, params, "tenant-f")
	})
}
