package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
)

type updatesEnv struct {
	*accountEnv
	queue    *updatereq.MemQueue
	now      time.Time
	checks   atomic.Int32
	versions *fakeVersionsPtr
}

// fakeVersionsPtr lets a test change the updater document after the server was built.
type fakeVersionsPtr struct{ info updatecheck.Info }

func (f *fakeVersionsPtr) Info(context.Context) updatecheck.Info { return f.info }

const notifyUpdater = `{"engine":"compose","mode":"notify","state":"available","target_version":"0.9.1","checked_at":"2026-09-14T10:00:00Z"}`

func newUpdatesEnv(t *testing.T, authn func(svc *auth.Service) auth.Authenticator, cfg auth.Config) *updatesEnv {
	t.Helper()
	st := memstore.New()
	cfg.CookieSecure, cfg.LoginMaxFailures = true, 5
	svc := auth.NewService(st, cfg, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), authn(svc), quietLog(), nil)
	s.SetAccounts(svc)
	e := &updatesEnv{accountEnv: &accountEnv{svc: svc, st: st, conn: conn}, now: time.Now().UTC()}
	e.queue = &updatereq.MemQueue{Now: func() time.Time { return e.now }}
	e.versions = &fakeVersionsPtr{info: updatecheck.Info{UpdateCheck: updatecheck.StatusEnabled, Updater: json.RawMessage(notifyUpdater)}}
	s.SetVersionSource(e.versions)
	s.SetUpdateRequests(e.queue, func(context.Context) error { e.checks.Add(1); return nil })
	e.h = s.srv.Handler
	return e
}

func sessionAuthn(svc *auth.Service) auth.Authenticator { return svc }

type versionBody struct {
	UpdateRequests *struct {
		CanRequest       bool    `json:"can_request"`
		UpdaterListening bool    `json:"updater_listening"`
		UpdaterPolledAt  *string `json:"updater_polled_at"`
		Latest           *struct {
			ID                      string  `json:"id"`
			Action                  string  `json:"action"`
			TargetVersion           string  `json:"target_version"`
			IgnoreMaintenanceWindow bool    `json:"ignore_maintenance_window"`
			State                   string  `json:"state"`
			RequestedByEmail        *string `json:"requested_by_email"`
		} `json:"latest"`
	} `json:"update_requests"`
}

func (e *updatesEnv) audit(t *testing.T, orgID string) map[string]auth.AuditEvent {
	t.Helper()
	evs, err := e.st.ListAuditEvents(context.Background(), orgID, auth.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]auth.AuditEvent{}
	for _, ev := range evs {
		if _, ok := out[ev.Action]; !ok {
			out[ev.Action] = ev
		}
	}
	return out
}

func TestUpdateRequestsAPI(t *testing.T) {
	e := newUpdatesEnv(t, sessionAuthn, auth.Config{})
	owner := e.login(t, "owner@example.com", ownerPassword)
	orgID := decode[orgJSON](t, owner.do(http.MethodGet, "/api/v1/orgs/current", nil)).ID

	v := decode[versionBody](t, owner.do(http.MethodGet, "/api/v1/version", nil))
	if v.UpdateRequests == nil || !v.UpdateRequests.CanRequest || v.UpdateRequests.Latest != nil || v.UpdateRequests.UpdaterListening {
		t.Fatalf("initial update_requests = %+v", v.UpdateRequests)
	}

	// CSRF as for every cookie-authenticated mutation.
	noCSRF := *owner
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPost, "/api/v1/version/check", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("check without CSRF: %d %s", rec.Code, rec.Body)
	}

	rec := owner.do(http.MethodPost, "/api/v1/version/check", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	v = decode[versionBody](t, rec)
	if l := v.UpdateRequests.Latest; l == nil || l.Action != "check" || l.State != "pending" || l.RequestedByEmail == nil || *l.RequestedByEmail != "owner@example.com" {
		t.Fatalf("check response = %s", rec.Body)
	}
	if e.checks.Load() != 1 {
		t.Fatalf("api release checks = %d", e.checks.Load())
	}
	if ev, ok := e.audit(t, orgID)["update.check_requested"]; !ok || ev.TargetType != "update_request" || ev.ActorEmail != "owner@example.com" {
		t.Fatalf("audit = %+v", e.audit(t, orgID))
	}

	// Rate limit: once per 30 s for the whole installation.
	e.now = e.now.Add(10 * time.Second)
	rec = owner.do(http.MethodPost, "/api/v1/version/check", nil)
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); rec.Code != http.StatusTooManyRequests || ra < 19 || ra > 21 {
		t.Fatalf("second check: %d Retry-After %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if e.checks.Load() != 1 {
		t.Fatalf("rate-limited check ran the release check")
	}

	// Update now.
	for _, tc := range []struct {
		body any
		code int
	}{
		{map[string]any{"target_version": "not a version"}, http.StatusBadRequest},
		{map[string]any{"target_version": version.String()}, http.StatusConflict}, // not newer
	} {
		if rec := owner.do(http.MethodPost, "/api/v1/version/update", tc.body); rec.Code != tc.code {
			t.Fatalf("update %v: %d %s", tc.body, rec.Code, rec.Body)
		}
	}
	rec = owner.do(http.MethodPost, "/api/v1/version/update", map[string]any{"target_version": "v0.9.1", "ignore_maintenance_window": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	req := decode[map[string]any](t, rec)
	if req["action"] != "apply" || req["target_version"] != "0.9.1" || req["state"] != "pending" || req["ignore_maintenance_window"] != true {
		t.Fatalf("update response = %v", req)
	}
	ev, ok := e.audit(t, orgID)["update.apply_requested"]
	if !ok || ev.Details["to"] != "0.9.1" || ev.Details["ignore_maintenance_window"] != true || ev.Details["engine"] != "compose" {
		t.Fatalf("apply audit = %+v", ev)
	}
	e.now = e.now.Add(time.Minute)
	if rec := owner.do(http.MethodPost, "/api/v1/version/update", map[string]any{"target_version": "0.9.1"}); rec.Code != http.StatusConflict {
		t.Fatalf("second update while pending: %d %s", rec.Code, rec.Body)
	}

	// The polling updater's heartbeat shows up as updater_listening.
	if err := e.queue.PutHeartbeat(context.Background(), updatereq.Heartbeat{Engine: "compose", Mode: "notify", PollSeconds: 10, PolledAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	v = decode[versionBody](t, owner.do(http.MethodGet, "/api/v1/version", nil))
	if !v.UpdateRequests.UpdaterListening || v.UpdateRequests.UpdaterPolledAt == nil || v.UpdateRequests.Latest.Action != "apply" {
		t.Fatalf("after heartbeat = %+v", v.UpdateRequests)
	}

	// Without an updater, or with one that is off, nothing is queued.
	for _, doc := range []string{"", `{"engine":"compose","mode":"off","state":"off","checked_at":"2026-09-14T10:00:00Z"}`,
		`{"engine":"compose","mode":"auto","state":"updating","checked_at":"2026-09-14T10:00:00Z"}`} {
		e.versions.info.Updater = json.RawMessage(doc)
		if rec := owner.do(http.MethodPost, "/api/v1/version/update", map[string]any{"target_version": "0.9.2"}); rec.Code != http.StatusConflict {
			t.Fatalf("updater %q: %d %s", doc, rec.Code, rec.Body)
		}
	}
}

func TestUpdateRequestsPermissions(t *testing.T) {
	p := &auth.Principal{Kind: auth.KindSession, UserID: "u-member", Email: "member@example.com", OrgID: "org-a", TenantID: "tenant-a", Role: auth.RoleMember}
	e := newUpdatesEnv(t, func(*auth.Service) auth.Authenticator { return stubAuthn{p} }, auth.Config{})
	c := &client{t: t, h: e.h}
	// A request made by an admin is visible to members without the email.
	if err := e.queue.Enqueue(context.Background(), &updatereq.Request{Action: updatereq.ActionCheck, RequestedByEmail: "owner@example.com"}, 0); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	v := decode[versionBody](t, c.do(http.MethodGet, "/api/v1/version", nil))
	if v.UpdateRequests == nil || v.UpdateRequests.CanRequest || v.UpdateRequests.Latest == nil || v.UpdateRequests.Latest.RequestedByEmail != nil {
		t.Fatalf("member view = %+v", v.UpdateRequests)
	}
	for _, tc := range []struct {
		name string
		p    auth.Principal
		code int
	}{
		{"member", auth.Principal{Kind: auth.KindSession, OrgID: "org-a", Role: auth.RoleMember}, http.StatusForbidden},
		{"viewer", auth.Principal{Kind: auth.KindSession, OrgID: "org-a", Role: auth.RoleViewer}, http.StatusForbidden},
		{"api key", auth.Principal{Kind: auth.KindAPIKey, OrgID: "org-a", Role: auth.RoleAdmin}, http.StatusForbidden},
		{"no org", auth.Principal{Kind: auth.KindSession, Role: auth.RoleAdmin}, http.StatusForbidden},
		{"admin", auth.Principal{Kind: auth.KindSession, UserID: "u-admin", Email: "admin@example.com", OrgID: "org-a", TenantID: "tenant-a", Role: auth.RoleAdmin}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*p = tc.p
			if rec := c.do(http.MethodPost, "/api/v1/version/check", nil); rec.Code != tc.code {
				t.Fatalf("check: %d %s", rec.Code, rec.Body)
			}
		})
	}
	if n := len(e.queue.All()); n != 2 {
		t.Fatalf("queued requests = %d, want 2 (seed + admin)", n)
	}

	// Multi-tenant installation: organization admins are not operators.
	*p = auth.Principal{Kind: auth.KindSession, UserID: "u-owner", OrgID: "org-a", Role: auth.RoleOwner}
	saas := newUpdatesEnv(t, func(*auth.Service) auth.Authenticator { return stubAuthn{p} }, auth.Config{SignupEnabled: true})
	sc := &client{t: t, h: saas.h}
	if rec := sc.do(http.MethodPost, "/api/v1/version/update", map[string]any{"target_version": "0.9.1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("signup-enabled update: %d %s", rec.Code, rec.Body)
	}
	if v := decode[versionBody](t, sc.do(http.MethodGet, "/api/v1/version", nil)); v.UpdateRequests.CanRequest {
		t.Fatal("can_request with OPENLOG_SIGNUP_ENABLED=true")
	}
}

func TestUpdateRequestsNeedAccounts(t *testing.T) {
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second),
		stubAuthn{p: &auth.Principal{Kind: auth.KindLicenseKey, OrgID: "o", TenantID: "t", Role: auth.RoleViewer}}, quietLog(), nil)
	s.SetUpdateRequests(&updatereq.MemQueue{}, nil)
	c := &client{t: t, h: s.srv.Handler}
	if rec := c.do(http.MethodPost, "/api/v1/version/check", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("static mode check route: %d", rec.Code)
	}
	if v := decode[map[string]any](t, c.do(http.MethodGet, "/api/v1/version", nil)); v["update_requests"] != nil {
		t.Fatalf("static mode update_requests = %v", v["update_requests"])
	}
}
