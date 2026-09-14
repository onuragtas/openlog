//go:build integration

package oql_test

import (
	"slices"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/oql"
)

// Dashboard cross-widget filters (oql.md §7) against ClickHouse: a filtered query returns exactly what the same
// predicate written in WHERE returns.
func TestIntegrationDashboardFilters(t *testing.T) {
	e := setup(t)
	single := func(r *oql.Result) any {
		t.Helper()
		if len(r.Rows) != 1 || len(r.Rows[0].Values) == 0 {
			t.Fatalf("expected one row: %+v", r.Rows)
		}
		return r.Rows[0].Values[0]
	}
	opt := func(fs ...oql.Filter) oql.Options { return oql.Options{Filters: fs} }

	for _, tc := range []struct {
		name, query, equivalent string
		filters                 []oql.Filter
	}{
		{"known attribute", "SELECT count(*) FROM Log SINCE 3 hours ago", "SELECT count(*) FROM Log WHERE host.name = 'h1.local' SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "host.name", Value: "h1.local"}}},
		{"combined with WHERE", "SELECT count(*) FROM Log WHERE severity = 'ERROR' SINCE 3 hours ago",
			"SELECT count(*) FROM Log WHERE severity = 'ERROR' AND service.name = 'api' SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "service.name", Value: "api", EventType: "Log"}}},
		{"implicit attribute of the event type", "SELECT count(*) FROM Log SINCE 3 hours ago",
			"SELECT count(*) FROM Log WHERE attributes['http.route'] = '/r1' SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "http.route", Value: "/r1", EventType: "Log"}}},
		{"resource map", "SELECT count(*) FROM Log SINCE 3 hours ago", "SELECT count(*) FROM Log WHERE resource['k8s.namespace.name'] = 'prod' SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "resource['k8s.namespace.name']", Value: "prod"}}},
		{"several filters", "SELECT count(*) FROM Transaction SINCE 3 hours ago",
			"SELECT count(*) FROM Transaction WHERE service.name = 'checkout' AND http.status_code = 503 SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "service.name", Value: "checkout"}, {Attribute: "http.status_code", Value: "503"}}},
		{"injection attempt is only a value", "SELECT count(*) FROM Log SINCE 3 hours ago",
			"SELECT count(*) FROM Log WHERE host.name = 'h1.local'' OR 1=1 --' SINCE 3 hours ago",
			[]oql.Filter{{Attribute: "host.name", Value: "h1.local' OR 1=1 --"}}},
	} {
		got := single(e.run(t, e.tenantA, tc.query, opt(tc.filters...)))
		want := single(e.run(t, e.tenantA, tc.equivalent, oql.Options{}))
		if got != want {
			t.Errorf("%s: filtered %v, WHERE %v", tc.name, got, want)
		}
		if tc.name == "injection attempt is only a value" && got != float64(0) {
			t.Errorf("injection value matched rows: %v", got)
		}
		if tc.name == "known attribute" {
			all := single(e.run(t, e.tenantA, tc.query, oql.Options{}))
			if got == all || got == float64(0) {
				t.Errorf("filter did not narrow the result: %v of %v", got, all)
			}
		}
	}

	// A filter the event type does not have is skipped and reported.
	r := e.run(t, e.tenantA, "SELECT count(*) FROM Host", opt(oql.Filter{Attribute: "http.route", Value: "/r1", EventType: "Log"}))
	if all := single(e.run(t, e.tenantA, "SELECT count(*) FROM Host", oql.Options{})); single(r) != all || !slices.Equal(r.Metadata.IgnoredFilters, []string{"http.route"}) {
		t.Errorf("ignored filter: %v (all %v), ignored %v", single(r), all, r.Metadata.IgnoredFilters)
	}

	// Facets and timeseries keep the tenant scope and match WHERE; rollup eligibility is the same as with WHERE.
	since := "SINCE 8 hours ago"
	f := e.run(t, e.tenantB, "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' FACET host.name "+since,
		opt(oql.Filter{Attribute: "host.name", Value: "h0.local"}))
	w := e.run(t, e.tenantB, "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' AND host.name = 'h0.local' FACET host.name "+since, oql.Options{})
	if len(f.Rows) != 1 || len(w.Rows) != 1 || f.Rows[0].Facets[0] != "h0.local" || f.Rows[0].Values[0] != w.Rows[0].Values[0] || f.Metadata.Rollup != w.Metadata.Rollup {
		t.Errorf("metric facets: filtered %+v (rollup %v), WHERE %+v (rollup %v)", f.Rows, f.Metadata.Rollup, w.Rows, w.Metadata.Rollup)
	}
	ts := e.run(t, e.tenantA, "SELECT count(*) FROM Log FACET service.name TIMESERIES 10 minutes SINCE 1 hour ago",
		oql.Options{Filters: []oql.Filter{{Attribute: "service.name", Value: "web"}}, Now: e.now.Add(time.Second)})
	for _, s := range ts.Series {
		if !slices.Equal(s.Facets, []string{"web"}) {
			t.Errorf("timeseries group outside the filter: %v", s.Facets)
		}
	}
	if len(ts.Series) != 1 {
		t.Errorf("timeseries series: %d", len(ts.Series))
	}
}
