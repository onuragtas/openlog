package release

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// Product is the only accepted manifest/index product name.
	Product = "openlog"
	// SchemaVersion is the manifest and index schema this package understands.
	SchemaVersion = 1

	ChannelStable = "stable"
	ChannelBeta   = "beta"

	ComponentInfraAgent = "infra-agent"
	// ComponentPHPAgent is the PHP extension (openlog.so for every PHP ABI + openlog-php-install),
	// docs/contracts/php-agent.md §7. Consumers ignore components and formats they do not know, so adding
	// them does not change the schema version.
	ComponentPHPAgent = "php-agent"
	// ComponentJavaAgent is openlog-javaagent-<v>.jar (agents/java, D-072). The jar runs on every platform, so its
	// os and arch are PlatformAny.
	ComponentJavaAgent = "java-agent"
	// ComponentNodeAgent is the npm package tarball openlog-node-<v>.tgz of @openlog/node (agents/node, `npm pack`).
	ComponentNodeAgent = "node-agent"
	// ComponentPythonAgent is the wheel openlog_agent-<pep440 v>-py3-none-any.whl of openlog-agent (agents/python).
	ComponentPythonAgent = "python-agent"
	// ComponentDotnetAgent is the NuGet package OpenLog.Agent.<v>.nupkg (agents/dotnet).
	ComponentDotnetAgent = "dotnet-agent"

	FormatTarGz = "tar.gz"
	FormatDeb   = "deb"
	FormatRPM   = "rpm"
	FormatAPK   = "apk"
	FormatJar   = "jar"
	FormatTgz   = "tgz"   // npm package tarball
	FormatWheel = "whl"   // Python wheel
	FormatNupkg = "nupkg" // NuGet package

	// PlatformAny is the os and arch of platform-independent artifacts (the Java agent jar, language agent packages).
	PlatformAny = "any"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Manifest describes one release. See docs/contracts/releases-updates.md §2.
type Manifest struct {
	Schema        int               `json:"schema"`
	Product       string            `json:"product"`
	Version       string            `json:"version"`
	Channel       string            `json:"channel"`
	ReleasedAt    time.Time         `json:"released_at"`
	NotesURL      string            `json:"notes_url,omitempty"`
	Compatibility Compatibility     `json:"compatibility"`
	Artifacts     []Artifact        `json:"artifacts"`
	Images        map[string]string `json:"images,omitempty"`
	HelmChart     *File             `json:"helm_chart,omitempty"`
	// HelmCharts lists every Helm chart of the release by chart name ("openlog", "openlog-agent").
	// helm_chart stays the "openlog" chart for consumers that predate this field; unknown fields are
	// ignored by older binaries, so adding it does not change the schema version.
	HelmCharts map[string]File          `json:"helm_charts,omitempty"`
	Migrations map[string]MigrationInfo `json:"migrations,omitempty"`

	version Version
}

// Compatibility holds the version bounds of a release. Empty fields mean "no bound".
type Compatibility struct {
	MinBackendForAgent   string `json:"min_backend_for_agent,omitempty"`
	OldestSupportedAgent string `json:"oldest_supported_agent,omitempty"`
	MinUpgradeFrom       string `json:"min_upgrade_from,omitempty"`
	RollbackFloor        string `json:"rollback_floor,omitempty"`
}

// Artifact is one downloadable file of a release.
type Artifact struct {
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Format    string `json:"format"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// File is a release file that is not platform specific.
type File struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// MigrationInfo summarizes the migrations shipped with a release for one database.
type MigrationInfo struct {
	Latest          int   `json:"latest"`
	ContractPending []int `json:"contract_pending"`
}

// ParseManifest decodes and validates a manifest. It does not verify signatures; use
// VerifyManifest for untrusted input.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("unsupported schema %d", m.Schema)
	}
	if m.Product != Product {
		return fmt.Errorf("unexpected product %q", m.Product)
	}
	v, err := ParseVersion(m.Version)
	if err != nil {
		return err
	}
	if strings.HasPrefix(m.Version, "v") {
		return fmt.Errorf("version %q must not have a leading v", m.Version)
	}
	m.version = v
	switch m.Channel {
	case ChannelStable:
		if v.IsPrerelease() {
			return fmt.Errorf("pre-release %s cannot be on channel stable", m.Version)
		}
	case ChannelBeta:
	default:
		return fmt.Errorf("unknown channel %q", m.Channel)
	}
	for name, s := range map[string]string{
		"min_backend_for_agent":  m.Compatibility.MinBackendForAgent,
		"oldest_supported_agent": m.Compatibility.OldestSupportedAgent,
		"min_upgrade_from":       m.Compatibility.MinUpgradeFrom,
		"rollback_floor":         m.Compatibility.RollbackFloor,
	} {
		if s == "" {
			continue
		}
		if _, err := ParseVersion(s); err != nil {
			return fmt.Errorf("compatibility.%s: %w", name, err)
		}
	}
	seen := map[string]bool{}
	for i, a := range m.Artifacts {
		if a.Component == "" || a.OS == "" || a.Arch == "" || a.Format == "" {
			return fmt.Errorf("artifacts[%d]: component, os, arch and format are required", i)
		}
		if a.Name == "" || strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
			return fmt.Errorf("artifacts[%d]: invalid name %q", i, a.Name)
		}
		if err := checkURL(a.URL); err != nil {
			return fmt.Errorf("artifacts[%d]: %w", i, err)
		}
		if !sha256Hex.MatchString(a.SHA256) {
			return fmt.Errorf("artifacts[%d]: sha256 must be 64 lowercase hex characters", i)
		}
		if a.Size <= 0 {
			return fmt.Errorf("artifacts[%d]: size must be positive", i)
		}
		key := a.Component + "/" + a.OS + "/" + a.Arch + "/" + a.Format
		if seen[key] {
			return fmt.Errorf("artifacts[%d]: duplicate %s", i, key)
		}
		seen[key] = true
	}
	if m.HelmChart != nil && m.HelmChart.SHA256 != "" && !sha256Hex.MatchString(m.HelmChart.SHA256) {
		return fmt.Errorf("helm_chart: sha256 must be 64 lowercase hex characters")
	}
	for chart, f := range m.HelmCharts {
		if chart == "" || strings.ContainsAny(chart, `/\`) {
			return fmt.Errorf("helm_charts: invalid chart name %q", chart)
		}
		if f.Name == "" || strings.ContainsAny(f.Name, `/\`) || f.Name == "." || f.Name == ".." {
			return fmt.Errorf("helm_charts.%s: invalid name %q", chart, f.Name)
		}
		if err := checkURL(f.URL); err != nil {
			return fmt.Errorf("helm_charts.%s: %w", chart, err)
		}
		if !sha256Hex.MatchString(f.SHA256) {
			return fmt.Errorf("helm_charts.%s: sha256 must be 64 lowercase hex characters", chart)
		}
	}
	return nil
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("url %q: scheme must be https or http", s)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q: missing host", s)
	}
	return nil
}

// ParsedVersion returns the validated version of the manifest.
func (m *Manifest) ParsedVersion() Version { return m.version }

// Artifact finds the artifact for a component and platform.
func (m *Manifest) Artifact(component, os, arch, format string) (Artifact, bool) {
	for _, a := range m.Artifacts {
		if a.Component == component && a.OS == os && a.Arch == arch && a.Format == format {
			return a, true
		}
	}
	return Artifact{}, false
}

// Index lists releases per channel. See docs/contracts/releases-updates.md §2 "Index".
type Index struct {
	Schema      int                     `json:"schema"`
	Product     string                  `json:"product"`
	GeneratedAt time.Time               `json:"generated_at"`
	Channels    map[string][]IndexEntry `json:"channels"`
}

// IndexEntry points to a release manifest.
type IndexEntry struct {
	Version     string `json:"version"`
	ManifestURL string `json:"manifest_url"`
}

// ParseIndex decodes and validates an index. It does not verify signatures; use VerifyIndex for
// untrusted input.
func ParseIndex(data []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	if idx.Schema != SchemaVersion {
		return nil, fmt.Errorf("index: unsupported schema %d", idx.Schema)
	}
	if idx.Product != Product {
		return nil, fmt.Errorf("index: unexpected product %q", idx.Product)
	}
	for ch, entries := range idx.Channels {
		if ch != ChannelStable && ch != ChannelBeta {
			return nil, fmt.Errorf("index: unknown channel %q", ch)
		}
		for i, e := range entries {
			v, err := ParseVersion(e.Version)
			if err != nil {
				return nil, fmt.Errorf("index: channels.%s[%d]: %w", ch, i, err)
			}
			if ch == ChannelStable && v.IsPrerelease() {
				return nil, fmt.Errorf("index: channels.stable[%d]: pre-release %s", i, e.Version)
			}
			if err := checkURL(e.ManifestURL); err != nil {
				return nil, fmt.Errorf("index: channels.%s[%d]: %w", ch, i, err)
			}
		}
	}
	return &idx, nil
}

// Latest returns the newest release visible on a channel. The beta channel also sees stable
// releases.
func (idx *Index) Latest(channel string) (IndexEntry, bool) {
	var candidates []IndexEntry
	switch channel {
	case ChannelStable:
		candidates = idx.Channels[ChannelStable]
	case ChannelBeta:
		candidates = append(append(candidates, idx.Channels[ChannelBeta]...), idx.Channels[ChannelStable]...)
	default:
		return IndexEntry{}, false
	}
	var best IndexEntry
	var bestV Version
	found := false
	for _, e := range candidates {
		v, err := ParseVersion(e.Version)
		if err != nil {
			continue
		}
		if !found || Compare(v, bestV) > 0 {
			best, bestV, found = e, v, true
		}
	}
	return best, found
}
