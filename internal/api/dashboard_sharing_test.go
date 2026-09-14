package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
)

func TestDashboardSharingEndpoints(t *testing.T) {
	const (
		orgID   = "11111111-1111-1111-1111-111111111111"
		aliceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	pr := func(kind auth.Kind, user string, role auth.Role) *auth.Principal {
		return &auth.Principal{Kind: kind, UserID: user, Email: user + "@example.com", OrgID: orgID, OrgName: "Secret Org", TenantID: "tenant-secret", Role: role}
	}
	authn := switchAuth{
		"alice":  pr(auth.KindSession, aliceID, auth.RoleMember),
		"admin":  pr(auth.KindSession, "cccccccc-cccc-cccc-cccc-cccccccccccc", auth.RoleAdmin),
		"viewer": pr(auth.KindSession, "dddddddd-dddd-dddd-dddd-dddddddddddd", auth.RoleViewer),
	}
	store := dashboardtest.New()
	store.Emails[aliceID] = "alice@example.com"
	store.Members[orgID] = []string{"alice@example.com"}
	store.Tenants[orgID] = "tenant-secret"
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), authn,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.SetDashboards(dashboard.NewManager(store))
	h := s.Handler()
	do := func(as, method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if as != "" {
			req.Header.Set("X-Test-As", as)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
	}

	rec := do("alice", "POST", "/api/v1/dashboards", `{"name": "Checkout", "pages": [{"name": "Overview", "widgets": [
	  {"title": "Errors", "visualization": "billboard", "layout": {"x": 0, "y": 0, "w": 6, "h": 3}, "query": "SELECT count(*) FROM Log WHERE severity = 'ERROR'"},
	  {"title": "Notes", "visualization": "markdown", "layout": {"x": 6, "y": 0, "w": 6, "h": 3}, "markdown": "hi"}]}]}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		ID      string
		Version int
		Pages   []struct {
			Widgets []struct{ ID string }
		}
	}
	decode(rec, &d)
	base := "/api/v1/dashboards/" + d.ID
	if rec := do("alice", "PUT", base, `{"name": "Checkout v2", "version": 1}`); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		as, method, path, body string
		status                 int
		contains               string
	}{
		{"viewer", "GET", base + "/versions", "", 200, `"can_restore":false,"current_version":2`},
		{"alice", "GET", base + "/versions", "", 200, `"version":1`},
		{"viewer", "GET", base + "/versions/1", "", 200, `"previous_version":null,"changes":null,"differences_from_current":{"name":true`},
		{"viewer", "GET", base + "/versions/2", "", 200, `"previous_version":1`},
		{"viewer", "GET", base + "/versions/x", "", 404, "not found"},
		{"viewer", "GET", base + "/versions/9", "", 404, "not found"},
		{"viewer", "POST", base + "/versions/1/restore", `{"version": 2}`, 403, "role (viewer)"},
		{"alice", "POST", base + "/versions/1/restore", `{}`, 400, "version is required"},
		{"alice", "POST", base + "/versions/1/restore", `{"version": 1}`, 409, "reload"},
		{"alice", "POST", base + "/versions/1/restore", `{"version": 2}`, 200, `"version":3`},

		{"viewer", "GET", "/api/v1/dashboards/settings", "", 200, `"share_links_enabled":false,"report_domains":[],"updated_at":null,"can_edit":false`},
		{"alice", "PUT", "/api/v1/dashboards/settings", `{"share_links_enabled": true}`, 403, "only admins and owners"},
		{"admin", "PUT", "/api/v1/dashboards/settings", `{}`, 400, "share_links_enabled is required"},
		{"alice", "POST", base + "/shares", `{"range": "24h", "expires_at": "` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"}`, 409, "disabled"},
		{"admin", "PUT", "/api/v1/dashboards/settings", `{"share_links_enabled": true, "report_domains": ["partner.io"]}`, 200, `"share_links_enabled":true,"report_domains":["partner.io"]`},
		{"alice", "POST", base + "/shares", `{"range": "24h"}`, 400, "expires_at is required"},
		{"viewer", "POST", base + "/shares", `{"range": "24h"}`, 403, "role (viewer)"},
		{"viewer", "GET", base + "/shares", "", 403, ""},
	} {
		rec := do(tc.as, tc.method, tc.path, tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s %s %s: %d %s", tc.as, tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	rec = do("alice", "POST", base+"/shares", `{"label": "TV", "range": "24h", "expires_at": "`+time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)+`"}`)
	if rec.Code != 201 {
		t.Fatalf("create share: %d %s", rec.Code, rec.Body)
	}
	var sh struct {
		ID, Token, Path string
		Active          bool
	}
	decode(rec, &sh)
	if !sh.Active || !strings.HasPrefix(sh.Token, "olds_") || sh.Path != "/shared/dashboards/"+sh.Token {
		t.Fatalf("share %+v", sh)
	}
	if rec := do("admin", "GET", base+"/shares", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), sh.Token) || !strings.Contains(rec.Body.String(), `"share_links_enabled":true`) {
		t.Errorf("list shares must not return tokens: %d %s", rec.Code, rec.Body)
	}

	// Public endpoints: no authentication, restrictive headers, nothing about the organization, users or queries.
	pub := "/api/v1/public/dashboards/" + sh.Token
	rec = do("", "GET", pub, "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"Checkout","description":""`) || !strings.Contains(body, `"time_range":{"range":"24h","from":null,"to":null}`) {
		t.Fatalf("public dashboard: %d %s", rec.Code, body)
	}
	for _, secret := range []string{"alice", orgID, "tenant-secret", "Secret Org", "SELECT", "created_by", "version", d.ID} {
		if strings.Contains(body, secret) {
			t.Errorf("public dashboard leaks %q: %s", secret, body)
		}
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'", "X-Robots-Tag": "noindex, nofollow"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	rec = do("", "GET", pub+"/widgets/"+d.Pages[0].Widgets[0].ID+"/result", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"single"`) || !strings.Contains(rec.Body.String(), `"table":"","rows_read":0,"bytes_read":0`) {
		t.Fatalf("public widget result: %d %s", rec.Code, rec.Body)
	}
	conn.mu.Lock()
	lastSQL := conn.sql[len(conn.sql)-1]
	conn.mu.Unlock()
	if !strings.Contains(lastSQL, "tenant_id") {
		t.Errorf("shared widget query is not tenant scoped: %s", lastSQL)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", pub + "/widgets/" + d.Pages[0].Widgets[1].ID + "/result", 404}, // markdown
		{"GET", pub + "/widgets/nope/result", 404},
		{"POST", pub, 404},                 // only GET is routed; other methods reach the /api/ catch-all
		{"GET", "/api/v1/dashboards", 401}, // the token is not a credential
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+sh.Token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	// Reports.
	rec = do("alice", "POST", base+"/reports", `{"frequency": "daily", "hour": 9, "timezone": "Europe/Istanbul", "recipients": ["alice@example.com", "ops@partner.io"]}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"range":"24h"`) || strings.Contains(rec.Body.String(), `"next_run_at":null`) {
		t.Fatalf("create report: %d %s", rec.Code, rec.Body)
	}
	var rp struct{ ID string }
	decode(rec, &rp)
	for _, tc := range []struct {
		as, method, path, body string
		status                 int
		contains               string
	}{
		{"alice", "POST", base + "/reports", `{"frequency": "daily", "hour": 9, "recipients": ["x@evil.com"]}`, 400, "neither a member"},
		{"viewer", "GET", base + "/reports", "", 403, ""},
		{"admin", "GET", base + "/reports", "", 200, `"recipients":["alice@example.com","ops@partner.io"]`},
		{"alice", "PUT", base + "/reports/" + rp.ID, `{"frequency": "weekly", "weekday": 1, "hour": 8, "recipients": ["alice@example.com"], "enabled": false}`, 200, `"enabled":false`},
		{"alice", "DELETE", base + "/reports/" + rp.ID, "", 204, ""},
		{"alice", "DELETE", base + "/reports/" + rp.ID, "", 404, ""},
	} {
		rec := do(tc.as, tc.method, tc.path, tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s %s %s: %d %s", tc.as, tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	// Revoked links and failed lookups.
	if rec := do("alice", "DELETE", base+"/shares/"+sh.ID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := do("", "GET", pub, ""); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("revoked link: %d", rec.Code)
	}
	s.publicShares = newShareLimiter(shareLimits{perIP: 1000, perToken: 1000, missesPerIP: 2})
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, do("", "GET", "/api/v1/public/dashboards/olds_unknown", "").Code)
	}
	if codes[0] != 404 || codes[1] != 404 || codes[2] != 429 {
		t.Errorf("failed lookups are rate limited per IP: %v", codes)
	}
}
