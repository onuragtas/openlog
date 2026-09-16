package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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

// switchAuth authenticates as the principal selected by the X-Test-As header.
type switchAuth map[string]*auth.Principal

func (a switchAuth) Authenticate(r *http.Request) (*auth.Principal, error) {
	p, ok := a[r.Header.Get("X-Test-As")]
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	cp := *p
	return &cp, nil
}

func TestDashboardEndpoints(t *testing.T) {
	const orgID = "11111111-1111-1111-1111-111111111111"
	pr := func(kind auth.Kind, user string, role auth.Role) *auth.Principal {
		return &auth.Principal{Kind: kind, UserID: user, Email: user + "@example.com", OrgID: orgID, TenantID: "t1", Role: role}
	}
	authn := switchAuth{
		"alice":  pr(auth.KindSession, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", auth.RoleMember),
		"bob":    pr(auth.KindSession, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", auth.RoleMember),
		"admin":  pr(auth.KindSession, "cccccccc-cccc-cccc-cccc-cccccccccccc", auth.RoleAdmin),
		"viewer": pr(auth.KindSession, "dddddddd-dddd-dddd-dddd-dddddddddddd", auth.RoleViewer),
		"key":    pr(auth.KindAPIKey, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", auth.RoleViewer),
		// A key with a writing role (D-133). It has no user id: a key owns no dashboard.
		"key-member": {Kind: auth.KindAPIKey, APIKeyID: "ffffffff-ffff-ffff-ffff-ffffffffffff", APIKeyName: "terraform",
			OrgID: orgID, TenantID: "t1", Role: auth.RoleMember},
		"noorg": {Kind: auth.KindSession, UserID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"},
	}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(&recordingConn{}, "openlog", time.Second), authn,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
		req.Header.Set("X-Test-As", as)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("alice", "GET", "/api/v1/dashboards", ""); rec.Code != 404 {
		t.Fatalf("without a manager (static auth mode): %d", rec.Code)
	}
	s.SetDashboards(dashboard.NewManager(dashboardtest.New()))
	h = s.Handler()

	body := `{"name": "Checkout", "variables": [{"name": "host", "type": "text"}],
	  "pages": [{"name": "Overview", "widgets": [{"title": "Errors", "visualization": "line", "layout": {"x": 0, "y": 0, "w": 6, "h": 3},
	    "query": "SELECT count(*) FROM Log WHERE host.name = {{host}} TIMESERIES"}]}]}`
	rec := do("alice", "POST", "/api/v1/dashboards", body)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		ID, Visibility  string
		Version         int
		CanEdit         bool    `json:"can_edit"`
		CreatedByUserID *string `json:"created_by_user_id"`
		Pages           []struct {
			ID      string
			Widgets []struct {
				ID         string
				Thresholds []any
			}
		}
		Variables []struct{ Values []string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !d.CanEdit || d.Version != 1 || d.Visibility != "org" || len(d.Pages) != 1 || d.Pages[0].Widgets[0].Thresholds == nil ||
		d.Variables[0].Values == nil || d.CreatedByUserID == nil || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("create response %s", rec.Body)
	}
	path := "/api/v1/dashboards/" + d.ID

	for _, tc := range []struct {
		as, method, path, body string
		status                 int
		contains               string
	}{
		{"viewer", "GET", path, "", 200, `"can_edit":false`},
		{"key", "GET", path, "", 200, `"can_edit":false`},
		{"key", "GET", "/api/v1/dashboards?q=check", "", 200, `"widget_count":1`},
		{"viewer", "GET", "/api/v1/dashboards?q=nomatch", "", 200, `"dashboards":[]`},
		{"noorg", "GET", "/api/v1/dashboards", "", 403, "not a member"},
		{"nobody", "GET", "/api/v1/dashboards", "", 401, ""},
		{"viewer", "POST", "/api/v1/dashboards", body, 403, "role (viewer)"},
		{"key", "POST", "/api/v1/dashboards", body, 403, "this API key's role (viewer)"},
		// The same endpoint accepts a key whose role may write; it owns nothing, so the
		// dashboard has no creator.
		{"key-member", "POST", "/api/v1/dashboards", body, 201, `"created_by_user_id":null`},
		{"bob", "PUT", path, `{"name": "X", "version": 1}`, 403, "only the creator"},
		{"bob", "DELETE", path, "", 403, "only the creator"},
		{"viewer", "DELETE", path, "", 403, "role (viewer)"},
		{"alice", "PUT", path, `{"name": "X", "version": 7}`, 409, "reload"},
		{"alice", "PUT", path, `{"name": "X"}`, 400, "version is required"},
		{"alice", "PUT", path, `{"name": "X", "version": 1, "pages": [{"name": "P", "widgets": [{"visualization": "line", "layout": {"x": 0, "y": 0, "w": 13, "h": 1}, "query": "SELECT count(*) FROM Log"}]}]}`, 400, "12-column grid"},
		{"alice", "POST", "/api/v1/dashboards", `{"name": "Bad", "pages": [{"name": "P", "widgets": [{"visualization": "table", "layout": {"x": 0, "y": 0, "w": 3, "h": 1}, "query": "SELECT count(*) FROM Log WHERE tenant_id = 'x'"}]}]}`, 400, "line 1, column 32: unknown attribute"},
		{"alice", "POST", "/api/v1/dashboards", `nope`, 400, "invalid JSON body"},
		{"alice", "GET", "/api/v1/dashboards/not-a-uuid", "", 404, "not found"},
		{"admin", "PUT", path, `{"name": "Renamed by admin", "version": 1, "pages": [{"id": "` + d.Pages[0].ID + `", "name": "Overview", "widgets": []}]}`, 200, `"version":2`},
		{"alice", "POST", path + "/widgets", `{"widget": {"title": "Added", "visualization": "billboard", "query": "SELECT count(*) FROM Span"}}`, 200, `"version":3`},
		{"bob", "POST", path + "/duplicate", `{}`, 201, `"name":"Renamed by admin (copy)"`},
		{"bob", "POST", path + "/duplicate", ``, 201, `"can_edit":true`},
		{"viewer", "GET", path + "/export", "", 200, `"openlog_dashboard":1`},
		{"bob", "POST", "/api/v1/dashboards/import", `{"openlog_dashboard": 1, "name": "Imported", "visibility": "private", "pages": []}`, 201, `"visibility":"private"`},
		{"bob", "POST", "/api/v1/dashboards/import", `{"openlog_dashboard": 3, "name": "Imported"}`, 400, "openlog_dashboard must be 1"},
		{"alice", "GET", "/api/v1/dashboards", "", 200, `"name":"Renamed by admin"`},
		{"admin", "DELETE", path, "", 204, ""},
		{"alice", "GET", path, "", 404, ""},
	} {
		rec := do(tc.as, tc.method, tc.path, tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s %s %s: %d %s", tc.as, tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	// The private import of bob is not listed for alice.
	if rec := do("alice", "GET", "/api/v1/dashboards", ""); strings.Contains(rec.Body.String(), "Imported") {
		t.Errorf("private dashboard listed for another user: %s", rec.Body)
	}
}
