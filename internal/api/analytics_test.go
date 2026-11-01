package api

import (
	"strings"
	"testing"
	"time"
)

// These queries nest sub-selects and spread rows with arrayJoin, and a handler test against an empty result set would
// not notice the query layer refusing a fragment, or a sub-select losing the tenant. Their arithmetic can only be
// checked against a real ClickHouse (test/integration).
func TestAnalyticsQueriesBuild(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-30 * 24 * time.Hour)
	env := "production"
	f := rumFilter{app: "shop-web", env: &env}

	builds := map[string]func() (string, map[string]string, error){
		"activity": analyticsActivitySelect(sc, f, from, now).Build,
		"new":      analyticsNewActorsSelect(sc, f, from, now).Build,
	}
	for grain := range analyticsGrains {
		builds["retention "+grain] = analyticsRetentionSelect(sc, f, grain, from, now).Build
	}
	eventBuilds := map[string]func() (string, map[string]string, error){
		"overview":     analyticsOverviewSelect(sc, f, from, now).Build,
		"events":       analyticsEventsSelect(sc, f, "", "", from, now).Build,
		"event params": analyticsEventsSelect(sc, f, "signup", "plan", from, now).Build,
		"funnel":       analyticsFunnelSelect(sc, f, []string{"a", "b"}, false, time.Hour, from, now).Build,
		"funnel user":  analyticsFunnelSelect(sc, f, []string{"a", "b"}, true, time.Hour, from, now).Build,
	}
	for by, column := range analyticsDimensions {
		eventBuilds["acquisition "+by] = analyticsAcquisitionSelect(sc, f, column, from, now).Build
	}
	for name, build := range eventBuilds {
		sql, params, err := build()
		if err != nil {
			t.Errorf("%s: does not build: %v", name, err)
			continue
		}
		if !strings.Contains(sql, "tenant_id = {tenant_id:String}") {
			t.Errorf("%s: lost the tenant predicate: %s", name, sql)
		}
		if params["r_app"] != "shop-web" || params["r_env"] != env {
			t.Errorf("%s: application or environment not bound: %v", name, params)
		}
	}
	for name, build := range builds {
		sql, params, err := build()
		if err != nil {
			t.Errorf("%s: does not build: %v", name, err)
			continue
		}
		if !strings.Contains(sql, "tenant_id = {tenant_id:String}") {
			t.Errorf("%s: lost the tenant predicate: %s", name, sql)
		}
		if params["r_app"] != "shop-web" || params["r_env"] != env {
			t.Errorf("%s: application or environment not bound: %v", name, params)
		}
		if strings.Contains(sql, "shop-web") {
			t.Errorf("%s: application embedded in the statement: %s", name, sql)
		}
		if params["d_from"] != "2026-10-02" || params["d_to"] != "2026-11-01" {
			t.Errorf("%s: dates %q to %q", name, params["d_from"], params["d_to"])
		}
	}
}

// Sketches and not actors are what gets spread over the window: spreading actors ran out of memory at 2 million of them.
func TestAnalyticsActivitySpreadsSketches(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	sql, params, err := analyticsActivitySelect(sc, rumFilter{app: "a"}, now.Add(-time.Hour), now).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"uniqCombined64State(actor_hash)", "GROUP BY day", "uniqCombined64Merge(a_state)"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in: %s", want, sql)
		}
	}
	if strings.Contains(sql, "GROUP BY d, actor_hash") {
		t.Errorf("actors are spread again: %s", sql)
	}
	if params["d_span"] != "30" || params["d_wau"] != "7" || params["d_back"] != "29" {
		t.Errorf("windows: %v", params)
	}
}
