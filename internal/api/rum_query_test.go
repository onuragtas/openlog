package api

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/rum"
)

// TestRUMTimelineQueryBuilds proves the session timeline query passes the query layer's own validation.
//
// It did not: every RUM attribute key starts with "openlog.", and the tenant guard refuses that token
// anywhere in a fragment (internal/api/query: it is the database name), so GET /rum/sessions/{id} answered
// 500 for every session that existed. No handler test caught it, because a handler test runs against an
// empty result set and the timeline query is reached only after the session lookup returns a row — which is
// why this test builds the query directly instead of going through the endpoint.
func TestRUMTimelineQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sql, params, err := rumTimelineSelect(sc, "9f2c41b7a80d4e6fb35c1d8e07a4b620", now.Add(-time.Hour), now, 500).Build()
	if err != nil {
		t.Fatalf("timeline query does not build: %v", err)
	}
	if params["a_event"] != rum.AttrEvent || params["a_route"] != rum.AttrRoute {
		t.Errorf("attribute keys are not bound as parameters: %v", params)
	}
	// Identity and country travel the same way. Neither key happens to contain a token the guard refuses
	// today, which is exactly why they are pinned here: the next key added by copying these might.
	if params["a_user"] != rum.AttrUserID || params["a_country"] != rum.AttrGeoCountry {
		t.Errorf("identity keys are not bound as parameters: %v", params)
	}
	// The keys must reach ClickHouse as parameters, not as text in the statement.
	if strings.Contains(sql, rum.AttrEvent) || strings.Contains(sql, rum.AttrRoute) {
		t.Errorf("attribute key embedded in the statement: %s", sql)
	}
}

// TestRUMSessionIDsQueryBuilds is the same guard, one query later: `GET /rum/sessions?user_id=` reads the
// identity from the spans, and every RUM attribute key starts with "openlog." — the token the query layer
// refuses inside a fragment. A subquery naming the spans table would have been refused for the same
// reason, which is why the handler runs two queries instead.
func TestRUMSessionIDsQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sql, params, err := rumSessionIDsSelect(sc, "user-42", now.Add(-time.Hour), now, 500).Build()
	if err != nil {
		t.Fatalf("session id query does not build: %v", err)
	}
	if params["a_user"] != rum.AttrUserID || params["a_session"] != rum.AttrSessionID {
		t.Errorf("attribute keys are not bound as parameters: %v", params)
	}
	if params["u_id"] != "user-42" {
		t.Errorf("the identity is not a parameter: %v", params)
	}
	if strings.Contains(sql, rum.AttrUserID) {
		t.Errorf("attribute key embedded in the statement: %s", sql)
	}
}

// The facets narrow a read to one kind of visitor. They are plain columns of the rollups, so what is
// pinned here is which rollup has which: the page-view one is keyed by device and browser, the vitals one
// by device alone (schema 0094). The handler refuses `browser` for vitals rather than answering a wider
// question under a narrower name, and that refusal is only right while this stays true.
func TestRUMFilterFacets(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	f := rumFilter{app: "shop-web", device: "mobile", browser: "Safari"}

	pages := f.applyBrowser(f.applyDevice(f.apply(sc.From(query.RumPageViews1m).Columns("route"))))
	sql, params, err := pages.Build()
	if err != nil {
		t.Fatalf("page views query does not build: %v", err)
	}
	if params["r_device"] != "mobile" || params["r_browser"] != "Safari" {
		t.Errorf("facets are not bound as parameters: %v", params)
	}
	if !strings.Contains(sql, "device_type = ") || !strings.Contains(sql, "browser_name = ") {
		t.Errorf("page views query does not filter on both facets: %s", sql)
	}

	vitals := f.applyDevice(f.apply(sc.From(query.RumVitals1m).Columns("vital")))
	sql, _, err = vitals.Build()
	if err != nil {
		t.Fatalf("vitals query does not build: %v", err)
	}
	if strings.Contains(sql, "browser_name") {
		t.Errorf("the vitals rollup has no browser column: %s", sql)
	}
}
