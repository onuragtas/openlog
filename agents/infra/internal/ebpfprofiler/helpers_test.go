package ebpfprofiler

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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
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
	return testKey{pub: pub, priv: priv}
}

// testManifest builds a manifest carrying one profiler tarball for os/arch.
func testManifest(t *testing.T, version, osName, arch, floor string) []byte {
	t.Helper()
	payload := []byte("profiler " + version)
	sum := sha256.Sum256(payload)
	name := TopDir(version, osName, arch) + ".tar.gz"
	m := lib.Manifest{
		Schema: 1, Product: lib.Product, Version: version, Channel: lib.ChannelStable,
		Compatibility: lib.Compatibility{RollbackFloor: floor},
		Artifacts: []lib.Artifact{{
			Component: lib.ComponentEBPFProfiler, OS: osName, Arch: arch, Format: lib.FormatTarGz,
			Name: name, URL: "https://example.invalid/" + name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload)),
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.ParseManifest(b); err != nil {
		t.Fatalf("the test manifest is not valid: %v", err)
	}
	return b
}

func signLine(data []byte, k testKey) []byte { return []byte(lib.SignatureLine(data, k.priv) + "\n") }

func parsed(t *testing.T, b []byte) *lib.Manifest {
	t.Helper()
	m, err := lib.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// --- apply ---

type archiveOpts struct {
	noBinary, noUnit bool
	extra            string
}

// testArchive builds a release tarball the way the Makefile stages it: everything below TopDir.
func testArchive(t *testing.T, version, osName, arch string, opt archiveOpts) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	top := TopDir(version, osName, arch)
	add := func(name string, mode int64, body string) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if !opt.noBinary {
		// 0644 on purpose: apply must make the profiler executable itself.
		add(BinaryRel, 0o644, "profiler binary "+version)
	}
	if !opt.noUnit {
		add(UnitRel, 0o644, "[Unit]\nDescription=openlog eBPF profiler "+version+"\n")
	}
	if opt.extra != "" {
		add(opt.extra, 0o644, "x")
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testArchiveManifest returns a manifest whose profiler artifact matches archive.
func testArchiveManifest(t *testing.T, version, osName, arch, floor string, archive []byte) []byte {
	t.Helper()
	return testArchiveManifestAt(t, version, osName, arch, floor, archive, "https://example.invalid")
}

// testArchiveManifestAt points the artifact at baseURL, so a manager test can actually download it.
func testArchiveManifestAt(t *testing.T, version, osName, arch, floor string, archive []byte, baseURL string) []byte {
	t.Helper()
	sum := sha256.Sum256(archive)
	name := TopDir(version, osName, arch) + ".tar.gz"
	m := lib.Manifest{
		Schema: 1, Product: lib.Product, Version: version, Channel: lib.ChannelStable,
		Compatibility: lib.Compatibility{RollbackFloor: floor},
		Artifacts: []lib.Artifact{{
			Component: lib.ComponentEBPFProfiler, OS: osName, Arch: arch, Format: lib.FormatTarGz,
			Name: name, URL: baseURL + "/" + name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive)),
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.ParseManifest(b); err != nil {
		t.Fatalf("the test manifest is not valid: %v", err)
	}
	return b
}

// env is one host below a temp dir: Sys.Root, the install root, state_dir and the infra install root.
type env struct {
	t                        *testing.T
	key                      testKey
	base, root, state, infra string
	cfg                      config.EBPFProfilerConfig
	now                      time.Time
	goos, goarch             string
	// runLog records every system command; failActive makes "is-active" fail while that version is current.
	runLog     []string
	failActive string
	n          int
	// uidShift moves Sys.RootUID away from the test user, so the ownership checks of extracted files fail the
	// way they would when the agent user wrote them.
	uidShift int
	// unitActive is what the injected UnitActive reports; restarts counts hand-overs to the privileged step.
	unitActive bool
	restarts   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, key: newKey(t), base: base,
		root:  filepath.Join(base, "opt", "openlog", "ebpf-profiler"),
		state: filepath.Join(base, "state"), infra: filepath.Join(base, "infra"),
		now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC), goos: runtime.GOOS, goarch: runtime.GOARCH}
	for _, d := range []string{e.state, e.infra, filepath.Join(base, "etc", "systemd", "system"),
		filepath.Join(base, "usr", "lib", "systemd", "system"), filepath.Join(base, "run", "systemd", "system")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = config.EBPFProfilerConfig{Mode: config.EBPFProfilerModeAuto, Version: config.EBPFProfilerVersionAgent,
		RemoteConfig: true, HealthCheckAfter: config.Duration(time.Minute), InstallRoot: e.root}
	return e
}

func (e *env) sys() *update.Sys {
	sys := &update.Sys{Root: e.base, RootUID: os.Getuid() + e.uidShift, RootGID: os.Getgid(), IsRoot: func() bool { return true },
		Lchown: func(string, int, int) error { return nil },
		Run: func(_ context.Context, name string, args ...string) error {
			line := name + " " + strings.Join(args, " ")
			e.runLog = append(e.runLog, line)
			if e.failActive != "" && strings.Contains(line, "is-active") && InstalledVersion(e.root) == e.failActive {
				return errors.New("inactive (exit status 3)")
			}
			return nil
		}}
	if runtime.GOOS == "windows" {
		sys.TrustPath = func(string) bool { return true }
	}
	return sys
}

// stage writes what the agent stages for version and returns the archive.
func (e *env) stage(version, floor string) []byte {
	e.t.Helper()
	ar := testArchive(e.t, version, e.goos, e.goarch, archiveOpts{})
	e.stageRaw(version, ar, testArchiveManifest(e.t, version, e.goos, e.goarch, floor, ar))
	return ar
}

func (e *env) stageRaw(version string, archive, manifest []byte) {
	e.t.Helper()
	e.stageSigned(version, archive, manifest, signLine(manifest, e.key))
}

func (e *env) stageSigned(version string, archive, manifest, sig []byte) {
	e.t.Helper()
	dir := filepath.Join(e.state, StateSubdir, StagedDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for name, data := range map[string][]byte{ArchiveFile: archive, ManifestFile: manifest, SignatureFile: sig} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) apply(action, version string) *Status {
	e.t.Helper()
	e.n++
	req := Request{ID: fmt.Sprintf("%016x", e.n), Action: action, Version: version, At: e.now}
	b, _ := json.Marshal(req)
	if err := os.MkdirAll(filepath.Join(e.state, StateSubdir), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, StateSubdir, RequestFile), b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	now := e.now
	return Apply(context.Background(), ApplyOptions{Sys: e.sys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg,
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: e.goos, Arch: e.goarch, Now: func() time.Time { return now }})
}

func (e *env) binPath(v string) string { return filepath.Join(e.root, "versions", v, BinaryRel) }

// unitPath is where the agent installs the unit (tarball installs own /etc/systemd/system); pkgUnitPath is
// where a .deb/.rpm would have put it.
func (e *env) unitPath() string {
	return filepath.Join(e.base, "etc", "systemd", "system", UnitName)
}
func (e *env) pkgUnitPath() string {
	return filepath.Join(e.base, "usr", "lib", "systemd", "system", UnitName)
}
func (e *env) ran(cmd string) bool { return slices.Contains(e.runLog, cmd) }
func (e *env) currentTarget() string {
	t, err := os.Readlink(filepath.Join(e.root, "current"))
	if err != nil {
		return ""
	}
	return t
}

// manager builds a Manager for this host. osName decides which platform's release it looks for.
func (e *env) manager(osName string, mod func(*Options)) *Manager {
	e.t.Helper()
	o := Options{Config: e.cfg, StateDir: e.state, StatusDir: e.infra, AgentVersion: "1.0.0", OS: osName,
		Trusted: []ed25519.PublicKey{e.key.pub}, Capable: true,
		Now:        func() time.Time { return e.now },
		UnitActive: func(context.Context) bool { return e.unitActive },
		Restart:    func() { e.restarts++ },
		Tick:       time.Hour}
	if mod != nil {
		mod(&o)
	}
	return NewManager(o)
}

// fakeInstall puts an installation of version in place the way the privileged step would have left it.
func (e *env) fakeInstall(version, osName string, marker bool) {
	e.t.Helper()
	dir := filepath.Join(e.root, "versions", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	ar := testArchive(e.t, version, osName, e.goarch, archiveOpts{})
	files := map[string][]byte{
		BinaryRel:    []byte("profiler binary " + version),
		ManifestFile: testArchiveManifest(e.t, version, osName, e.goarch, "", ar),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
	if marker {
		if err := os.WriteFile(filepath.Join(e.root, MarkerFile), []byte("managed\n"), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	cur := filepath.Join(e.root, "current")
	os.Remove(cur)
	if err := os.Symlink(filepath.Join("versions", version), cur); err != nil {
		e.t.Fatal(err)
	}
}

// readState is <state_dir>/ebpf-profiler/state.json.
func (e *env) readState() State {
	e.t.Helper()
	var st State
	b, err := os.ReadFile(filepath.Join(e.state, StateSubdir, StateFile))
	if err != nil {
		return st
	}
	if err := json.Unmarshal(b, &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

// readRequest is the request left for the privileged step (ok=false when there is none).
func (e *env) readRequest() (Request, bool) {
	e.t.Helper()
	var req Request
	b, err := os.ReadFile(filepath.Join(e.state, StateSubdir, RequestFile))
	if err != nil {
		return req, false
	}
	if err := json.Unmarshal(b, &req); err != nil {
		e.t.Fatal(err)
	}
	return req, true
}
