package javaagent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	return testKey{pub, priv}
}

// testJar builds a Java agent jar whose manifest names version (premain=false: not an agent).
func testJar(t *testing.T, version string, premain bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	mf := "Manifest-Version: 1.0\r\n"
	if premain {
		mf += "Premain-Class: io.opentelemetry.javaagent.OpenTelemetryAgent\r\n"
	}
	mf += VersionAttribute + ": " + version + "\r\nOpenlog-Upstream-Javaagent-Version: 2.31.1\r\n\r\n"
	w.Write([]byte(mf))
	c, _ := zw.Create("io/opentelemetry/javaagent/OpenTelemetryAgent.class")
	c.Write([]byte("cafebabe " + version))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testManifest returns a manifest for version with a java-agent jar artifact matching jar.
func testManifest(t *testing.T, version, baseURL string, jar []byte, floor string) []byte {
	t.Helper()
	sum := sha256.Sum256(jar)
	name := "openlog-javaagent-" + version + ".jar"
	m := lib.Manifest{Schema: 1, Product: lib.Product, Version: version, Channel: lib.ChannelStable,
		Compatibility: lib.Compatibility{RollbackFloor: floor},
		Artifacts: []lib.Artifact{{Component: lib.ComponentJavaAgent, OS: lib.PlatformAny, Arch: lib.PlatformAny, Format: lib.FormatJar,
			Name: name, URL: baseURL + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(jar))}}}
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

func testSys() *update.Sys {
	sys := &update.Sys{Root: "/", RootUID: os.Getuid(), RootGID: os.Getgid(), IsRoot: func() bool { return true },
		Lchown: func(string, int, int) error { return nil }}
	if runtime.GOOS == "windows" {
		// TrustedTree reads ACLs on Windows; temporary directories of a test run are not SYSTEM/Administrators-only.
		// Unix keeps the real ownership check (RootUID/RootGID = the test user).
		sys.TrustTree = func(string) bool { return true }
	}
	return sys
}

// env is one host: install root, link path, state dir and infra install root below a temp dir.
type env struct {
	t                              *testing.T
	key                            testKey
	base, root, link, state, infra string
	cfg                            config.JavaAgentConfig
	now                            time.Time
	goos                           string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, key: newKey(t), base: base, root: filepath.Join(base, "opt", "java-agent"),
		link: filepath.Join(base, "opt", JarName), state: filepath.Join(base, "state"), infra: filepath.Join(base, "infra"),
		now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC), goos: runtime.GOOS}
	for _, d := range []string{e.state, e.infra, filepath.Join(base, "opt")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = config.JavaAgentConfig{Mode: config.JavaAgentModeAuto, Version: config.JavaAgentVersionAgent, RemoteConfig: true,
		HealthCheckAfter: config.Duration(time.Minute), InstallRoot: e.root, LinkPath: e.link}
	return e
}

// stage writes what the agent stages for version and returns the jar.
func (e *env) stage(version, floor string) []byte {
	e.t.Helper()
	jar := testJar(e.t, version, true)
	e.stageJar(version, jar, testManifest(e.t, version, "https://example.com", jar, floor))
	return jar
}

func (e *env) stageJar(version string, jar, manifest []byte) {
	e.t.Helper()
	dir := filepath.Join(e.state, StateSubdir, StagedDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for name, data := range map[string][]byte{JarName: jar, ManifestFile: manifest, SignatureFile: signLine(manifest, e.key)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) apply(action, version string, inUse ...string) *Status {
	e.t.Helper()
	req := Request{ID: newID(), Action: action, Version: version, InUse: inUse, At: e.now}
	b, _ := json.Marshal(req)
	if err := os.MkdirAll(filepath.Join(e.state, StateSubdir), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, StateSubdir, RequestFile), b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	now := e.now
	return Apply(context.Background(), ApplyOptions{Sys: testSys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg,
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: e.goos, Now: func() time.Time { return now }})
}

func (e *env) linkTarget() string {
	e.t.Helper()
	t, err := os.Readlink(e.link)
	if err != nil {
		e.t.Fatalf("link_path: %v", err)
	}
	return t
}

func (e *env) jarPath(v string) string { return filepath.Join(e.root, "versions", v, JarName) }
