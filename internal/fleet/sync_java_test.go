package fleet_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/fleet/fleettest"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
	"github.com/onuragtas/openlog/internal/tenant"
	lib "github.com/onuragtas/openlog/libs/release"
)

const javaAgentBody = `{"host_id":"h1","host_name":"app-1","agent":{"name":"openlog-infra-agent","version":"0.9.1","commit":"abc",
	"os":"linux","arch":"amd64","install_method":"tarball","update_capable":true},
	"update":{"state":"idle"},"config_hash":"sha256:x",
	"java_agent":{"mode":"manual","source":"local","capable":true,"managed":false,"current_version":"","status":"not_found",
	  "link_path":"/opt/openlog/openlog-javaagent.jar","link_state":"missing",
	  "jvms":[{"pid":42,"name":"java","agent_path":"/opt/openlog/openlog-javaagent.jar","loaded_version":"","managed":true}]}}`

func TestSyncJavaAgent(t *testing.T) {
	s, _ := testutil.NewSigner()
	dir := t.TempDir()
	specs := []testutil.ReleaseSpec{{Version: "0.9.1", JavaAgent: true, ReleasedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
	if err := testutil.WriteMirror(dir, s, specs, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := tenant.ParseStatic(licenseKey + "=" + tenantID)
	store := fleettest.NewMemStore()
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	rec := fleet.NewRecorder(store, fleet.RecorderOptions{})
	svc := fleet.NewSyncService(res, fleet.NewStateCache(store, fleet.StateCacheOptions{TTL: time.Nanosecond}), rec, cat,
		fleet.SyncOptions{ServeMirror: true, MirrorBaseURL: "https://ingest.example.com", Now: func() time.Time { return now }})
	mux := http.NewServeMux()
	svc.Register(mux)
	post := func() fleet.SyncResponse {
		req := httptest.NewRequest(http.MethodPost, fleet.SyncPath, strings.NewReader(javaAgentBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("openlog-license-key", licenseKey)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return decodeSync(t, w)
	}

	j := post().JavaAgent
	if j == nil || j.Mode != "manual" || j.Version != "agent" || j.TargetVersion != "" || j.Manifest != "" || j.Reason != "manual" {
		t.Fatalf("default java_agent = %+v", j)
	}

	org := store.OrgOf(tenantID)
	pol := fleet.DefaultPolicy()
	pol.WaveSoakMinutes = 0
	pol.JavaAgent = fleet.JavaAgentPolicy{Mode: "auto", Version: "agent"}
	if err := store.PutPolicy(context.Background(), org, pol, "", now); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	j = post().JavaAgent
	if j.Mode != "auto" || j.Reason != "offer" || j.TargetVersion != "0.9.1" {
		t.Fatalf("auto java_agent = %+v", j)
	}
	raw, err := base64.StdEncoding.DecodeString(j.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := lib.VerifyManifest(raw, []byte(j.Signature), []ed25519.PublicKey{s.PublicKey()})
	if err != nil {
		t.Fatalf("manifest from sync does not verify: %v", err)
	}
	if _, ok := m.Artifact(lib.ComponentJavaAgent, lib.PlatformAny, lib.PlatformAny, lib.FormatJar); !ok {
		t.Error("manifest has no java-agent jar")
	}
	if want := "https://ingest.example.com/v1/openlog/releases/0.9.1/" + testutil.JavaArtifactName("0.9.1"); j.DownloadURL != want {
		t.Errorf("download_url = %s, want %s", j.DownloadURL, want)
	}

	if err := store.PutJavaOverride(context.Background(), org, fleet.JavaOverride{HostID: "h1", Mode: "off", UpdatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if j = post().JavaAgent; j.Mode != "off" || j.TargetVersion != "" || j.Reason != "mode_off" {
		t.Fatalf("override java_agent = %+v", j)
	}

	// The report is stored with the host.
	rec.Flush(context.Background())
	h, err := store.GetHost(context.Background(), org, "h1")
	if err != nil || h.JavaAgent == nil || len(h.JavaAgent.JVMs) != 1 || h.JavaAgent.LinkPath != "/opt/openlog/openlog-javaagent.jar" {
		t.Fatalf("stored host = %+v %v", h.JavaAgent, err)
	}
}
