package api

import (
	"context"
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
	"github.com/onuragtas/openlog/internal/renderer"
)

func TestDashboardRenderEndpoints(t *testing.T) {
	const (
		orgID   = "11111111-1111-1111-1111-111111111111"
		aliceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	ctx := context.Background()
	authn := switchAuth{"alice": &auth.Principal{Kind: auth.KindSession, UserID: aliceID, Email: "alice@example.com", OrgID: orgID,
		TenantID: "tenant-secret", Role: auth.RoleMember}}
	store := dashboardtest.New()
	store.Emails[aliceID] = "alice@example.com"
	store.Members[orgID] = []string{"alice@example.com"}
	store.Tenants[orgID] = "tenant-secret"
	m := dashboard.NewManager(store)
	member := dashboard.Viewer{UserID: aliceID, CanWrite: true}
	d, err := m.Create(ctx, orgID, dashboard.Input{Name: "Checkout", Pages: []dashboard.Page{{Name: "Overview", Widgets: []dashboard.Widget{
		{Title: "Errors", Visualization: "billboard", Layout: dashboard.Layout{W: 6, H: 3}, Query: "SELECT count(*) FROM Log"},
	}}}}, member)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.CreateReport(ctx, orgID, d.ID, dashboard.ReportInput{Frequency: "daily", Hour: 9, Timezone: "UTC", Recipients: []string{"alice@example.com"}}, member)
	if err != nil {
		t.Fatal(err)
	}

	newServer := func(key []byte) *Server {
		s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(&recordingConn{}, "openlog", time.Second), authn,
			slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		s.SetDashboards(m)
		if key != nil {
			s.SetRenderKey(key)
		}
		return s
	}
	key := renderer.KeyFromSecret("server-secret-server-secret-server-secret")
	h := newServer(key).Handler()
	do := func(authz, as, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		if as != "" {
			req.Header.Set("X-Test-As", as)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	from, to := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC), time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	claims := renderer.Claims{OrgID: orgID, TenantID: "tenant-secret", DashboardID: d.ID, ReportID: rep.ID, From: from.UnixMilli(), To: to.UnixMilli()}
	token, err := renderer.NewReportToken(key, claims, time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	rec := do("Bearer "+token, "", "/api/v1/render/dashboard")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"Checkout"`) || !strings.Contains(body, `"time_range":{"range":null,"from":"2026-09-13T09:00:00`) {
		t.Fatalf("render dashboard: %d %s", rec.Code, body)
	}
	for _, secret := range []string{"alice", orgID, "tenant-secret", "SELECT", rep.ID, d.ID} {
		if strings.Contains(body, secret) {
			t.Errorf("render dashboard leaks %q: %s", secret, body)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers %v", rec.Header())
	}
	rec = do("Bearer "+token, "", "/api/v1/render/dashboard/widgets/"+d.Pages[0].Widgets[0].ID+"/result")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"single"`) || !strings.Contains(rec.Body.String(), `"rows_read":0`) {
		t.Fatalf("render widget result: %d %s", rec.Code, rec.Body)
	}

	forgedKey, _ := renderer.NewReportToken(renderer.KeyFromSecret("renderer-shared-token-renderer-shared"), claims, time.Now(), time.Minute)
	expired, _ := renderer.NewReportToken(key, claims, time.Now().Add(-time.Hour), 5*time.Minute)
	otherReport := claims
	otherReport.ReportID = "33333333-3333-3333-3333-333333333333"
	unknown, _ := renderer.NewReportToken(key, otherReport, time.Now(), time.Minute)
	for name, tc := range map[string]struct {
		authz, as string
		status    int
	}{
		"no token":          {"", "", 401},
		"session only":      {"", "alice", 401},
		"share token":       {"Bearer olds_" + strings.Repeat("a", 43), "", 401},
		"other key":         {"Bearer " + forgedKey, "", 401},
		"expired":           {"Bearer " + expired, "", 401},
		"unknown report":    {"Bearer " + unknown, "", 404},
		"basic credentials": {"Basic " + token, "", 401},
	} {
		if rec := do(tc.authz, tc.as, "/api/v1/render/dashboard"); rec.Code != tc.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Render tokens are not credentials for other endpoints.
	if rec := do("Bearer "+token, "", "/api/v1/dashboards/"+d.ID); rec.Code != 401 {
		t.Errorf("render token on the dashboards API: %d", rec.Code)
	}

	// A disabled report cannot be rendered.
	disabled := false
	if _, err := m.UpdateReport(ctx, orgID, d.ID, rep.ID, dashboard.ReportInput{Frequency: "daily", Hour: 9, Timezone: "UTC",
		Recipients: []string{"alice@example.com"}, Enabled: &disabled}, member); err != nil {
		t.Fatal(err)
	}
	if rec := do("Bearer "+token, "", "/api/v1/render/dashboard"); rec.Code != 404 {
		t.Errorf("disabled report: %d %s", rec.Code, rec.Body)
	}

	// Without a render key the routes do not exist.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/render/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	newServer(nil).Handler().ServeHTTP(rec, req)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "Checkout") {
		t.Errorf("render endpoint without key: %d %s", rec.Code, rec.Body)
	}
}
