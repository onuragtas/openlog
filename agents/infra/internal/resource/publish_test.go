package resource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishHostID(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if err := PublishHostID(missing, "3f0e9c52-1b7a-4c1e-9d0a-2f7f5e1c8b11"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing runtime dir must not be created: %v", err)
	}

	dir := t.TempDir()
	for _, id := range []string{"3f0e9c52-1b7a-4c1e-9d0a-2f7f5e1c8b11", "3f0e9c52-1b7a-4c1e-9d0a-2f7f5e1c8b11", "a1b2c3d4e5f60718293a4b5c6d7e8f90"} {
		if err := PublishHostID(dir, id); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, HostIDFile))
		if err != nil || string(b) != id+"\n" {
			t.Fatalf("host-id = %q, %v; want %q", b, err, id)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, HostIDFile))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(dir, HostIDFile+".tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}
