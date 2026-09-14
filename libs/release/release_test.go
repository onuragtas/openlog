package release

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"0.0.0", "v1.2.3", "1.2.3-beta.1", "1.2.3-rc.1+build.5", "10.20.30-alpha-1"} {
		if _, err := ParseVersion(s); err != nil {
			t.Errorf("ParseVersion(%q): %v", s, err)
		}
	}
	for _, s := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.x", "1.2.3-ü"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q): want error", s)
		}
	}
	if got := MustParseVersion("v1.2.3-beta.1+x").String(); got != "1.2.3-beta.1" {
		t.Errorf("String() = %q", got)
	}
}

func TestComparePrecedence(t *testing.T) {
	// Ordered list from the SemVer 2.0 specification, plus numeric-width cases.
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "1.10.0", "2.0.0",
	}
	for i := 0; i < len(ordered); i++ {
		for j := 0; j < len(ordered); j++ {
			a, b := MustParseVersion(ordered[i]), MustParseVersion(ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if Compare(MustParseVersion("1.0.0+a"), MustParseVersion("1.0.0+b")) != 0 {
		t.Error("build metadata must not affect precedence")
	}
}

const validManifest = `{
  "schema": 1, "product": "openlog", "version": "0.4.0", "channel": "stable",
  "released_at": "2026-10-01T12:00:00Z",
  "compatibility": {"min_upgrade_from": "0.3.0", "rollback_floor": "0.3.0"},
  "artifacts": [{"component": "infra-agent", "os": "linux", "arch": "amd64", "format": "tar.gz",
    "name": "openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
    "url": "https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
    "sha256": "` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `", "size": 42}]
}`

// A manifest of the PHP agent component (php-agent.md §7.1) keeps schema 1: consumers that only know infra-agent
// ignore the extra artifacts, and apk is a valid format.
func TestParseManifestPHPAgentArtifacts(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	art := func(format string) string {
		name := "openlog-php-agent_0.4.0_linux_arm64." + format
		return `{"component": "php-agent", "os": "linux", "arch": "arm64", "format": "` + format + `", "name": "` + name +
			`", "url": "https://example.com/` + name + `", "sha256": "` + sum + `", "size": 7}`
	}
	data := strings.Replace(validManifest, `"artifacts": [`, `"artifacts": [`+art("tar.gz")+`, `+art("apk")+`, `, 1)
	m, err := ParseManifest([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != SchemaVersion {
		t.Errorf("schema = %d", m.Schema)
	}
	for _, f := range []string{FormatTarGz, FormatAPK} {
		if _, ok := m.Artifact(ComponentPHPAgent, "linux", "arm64", f); !ok {
			t.Errorf("php-agent %s not found", f)
		}
	}
	if _, ok := m.Artifact(ComponentInfraAgent, "linux", "amd64", FormatTarGz); !ok {
		t.Error("infra-agent artifact lost")
	}
	dup := strings.Replace(data, art("apk"), art("tar.gz"), 1)
	if _, err := ParseManifest([]byte(dup)); err == nil || !strings.Contains(err.Error(), "duplicate php-agent/linux/arm64/tar.gz") {
		t.Errorf("duplicate php-agent artifact: err = %v", err)
	}
}

// helm_charts (every chart of the release) is optional and ignored by consumers that only know helm_chart.
func TestParseManifestHelmCharts(t *testing.T) {
	sum := strings.Repeat("cd", 32)
	chart := func(name, file, sha string) string {
		return `"` + name + `": {"name": "` + file + `", "url": "https://example.com/` + file + `", "sha256": "` + sha + `"}`
	}
	with := func(entries string) string {
		return strings.Replace(validManifest, `"artifacts": [`, `"helm_charts": {`+entries+`}, "artifacts": [`, 1)
	}
	m, err := ParseManifest([]byte(with(chart("openlog", "openlog-0.4.0.tgz", sum) + ", " + chart("openlog-agent", "openlog-agent-0.4.0.tgz", sum))))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.HelmCharts) != 2 || m.HelmCharts["openlog-agent"].Name != "openlog-agent-0.4.0.tgz" {
		t.Errorf("helm_charts = %+v", m.HelmCharts)
	}
	for name, entries := range map[string]string{
		"sha256":     chart("openlog-agent", "openlog-agent-0.4.0.tgz", "xyz"),
		"file name":  chart("openlog-agent", "../openlog-agent-0.4.0.tgz", sum),
		"chart name": chart("", "openlog-agent-0.4.0.tgz", sum),
	} {
		if _, err := ParseManifest([]byte(with(entries))); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// An older consumer's view of the manifest: the unknown field does not break decoding.
	var old struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(with(chart("openlog-agent", "openlog-agent-0.4.0.tgz", sum))), &old); err != nil || old.Version != "0.4.0" {
		t.Errorf("old decode: %v %q", err, old.Version)
	}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.ParsedVersion().String() != "0.4.0" {
		t.Errorf("version = %s", m.ParsedVersion())
	}
	if _, ok := m.Artifact(ComponentInfraAgent, "linux", "amd64", FormatTarGz); !ok {
		t.Error("artifact not found")
	}
	if _, ok := m.Artifact(ComponentInfraAgent, "linux", "arm64", FormatTarGz); ok {
		t.Error("unexpected arm64 artifact")
	}

	bad := map[string]func(string) string{
		"schema":     func(s string) string { return strings.Replace(s, `"schema": 1`, `"schema": 2`, 1) },
		"product":    func(s string) string { return strings.Replace(s, `"openlog"`, `"other"`, 1) },
		"leading v":  func(s string) string { return strings.Replace(s, `"version": "0.4.0"`, `"version": "v0.4.0"`, 1) },
		"prerelease": func(s string) string { return strings.Replace(s, `"version": "0.4.0"`, `"version": "0.4.0-beta.1"`, 1) },
		"channel":    func(s string) string { return strings.Replace(s, `"stable"`, `"nightly"`, 1) },
		"compat version": func(s string) string {
			return strings.Replace(s, `"min_upgrade_from": "0.3.0"`, `"min_upgrade_from": "x"`, 1)
		},
		"sha256": func(s string) string { return strings.Replace(s, "0123456789abcdef0123", "0123456789ABCDEF0123", 1) },
		"size":   func(s string) string { return strings.Replace(s, `"size": 42`, `"size": 0`, 1) },
		"name path": func(s string) string {
			return strings.Replace(s, `"name": "openlog-infra-agent`, `"name": "../openlog-infra-agent`, 1)
		},
		"url scheme": func(s string) string { return strings.Replace(s, `"url": "https://`, `"url": "file://`, 1) },
	}
	for name, mutate := range bad {
		if _, err := ParseManifest([]byte(mutate(validManifest))); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSignAndVerify(t *testing.T) {
	pub1, seed1, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub2, seed2, _ := GenerateKey()
	priv1, err := PrivateKeyFromSeed(seed1)
	if err != nil {
		t.Fatal(err)
	}
	priv2, _ := PrivateKeyFromSeed(seed2)
	k1, _ := ParsePublicKey(pub1)
	k2, _ := ParsePublicKey(pub2)
	data := []byte(validManifest)

	sig1 := SignatureLine(data, priv1)
	if id, err := Verify(data, []byte(sig1+"\n"), []ed25519.PublicKey{k1}); err != nil || id != KeyID(k1) {
		t.Fatalf("Verify = %q, %v", id, err)
	}

	// Rotation: a file signed by the old and new key verifies with either trusted set.
	both := []byte("# comment\n" + SignatureLine(data, priv2) + "\n" + sig1 + "\n")
	for _, trusted := range [][]ed25519.PublicKey{{k1}, {k2}, {k2, k1}} {
		if _, err := Verify(data, both, trusted); err != nil {
			t.Errorf("rotation verify: %v", err)
		}
	}

	if _, err := Verify(data, []byte(sig1), []ed25519.PublicKey{k2}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Errorf("untrusted key: err = %v", err)
	}
	tampered := []byte(strings.Replace(validManifest, `"size": 42`, `"size": 43`, 1))
	if _, err := Verify(tampered, []byte(sig1), []ed25519.PublicKey{k1}); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered data: err = %v", err)
	}
	if _, _, err := VerifyManifest(tampered, []byte(sig1), []ed25519.PublicKey{k1}); err == nil {
		t.Error("VerifyManifest accepted tampered data")
	}
	m, id, err := VerifyManifest(data, []byte(sig1), []ed25519.PublicKey{k1})
	if err != nil || m.Version != "0.4.0" || id != KeyID(k1) {
		t.Errorf("VerifyManifest = %v, %q, %v", m, id, err)
	}

	if _, err := ParsePublicKey("AAAA"); err == nil {
		t.Error("short public key accepted")
	}
	if _, err := PrivateKeyFromSeed("AAAA"); err == nil {
		t.Error("short seed accepted")
	}
}

func TestIndexLatest(t *testing.T) {
	idx, err := ParseIndex([]byte(`{"schema": 1, "product": "openlog", "generated_at": "2026-10-01T12:00:00Z",
	  "channels": {
	    "stable": [{"version": "0.3.1", "manifest_url": "https://example.com/v0.3.1/manifest.json"},
	               {"version": "0.4.0", "manifest_url": "https://example.com/v0.4.0/manifest.json"}],
	    "beta":   [{"version": "0.4.0-beta.2", "manifest_url": "https://example.com/v0.4.0-beta.2/manifest.json"},
	               {"version": "0.5.0-beta.1", "manifest_url": "https://example.com/v0.5.0-beta.1/manifest.json"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := idx.Latest(ChannelStable); e.Version != "0.4.0" {
		t.Errorf("stable latest = %s", e.Version)
	}
	if e, _ := idx.Latest(ChannelBeta); e.Version != "0.5.0-beta.1" {
		t.Errorf("beta latest = %s", e.Version)
	}
	if _, ok := idx.Latest("nightly"); ok {
		t.Error("unknown channel returned an entry")
	}
	if _, err := ParseIndex([]byte(`{"schema": 1, "product": "openlog", "channels": {"stable": [{"version": "1.0.0-rc.1", "manifest_url": "https://e.com/m"}]}}`)); err == nil {
		t.Error("pre-release on stable accepted")
	}
}
