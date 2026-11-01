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

// A sort name selects a fragment, and the query layer refuses fragments it does not like only when one is built. An empty result set stops
// nothing here, because the first statement of each is the list itself.
func TestRUMListsSortAndPageInTheStatement(t *testing.T) {
	cases := []struct {
		path  string
		sort  string
		dir   string
		order string
	}{
		{"/api/v1/rum/apps?", "views", "", "v_views DESC, app, deployment_environment"},
		{"/api/v1/rum/apps?", "sessions", "", "v_sessions DESC"},
		{"/api/v1/rum/apps?", "errors", "asc", "v_errors ASC"},
		{"/api/v1/rum/apps?", "last_seen", "", "v_last DESC"},
		{"/api/v1/rum/apps?", "app", "", "app ASC"},
		{"/api/v1/rum/apps?", "environment", "desc", "deployment_environment DESC"},
		{"/api/v1/rum/pages?app=a&", "views", "", "p_views DESC, route"},
		{"/api/v1/rum/pages?app=a&", "slowest", "", "p_dsum DESC, route"},
		{"/api/v1/rum/pages?app=a&", "avg", "", "p_dsum / p_views DESC, route"},
		{"/api/v1/rum/pages?app=a&", "ttfb", "", "p_ttfb / p_views DESC, route"},
		{"/api/v1/rum/pages?app=a&", "route", "", "route ASC, route"},
		{"/api/v1/rum/sessions?app=a&", "last_seen", "", "s_last DESC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "started", "asc", "s_first ASC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "duration", "", "dateDiff('millisecond', s_first, s_last) DESC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "page_views", "", "s_views DESC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "errors", "", "s_errors DESC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "entry", "", "s_entry ASC, session_id"},
		{"/api/v1/rum/sessions?app=a&", "browser", "", "s_browser ASC, session_id"},
		{"/api/v1/rum/releases?app=a&", "last_seen", "", "r_last DESC, r_version"},
		{"/api/v1/rum/releases?app=a&", "first_seen", "", "r_first DESC, r_version"},
		{"/api/v1/rum/releases?app=a&", "sessions", "", "r_sessions DESC, r_version"},
		{"/api/v1/rum/releases?app=a&", "error_sessions", "", "r_errors DESC, r_version"},
		{"/api/v1/rum/releases?app=a&", "error_free", "asc", "greatest(1 - r_errors / r_sessions, 0)) ASC, r_version"},
		{"/api/v1/rum/releases?app=a&", "crash_free", "", "greatest(1 - r_crashed / r_capturing, 0)) DESC, r_version"},
		{"/api/v1/rum/releases?app=a&", "version", "desc", "splitByChar('.', r_version)) DESC, r_version"},
	}
	for _, c := range cases {
		t.Run(c.path+c.sort, func(t *testing.T) {
			s, conn := newTestServer(t)
			path := c.path + "sort=" + c.sort + "&limit=25&offset=50"
			if c.dir != "" {
				path += "&dir=" + c.dir
			}
			if rec := get(t, s.Handler(), path); rec.Code != 200 {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(conn.sql[0], c.order) || !strings.Contains(conn.sql[0], "LIMIT 26 OFFSET 50") {
				t.Errorf("want order %q and a page:\n%s", c.order, conn.sql[0])
			}
		})
	}
}

func TestRUMListsRefuseWhatTheyDoNotName(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{
		"/api/v1/rum/apps?sort=nope",
		"/api/v1/rum/apps?dir=up",
		"/api/v1/rum/pages?app=a&sort=p75",
		"/api/v1/rum/pages?app=a&offset=-1",
		"/api/v1/rum/sessions?app=a&sort=nope",
		"/api/v1/rum/releases?app=a&sort=nope",
		"/api/v1/rum/releases?app=a&limit=0",
	} {
		if r := get(t, h, p); r.Code != 400 {
			t.Errorf("%s: status %d", p, r.Code)
		}
	}
	if len(conn.sql) != 0 {
		t.Errorf("queries ran for invalid requests: %v", conn.sql)
	}
}

// A request with no paging gets what it always got: the store's console pages nothing and talks to this server too.
func TestRUMListsKeepTheirOldDefaults(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"/api/v1/rum/apps", "ORDER BY v_views DESC"},
		{"/api/v1/rum/apps", "LIMIT 1001"},
		{"/api/v1/rum/pages?app=a", "ORDER BY p_views DESC"},
		{"/api/v1/rum/pages?app=a", "LIMIT 51"},
		{"/api/v1/rum/sessions?app=a", "ORDER BY s_last DESC"},
		{"/api/v1/rum/sessions?app=a", "LIMIT 51"},
		{"/api/v1/rum/releases?app=a", "ORDER BY r_last DESC"},
		{"/api/v1/rum/releases?app=a", "LIMIT 201"},
	}
	for _, c := range cases {
		s, conn := newTestServer(t)
		if r := get(t, s.Handler(), c.path); r.Code != 200 {
			t.Fatalf("%s: status %d %s", c.path, r.Code, r.Body)
		}
		if !strings.Contains(conn.sql[0], c.want) {
			t.Errorf("%s: want %q:\n%s", c.path, c.want, conn.sql[0])
		}
	}
}

// The ids of one person have to outlast the page: cut to the page's size, they would select nothing at all from page two onwards.
func TestRUMSessionsOfOneUserAreNotCutToThePage(t *testing.T) {
	s, conn := newTestServer(t)
	if r := get(t, s.Handler(), "/api/v1/rum/sessions?app=a&user_id=u&limit=25&offset=25"); r.Code != 200 {
		t.Fatalf("status %d %s", r.Code, r.Body)
	}
	// The test server's MaxRows is 1000, below rumMaxUserSessions.
	if len(conn.sql) != 1 || !strings.Contains(conn.sql[0], "LIMIT 1000") || !strings.Contains(conn.sql[0], "ORDER BY max(timestamp) DESC") {
		t.Errorf("%v", conn.sql)
	}
}

// The LCP of a page has to be read for the routes on that page, or a page past the first shows none.
func TestRUMPageLCPNamesItsRoutes(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sql, params, err := rumPageLCPSelect(sc, rumFilter{app: "a"}, []string{"/x", "/y"}, now.Add(-time.Hour), now).Build()
	if err != nil {
		t.Fatalf("does not build: %v", err)
	}
	if !strings.Contains(sql, "route IN ({v_routes:Array(String)})") || !strings.Contains(sql, "LIMIT 2") {
		t.Errorf("%s", sql)
	}
	if !strings.Contains(params["v_routes"], "/y") {
		t.Errorf("%v", params)
	}
}

// The console's largest page is 250. A server that gave it fewer would have Next step over the rows in between.
func TestRUMListsServeTheLargestPage(t *testing.T) {
	for _, path := range []string{"/api/v1/rum/apps?limit=250", "/api/v1/rum/pages?app=a&limit=250", "/api/v1/rum/sessions?app=a&limit=250", "/api/v1/rum/releases?app=a&limit=250"} {
		s, conn := newTestServer(t)
		if r := get(t, s.Handler(), path); r.Code != 200 {
			t.Fatalf("%s: status %d %s", path, r.Code, r.Body)
		}
		if !strings.Contains(conn.sql[0], "LIMIT 251") {
			t.Errorf("%s: %s", path, conn.sql[0])
		}
	}
}
