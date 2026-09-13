package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/fleettest"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/intsettings/intsettingstest"
)

type intEnv struct {
	*fleetEnv
	ints *intsettingstest.MemStore
}

func newIntEnv(t *testing.T, keys *secrets.Keyring) *intEnv {
	t.Helper()
	fe := newFleetEnv(t)
	ints := intsettingstest.NewMemStore()
	// newFleetEnv's handler has no integration routes: rebuild the server with both managers.
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(fe.conn, "openlog", time.Second), fe.svc, quietLog(), nil)
	s.SetAccounts(fe.svc)
	s.SetFleet(fleet.NewManager(fe.fleet, nil, fleet.ManagerOptions{}))
	s.SetIntegrationSettings(intsettings.NewManager(ints, intsettings.ManagerOptions{Keys: keys}))
	fe.h = s.srv.Handler
	fe.owner.h = s.srv.Handler
	return &intEnv{fleetEnv: fe, ints: ints}
}

func apiKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

const settingsPath = "/api/v1/integrations/settings"

func TestIntegrationSettingsAPI(t *testing.T) {
	e := newIntEnv(t, apiKeyring(t))
	body := map[string]any{"integration": "redis", "match": map[string]any{"port": 6380}, "endpoint": "127.0.0.1:6380",
		"username": "default", "password": "top-secret-pw"}
	rec := e.owner.do(http.MethodPost, settingsPath, body)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "top-secret-pw") || strings.Contains(rec.Body.String(), "ol1:") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[map[string]any](t, rec)
	id := created["id"].(string)
	match := created["match"].(map[string]any)
	if created["password_set"] != true || created["host_id"] != nil || match["port"] != float64(6380) || created["updated_by_email"] != "owner@example.com" ||
		len(created["databases"].([]any)) != 0 {
		t.Fatalf("created = %s", rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, settingsPath, body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, settingsPath, map[string]any{"integration": "nginx", "password": "x"}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "invalid_argument") {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	host := "h1"
	if rec := e.owner.do(http.MethodPost, settingsPath, map[string]any{"integration": "nginx", "host_id": host, "endpoint": "http://127.0.0.1/nginx_status"}); rec.Code != http.StatusCreated {
		t.Fatalf("host setting: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, settingsPath, map[string]any{"integration": "docker", "host_id": "h2"}); rec.Code != http.StatusCreated {
		t.Fatalf("other host: %d %s", rec.Code, rec.Body)
	}

	// PUT: omitted password keeps it, "" clears it.
	upd := map[string]any{"integration": "redis", "match": map[string]any{"port": 6380}, "endpoint": "127.0.0.1:6381", "enabled": false}
	rec = e.owner.do(http.MethodPut, settingsPath+"/"+id, upd)
	if got := decode[map[string]any](t, rec); rec.Code != http.StatusOK || got["password_set"] != true || got["enabled"] != false || got["username"] != "" {
		t.Fatalf("keep password: %d %s", rec.Code, rec.Body)
	}
	upd["password"] = nil
	if got := decode[map[string]any](t, e.owner.do(http.MethodPut, settingsPath+"/"+id, upd)); got["password_set"] != true {
		t.Fatalf("null password: %v", got)
	}
	upd["password"] = ""
	if got := decode[map[string]any](t, e.owner.do(http.MethodPut, settingsPath+"/"+id, upd)); got["password_set"] != false {
		t.Fatalf("clear password: %v", got)
	}
	if rec := e.owner.do(http.MethodPut, settingsPath+"/00000000-0000-4000-8000-000000000000", upd); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}

	// Lists: all settings, or the host's effective settings with its revisions.
	type listResp struct {
		Items []map[string]any `json:"items"`
		Host  *struct {
			HostID               string  `json:"host_id"`
			Revision             string  `json:"revision"`
			AppliedRevision      string  `json:"applied_revision"`
			AppliedAt            *string `json:"applied_at"`
			RemoteConfigDisabled bool    `json:"remote_config_disabled"`
		} `json:"host"`
	}
	rec = e.owner.do(http.MethodGet, settingsPath, nil)
	if l := decode[listResp](t, rec); len(l.Items) != 3 || l.Host != nil || !strings.Contains(rec.Body.String(), `"host":null`) {
		t.Fatalf("list all: %s", rec.Body)
	}
	l := decode[listResp](t, e.owner.do(http.MethodGet, settingsPath+"?host_id=h1", nil))
	if len(l.Items) != 2 || l.Items[0]["integration"] != "redis" || l.Items[1]["integration"] != "nginx" || l.Host == nil ||
		!strings.HasPrefix(l.Host.Revision, "sha256:") || l.Host.AppliedRevision != "" || l.Host.AppliedAt != nil {
		t.Fatalf("list h1: %+v", l)
	}
	// The agent reports the revision in sync (recorded on the host row).
	now := time.Now()
	report := func(rev string) {
		if err := e.fleet.UpsertHosts(context.Background(), []fleet.HostRecord{{TenantID: "tenant-a", SyncAt: now,
			Report: fleet.HostReport{HostID: "h1", HostName: "web-1", Version: "0.3.0", IntegrationsConfigRevision: rev}}}); err != nil {
			t.Fatal(err)
		}
	}
	report(l.Host.Revision)
	l2 := decode[listResp](t, e.owner.do(http.MethodGet, settingsPath+"?host_id=h1", nil))
	if l2.Host.AppliedRevision != l.Host.Revision || l2.Host.Revision != l.Host.Revision || l2.Host.AppliedAt == nil || l2.Host.RemoteConfigDisabled {
		t.Fatalf("applied: %+v", l2.Host)
	}
	report(intsettings.RevisionDisabled)
	if l3 := decode[listResp](t, e.owner.do(http.MethodGet, settingsPath+"?host_id=h1", nil)); !l3.Host.RemoteConfigDisabled {
		t.Fatalf("disabled: %+v", l3.Host)
	}
	if l := decode[listResp](t, e.owner.do(http.MethodGet, settingsPath+"?host_id=never-synced", nil)); len(l.Items) != 1 || l.Host.AppliedAt != nil {
		t.Fatalf("unknown host: %+v", l)
	}
	if rec := e.owner.do(http.MethodGet, settingsPath+"?host_id="+strings.Repeat("x", 257), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("long host id: %d", rec.Code)
	}

	// API keys (viewer) read, never write.
	key := decode[map[string]any](t, e.owner.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "ro"}))
	viewer := &client{t: t, h: e.h, bearer: key["key"].(string)}
	if rec := viewer.do(http.MethodGet, settingsPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list: %d", rec.Code)
	}
	for _, c := range []struct{ method, path string }{{http.MethodPost, settingsPath}, {http.MethodPut, settingsPath + "/" + id}, {http.MethodDelete, settingsPath + "/" + id}} {
		if rec := viewer.do(c.method, c.path, body); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer %s: %d", c.method, rec.Code)
		}
	}
	noCSRF := *e.owner
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodDelete, settingsPath+"/"+id, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF: %d", rec.Code)
	}
	if rec := e.owner.do(http.MethodDelete, settingsPath+"/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodDelete, settingsPath+"/"+id, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}

	var actions []string
	for _, a := range e.ints.Audit() {
		actions = append(actions, a.Action)
		b, _ := json.Marshal(a.Details)
		if strings.Contains(string(b), "top-secret-pw") || a.TargetType != "integration_setting" || a.ActorEmail != "owner@example.com" {
			t.Fatalf("audit entry %+v %s", a, b)
		}
	}
	want := "integration_setting.create,integration_setting.create,integration_setting.create," +
		"integration_setting.update,integration_setting.update,integration_setting.update,integration_setting.delete"
	if strings.Join(actions, ",") != want {
		t.Fatalf("audit = %v", actions)
	}
}

func TestIntegrationSettingsWithoutSecretsKey(t *testing.T) {
	e := newIntEnv(t, nil)
	rec := e.owner.do(http.MethodPost, settingsPath, map[string]any{"integration": "mysql", "password": "x"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "OPENLOG_SECRETS_KEY") {
		t.Fatalf("password without key: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, settingsPath, map[string]any{"integration": "mysql", "endpoint": "db:3306"}); rec.Code != http.StatusCreated {
		t.Fatalf("without password: %d %s", rec.Code, rec.Body)
	}
}

func TestIntegrationSettingsRoutesNeedAccounts(t *testing.T) {
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second),
		stubAuthn{p: &auth.Principal{Kind: auth.KindLicenseKey, OrgID: "o", TenantID: "t", Role: auth.RoleViewer}}, quietLog(), nil)
	s.SetFleet(fleet.NewManager(fleettest.NewMemStore(), nil, fleet.ManagerOptions{}))
	s.SetIntegrationSettings(intsettings.NewManager(intsettingstest.NewMemStore(), intsettings.ManagerOptions{}))
	c := &client{t: t, h: s.srv.Handler}
	if rec := c.do(http.MethodGet, settingsPath, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("static mode route: %d", rec.Code)
	}
}
