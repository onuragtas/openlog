package oql_test

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/oql"
)

func filterSQL(t *testing.T, src string, fs ...oql.Filter) (*oql.Plan, string, map[string]string) {
	t.Helper()
	p, err := oql.Compile(src, oql.Options{Now: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), Filters: fs})
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	sc, err := query.New(nil, "openlog", time.Second).Scope("t1")
	if err != nil {
		t.Fatal(err)
	}
	sql, params, err := p.SQL(sc)
	if err != nil {
		t.Fatal(err)
	}
	return p, sql, params
}

func hasParam(params map[string]string, want string) bool {
	for _, v := range params {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

func TestFiltersApplied(t *testing.T) {
	evil := "web-1' OR 1=1 --"
	p, sql, params := filterSQL(t, "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name",
		oql.Filter{Attribute: "host.name", Value: evil})
	if len(p.IgnoredFilters) != 0 {
		t.Errorf("ignored %v", p.IgnoredFilters)
	}
	if strings.Contains(sql, "OR 1=1") || !hasParam(params, evil) {
		t.Errorf("filter value must be a bound parameter:\n%s\n%v", sql, params)
	}
	if !strings.Contains(sql, "AND") {
		t.Errorf("filter must be ANDed with WHERE: %s", sql)
	}

	// Numbers and implicit attributes.
	p, _, params = filterSQL(t, "SELECT count(*) FROM Transaction TIMESERIES",
		oql.Filter{Attribute: "duration.ms", Value: "250"},
		oql.Filter{Attribute: "http.route", Value: "/checkout", EventType: "Transaction"},
		oql.Filter{Attribute: "attributes['customer.tier']", Value: "gold"})
	if len(p.IgnoredFilters) != 0 || !hasParam(params, "/checkout") || !hasParam(params, "gold") {
		t.Errorf("ignored %v params %v", p.IgnoredFilters, params)
	}
}

func TestFiltersIgnored(t *testing.T) {
	for _, tc := range []struct {
		query  string
		filter oql.Filter
	}{
		{"SELECT count(*) FROM Host", oql.Filter{Attribute: "attributes['x']", Value: "1"}},                // Host has no attributes map
		{"SELECT count(*) FROM Log", oql.Filter{Attribute: "http.route", Value: "/", EventType: "Span"}},   // implicit, other event type
		{"SELECT count(*) FROM Log", oql.Filter{Attribute: "http.route", Value: "/"}},                      // implicit without event type
		{"SELECT count(*) FROM Transaction", oql.Filter{Attribute: "duration.ms", Value: "slow"}},          // not a number
		{"SELECT count(*) FROM Log", oql.Filter{Attribute: "tenant_id", Value: "other", EventType: "Log"}}, // never
	} {
		p, sql, params := filterSQL(t, tc.query, tc.filter)
		if len(p.IgnoredFilters) != 1 || hasParam(params, tc.filter.Value) && tc.filter.Value != "1" {
			t.Errorf("%s %+v: ignored %v sql %s", tc.query, tc.filter, p.IgnoredFilters, sql)
		}
	}
}

func TestFiltersInvalid(t *testing.T) {
	many := make([]oql.Filter, oql.MaxFilters+1)
	for i := range many {
		many[i] = oql.Filter{Attribute: "host.name", Value: "x"}
	}
	for name, fs := range map[string][]oql.Filter{
		"at most":        many,
		"attribute":      {{Attribute: "host.name OR 1", Value: "x"}},
		"control":        {{Attribute: "host.name", Value: "a\nb"}},
		"keyword":        {{Attribute: "select", Value: "x"}},
		"empty map key":  {{Attribute: "attributes['']", Value: "x"}},
		"trailing token": {{Attribute: "host.name)", Value: "x"}},
	} {
		if _, err := oql.Compile("SELECT count(*) FROM Log", oql.Options{Filters: fs}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
