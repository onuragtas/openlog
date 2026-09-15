// ReconcileNative exists for macOS and Windows only; Windows trusts ACLs instead of the POSIX fixture of fakeSys.

//go:build darwin

package update

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconcileNativePackageContext: an MSI/pkg (context package) points current at its version unless a newer
// self-updated version is current, and reports whether the embedded manifest makes rollbacks from it possible.
func TestReconcileNativePackageContext(t *testing.T) {
	key := newKey(t)
	floor := map[string]string{"rollback_floor": "0.9.0"}
	run := func(t *testing.T, fs *fakeSys, root, ver string) *ReconcileStatus {
		t.Helper()
		st, err := ReconcileNative(context.Background(), NativeReconcileOptions{
			Sys: fs.Sys, Install: Install{InstallRoot: root, VersionDir: ver, Method: MethodMSI},
			StateDir: filepath.Join(fs.Root, "state"), Version: ver, Context: ReconcilePackage,
			Trusted: []ed25519.PublicKey{key.pub},
		})
		if err != nil {
			t.Fatalf("reconcile: %v (status %+v)", err, st)
		}
		return st
	}
	hasNote := func(st *ReconcileStatus, sub string) bool {
		for _, n := range st.Notes {
			if strings.Contains(n, sub) {
				return true
			}
		}
		return false
	}

	t.Run("first install with an embedded manifest", func(t *testing.T) {
		fs := newFakeSys(t)
		root := filepath.Join(fs.Root, "opt", "openlog", "infra-agent")
		m := manifestFor(t, "0.9.1", "https://x", []byte("zip"), floor, nil)
		installVersion(t, root, "0.9.1", "exit 0", nil)
		writeVersionManifest(t, filepath.Join(root, "versions", "0.9.1"), m, sign(m, key))
		st := run(t, fs, root, "0.9.1")
		if cur, _ := CurrentDir(root); cur != "0.9.1" {
			t.Fatalf("current = %q, want 0.9.1", cur)
		}
		if hasNote(st, "manifest") {
			t.Errorf("unexpected manifest note: %v", st.Notes)
		}
	})

	t.Run("newer self-updated version stays current; missing manifest reported", func(t *testing.T) {
		fs := newFakeSys(t)
		root := filepath.Join(fs.Root, "opt", "openlog", "infra-agent")
		installVersion(t, root, "0.9.1", "exit 0", nil)
		installVersion(t, root, "0.9.2", "exit 0", nil)
		if err := SwitchCurrent(root, "0.9.2"); err != nil {
			t.Fatal(err)
		}
		st := run(t, fs, root, "0.9.1")
		if cur, _ := CurrentDir(root); cur != "0.9.2" {
			t.Fatalf("current = %q, want the newer 0.9.2", cur)
		}
		if !hasNote(st, "kept") || !hasNote(st, "has no manifest.json") {
			t.Errorf("notes %v", st.Notes)
		}
	})

	t.Run("older current is replaced; manifest signed by another key reported", func(t *testing.T) {
		fs := newFakeSys(t)
		root := filepath.Join(fs.Root, "opt", "openlog", "infra-agent")
		installVersion(t, root, "0.9.0", "exit 0", nil)
		m := manifestFor(t, "0.9.1", "https://x", []byte("zip"), floor, nil)
		installVersion(t, root, "0.9.1", "exit 0", nil)
		writeVersionManifest(t, filepath.Join(root, "versions", "0.9.1"), m, sign(m, newKey(t)))
		if err := SwitchCurrent(root, "0.9.0"); err != nil {
			t.Fatal(err)
		}
		st := run(t, fs, root, "0.9.1")
		if cur, _ := CurrentDir(root); cur != "0.9.1" {
			t.Fatalf("current = %q, want 0.9.1", cur)
		}
		if !hasNote(st, "not usable") {
			t.Errorf("notes %v", st.Notes)
		}
		if _, err := os.Stat(filepath.Join(root, "versions", "0.9.1", ManifestFile)); err != nil {
			t.Errorf("the package's file was removed: %v", err)
		}
	})
}
