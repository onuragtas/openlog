package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getAs(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The filter values must reach ClickHouse as bound parameters, never as text in the statement. A service or
// host name is caller-supplied, and the query layer's guard is what stands between it and the tenant
// boundary — a value spliced into the SQL would walk straight past that guard.
func TestProfileFiltersAreBoundParameters(t *testing.T) {
	s, conn := newTestServer(t)
	rec := getAs(t, s.Handler(), "/api/v1/profiles/flame?service=orders&type=cpu&environment=prod&host=h-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(conn.sql) == 0 {
		t.Fatal("no statement executed")
	}
	for _, sql := range conn.sql {
		for _, literal := range []string{"orders", "'cpu'", "prod", "h-1"} {
			if strings.Contains(sql, literal) {
				t.Errorf("filter value %q embedded in the statement: %s", literal, sql)
			}
		}
	}
}

// A profile type is required wherever values are summed: nanoseconds and bytes do not add up, so a flame
// graph over an unstated type would be a number with no unit rather than a useful default.
func TestProfileTypeAndServiceAreRequired(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, path := range []string{
		"/api/v1/profiles/flame?service=orders",
		"/api/v1/profiles/flame?type=cpu",
		"/api/v1/profiles/functions?service=orders",
		"/api/v1/profiles/functions?type=cpu",
	} {
		if rec := getAs(t, h, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, rec.Code)
		}
	}
	if len(conn.sql) != 0 {
		t.Errorf("%d statements executed for refused requests", len(conn.sql))
	}
}
