package resource

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPublishHostID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
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

func TestPublishServices(t *testing.T) {
	dir := t.TempDir()
	entries := []ServiceEntry{
		{Kind: "container", Key: "abc123", ID: "redis"},
		{Kind: "exe", Key: "/usr/bin/redis-server", ID: "redis"},
		{Kind: "exe", Key: "/usr/bin/redis-server", ID: "redis"}, // duplicate
		{Kind: "exe", Key: "", ID: "broken"},                     // incomplete
		{Kind: "exe", Key: "/bad\tkey", ID: "tabbed"},            // would reshape the file
	}
	if err := PublishServices(dir, entries); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ServicesFile))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := "# openlog discovered services, published by " + AgentName + "\n" +
		"container\tabc123\tredis\n" +
		"exe\t/usr/bin/redis-server\tredis\n"
	if got != want {
		t.Errorf("published:\n%q\nwant:\n%q", got, want)
	}
}

// The same discovery must produce the same bytes, whatever order it arrives in: otherwise the file is
// rewritten every round and readers see a change that did not happen.
func TestPublishServicesIsStableAcrossOrder(t *testing.T) {
	dir := t.TempDir()
	a := []ServiceEntry{{Kind: "exe", Key: "/a", ID: "one"}, {Kind: "exe", Key: "/b", ID: "two"}}
	b := []ServiceEntry{{Kind: "exe", Key: "/b", ID: "two"}, {Kind: "exe", Key: "/a", ID: "one"}}
	if err := PublishServices(dir, a); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, ServicesFile))
	if err := PublishServices(dir, b); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, ServicesFile))
	if string(first) != string(second) {
		t.Errorf("shuffled input produced different bytes:\n%q\n%q", first, second)
	}
}

// A missing runtime directory is the normal case off systemd. It must not be created and must not fail.
func TestPublishServicesWithoutRuntimeDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if err := PublishServices(missing, []ServiceEntry{{Kind: "exe", Key: "/x", ID: "y"}}); err != nil {
		t.Errorf("missing dir reported an error: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("the directory was created")
	}
}
