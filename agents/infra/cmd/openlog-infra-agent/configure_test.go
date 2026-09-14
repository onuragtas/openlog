package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

func TestSetTopLevelKey(t *testing.T) {
	in := "# comment\nlicense_key: \"\"   # required\nendpoint: \"\"\ninterval: 10s\n"
	out := string(SetTopLevelKey(SetTopLevelKey([]byte(in), "license_key", "abc123"), "endpoint", "https://ingest.example.com:4318"))
	want := "# comment\nlicense_key: \"abc123\"\nendpoint: \"https://ingest.example.com:4318\"\ninterval: 10s\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	// Missing key, no trailing newline, CRLF files (Notepad).
	if got := string(SetTopLevelKey([]byte("interval: 10s"), "license_key", "k")); got != "interval: 10s\nlicense_key: \"k\"\n" {
		t.Errorf("append = %q", got)
	}
	if got := string(SetTopLevelKey([]byte("license_key: old\r\nendpoint: x\r\n"), "license_key", "k")); got != "license_key: \"k\"\r\nendpoint: x\r\n" {
		t.Errorf("crlf = %q", got)
	}
	// A nested key with the same name is not a top-level key.
	if got := string(SetTopLevelKey([]byte("integrations:\n  license_key: x\n"), "license_key", "k")); !strings.HasSuffix(got, "\nlicense_key: \"k\"\n") {
		t.Errorf("nested = %q", got)
	}
}

func TestRunConfigure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	if code := runConfigure(path, "key-1", "https://ingest.example.com:4318", true, true); code != 0 {
		t.Fatalf("create: exit %d", code)
	}
	cfg, err := config.Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseKey != "key-1" || cfg.Endpoint != "https://ingest.example.com:4318" {
		t.Errorf("loaded %q %q", cfg.LicenseKey, cfg.Endpoint)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); runtime.GOOS == "darwin" && fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	// Re-run with only a new endpoint keeps the key and any user edits.
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(b, []byte("# edited by the operator\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runConfigure(path, "", "http://127.0.0.1:4318", false, true); code != 0 {
		t.Fatalf("update: exit %d", code)
	}
	cfg, err = config.Load(path, false)
	if err != nil || cfg.LicenseKey != "key-1" || cfg.Endpoint != "http://127.0.0.1:4318" {
		t.Errorf("after update: %+v %v", cfg, err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "# edited by the operator\n") {
		t.Error("operator edit lost")
	}
	for _, bad := range [][2]string{{"with space", ""}, {"", "ftp://x"}, {"", "http://h/\"x"}} {
		if code := runConfigure(path, bad[0], bad[1], bad[0] != "", bad[1] != ""); code == 0 {
			t.Errorf("accepted %q", bad)
		}
	}
}
