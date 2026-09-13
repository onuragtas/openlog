package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/fleettest"
)

type fleetEnv struct {
	*accountEnv
	fleet *fleettest.MemStore
	orgID string
	owner *client
}

func newFleetEnv(t *testing.T) *fleetEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	fs := fleettest.NewMemStore()
	s.SetFleet(fleet.NewManager(fs, nil, fleet.ManagerOptions{}))
	e := &fleetEnv{accountEnv: &accountEnv{h: s.srv.Handler, svc: svc, st: st, conn: conn}, fleet: fs}
	e.owner = e.login(t, "owner@example.com", ownerPassword)
	org := decode[orgJSON](t, e.owner.do(http.MethodGet, "/api/v1/orgs/current", nil))
	e.orgID = org.ID
	fs.TenantOrgs["tenant-a"] = org.ID
	now := time.Now()
	if err := fs.UpsertHosts(context.Background(), []fleet.HostRecord{{TenantID: "tenant-a", SyncAt: now, Report: fleet.HostReport{
		HostID: "h1", HostName: "web-1", Version: "0.3.0", OS: "linux", Arch: "amd64", InstallMethod: "tarball", UpdateCapable: true, UpdateState: "idle"}}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestFleetPolicyAPI(t *testing.T) {
	e := newFleetEnv(t)
	rec := e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("get policy: %d %s", rec.Code, rec.Body)
	}
	p := decode[map[string]any](t, rec)
	if p["mode"] != "auto" || p["is_default"] != true || p["channel"] != "stable" || len(p["waves"].([]any)) != 3 || p["pinned_version"] != nil {
		t.Fatalf("default policy = %s", rec.Body)
	}

	bad := map[string]any{"mode": "auto", "channel": "stable", "target": "latest", "waves": []int{50, 20, 100}, "wave_soak_minutes": 5, "halt_failure_rate": 0.1, "maintenance_windows": []any{}}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", bad); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_argument") {
		t.Fatalf("invalid waves: %d %s", rec.Code, rec.Body)
	}
	good := map[string]any{"mode": "notify", "channel": "beta", "target": "pinned", "pinned_version": "v0.4.0", "waves": []int{5, 100},
		"wave_soak_minutes": 1, "halt_failure_rate": 0.2, "maintenance_windows": []map[string]any{{"days": []string{"sat"}, "start": "01:00", "end": "03:00"}}}
	noCSRF := *e.owner
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPut, "/api/v1/fleet/policy", good); rec.Code != http.StatusForbidden {
		t.Fatalf("put without CSRF: %d", rec.Code)
	}
	rec = e.owner.do(http.MethodPut, "/api/v1/fleet/policy", good)
	if rec.Code != http.StatusOK {
		t.Fatalf("put policy: %d %s", rec.Code, rec.Body)
	}
	p = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))
	if p["mode"] != "notify" || p["pinned_version"] != "0.4.0" || p["is_default"] != false {
		t.Fatalf("stored policy = %v", p)
	}
	if got := e.fleet.AuditActions(); len(got) != 1 || got[0] != "fleet.policy.update" {
		t.Errorf("audit = %v", got)
	}

	// API keys act as viewers: fleet reads are allowed, changes are not.
	created := decode[map[string]any](t, e.owner.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "ro"}))
	viewer := &client{t: t, h: e.h, bearer: created["key"].(string)}
	if rec := viewer.do(http.MethodGet, "/api/v1/fleet/summary", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer summary: %d %s", rec.Code, rec.Body)
	}
	if rec := viewer.do(http.MethodPut, "/api/v1/fleet/policy", good); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer put policy: %d", rec.Code)
	}
	if rec := viewer.do(http.MethodPost, "/api/v1/fleet/rollback", map[string]string{"to_version": "0.3.0"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer rollback: %d", rec.Code)
	}
	anon := &client{t: t, h: e.h}
	if rec := anon.do(http.MethodGet, "/api/v1/fleet/policy", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

func TestFleetHostsAndOverridesAPI(t *testing.T) {
	e := newFleetEnv(t)
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/override", map[string]string{"action": "freeze"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action: %d", rec.Code)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/missing/override", map[string]string{"action": "hold"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", rec.Code)
	}
	rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/override", map[string]string{"action": "hold"})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["action"] != "hold" {
		t.Fatalf("hold: %d %s", rec.Code, rec.Body)
	}
	type hostsResp struct {
		Hosts []struct {
			HostID string `json:"host_id"`
			Agent  struct {
				Version string `json:"version"`
			} `json:"agent"`
			Override *struct {
				Action string `json:"action"`
			} `json:"override"`
			Status string `json:"status"`
		} `json:"hosts"`
		NextCursor *string `json:"next_cursor"`
	}
	hr := decode[hostsResp](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts?q=web", nil))
	if len(hr.Hosts) != 1 || hr.Hosts[0].Override == nil || hr.Hosts[0].Status != "hold" || hr.Hosts[0].Agent.Version != "0.3.0" || hr.NextCursor != nil {
		t.Fatalf("hosts = %+v", hr)
	}
	if hr := decode[hostsResp](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts?version=9.9.9", nil)); len(hr.Hosts) != 0 {
		t.Fatalf("version filter: %+v", hr)
	}
	if rec := e.owner.do(http.MethodGet, "/api/v1/fleet/hosts?limit=x", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	if rec := e.owner.do(http.MethodDelete, "/api/v1/fleet/hosts/h1/override", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := e.owner.do(http.MethodDelete, "/api/v1/fleet/hosts/h1/override", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete is not idempotent: %d", rec.Code)
	}
}

func TestFleetRolloutsAPI(t *testing.T) {
	e := newFleetEnv(t)
	now := time.Now()
	r := &fleet.Rollout{OrgID: e.orgID, Action: fleet.ActionUpgrade, FromVersion: "0.3.0", ToVersion: "0.4.0", Waves: []int{10, 50, 100},
		WaveStartedAt: now, WaveSoakMinutes: 60, HaltFailureRate: 0.05, State: fleet.RolloutActive, CreatedAt: now}
	if err := e.fleet.CreateRollout(context.Background(), r, ""); err != nil {
		t.Fatal(err)
	}
	list := decode[map[string][]map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/rollouts", nil))
	got := list["rollouts"]
	if len(got) != 1 || got[0]["id"] != r.ID || got[0]["wave_percent"] != float64(10) || got[0]["next_wave_at"] == nil || got[0]["created_by_email"] != nil {
		t.Fatalf("rollouts = %v", got)
	}
	rec := e.owner.do(http.MethodPost, "/api/v1/fleet/rollouts/"+r.ID+"/pause", nil)
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["state"] != "paused" {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, "/api/v1/fleet/rollouts/"+r.ID+"/pause", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "failed_precondition") {
		t.Fatalf("second pause: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, "/api/v1/fleet/rollouts/"+r.ID+"/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, "/api/v1/fleet/rollouts/00000000-0000-4000-8000-999999999999/pause", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rollout: %d", rec.Code)
	}
	// No release catalog configured: rollback cannot be validated.
	if rec := e.owner.do(http.MethodPost, "/api/v1/fleet/rollback", map[string]string{"to_version": "0.3.0"}); rec.Code != http.StatusConflict {
		t.Fatalf("rollback without catalog: %d %s", rec.Code, rec.Body)
	}
	sum := decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/summary", nil))
	cat := sum["catalog"].(map[string]any)
	if sum["total_hosts"] != float64(1) || sum["current_rollout"] == nil || cat["status"] != "disabled" {
		t.Fatalf("summary = %v", sum)
	}
}

func TestFleetRoutesNeedAccounts(t *testing.T) {
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second),
		stubAuthn{p: &auth.Principal{Kind: auth.KindLicenseKey, OrgID: "o", TenantID: "t", Role: auth.RoleViewer}}, quietLog(), nil)
	s.SetFleet(fleet.NewManager(fleettest.NewMemStore(), nil, fleet.ManagerOptions{}))
	c := &client{t: t, h: s.srv.Handler}
	if rec := c.do(http.MethodGet, "/api/v1/fleet/policy", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("static mode fleet route: %d", rec.Code)
	}
}
