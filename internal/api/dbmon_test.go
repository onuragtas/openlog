package api

import (
	"strings"
	"testing"
	"time"
)

// Database monitoring reads (dbmon.go, D-138): one instance at a time, every value bound as a parameter, invalid
// requests rejected before ClickHouse.

func TestDBQueriesShape(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	rec := get(t, h, "/api/v1/db/queries?instance=db1.internal%3A5432&sort=reads&db=shop&q=orders&limit=10")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if len(conn.sql) != 2 {
		t.Fatalf("statements = %d: %v", len(conn.sql), conn.sql)
	}
	sql := conn.sql[0]
	for _, want := range []string{
		"`openlog`.db_query_stats WHERE (tenant_id = {tenant_id:String})",
		"instance = {d_inst:String}", "db_name = {d_db:String}", "positionCaseInsensitive(query_text, {d_q:String}) > 0",
		"GROUP BY fingerprint", "ORDER BY d_read DESC, fingerprint", "LIMIT 11",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q:\n%s", want, sql)
		}
	}
	for _, literal := range []string{"db1.internal", "shop", "orders"} {
		if strings.Contains(sql, literal) {
			t.Errorf("literal %q spliced into SQL:\n%s", literal, sql)
		}
	}
}

func TestDBRejectsInvalidRequests(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{
		"/api/v1/db/queries",
		"/api/v1/db/queries?instance=x&sort=nope",
		"/api/v1/db/queries?instance=" + strings.Repeat("x", dbMaxInstanceID+1),
		"/api/v1/db/queries/abc?instance=x",
		"/api/v1/db/queries/0?instance=x",
		"/api/v1/db/activity",
		"/api/v1/db/activity/waits",
		"/api/v1/db/activity/waits?instance=x&sort=nope",
		"/api/v1/db/activity/queries",
		"/api/v1/db/activity/queries?instance=x&dir=sideways",
		"/api/v1/db/instances?sort=nope",
		"/api/v1/db/queries?instance=x&offset=-1",
		"/api/v1/db/sessions?instance=x&at=yesterday",
		"/api/v1/db/lookup?statement=SELECT%201",
	} {
		if rec := get(t, h, p); rec.Code != 400 {
			t.Errorf("%s: status %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(conn.sql) != 0 {
		t.Errorf("queries ran for invalid requests: %v", conn.sql)
	}
}

func TestDBQueryDetailUnknownIs404(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := get(t, s.Handler(), "/api/v1/db/queries/12345?instance=db1%3A5432"); rec.Code != 404 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

func TestCountBlocked(t *testing.T) {
	ss := []dbSessionJSON{
		{SessionID: "1"}, // head: blocks 2 and, through 2, 3 and 4
		{SessionID: "2", BlockingSessionIDs: []string{"1"}}, // blocks 3 and 4
		{SessionID: "3", BlockingSessionIDs: []string{"2"}},
		{SessionID: "4", BlockingSessionIDs: []string{"2", "1"}},
		// A deadlock the server has not broken yet: 5 and 6 wait for each other.
		{SessionID: "5", BlockingSessionIDs: []string{"6"}},
		{SessionID: "6", BlockingSessionIDs: []string{"5"}},
	}
	countBlocked(ss)
	want := map[string]int{"1": 3, "2": 2, "3": 0, "4": 0, "5": 1, "6": 1}
	for _, s := range ss {
		if s.Blocks != want[s.SessionID] {
			t.Errorf("session %s blocks %d, want %d", s.SessionID, s.Blocks, want[s.SessionID])
		}
	}
}

// Every sort a list names has to survive the query layer, which refuses fragments it does not like, and a page has to ask for one row more than
// it shows and skip what came before it.
func TestDBListsSortAndPageInTheStatement(t *testing.T) {
	cases := []struct {
		path  string
		sort  string
		dir   string
		order string
	}{
		{path: "/api/v1/db/queries?instance=x", sort: "time", order: "d_time DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "calls", order: "d_calls DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "avg", order: "d_avg DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "rows", order: "d_rows DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "errors", dir: "asc", order: "d_err ASC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "reads", order: "d_read DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "rows_per_call", order: "d_rows / greatest(d_calls, 1) DESC, fingerprint"},
		{path: "/api/v1/db/queries?instance=x", sort: "statement", order: "d_text ASC, fingerprint"},
		{path: "/api/v1/db/activity/waits?instance=x", sort: "samples", order: "d_n DESC, d_type, wait_event"},
		{path: "/api/v1/db/activity/waits?instance=x", sort: "type", order: "d_type ASC, d_type, wait_event"},
		{path: "/api/v1/db/activity/waits?instance=x", sort: "event", dir: "desc", order: "wait_event DESC, d_type, wait_event"},
		{path: "/api/v1/db/activity/queries?instance=x", sort: "samples", order: "d_n DESC, fingerprint"},
		{path: "/api/v1/db/activity/queries?instance=x", sort: "statement", order: "d_text ASC, fingerprint"},
		{path: "/api/v1/db/activity/queries?instance=x", sort: "wait", order: "d_wait ASC, fingerprint"},
	}
	for _, c := range cases {
		t.Run(c.path+" "+c.sort, func(t *testing.T) {
			s, conn := newTestServer(t)
			path := c.path + "&sort=" + c.sort + "&limit=25&offset=50"
			if c.dir != "" {
				path += "&dir=" + c.dir
			}
			if rec := get(t, s.Handler(), path); rec.Code != 200 {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			var found bool
			for _, sql := range conn.sql {
				if strings.Contains(sql, "ORDER BY "+c.order) && strings.Contains(sql, "LIMIT 26 OFFSET 50") {
					found = true
				}
			}
			if !found {
				t.Errorf("no statement orders by %q and pages: %v", c.order, conn.sql)
			}
		})
	}
}

// The console's largest page is 250. A server that gave it fewer would have Next step over the rows in between.
func TestDBListsServeTheLargestPage(t *testing.T) {
	s, conn := newTestServer(t)
	if rec := get(t, s.Handler(), "/api/v1/db/queries?instance=x&limit=250"); rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(conn.sql[0], "LIMIT 251") {
		t.Errorf("%s", conn.sql[0])
	}
}

func dbFloat(v float64) *float64 { return &v }

func TestDBInstancesSortInGo(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rows := func() []dbInstanceJSON {
		return []dbInstanceJSON{
			{Instance: "a", HostName: "", TotalTimeMs: 10, Calls: 1, AvgMs: dbFloat(10), AvgActiveSessions: nil, TopWait: "Lock", last: t0.Add(2 * time.Hour)},
			{Instance: "b", HostName: "zeta", TotalTimeMs: 30, Calls: 2, AvgMs: nil, AvgActiveSessions: dbFloat(2), TopWait: "", last: t0},
			{Instance: "c", HostName: "alpha", TotalTimeMs: 30, Calls: 3, AvgMs: dbFloat(5), AvgActiveSessions: dbFloat(1), TopWait: "CPU", last: t0.Add(time.Hour)},
			// Only session samples: it has no statement numbers, and a time sort still has to reach it.
			{Instance: "d", HostName: "mid", AvgActiveSessions: dbFloat(9), TopWait: "IO", last: t0.Add(3 * time.Hour)},
		}
	}
	cases := []struct {
		sort string
		asc  bool
		want string
	}{
		{"time", false, "bcad"}, // b and c tie, and the name decides
		{"time", true, "dabc"},
		{"calls", false, "cbad"},
		{"avg", false, "acbd"}, // nothing to say about b and d: last in either direction
		{"avg", true, "cabd"},
		{"aas", false, "dbca"},
		{"aas", true, "cbda"},
		{"instance", false, "dcba"},
		{"host", true, "cdba"}, // a has no host: last
		{"host", false, "bdca"},
		{"wait", true, "cdab"},
		{"last_seen", false, "dacb"},
	}
	for _, c := range cases {
		got := rows()
		sortDBInstances(got, listRequest{sort: c.sort, asc: c.asc})
		var order strings.Builder
		for _, d := range got {
			order.WriteString(d.Instance)
		}
		if order.String() != c.want {
			t.Errorf("%s asc=%v: %s, want %s", c.sort, c.asc, order.String(), c.want)
		}
	}
}

func TestPageOfMerged(t *testing.T) {
	cases := []struct {
		offset, limit int
		want          []int
		more          bool
	}{
		{0, 2, []int{1, 2}, true},
		{2, 2, []int{3, 4}, true},
		{4, 2, []int{5}, false},
		{5, 2, []int{}, false},
		{50, 2, []int{}, false},
	}
	for _, c := range cases {
		got, more := pageOfMerged([]int{1, 2, 3, 4, 5}, listRequest{offset: c.offset, limit: c.limit})
		if len(got) != len(c.want) || more != c.more {
			t.Errorf("offset %d: %v %v", c.offset, got, more)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("offset %d: %v", c.offset, got)
			}
		}
	}
}
