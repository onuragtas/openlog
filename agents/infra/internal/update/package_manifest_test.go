package update

import (
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVersionManifest stores manifest.json and, when sig is not "-", manifest.json.sig in dir.
func writeVersionManifest(t *testing.T, dir string, manifest []byte, sig string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if sig != "-" {
		if err := os.WriteFile(filepath.Join(dir, SignatureFile), []byte(sig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckVersionManifest(t *testing.T) {
	key, other := newKey(t), newKey(t)
	floor := map[string]string{"rollback_floor": "0.8.0"}
	good := manifestFor(t, "0.9.0", "https://x", []byte("zip"), floor, nil)
	cases := []struct {
		name     string
		manifest []byte
		sig      string // "-": no signature file
		skip     bool   // no manifest at all
		trusted  []ed25519.PublicKey
		want     string // "" ok, "notexist" fs.ErrNotExist, else substring
	}{
		{name: "valid", manifest: good, sig: sign(good, key), trusted: []ed25519.PublicKey{key.pub}},
		{name: "valid with a rotated second key", manifest: good, sig: sign(good, other, key), trusted: []ed25519.PublicKey{key.pub}},
		{name: "no manifest (MSI built without one)", skip: true, trusted: []ed25519.PublicKey{key.pub}, want: "notexist"},
		{name: "no signature file", manifest: good, sig: "-", trusted: []ed25519.PublicKey{key.pub}, want: "notexist"},
		{name: "untrusted key", manifest: good, sig: sign(good, other), trusted: []ed25519.PublicKey{key.pub}, want: "manifest signature"},
		{name: "no trusted keys in this build", manifest: good, sig: sign(good, key), want: ErrNoTrustedKeys},
		{name: "modified after signing", manifest: []byte(strings.Replace(string(good), "0.8.0", "0.1.0", 1)), sig: sign(good, key),
			trusted: []ed25519.PublicKey{key.pub}, want: "manifest signature"},
		{name: "manifest of another version", manifest: manifestFor(t, "0.9.1", "https://x", []byte("zip"), floor, nil),
			sig: sign(manifestFor(t, "0.9.1", "https://x", []byte("zip"), floor, nil), key), trusted: []ed25519.PublicKey{key.pub}, want: "not 0.9.0"},
		{name: "no rollback_floor", manifest: manifestFor(t, "0.9.0", "https://x", []byte("zip"), nil, nil),
			sig: sign(manifestFor(t, "0.9.0", "https://x", []byte("zip"), nil, nil), key), trusted: []ed25519.PublicKey{key.pub}, want: "no rollback_floor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "0.9.0")
			if c.skip {
				os.MkdirAll(dir, 0o755)
			} else {
				writeVersionManifest(t, dir, c.manifest, c.sig)
			}
			err := CheckVersionManifest(dir, "0.9.0", c.trusted)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want == "":
			case c.want == "notexist":
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("error %v, want fs.ErrNotExist", err)
				}
			case err == nil || !strings.Contains(err.Error(), c.want):
				t.Fatalf("error %v, want %q", err, c.want)
			}
		})
	}
}
