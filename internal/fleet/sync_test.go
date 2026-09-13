package fleet_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

const licenseKey = "olk_test"

type syncEnv struct {
	h      http.Handler
	store  *fleettest.MemStore
	rec    *fleet.Recorder
	cat    *catalog.Catalog
	signer testutil.Signer
	dir    string
	now    time.Time
}

func newSyncEnv(t *testing.T, o fleet.SyncOptions) *syncEnv {
	t.Helper()
	s, _ := testutil.NewSigner()
	dir := t.TempDir()
	if err := testutil.WriteMirror(dir, s, baseSpecs, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := tenant.ParseStatic(licenseKey + "=" + tenantID)
	store := fleettest.NewMemStore()
	e := &syncEnv{store: store, cat: cat, signer: s, dir: dir, now: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)}
	e.rec = fleet.NewRecorder(store, fleet.RecorderOptions{})
	o.Now = func() time.Time { return e.now }
	svc := fleet.NewSyncService(res, fleet.NewStateCache(store, fleet.StateCacheOptions{}), e.rec, cat, o)
	mux := http.NewServeMux()
	svc.Register(mux)
	e.h = mux
	return e
}

const agentBody = `{"host_id":"h1","host_name":"web-1","agent":{"name":"openlog-infra-agent","version":"0.3.0","commit":"abc",
	"os":"linux","arch":"amd64","install_method":"tarball","update_capable":true},
	"update":{"state":"idle","from_version":"","to_version":"","error":"","changed_at":""},"config_hash":"sha256:x","future_field":1}`

func (e *syncEnv) post(body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, fleet.SyncPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("openlog-license-key", licenseKey)
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decodeSync(t *testing.T, rec *httptest.ResponseRecorder) fleet.SyncResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp fleet.SyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSyncErrors(t *testing.T) {
	e := newSyncEnv(t, fleet.SyncOptions{})
	cases := []struct {
		name string
		body string
		hdr  map[string]string
		code int
	}{
		{"no key", agentBody, map[string]string{"openlog-license-key": ""}, http.StatusUnauthorized},
		{"wrong key", agentBody, map[string]string{"openlog-license-key": "nope"}, http.StatusUnauthorized},
		{"bearer key", agentBody, map[string]string{"openlog-license-key": "", "Authorization": "Bearer " + licenseKey}, http.StatusOK},
		{"content type", agentBody, map[string]string{"Content-Type": "application/x-protobuf"}, http.StatusUnsupportedMediaType},
		{"bad json", `{`, nil, http.StatusBadRequest},
		{"no host id", `{"agent":{"version":"0.3.0"}}`, nil, http.StatusBadRequest},
		{"too large", `{"host_id":"h","host_name":"` + strings.Repeat("x", 70000) + `"}`, nil, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := e.post(tc.body, tc.hdr); rec.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestSyncNoRolloutRecordsHost(t *testing.T) {
	e := newSyncEnv(t, fleet.SyncOptions{})
	resp := decodeSync(t, e.post(agentBody, nil))
	if resp.Update != nil || resp.PollIntervalSeconds != 300 || resp.ServerVersion == "" {
		t.Fatalf("response = %+v", resp)
	}
	if e.store.Upserts() != 0 {
		t.Fatal("sync wrote to the store synchronously")
	}
	e.rec.Flush(context.Background())
	h, err := e.store.GetHost(context.Background(), e.store.OrgOf(tenantID), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if h.HostName != "web-1" || h.Version != "0.3.0" || h.InstallMethod != "tarball" || !h.UpdateCapable || h.ConfigHash != "sha256:x" || !h.LastSyncAt.Equal(e.now) {
		t.Errorf("host = %+v", h)
	}
}

func TestSyncOffersSignedUpdate(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(map[bool]string{false: "release url", true: "mirror"}[mirror], func(t *testing.T) {
			e := newSyncEnv(t, fleet.SyncOptions{ServeMirror: mirror, PollInterval: 120 * time.Second})
			org := e.store.OrgOf(tenantID)
			r := &fleet.Rollout{OrgID: org, Action: fleet.ActionUpgrade, ToVersion: "0.4.0", Waves: []int{100}, State: fleet.RolloutActive,
				CreatedAt: e.now.Add(-time.Hour), WaveStartedAt: e.now.Add(-time.Hour)}
			if err := e.store.CreateRollout(context.Background(), r, ""); err != nil {
				t.Fatal(err)
			}
			resp := decodeSync(t, e.post(agentBody, nil))
			u := resp.Update
			if u == nil || u.Action != "upgrade" || u.TargetVersion != "0.4.0" || u.RolloutID != r.ID || resp.PollIntervalSeconds != 120 || u.NotBefore != "" {
				t.Fatalf("response = %+v", resp)
			}
			raw, err := base64.StdEncoding.DecodeString(u.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			m, _, err := lib.VerifyManifest(raw, []byte(u.Signature), []ed25519.PublicKey{e.signer.PublicKey()})
			if err != nil || m.Version != "0.4.0" {
				t.Fatalf("manifest does not verify: %v", err)
			}
			art, _ := m.Artifact(lib.ComponentInfraAgent, "linux", "amd64", lib.FormatTarGz)
			wantURL := art.URL
			if mirror {
				wantURL = "http://example.com/v1/openlog/releases/0.4.0/" + art.Name
			}
			if u.DownloadURL != wantURL {
				t.Errorf("download_url = %s, want %s", u.DownloadURL, wantURL)
			}
			e.rec.Flush(context.Background())
			if h, _ := e.store.GetHost(context.Background(), org, "h1"); h.RolloutID != r.ID {
				t.Errorf("rollout id not recorded: %+v", h)
			}
			if !mirror {
				return
			}
			// The agent downloads from the mirror and checks the signed sha256.
			get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("openlog-license-key", licenseKey)
				for k, v := range hdr {
					if v == "" {
						req.Header.Del(k)
					} else {
						req.Header.Set(k, v)
					}
				}
				rec := httptest.NewRecorder()
				e.h.ServeHTTP(rec, req)
				return rec
			}
			dl := get(strings.TrimPrefix(u.DownloadURL, "http://example.com"), nil)
			sum := sha256.Sum256(dl.Body.Bytes())
			if dl.Code != http.StatusOK || hex.EncodeToString(sum[:]) != art.SHA256 || dl.Header().Get("Content-Type") != "application/gzip" {
				t.Fatalf("download: %d %s", dl.Code, dl.Header())
			}
			if rg := get("/v1/openlog/releases/0.4.0/"+art.Name, map[string]string{"Range": "bytes=0-9"}); rg.Code != http.StatusPartialContent || rg.Body.Len() != 10 {
				t.Errorf("range: %d, %d bytes", rg.Code, rg.Body.Len())
			}
			if rec := get("/v1/openlog/releases/0.4.0/"+art.Name, map[string]string{"openlog-license-key": ""}); rec.Code != http.StatusUnauthorized {
				t.Errorf("no key: %d", rec.Code)
			}
			_ = os.WriteFile(filepath.Join(e.dir, "secret"), []byte("TOPSECRET-CONTENT"), 0o644)
			for _, p := range []string{
				"/v1/openlog/releases/0.4.0/manifest.json",
				"/v1/openlog/releases/0.4.0/..%2F..%2Fsecret",
				"/v1/openlog/releases/..%2Fsecret/x",
				"/v1/openlog/releases/0.4.0/../../secret",
				"/v1/openlog/releases/9.9.9/" + art.Name,
			} {
				if rec := get(p, nil); rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("TOPSECRET")) {
					t.Errorf("GET %s: %d %q", p, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func TestSyncWindowAndStoreOutage(t *testing.T) {
	e := newSyncEnv(t, fleet.SyncOptions{})
	org := e.store.OrgOf(tenantID)
	p := fleet.DefaultPolicy()
	p.MaintenanceWindows = []fleet.Window{{Start: "09:00", End: "11:00"}}
	_ = e.store.PutPolicy(context.Background(), org, p, "", e.now)
	r := &fleet.Rollout{OrgID: org, Action: fleet.ActionUpgrade, ToVersion: "0.4.0", Waves: []int{100}, State: fleet.RolloutActive, CreatedAt: e.now}
	_ = e.store.CreateRollout(context.Background(), r, "")
	u := decodeSync(t, e.post(agentBody, nil)).Update
	if u == nil || u.NotBefore != "2026-09-14T10:00:00Z" || u.Deadline != "2026-09-14T11:00:00Z" {
		t.Fatalf("update = %+v", u)
	}

	// PostgreSQL down and nothing cached for the tenant: sync still answers, without an update.
	e2 := newSyncEnv(t, fleet.SyncOptions{})
	e2.store.SetErr(errors.New("connection refused"))
	if resp := decodeSync(t, e2.post(agentBody, nil)); resp.Update != nil {
		t.Fatalf("update offered without policy: %+v", resp)
	}
}

// Hosts waiting for a later wave sync every RolloutPollInterval, so "Deploy now" reaches them within
// about a minute instead of a full sync interval.
func TestSyncShortPollWhileWaitingForWave(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*syncEnv, *fleet.Rollout, string) {
		e := newSyncEnv(t, fleet.SyncOptions{PollInterval: 300 * time.Second})
		r := &fleet.Rollout{OrgID: e.store.OrgOf(tenantID), Action: fleet.ActionUpgrade, ToVersion: "0.4.0", Waves: []int{50, 100},
			WaveSoakMinutes: 60, State: fleet.RolloutActive, CreatedAt: e.now.Add(-time.Minute), WaveStartedAt: e.now.Add(-time.Minute)}
		if err := e.store.CreateRollout(ctx, r, ""); err != nil {
			t.Fatal(err)
		}
		// A host outside the first (50 %) wave.
		for i := 0; ; i++ {
			host := fmt.Sprintf("host-%d", i)
			if fleet.Bucket(host, r.ID) >= 50 {
				return e, r, strings.Replace(agentBody, `"host_id":"h1"`, `"host_id":"`+host+`"`, 1)
			}
		}
	}

	e, _, body := setup(t)
	if resp := decodeSync(t, e.post(body, nil)); resp.Update != nil || resp.PollIntervalSeconds != 60 {
		t.Fatalf("waiting for wave: %+v", resp)
	}

	e, r, body := setup(t)
	m := fleet.NewManager(e.store, e.cat, fleet.ManagerOptions{Now: func() time.Time { return e.now }})
	got, err := m.DeployNow(ctx, r.OrgID, r.ID, fleet.Actor{Email: "admin@example.com"})
	if err != nil || got.CurrentWave != 1 || !got.WaveStartedAt.Equal(e.now) {
		t.Fatalf("deploy now = %+v, %v", got, err)
	}
	if resp := decodeSync(t, e.post(body, nil)); resp.Update == nil || resp.Update.TargetVersion != "0.4.0" || resp.PollIntervalSeconds != 300 {
		t.Fatalf("after deploy now: %+v", resp)
	}
	if acts := e.store.AuditActions(); len(acts) == 0 || acts[len(acts)-1] != "fleet.rollout.deploy_now" {
		t.Fatalf("audit = %v", acts)
	}
	var pe *fleet.PreconditionError
	if _, err := m.DeployNow(ctx, r.OrgID, r.ID, fleet.Actor{}); !errors.As(err, &pe) {
		t.Fatalf("second deploy now = %v", err)
	}
	if _, err := m.Pause(ctx, r.OrgID, r.ID, fleet.Actor{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployNow(ctx, r.OrgID, r.ID, fleet.Actor{}); !errors.As(err, &pe) {
		t.Fatalf("deploy now of a paused rollout = %v", err)
	}
}
