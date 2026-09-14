package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

func runCmd(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return out.String() + errb.String(), code
}

// keygen returns the public key and seed printed by the keygen command.
func keygen(t *testing.T) (pub, seed string) {
	t.Helper()
	out, code := runCmd(t, "keygen")
	if code != 0 {
		t.Fatalf("keygen: %d %s", code, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "OPENLOG_RELEASE_PUBLIC_KEYS="); ok {
			pub = v
		}
		if v, ok := strings.CutPrefix(l, "OPENLOG_RELEASE_SIGNING_KEY="); ok {
			seed = v
		}
	}
	if _, err := lib.ParsePublicKey(pub); err != nil {
		t.Fatalf("public key: %v", err)
	}
	if _, err := lib.PrivateKeyFromSeed(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return pub, seed
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]*lib.Artifact{
		"openlog-infra-agent_0.4.0_linux_amd64.tar.gz": {Component: "infra-agent", OS: "linux", Arch: "amd64", Format: "tar.gz"},
		"openlog-infra-agent_0.4.0_linux_arm64.deb":    {Component: "infra-agent", OS: "linux", Arch: "arm64", Format: "deb"},
		"openlog-infra-agent_0.4.0_linux_amd64.rpm":    {Component: "infra-agent", OS: "linux", Arch: "amd64", Format: "rpm"},
		"openlog_0.4.0_linux_arm64.tar.gz":             {Component: "backend", OS: "linux", Arch: "arm64", Format: "tar.gz"},
		"openlog-php-agent_0.4.0_linux_amd64.tar.gz":   {Component: "php-agent", OS: "linux", Arch: "amd64", Format: "tar.gz"},
		"openlog-php-agent_0.4.0_linux_arm64.deb":      {Component: "php-agent", OS: "linux", Arch: "arm64", Format: "deb"},
		"openlog-php-agent_0.4.0_linux_amd64.rpm":      {Component: "php-agent", OS: "linux", Arch: "amd64", Format: "rpm"},
		"openlog-php-agent_0.4.0_linux_arm64.apk":      {Component: "php-agent", OS: "linux", Arch: "arm64", Format: "apk"},
		"openlog-php-agent_0.3.0_linux_amd64.apk":      nil,
		"openlog-infra-agent_0.4.0_linux_amd64.apk":    {Component: "infra-agent", OS: "linux", Arch: "amd64", Format: "apk"},
		"openlog_0.4.0_linux_amd64.zip":                nil,
		"openlog-infra-agent_0.3.0_linux_amd64.tar.gz": nil, // other version
		"openlog-0.4.0.tgz":                            nil,
		"manifest.json":                                nil,
		"install.sh":                                   nil,
		"openlog-infra-agent_0.4.0_linux.tar.gz":       nil,
		"openlog-javaagent-0.4.0.jar":                  {Component: "java-agent", OS: "any", Arch: "any", Format: "jar"},
		"openlog-javaagent-0.3.0.jar":                  nil,
		"openlog-javaagent-0.4.0.jar.sha256":           nil,
	}
	for name, want := range cases {
		got, ok := classify(name, "0.4.0")
		if want == nil {
			if ok {
				t.Errorf("%s: unexpected artifact %+v", name, got)
			}
			continue
		}
		want.Name = name
		if !ok || got != *want {
			t.Errorf("%s: got %+v %v, want %+v", name, got, ok, *want)
		}
	}
}

func TestReleaseFlow(t *testing.T) {
	pub, seed := keygen(t)
	pub2, seed2 := keygen(t)
	root := t.TempDir()
	dist := filepath.Join(root, "v0.4.0")
	writeFile(t, filepath.Join(dist, "openlog-infra-agent_0.4.0_linux_amd64.tar.gz"), "agent-amd64")
	writeFile(t, filepath.Join(dist, "openlog-infra-agent_0.4.0_linux_amd64.deb"), "deb")
	writeFile(t, filepath.Join(dist, "openlog_0.4.0_linux_amd64.tar.gz"), "backend")
	writeFile(t, filepath.Join(dist, "openlog-php-agent_0.4.0_linux_amd64.tar.gz"), "php-modules")
	writeFile(t, filepath.Join(dist, "openlog-php-agent_0.4.0_linux_amd64.apk"), "apk")
	writeFile(t, filepath.Join(dist, "openlog-javaagent-0.4.0.jar"), "jar")
	writeFile(t, filepath.Join(dist, "openlog-javaagent-0.4.0.jar.sha256"), "sum")
	writeFile(t, filepath.Join(dist, "openlog-0.4.0.tgz"), "chart")
	writeFile(t, filepath.Join(dist, "openlog-agent-0.4.0.tgz"), "agent-chart")
	writeFile(t, filepath.Join(dist, "openlog-agent-0.3.0.tgz"), "old agent chart")
	writeFile(t, filepath.Join(dist, "install.sh"), "#!/bin/sh\n")
	mig := filepath.Join(root, "migrations")
	writeFile(t, filepath.Join(mig, "0001_init.sql"), "-- openlog:phase expand\n")
	writeFile(t, filepath.Join(mig, "0002_drop.sql"), "-- openlog:phase contract\n")
	writeFile(t, filepath.Join(mig, "README"), "")

	base := "https://example.com/releases/download/v0.4.0"
	out, code := runCmd(t, "build-manifest", "--version", "v0.4.0", "--dist", dist, "--base-url", base+"/",
		"--released-at", "2026-10-01T12:00:00Z", "--compat", "min_upgrade_from=0.3.0",
		"--image", "openlog=ghcr.io/onuragtas/openlog@sha256:"+strings.Repeat("a", 64),
		"--migrations", "postgres="+mig)
	if code != 0 {
		t.Fatalf("build-manifest: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(dist, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := lib.ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Channel != "stable" || m.Version != "0.4.0" || len(m.Artifacts) != 6 {
		t.Fatalf("manifest = %+v", m)
	}
	if a, ok := m.Artifact(lib.ComponentJavaAgent, lib.PlatformAny, lib.PlatformAny, lib.FormatJar); !ok || a.Size != int64(len("jar")) {
		t.Errorf("java-agent jar = %+v %v", a, ok)
	}
	if a, ok := m.Artifact(lib.ComponentPHPAgent, "linux", "amd64", lib.FormatTarGz); !ok || a.Size != int64(len("php-modules")) {
		t.Errorf("php-agent tarball = %+v %v", a, ok)
	}
	if _, ok := m.Artifact(lib.ComponentPHPAgent, "linux", "amd64", lib.FormatAPK); !ok {
		t.Error("php-agent apk missing from the manifest")
	}
	if m.Compatibility != (lib.Compatibility{MinUpgradeFrom: "0.3.0", OldestSupportedAgent: "0.2.0", RollbackFloor: "0.3.0"}) {
		t.Errorf("compatibility = %+v", m.Compatibility)
	}
	a, ok := m.Artifact(lib.ComponentInfraAgent, "linux", "amd64", lib.FormatTarGz)
	if !ok || a.URL != base+"/openlog-infra-agent_0.4.0_linux_amd64.tar.gz" || a.Size != int64(len("agent-amd64")) {
		t.Errorf("agent artifact = %+v", a)
	}
	if m.HelmChart == nil || m.HelmChart.Name != "openlog-0.4.0.tgz" {
		t.Errorf("helm chart = %+v", m.HelmChart)
	}
	if len(m.HelmCharts) != 2 || m.HelmCharts["openlog"] != *m.HelmChart ||
		m.HelmCharts["openlog-agent"].URL != base+"/openlog-agent-0.4.0.tgz" {
		t.Errorf("helm charts = %+v", m.HelmCharts)
	}
	if got := m.Migrations["postgres"]; got.Latest != 2 || len(got.ContractPending) != 1 || got.ContractPending[0] != 2 {
		t.Errorf("migrations = %+v", got)
	}

	manifest := filepath.Join(dist, "manifest.json")
	if out, code := runCmd(t, "verify", "--keys", pub, manifest); code == 0 {
		t.Fatalf("verify without signature succeeded: %s", out)
	}
	t.Setenv("OPENLOG_RELEASE_SIGNING_KEY", seed)
	if out, code := runCmd(t, "sign", manifest); code != 0 {
		t.Fatalf("sign: %s", out)
	}
	// Re-signing with the same key replaces its line; a second key adds one (rotation).
	runCmd(t, "sign", manifest)
	t.Setenv("OTHER_KEY", seed2)
	if out, code := runCmd(t, "sign", "--key-env", "OTHER_KEY", manifest); code != 0 {
		t.Fatalf("sign 2: %s", out)
	}
	sig, _ := os.ReadFile(manifest + ".sig")
	if n := strings.Count(string(sig), lib.SignaturePrefix); n != 2 {
		t.Errorf("signature lines = %d:\n%s", n, sig)
	}

	keyFile := filepath.Join(root, "keys")
	writeFile(t, keyFile, "# test\n"+pub2+"\n")
	for _, keys := range []string{pub, keyFile, pub + "," + pub2} {
		out, code := runCmd(t, "verify", "--keys", keys, "--check-artifacts", manifest)
		if code != 0 || !strings.Contains(out, "OK manifest 0.4.0") || !strings.Contains(out, "OK helm chart openlog-0.4.0.tgz") ||
			!strings.Contains(out, "OK helm chart openlog-agent-0.4.0.tgz") {
			t.Errorf("verify --keys %s: %d %s", keys, code, out)
		}
	}
	other, _ := keygen(t)
	if out, code := runCmd(t, "verify", "--keys", other, manifest); code != 1 {
		t.Errorf("verify with untrusted key: %d %s", code, out)
	}
	agentChart := filepath.Join(dist, "openlog-agent-0.4.0.tgz")
	writeFile(t, agentChart, "tampered chart")
	if out, code := runCmd(t, "verify", "--keys", pub, "--check-artifacts", manifest); code != 1 ||
		!strings.Contains(out, "helm chart openlog-agent-0.4.0.tgz: sha256 mismatch") {
		t.Errorf("verify tampered agent chart: %d %s", code, out)
	}
	writeFile(t, agentChart, "agent-chart")
	writeFile(t, filepath.Join(dist, "openlog-infra-agent_0.4.0_linux_amd64.deb"), "tampered")
	if out, code := runCmd(t, "verify", "--keys", pub, "--check-artifacts", manifest); code != 1 || !strings.Contains(out, "mismatch") {
		t.Errorf("verify tampered artifact: %d %s", code, out)
	}

	// Index: a beta manifest, an existing entry and a merged index.
	beta := filepath.Join(root, "v0.5.0-beta.1")
	writeFile(t, filepath.Join(beta, "openlog-infra-agent_0.5.0-beta.1_linux_amd64.tar.gz"), "b")
	if out, code := runCmd(t, "build-manifest", "--version", "0.5.0-beta.1", "--dist", beta, "--base-url", "https://example.com/b"); code != 0 {
		t.Fatalf("build-manifest beta: %s", out)
	}
	if out, code := runCmd(t, "sign", filepath.Join(beta, "manifest.json")); code != 0 {
		t.Fatal(out)
	}
	oldIndex := filepath.Join(root, "old-index.json")
	writeFile(t, oldIndex, `{"schema":1,"product":"openlog","generated_at":"2026-01-01T00:00:00Z","channels":{"stable":[{"version":"0.2.0","manifest_url":"https://example.com/v0.2.0/manifest.json"}],"beta":[]}}`)
	index := filepath.Join(root, "index.json")
	out, code = runCmd(t, "build-index", "--out", index, "--base-url", "https://example.com/releases/download",
		"--merge", oldIndex, "--entry", "0.3.0=https://example.com/v0.3.0/manifest.json", "--keys", pub,
		manifest, filepath.Join(beta, "manifest.json"))
	if code != 0 {
		t.Fatalf("build-index: %s", out)
	}
	idxData, _ := os.ReadFile(index)
	idx, err := lib.ParseIndex(idxData)
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, e := range idx.Channels["stable"] {
		versions = append(versions, e.Version)
	}
	if strings.Join(versions, ",") != "0.4.0,0.3.0,0.2.0" {
		t.Errorf("stable = %v", versions)
	}
	if b := idx.Channels["beta"]; len(b) != 1 || b[0].ManifestURL != "https://example.com/releases/download/v0.5.0-beta.1/manifest.json" {
		t.Errorf("beta = %+v", b)
	}
	if out, code := runCmd(t, "sign", index); code != 0 {
		t.Fatal(out)
	}
	if out, code := runCmd(t, "verify", "--keys", pub, index); code != 0 || !strings.Contains(out, "latest_stable=0.4.0 latest_beta=0.5.0-beta.1") {
		t.Errorf("verify index: %s", out)
	}
}

func TestBuildManifestErrors(t *testing.T) {
	dir := t.TempDir()
	if _, code := runCmd(t, "build-manifest", "--version", "0.4.0"); code != 2 {
		t.Error("missing flags accepted")
	}
	if out, code := runCmd(t, "build-manifest", "--version", "0.4.0", "--dist", dir, "--base-url", "https://e.com"); code != 1 || !strings.Contains(out, "no artifacts") {
		t.Errorf("empty dist: %d %s", code, out)
	}
	writeFile(t, filepath.Join(dir, "openlog-infra-agent_1.0.0-rc.1_linux_amd64.tar.gz"), "x")
	if _, code := runCmd(t, "build-manifest", "--version", "1.0.0-rc.1", "--channel", "stable", "--dist", dir, "--base-url", "https://e.com"); code != 1 {
		t.Error("pre-release on stable accepted")
	}
	if _, code := runCmd(t, "build-manifest", "--version", "1.0.0-rc.1", "--compat", "bogus=1.0.0", "--dist", dir, "--base-url", "https://e.com"); code != 2 {
		t.Error("unknown compat key accepted")
	}
	t.Setenv("OPENLOG_RELEASE_SIGNING_KEY", "")
	if out, code := runCmd(t, "sign", filepath.Join(dir, "openlog-infra-agent_1.0.0-rc.1_linux_amd64.tar.gz")); code != 1 || !strings.Contains(out, "empty") {
		t.Errorf("sign without key: %d %s", code, out)
	}
}

func TestArchive(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "openlog-infra-agent"), "bin")
	if err := os.Chmod(filepath.Join(src, "openlog-infra-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "LICENSE"), "l")
	writeFile(t, filepath.Join(src, "packaging", "systemd", "x.service"), "s")
	writeFile(t, filepath.Join(src, "._LICENSE"), "appledouble")
	out := filepath.Join(t.TempDir(), "a.tar.gz")
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	if o, code := runCmd(t, "archive", "--out", out, "--prefix", "top", src); code != 0 {
		t.Fatal(o)
	}
	f, _ := os.Open(out)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "top/openlog-infra-agent" && h.Mode != 0o755 {
			t.Errorf("binary mode %o", h.Mode)
		}
		if h.ModTime.Unix() != 1700000000 {
			t.Errorf("%s mtime %v", h.Name, h.ModTime)
		}
	}
	want := "top/,top/LICENSE,top/openlog-infra-agent,top/packaging/,top/packaging/systemd/,top/packaging/systemd/x.service"
	if strings.Join(names, ",") != want {
		t.Errorf("entries = %v", names)
	}

	if err := os.Symlink("LICENSE", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if _, code := runCmd(t, "archive", "--out", out, "--prefix", "top", src); code != 1 {
		t.Error("symlink accepted")
	}
}

func TestKeygenOutputIsJSONFree(t *testing.T) {
	out, _ := runCmd(t, "keygen")
	if json.Valid([]byte(out)) {
		t.Error("keygen should print KEY=value lines")
	}
	if _, code := runCmd(t, "nope"); code != 2 {
		t.Error("unknown command exit code")
	}
}
