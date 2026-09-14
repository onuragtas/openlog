package phpagent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

const (
	testArch = "amd64"
	fpmBin   = "/usr/sbin/php-fpm8.2"
	cliBin   = "/usr/bin/php8.2"
	goodSO   = "good module"
	badSO    = "not an ELF file"
)

var testModule = "20220829-nts-" + Libc()

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

// phpTarball builds a PHP agent release archive whose module for testModule has the given content.
func phpTarball(t *testing.T, version, so string) []byte {
	t.Helper()
	top := TopDir(version, "linux", testArch)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name string, typ byte, mode int64, body string) {
		hdr := &tar.Header{Name: top + name, Typeflag: typ, Mode: mode, Format: tar.FormatPAX}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("/", tar.TypeDir, 0o755, "")
	add("/bin/", tar.TypeDir, 0o755, "")
	add("/bin/openlog-php-install", tar.TypeReg, 0o755, "#!/bin/sh\nexit 0\n")
	add("/modules/", tar.TypeDir, 0o755, "")
	add("/modules/"+testModule+"/", tar.TypeDir, 0o755, "")
	add("/modules/"+testModule+"/openlog.so", tar.TypeReg, 0o644, so)
	add("/VERSION", tar.TypeReg, 0o644, version+"\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// phpManifest returns a manifest for version with a php-agent linux/amd64 tar.gz artifact matching archive.
func phpManifest(t *testing.T, version, baseURL string, archive []byte, floor string) []byte {
	t.Helper()
	sum := sha256.Sum256(archive)
	name := TopDir(version, "linux", testArch) + ".tar.gz"
	m := lib.Manifest{Schema: 1, Product: lib.Product, Version: version, Channel: lib.ChannelStable,
		Compatibility: lib.Compatibility{RollbackFloor: floor},
		Artifacts: []lib.Artifact{{Component: lib.ComponentPHPAgent, OS: "linux", Arch: testArch, Format: lib.FormatTarGz,
			Name: name, URL: baseURL + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive))}}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.ParseManifest(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func signLine(data []byte, k testKey) []byte { return []byte(lib.SignatureLine(data, k.priv) + "\n") }

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stageRelease writes what the agent stages for the privileged step.
func stageRelease(t *testing.T, stateDir, version string, archive, manifest, sig []byte) {
	t.Helper()
	dir := filepath.Join(stateDir, StateSubdir, StagedDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{ArchiveFile: archive, ManifestFile: manifest, SignatureFile: sig} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testSys() *update.Sys {
	return &update.Sys{Root: "/", RootUID: os.Getuid(), RootGID: os.Getgid(), IsRoot: func() bool { return true },
		Lchown: func(string, int, int) error { return nil }}
}

// fakeHost simulates openlog-php-install and systemctl for runtimes whose ini loads <root>/current/modules/<key>.
type fakeHost struct {
	root string
	bins []string

	mu      sync.Mutex
	enabled map[string]bool
	calls   []string
	units   map[string]string // unit -> is-active answer
	ini     func(bin string) bool
}

func newFakeHost(root string, bins ...string) *fakeHost {
	return &fakeHost{root: root, bins: bins, enabled: map[string]bool{}, units: map[string]string{"php8.2-fpm.service": "active"}}
}

func (h *fakeHost) moduleLoads() bool {
	b, err := os.ReadFile(filepath.Join(h.root, "current", "modules", testModule, "openlog.so"))
	return err == nil && string(b) == goodSO
}

func (h *fakeHost) status() []byte {
	var out bytes.Buffer
	for _, bin := range h.bins {
		r := Runtime{Bin: bin, Version: "8.2.29", API: "20220829", Libc: Libc(), ScanDir: "/etc/php/8.2/fpm/conf.d", Module: testModule,
			Supported: true, Enabled: h.enabled[bin]}
		r.Loaded = r.Enabled && h.moduleLoads()
		b, _ := json.Marshal(r)
		out.Write(append(b, '\n'))
	}
	return out.Bytes()
}

func (h *fakeHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	switch {
	case strings.HasSuffix(name, InstallerRel):
		switch args[0] {
		case "status":
			return h.status(), nil
		case "install":
			sel := h.bins
			if i := indexOf(args, "--php"); i >= 0 {
				sel = nil
				for j := 1; j < len(args); j++ {
					if args[j-1] == "--php" {
						sel = append(sel, args[j])
					}
				}
			}
			failed := false
			for _, b := range sel {
				h.enabled[b] = true
				if !h.moduleLoads() {
					h.enabled[b] = false // the installer removes the ini file again
					failed = true
				}
			}
			if failed {
				return []byte("openlog.so does not load\n"), fmt.Errorf("exit status 1")
			}
			return []byte("enabled\n"), nil
		case "uninstall":
			h.enabled = map[string]bool{}
			return nil, nil
		}
	case name == "systemctl" && len(args) > 0 && args[0] == "list-units":
		var units []string
		for u := range h.units {
			units = append(units, u+" loaded active running PHP FastCGI")
		}
		sort.Strings(units)
		return []byte(strings.Join(units, "\n")), nil
	case name == "systemctl" && len(args) == 2 && args[0] == "is-active":
		return []byte(h.units[args[1]] + "\n"), nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func (h *fakeHost) callsWith(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
