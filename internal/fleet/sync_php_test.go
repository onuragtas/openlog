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

const phpAgentBody = `{"host_id":"h1","host_name":"web-1","agent":{"name":"openlog-infra-agent","version":"0.9.1","commit":"abc",
	"os":"linux","arch":"amd64","install_method":"tarball","update_capable":true},
	"update":{"state":"idle"},"config_hash":"sha256:x",
	"php_agent":{"mode":"manual","source":"local","capable":true,"managed_by":"none","version":"",
	  "runtimes":[{"bin":"/usr/sbin/php-fpm8.2","version":"8.2.29","api":"20220829","zts":false,"debug":false,"libc":"glibc",
	    "scan_dir":"/etc/php/8.2/fpm/conf.d","module":"20220829-nts-glibc","supported":true,"enabled":false,"loaded":false}]}}`

func TestSyncPHPAgent(t *testing.T) {
	s, _ := testutil.NewSigner()
	dir := t.TempDir()
	specs := []testutil.ReleaseSpec{{Version: "0.9.1", PHPAgent: true, ReleasedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
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
	svc := fleet.NewSyncService(res, fleet.NewStateCache(store, fleet.StateCacheOptions{TTL: time.Nanosecond}), fleet.NewRecorder(store, fleet.RecorderOptions{}), cat,
		fleet.SyncOptions{ServeMirror: true, MirrorBaseURL: "https://ingest.example.com", Now: func() time.Time { return now }})
	mux := http.NewServeMux()
	svc.Register(mux)
	post := func() fleet.SyncResponse {
		req := httptest.NewRequest(http.MethodPost, fleet.SyncPath, strings.NewReader(phpAgentBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("openlog-license-key", licenseKey)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return decodeSync(t, rec)
	}

	resp := post()
	p := resp.PHPAgent
	if p == nil || p.Mode != "manual" || p.Version != "agent" || p.TargetVersion != "" || p.Manifest != "" || p.Reason != "manual" || p.ExcludeBins == nil {
		t.Fatalf("default php_agent = %+v", p)
	}

	org := store.OrgOf(tenantID)
	pol := fleet.DefaultPolicy()
	pol.WaveSoakMinutes = 0
	pol.PHPAgent = fleet.PHPAgentPolicy{Mode: "auto", Version: "agent", Reload: "graceful", ExcludeBins: []string{"/usr/bin/php7*"}}
	if err := store.PutPolicy(context.Background(), org, pol, "", now); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // state cache TTL
	p = post().PHPAgent
	if p.Mode != "auto" || p.Reason != "offer" || p.TargetVersion != "0.9.1" || p.Reload != "graceful" || len(p.ExcludeBins) != 1 {
		t.Fatalf("auto php_agent = %+v", p)
	}
	raw, err := base64.StdEncoding.DecodeString(p.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := lib.VerifyManifest(raw, []byte(p.Signature), []ed25519.PublicKey{s.PublicKey()})
	if err != nil {
		t.Fatalf("manifest from sync does not verify: %v", err)
	}
	if _, ok := m.Artifact(lib.ComponentPHPAgent, "linux", "amd64", lib.FormatTarGz); !ok {
		t.Error("manifest has no php-agent artifact")
	}
	if want := "https://ingest.example.com/v1/openlog/releases/0.9.1/" + testutil.PHPArtifactName("0.9.1", "linux", "amd64"); p.DownloadURL != want {
		t.Errorf("download_url = %s, want %s", p.DownloadURL, want)
	}

	// A per-host override turns it off for this host only.
	if err := store.PutPHPOverride(context.Background(), org, fleet.PHPOverride{HostID: "h1", Mode: "off", UpdatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if p = post().PHPAgent; p.Mode != "off" || p.TargetVersion != "" || p.Reason != "mode_off" {
		t.Fatalf("override php_agent = %+v", p)
	}
}
