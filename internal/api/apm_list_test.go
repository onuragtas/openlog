package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestApmListSorts holds every sort name a list has ever answered to, because an older console still sends them, and what each new one
// puts in the statement where the statement does the sorting.
func TestApmListSorts(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, c := range []struct {
		path string
		code int
		sql  string // part of the last statement, when ClickHouse does the sorting
	}{
		{path: "/api/v1/apm/services/orders/databases", code: 200, sql: "ORDER BY a_dsum DESC, db_system, db_name, db_statement_normalized LIMIT 101"},
		{path: "/api/v1/apm/services/orders/databases?sort=time", code: 200, sql: "ORDER BY a_dsum DESC"},
		{path: "/api/v1/apm/services/orders/databases?sort=calls", code: 200, sql: "ORDER BY a_req DESC"},
		{path: "/api/v1/apm/services/orders/databases?sort=slowest", code: 200, sql: "ORDER BY a_avg DESC"},
		{path: "/api/v1/apm/services/orders/databases?sort=errors", code: 200, sql: "ORDER BY a_err DESC"},
		{path: "/api/v1/apm/services/orders/databases?sort=error_rate&dir=asc", code: 200, sql: "ORDER BY a_err / greatest(a_req, 1e-9) ASC"},
		{path: "/api/v1/apm/services/orders/databases?sort=statement", code: 200, sql: "ORDER BY db_statement_normalized ASC"},
		{path: "/api/v1/apm/services/orders/databases?limit=10&offset=20", code: 200, sql: "LIMIT 11 OFFSET 20"},
		{path: "/api/v1/apm/services/orders/databases?sort=p95", code: 400},
		{path: "/api/v1/apm/services/orders/databases?dir=up", code: 400},
		{path: "/api/v1/apm/services/orders/databases?offset=-1", code: 400},

		{path: "/api/v1/apm/traces", code: 200, sql: "ORDER BY timestamp DESC, trace_id, span_id LIMIT 1 BY trace_id LIMIT 51"},
		{path: "/api/v1/apm/traces?sort=timestamp&dir=asc", code: 200, sql: "ORDER BY timestamp ASC"},
		{path: "/api/v1/apm/traces?sort=duration", code: 200, sql: "ORDER BY duration_ns DESC"},
		{path: "/api/v1/apm/traces?service=orders&limit=10&offset=30", code: 200, sql: "LIMIT 11 OFFSET 30"},
		{path: "/api/v1/apm/traces?sort=name", code: 400},

		{path: "/api/v1/apm/services/orders/transactions?sort=time", code: 200},
		{path: "/api/v1/apm/services/orders/transactions?sort=throughput", code: 200},
		{path: "/api/v1/apm/services/orders/transactions?sort=slowest", code: 200},
		{path: "/api/v1/apm/services/orders/transactions?sort=errors", code: 200},
		{path: "/api/v1/apm/services/orders/transactions?sort=apdex&dir=asc&limit=10&offset=10", code: 200},
		{path: "/api/v1/apm/services/orders/transactions?sort=bogus", code: 400},

		{path: "/api/v1/apm/errors?sort=count", code: 200},
		{path: "/api/v1/apm/errors?sort=last_seen", code: 200},
		{path: "/api/v1/apm/errors?sort=first_seen", code: 200},
		{path: "/api/v1/apm/errors?sort=assignee&dir=desc&offset=50", code: 200},
		{path: "/api/v1/apm/services/orders/errors?sort=comments", code: 200},
		{path: "/api/v1/apm/errors?sort=name", code: 400},

		{path: "/api/v1/apm/services", code: 200},
		{path: "/api/v1/apm/services?sort=p95&dir=asc&limit=10&offset=10", code: 200},
		{path: "/api/v1/apm/services?sort=bogus", code: 400},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Header.Set("openlog-license-key", "key-a")
		rec := httptest.NewRecorder()
		conn.sql = nil
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.path, rec.Code, rec.Body, c.code)
			continue
		}
		if c.sql != "" && (len(conn.sql) == 0 || !strings.Contains(conn.sql[len(conn.sql)-1], c.sql)) {
			t.Errorf("%s: %v, want %q", c.path, conn.sql, c.sql)
		}
		if c.code == 200 && !strings.Contains(rec.Body.String(), `"has_more":false`) {
			t.Errorf("%s: no has_more in %s", c.path, rec.Body)
		}
	}
}

// Without a page of its own a request for the services still gets every one of them: the map and older consoles read it that way.
func TestApmServicesHasNoPageUnlessAsked(t *testing.T) {
	l, err := apmServicesList.parse(httptest.NewRequest(http.MethodGet, "/x", nil), 1000)
	if err != nil || l.limit != 1000 || l.offset != 0 || l.sort != "name" || !l.asc {
		t.Errorf("%+v %v", l, err)
	}
}

func TestSortPage(t *testing.T) {
	type row struct {
		name string
		v    *float64
	}
	f := func(v float64) *float64 { return &v }
	rows := func() []row {
		return []row{{"b", f(2)}, {"a", f(2)}, {"none", nil}, {"c", f(1)}, {"d", f(3)}}
	}
	keys := map[string]func(row) rowKey{
		"v":    func(r row) rowKey { return optKey(r.v) },
		"name": func(r row) rowKey { return textKey(r.name) },
	}
	tie := func(a, b row) int { return strings.Compare(a.name, b.name) }
	for _, c := range []struct {
		name string
		l    listRequest
		want []string
		more bool
	}{
		// The same value keeps a fixed order, and no value stays last in both directions.
		{"largest first", listRequest{sort: "v", limit: 10}, []string{"d", "a", "b", "c", "none"}, false},
		{"smallest first", listRequest{sort: "v", asc: true, limit: 10}, []string{"c", "a", "b", "d", "none"}, false},
		{"first page", listRequest{sort: "v", limit: 2}, []string{"d", "a"}, true},
		{"second page", listRequest{sort: "v", limit: 2, offset: 2}, []string{"b", "c"}, true},
		{"last page is exactly full", listRequest{sort: "v", limit: 1, offset: 4}, []string{"none"}, false},
		{"past the end", listRequest{sort: "v", limit: 2, offset: 50}, []string{}, false},
		{"a name", listRequest{sort: "name", asc: true, limit: 2}, []string{"a", "b"}, true},
	} {
		page, more := sortPage(rows(), c.l, keys, tie)
		got := []string{}
		for _, r := range page {
			got = append(got, r.name)
		}
		if !reflect.DeepEqual(got, c.want) || more != c.more || page == nil {
			t.Errorf("%s: %v %v, want %v %v", c.name, got, more, c.want, c.more)
		}
	}
}

func TestServiceMatches(t *testing.T) {
	sv := &apmServiceJSON{serviceIdentity: serviceIdentity{ServiceName: "Orders", ServiceNamespace: "shop", Environment: "prod"}, Language: "go", Version: "1.4.2"}
	for q, want := range map[string]bool{"": true, "ord": true, "ORD": true, "shop prod": true, "1.4": true, "ord staging": false, "php": false} {
		if got := serviceMatches(sv, q); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
}
