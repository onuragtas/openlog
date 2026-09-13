package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUnknownHostIs404(t *testing.T) {
	paths := []string{
		"/api/v1/hosts/h1/inventory",
		"/api/v1/hosts/h1/inventory?category=package",
		"/api/v1/hosts/h1/services",
		"/api/v1/hosts/h1/metrics?name=system.cpu.utilization",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			s, conn := newTestServer(t)
			rec := get(t, s.Handler(), p)
			var body struct {
				Error struct{ Code, Message string }
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != http.StatusNotFound || body.Error.Code != "not_found" || body.Error.Message != "host not found" {
				t.Errorf("status %d body %s", rec.Code, rec.Body)
			}
			// Only the tenant-scoped existence check ran; no data query for an unknown host.
			if len(conn.sql) != 1 {
				t.Fatalf("statements = %d: %v", len(conn.sql), conn.sql)
			}
			sql := conn.sql[0]
			if !strings.Contains(sql, "`openlog`.hosts WHERE (tenant_id = {tenant_id:String}) AND (host_id = {host_id:String})") {
				t.Errorf("existence check not tenant-scoped: %s", sql)
			}

			s, conn = newTestServer(t)
			conn.hostKnown = true
			if rec := get(t, s.Handler(), p); rec.Code != http.StatusOK {
				t.Errorf("known host: status %d %s", rec.Code, rec.Body)
			}
			if len(conn.sql) < 2 {
				t.Errorf("known host: data query not executed: %v", conn.sql)
			}
		})
	}
	// Parameter errors are still reported before the existence check.
	s, conn := newTestServer(t)
	if rec := get(t, s.Handler(), "/api/v1/hosts/h1/metrics?name=x&agg=median"); rec.Code != http.StatusBadRequest || len(conn.sql) != 0 {
		t.Errorf("bad agg on unknown host: %d, %d statements", rec.Code, len(conn.sql))
	}
}

func TestMetricResourceFilters(t *testing.T) {
	s, conn := newTestServer(t)
	conn.hostKnown = true
	h := s.Handler()
	q := url.Values{
		"name":                                {"redis.memory.used"},
		"from":                                {"2026-09-13T00:00:00Z"},
		"to":                                  {"2026-09-13T23:00:00Z"}, // > 6h: would read the rollup without filters
		"resource.openlog.discovery.id":       {"redis"},
		"resource.openlog.discovery.instance": {"/usr/bin/redis-server'; DROP TABLE x --"},
		"group_by":                            {"db,resource.postgresql.database.name"},
	}
	rec := get(t, h, "/api/v1/hosts/h1/metrics?"+q.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	// Statements: host check, metadata. The mock returns no metadata rows, so no data query runs;
	// the metadata query must carry the filters.
	meta := conn.sql[len(conn.sql)-1]
	for _, want := range []string{
		"(resource_attributes[{res_key_0:String}] = {res_value_0:String})",
		"(resource_attributes[{res_key_1:String}] = {res_value_1:String})",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("filter %q missing: %s", want, meta)
		}
	}
	for _, leaked := range []string{"redis", "DROP", "discovery"} {
		if strings.Contains(meta, leaked) {
			t.Errorf("value %q interpolated into SQL: %s", leaked, meta)
		}
	}

	for _, bad := range []struct{ query, want string }{
		{"resource.host.name=web-1", "resource.host.name: unsupported resource attribute filter"},
		{"resource.tenant_id=other", "unsupported resource attribute filter"},
		{"resource.openlog.discovery.id=", "exactly one non-empty value"},
		{"resource.openlog.discovery.id=a&resource.openlog.discovery.id=b", "exactly one non-empty value"},
		{"resource.server.port=" + strings.Repeat("1", 1025), "at most 1024 bytes"},
		{"group_by=resource.env", "group_by: resource.env: unsupported resource attribute"},
		{"resource.server.port=1&resource.server.address=a&resource.service.instance.id=b&resource.openlog.integration.id=c&resource.openlog.discovery.id=d", "at most 4 resource.* filters"},
	} {
		s, conn := newTestServer(t) // unknown host: parameter errors come before the existence check
		rec := get(t, s.Handler(), "/api/v1/hosts/h1/metrics?name=x&"+bad.query)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), bad.want) {
			t.Errorf("%s: status %d body %s", truncate(bad.query, 60), rec.Code, rec.Body)
		}
		if len(conn.sql) != 0 {
			t.Errorf("%s: query executed despite invalid filter", truncate(bad.query, 60))
		}
	}
}

func TestLogAttributeFilters(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	q := url.Values{
		"host_id":                   {"h1"},
		"attr.openlog.log.source":   {"file"},
		"attr.log.file.path":        {"/var/log/nginx/access.log'; DROP TABLE x --"},
		"attr.openlog.discovery.id": {"nginx"},
	}
	rec := get(t, h, "/api/v1/logs?"+q.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	sql := conn.sql[len(conn.sql)-1]
	for i := range 3 {
		n := string(rune('0' + i))
		if !strings.Contains(sql, "(attributes[{attr_key_"+n+":String}] = {attr_value_"+n+":String})") {
			t.Errorf("filter %s missing: %s", n, sql)
		}
	}
	for _, leaked := range []string{"nginx", "DROP", "file.path", "openlog.log.source"} {
		if strings.Contains(sql, leaked) {
			t.Errorf("value %q interpolated into SQL: %s", leaked, sql)
		}
	}
	if !strings.HasPrefix(sql[strings.Index(sql, "`openlog`.logs")+len("`openlog`.logs"):], " WHERE (tenant_id = {tenant_id:String})") {
		t.Errorf("logs query not tenant-scoped: %s", sql)
	}

	for _, bad := range []struct{ query, want string }{
		{"attr.password=hunter2", "attr.password: unsupported attribute filter (supported: log.file.name, log.file.path, log.iostream, openlog.discovery.id, openlog.log.source, openlog.syslog.identifier, openlog.systemd.unit)"},
		{"attr.log.file.path=", "exactly one non-empty value"},
		{"attr.log.file.path=/a&attr.log.file.path=/b", "exactly one non-empty value"},
		{"attr.openlog.systemd.unit=" + strings.Repeat("x", 1025), "at most 1024 bytes"},
		{"attr.LOG.FILE.PATH=/a", "unsupported attribute filter"},
		{"attr.tenant_id=other", "unsupported attribute filter"},
	} {
		n := len(conn.sql)
		rec := get(t, h, "/api/v1/logs?"+bad.query)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), bad.want) {
			t.Errorf("%s: status %d body %s", truncate(bad.query, 60), rec.Code, rec.Body)
		}
		if len(conn.sql) != n {
			t.Errorf("%s: query executed despite invalid filter", truncate(bad.query, 60))
		}
	}
}
