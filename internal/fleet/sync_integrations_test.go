package fleet_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/intsettings/intsettingstest"
	"github.com/onuragtas/openlog/internal/tenant"
)

func syncBody(revision string) string {
	return strings.Replace(agentBody, `"config_hash":"sha256:x"`, `"config_hash":"sha256:x","integrations_config_revision":"`+revision+`"`, 1)
}

func TestSyncIntegrationsConfig(t *testing.T) {
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	ints := intsettingstest.NewMemStore()
	e := newSyncEnv(t, fleet.SyncOptions{Keys: kr})
	e.store.Integrations = ints
	org := e.store.OrgOf(tenantID)
	m := intsettings.NewManager(ints, intsettings.ManagerOptions{Keys: kr})
	ctx := context.Background()
	pw := "hunter2-redis"
	if _, err := m.Create(ctx, org, intsettings.Input{Integration: "redis", Endpoint: "127.0.0.1:6379", Password: &pw}, intsettings.Actor{}); err != nil {
		t.Fatal(err)
	}
	other := "other-host"
	if _, err := m.Create(ctx, org, intsettings.Input{Integration: "nginx", HostID: &other, Endpoint: "http://127.0.0.1/status"}, intsettings.Actor{}); err != nil {
		t.Fatal(err)
	}

	// No applied revision: the whole effective config for h1 (the other host's row excluded), plaintext password.
	resp := decodeSync(t, e.post(syncBody(""), nil))
	ic := resp.IntegrationsConfig
	if ic == nil || len(ic.Items) != 1 || ic.Items[0].Integration != "redis" || ic.Items[0].Password != pw || ic.Items[0].Match != nil {
		t.Fatalf("integrations_config = %+v", ic)
	}
	eff, rev, _ := m.ForHost(ctx, org, "h1")
	if ic.Revision != rev || len(eff) != 1 {
		t.Fatalf("revision %s, want %s", ic.Revision, rev)
	}
	e.rec.Flush(ctx)
	if h, _ := e.store.GetHost(ctx, org, "h1"); h.IntegrationsConfigRevision != "" {
		t.Fatalf("recorded revision = %q", h.IntegrationsConfigRevision)
	}

	// Applied: nothing sent, the reported revision is recorded.
	rec := e.post(syncBody(rev), nil)
	if resp := decodeSync(t, rec); resp.IntegrationsConfig != nil || !strings.Contains(rec.Body.String(), `"integrations_config":null`) {
		t.Fatalf("unchanged config sent again: %s", rec.Body)
	}
	e.rec.Flush(ctx)
	if h, _ := e.store.GetHost(ctx, org, "h1"); h.IntegrationsConfigRevision != rev {
		t.Fatalf("recorded revision = %q", h.IntegrationsConfigRevision)
	}

	// "disabled" never matches: the config keeps being offered.
	if resp := decodeSync(t, e.post(syncBody(intsettings.RevisionDisabled), nil)); resp.IntegrationsConfig == nil {
		t.Fatal("disabled agent got no config")
	}

	// The state cache is per TTL: a fresh service sees a changed password as a new revision.
	e2 := newSyncEnv(t, fleet.SyncOptions{Keys: kr})
	e2.store.Integrations = ints
	all, _ := ints.ListSettings(ctx, org)
	for _, s := range all {
		if s.Integration == "redis" {
			npw := "new-password"
			if _, err := m.Update(ctx, org, s.ID, intsettings.Input{Integration: "redis", Endpoint: s.Endpoint, Password: &npw}, intsettings.Actor{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ic := decodeSync(t, e2.post(syncBody(rev), nil)).IntegrationsConfig; ic == nil || ic.Revision == rev || ic.Items[0].Password != "new-password" {
		t.Fatalf("after password change: %+v", ic)
	}

	// Without the key the password cannot be decrypted: the object is omitted, the password never appears.
	e3 := newSyncEnv(t, fleet.SyncOptions{})
	e3.store.Integrations = ints
	rec = e3.post(syncBody(""), nil)
	if resp := decodeSync(t, rec); resp.IntegrationsConfig != nil || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("undecryptable config sent: %s", rec.Body)
	}

	// Store down with nothing cached: no config.
	e4 := newSyncEnv(t, fleet.SyncOptions{Keys: kr})
	e4.store.Integrations = ints
	e4.store.SetErr(context.DeadlineExceeded)
	if resp := decodeSync(t, e4.post(syncBody(""), nil)); resp.IntegrationsConfig != nil {
		t.Fatalf("config without state: %+v", resp.IntegrationsConfig)
	}
}

func TestSyncIntegrationsConfigStaticMode(t *testing.T) {
	res, _ := tenant.ParseStatic(licenseKey + "=" + tenantID)
	svc := fleet.NewSyncService(res, nil, nil, nil, fleet.SyncOptions{})
	mux := http.NewServeMux()
	svc.Register(mux)
	e := &syncEnv{h: mux}
	rec := e.post(syncBody(""), nil)
	if resp := decodeSync(t, rec); resp.IntegrationsConfig != nil {
		t.Fatalf("static mode config: %s", rec.Body)
	}
}
