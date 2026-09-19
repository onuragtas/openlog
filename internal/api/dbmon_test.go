package api

import (
	"strings"
	"testing"
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
		"GROUP BY fingerprint", "ORDER BY d_read DESC, fingerprint", "LIMIT 10",
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
