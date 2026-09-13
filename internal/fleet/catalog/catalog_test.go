package catalog_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
	lib "github.com/onuragtas/openlog/libs/release"
)

var specs = []testutil.ReleaseSpec{
	{Version: "0.3.0"},
	{Version: "0.3.1"},
	{Version: "0.4.0", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0"},
	{Version: "0.5.0-beta.1"},
}

func signer(t *testing.T) testutil.Signer {
	t.Helper()
	s, err := testutil.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mirror(t *testing.T, s testutil.Signer, sp []testutil.ReleaseSpec, gen time.Time) string {
	t.Helper()
	dir := t.TempDir()
	if err := testutil.WriteMirror(dir, s, sp, gen); err != nil {
		t.Fatal(err)
	}
	return dir
}

var gen = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

func TestMirrorLoad(t *testing.T) {
	s := signer(t)
	c := catalog.New(catalog.Options{MirrorDir: mirror(t, s, specs, gen), TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	if snap.Len() != 4 {
		t.Fatalf("releases = %d", snap.Len())
	}
	if r, _ := snap.Latest(lib.ChannelStable); r.VersionString() != "0.4.0" {
		t.Errorf("latest stable = %s", r.VersionString())
	}
	if r, _ := snap.Latest(lib.ChannelBeta); r.VersionString() != "0.5.0-beta.1" {
		t.Errorf("latest beta = %s", r.VersionString())
	}
	if r, _ := snap.LatestPatch(lib.ChannelStable, 0, 3); r.VersionString() != "0.3.1" {
		t.Errorf("latest 0.3 patch = %s", r.VersionString())
	}
	if _, ok := snap.Release("v0.3.0"); !ok {
		t.Error("release lookup with v prefix failed")
	}
	st := c.Status()
	if st.State != catalog.StateOK || st.Releases != 4 || !strings.HasPrefix(st.Source, "mirror:") {
		t.Errorf("status = %+v", st)
	}
}

func TestDisabledWithoutKeys(t *testing.T) {
	c := catalog.New(catalog.Options{MirrorDir: t.TempDir(), KeysError: errors.New("keys file missing")})
	if c.Enabled() {
		t.Fatal("enabled without keys")
	}
	st := c.Status()
	if st.State != catalog.StateDisabled || !strings.Contains(st.Error, "no trusted release keys") {
		t.Errorf("status = %+v", st)
	}
	if err := c.Refresh(context.Background()); err == nil || c.Snapshot() != nil {
		t.Error("refresh without keys must fail")
	}
}

func TestUntrustedIndex(t *testing.T) {
	s, other := signer(t), signer(t)
	c := catalog.New(catalog.Options{MirrorDir: mirror(t, s, specs, gen), TrustedKeys: []ed25519.PublicKey{other.PublicKey()}})
	err := c.Refresh(context.Background())
	if !errors.Is(err, lib.ErrNoTrustedSignature) {
		t.Fatalf("err = %v", err)
	}
	if c.Snapshot() != nil || c.Status().State != catalog.StateError {
		t.Errorf("status = %+v", c.Status())
	}
}

func TestTamperedManifestSkipped(t *testing.T) {
	s := signer(t)
	dir := mirror(t, s, specs, gen)
	p := filepath.Join(dir, "v0.4.0", "manifest.json")
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.Replace(raw, []byte(`"min_upgrade_from": "0.3.0"`), []byte(`"min_upgrade_from": "0.1.0"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	// A manifest signed by an untrusted key is skipped too.
	evil := signer(t)
	p2 := filepath.Join(dir, "v0.3.1", "manifest.json")
	raw2, _ := os.ReadFile(p2)
	_ = os.WriteFile(p2+".sig", evil.Sign(raw2), 0o644)

	c := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Snapshot().Release("0.4.0"); ok {
		t.Error("tampered 0.4.0 accepted")
	}
	if _, ok := c.Snapshot().Release("0.3.1"); ok {
		t.Error("untrusted 0.3.1 accepted")
	}
	if len(c.Status().Warnings) != 2 {
		t.Errorf("warnings = %v", c.Status().Warnings)
	}
	if r, _ := c.Snapshot().Latest(lib.ChannelStable); r.VersionString() != "0.3.0" {
		t.Errorf("latest = %s", r.VersionString())
	}
}

func TestStableEntryWithBetaManifestRejected(t *testing.T) {
	s := signer(t)
	dir := mirror(t, s, []testutil.ReleaseSpec{{Version: "0.3.0"}, {Version: "0.4.0", Channel: lib.ChannelBeta}}, gen)
	// Re-sign an index that lists the beta manifest on stable.
	raw, sig, err := testutil.Index(s, []testutil.ReleaseSpec{{Version: "0.3.0"}, {Version: "0.4.0", Channel: lib.ChannelStable}}, gen)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "index.json.sig"), sig, 0o644)
	c := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Snapshot().Release("0.4.0"); ok {
		t.Error("beta manifest listed on stable accepted")
	}
}

func TestReplayedOlderIndexRejected(t *testing.T) {
	s := signer(t)
	dir := mirror(t, s, specs, gen)
	c := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, sig, _ := testutil.Index(s, specs[:1], gen.Add(-time.Hour))
	_ = os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "index.json.sig"), sig, 0o644)
	if err := c.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("err = %v", err)
	}
	if c.Snapshot().Len() != 4 {
		t.Error("previous snapshot not kept")
	}
}

func TestURLSourceETagAndManifestCache(t *testing.T) {
	s := signer(t)
	var srvURL string
	var indexHits, notModified, manifestHits atomic.Int32
	files := map[string][]byte{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.json" {
			indexHits.Add(1)
			if r.Header.Get("If-None-Match") == `"v1"` {
				notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
		}
		if strings.HasSuffix(r.URL.Path, "/manifest.json") {
			manifestHits.Add(1)
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	sp := make([]testutil.ReleaseSpec, len(specs))
	copy(sp, specs)
	for i := range sp {
		sp[i].BaseURL = srvURL
		b, err := testutil.Build(s, sp[i])
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range b.Files {
			files["/v"+b.Release.Manifest.Version+"/"+name] = body
		}
	}
	raw, sig, _ := testutil.Index(s, sp, gen)
	files["/index.json"], files["/index.json.sig"] = raw, sig

	c := catalog.New(catalog.Options{IndexURL: srvURL + "/index.json", TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	for range 3 {
		if err := c.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if c.Snapshot().Len() != 4 {
		t.Fatalf("releases = %d", c.Snapshot().Len())
	}
	if indexHits.Load() != 3 || notModified.Load() != 2 {
		t.Errorf("index hits = %d, 304s = %d", indexHits.Load(), notModified.Load())
	}
	if manifestHits.Load() != 4 {
		t.Errorf("manifest fetches = %d, want 4 (cached after the first refresh)", manifestHits.Load())
	}
	if _, err := c.OpenMirrorFile("0.4.0", testutil.ArtifactName("0.4.0", "linux", "amd64")); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("URL source served a mirror file: %v", err)
	}
}

func TestOpenMirrorFile(t *testing.T) {
	s := signer(t)
	dir := mirror(t, s, specs, gen)
	c := catalog.New(catalog.Options{MirrorDir: dir, TrustedKeys: []ed25519.PublicKey{s.PublicKey()}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	name := testutil.ArtifactName("0.4.0", "linux", "amd64")
	mf, err := c.OpenMirrorFile("0.4.0", name)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(mf.File)
	mf.File.Close()
	if int64(len(body)) != mf.Artifact.Size {
		t.Errorf("size = %d", len(body))
	}

	// Files that exist but are not listed artifacts, traversal and unknown versions are not served.
	_ = os.WriteFile(filepath.Join(dir, "v0.4.0", "extra.tar.gz"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "secret"), []byte("x"), 0o644)
	for _, tc := range []struct{ version, name string }{
		{"0.4.0", "manifest.json"},
		{"0.4.0", "extra.tar.gz"},
		{"0.4.0", "../secret"},
		{"0.4.0", "..%2fsecret"},
		{"0.4.0", "/etc/passwd"},
		{"0.4.0", ".."},
		{"../v0.4.0", name},
		{"0.4.0/../..", name},
		{"9.9.9", name},
		{"0.4.0", ""},
	} {
		if f, err := c.OpenMirrorFile(tc.version, tc.name); !errors.Is(err, catalog.ErrNotFound) {
			if f != nil {
				f.File.Close()
			}
			t.Errorf("OpenMirrorFile(%q, %q) err = %v, want ErrNotFound", tc.version, tc.name, err)
		}
	}

	// A symlink pointing out of the mirror is not followed.
	arm := testutil.ArtifactName("0.4.0", "linux", "arm64")
	armPath := filepath.Join(dir, "v0.4.0", arm)
	_ = os.Remove(armPath)
	outside := filepath.Join(t.TempDir(), "outside")
	orig, _ := testutil.Tarball("0.4.0", "linux", "arm64")
	_ = os.WriteFile(outside, orig, 0o644)
	if err := os.Symlink(outside, armPath); err != nil {
		t.Fatal(err)
	}
	if f, err := c.OpenMirrorFile("0.4.0", arm); err == nil {
		f.File.Close()
		t.Error("symlink out of the mirror was followed")
	}

	// Same size, different content: sha256 mismatch.
	other := filepath.Join(dir, "v0.3.1", testutil.ArtifactName("0.3.1", "linux", "amd64"))
	b, _ := os.ReadFile(other)
	b[len(b)-1] ^= 0xff
	_ = os.WriteFile(other, b, 0o644)
	if f, err := c.OpenMirrorFile("0.3.1", testutil.ArtifactName("0.3.1", "linux", "amd64")); err == nil || errors.Is(err, catalog.ErrNotFound) {
		if f != nil {
			f.File.Close()
		}
		t.Errorf("tampered artifact: err = %v", err)
	}
}
