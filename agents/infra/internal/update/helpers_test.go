package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

type testKey struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub, priv}
}

// tarEntry is one entry of a test archive.
type tarEntry struct {
	name     string
	typ      byte
	body     string
	mode     int64
	linkname string
}

func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: mode, Linkname: e.linkname, Format: tar.FormatPAX}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// agentTarball builds a release archive whose binary is a shell script with the given body.
func agentTarball(t *testing.T, version, script string) []byte {
	top := TopDir(version, "linux", "amd64")
	return buildTarGz(t, []tarEntry{
		{name: top + "/", typ: tar.TypeDir, mode: 0o755},
		{name: top + "/" + BinaryName, typ: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\n" + script + "\n"},
		{name: top + "/LICENSE", typ: tar.TypeReg, body: "Apache-2.0\n"},
		{name: top + "/README.md", typ: tar.TypeReg, body: "readme\n"},
		{name: top + "/packaging/", typ: tar.TypeDir, mode: 0o755},
		{name: top + "/packaging/config.example.yaml", typ: tar.TypeReg, body: "license_key: \"\"\n"},
	})
}

// manifestFor returns manifest JSON for version with one linux/amd64 artifact matching archive.
// mutate may edit the raw JSON object before it is encoded.
func manifestFor(t *testing.T, version, baseURL string, archive []byte, compat map[string]string, mutate func(map[string]any)) []byte {
	t.Helper()
	sum := sha256.Sum256(archive)
	name := TopDir(version, "linux", "amd64") + ".tar.gz"
	channel := lib.ChannelStable
	if v, err := lib.ParseVersion(version); err == nil && v.IsPrerelease() {
		channel = lib.ChannelBeta
	}
	c := map[string]any{}
	for k, v := range compat {
		c[k] = v
	}
	m := map[string]any{
		"schema": 1, "product": "openlog", "version": version, "channel": channel,
		"released_at": "2026-09-01T00:00:00Z", "compatibility": c,
		"artifacts": []any{map[string]any{
			"component": "infra-agent", "os": "linux", "arch": "amd64", "format": "tar.gz",
			"name": name, "url": baseURL + "/" + name, "sha256": hex.EncodeToString(sum[:]), "size": len(archive),
		}},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sign(data []byte, keys ...testKey) string {
	s := ""
	for _, k := range keys {
		s += lib.SignatureLine(data, k.priv) + "\n"
	}
	return s
}

func instruction(action, target string, manifest []byte, sig, url string) *Instruction {
	return &Instruction{
		Action: action, TargetVersion: target, Manifest: base64.StdEncoding.EncodeToString(manifest),
		Signature: sig, DownloadURL: url, RolloutID: "r-" + action + "-" + target,
	}
}

// installVersion creates <root>/versions/<v>/ with a script binary and an optional manifest.
func installVersion(t *testing.T, root, v, script string, manifest []byte) {
	t.Helper()
	dir := filepath.Join(root, "versions", v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
