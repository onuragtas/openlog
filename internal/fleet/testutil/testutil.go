// Package testutil builds signed test releases (manifests, index, fake agent tarballs) and local
// mirror directories with libs/release. Test keys made here must never sign real releases.
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	lib "github.com/onuragtas/openlog/libs/release"
)

// DefaultBaseURL is used for manifest and artifact URLs when a spec has none.
const DefaultBaseURL = "https://github.com/onuragtas/openlog/releases/download"

// Signer holds a release signing key.
type Signer struct{ Priv ed25519.PrivateKey }

// NewSigner generates a random key.
func NewSigner() (Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return Signer{Priv: priv}, err
}

// SignerFromSeed derives a key from a 32-byte seed.
func SignerFromSeed(seed []byte) Signer { return Signer{Priv: ed25519.NewKeyFromSeed(seed)} }

// PublicKey returns the public key.
func (s Signer) PublicKey() ed25519.PublicKey { return s.Priv.Public().(ed25519.PublicKey) }

// PublicKeyBase64 is the key in the trusted keys file format.
func (s Signer) PublicKeyBase64() string { return base64.StdEncoding.EncodeToString(s.PublicKey()) }

// Sign returns a signature file for data.
func (s Signer) Sign(data []byte) []byte { return []byte(lib.SignatureLine(data, s.Priv) + "\n") }

// ReleaseSpec describes a test release.
type ReleaseSpec struct {
	Version              string
	Channel              string // default: beta for pre-releases, else stable
	MinUpgradeFrom       string
	RollbackFloor        string
	OldestSupportedAgent string
	Platforms            []string // "os/arch"; default linux/amd64 and linux/arm64
	ReleasedAt           time.Time
	BaseURL              string // default DefaultBaseURL
	// PHPAgent adds a php-agent tar.gz artifact for every platform (php-agent.md §7.1).
	PHPAgent bool
}

// PHPArtifactName is the tarball name of the PHP agent.
func PHPArtifactName(version, goos, arch string) string {
	return fmt.Sprintf("openlog-php-agent_%s_%s_%s.tar.gz", version, goos, arch)
}

func (s ReleaseSpec) withDefaults() (ReleaseSpec, lib.Version, error) {
	v, err := lib.ParseVersion(s.Version)
	if err != nil {
		return s, v, err
	}
	s.Version = v.String()
	if s.Channel == "" {
		s.Channel = lib.ChannelStable
		if v.IsPrerelease() {
			s.Channel = lib.ChannelBeta
		}
	}
	if len(s.Platforms) == 0 {
		s.Platforms = []string{"linux/amd64", "linux/arm64"}
	}
	if s.ReleasedAt.IsZero() {
		s.ReleasedAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	return s, v, nil
}

// Built is a signed release and its files (paths relative to the release directory v<version>/).
type Built struct {
	Release *catalog.Release
	Files   map[string][]byte
}

// ArtifactName is the tarball name of the infra agent.
func ArtifactName(version, goos, arch string) string {
	return fmt.Sprintf("openlog-infra-agent_%s_%s_%s.tar.gz", version, goos, arch)
}

// Tarball builds a deterministic agent tarball with the contract layout. The "binary" is a shell
// script that prints the version.
func Tarball(version, goos, arch string) ([]byte, error) {
	top := fmt.Sprintf("openlog-infra-agent_%s_%s_%s/", version, goos, arch)
	files := []struct {
		name string
		mode int64
		body string
	}{
		{top + "openlog-infra-agent", 0o755, "#!/bin/sh\necho openlog-infra-agent " + version + "\n"},
		{top + "LICENSE", 0o644, "test release\n"},
		{top + "README.md", 0o644, "# test release " + version + "\n"},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime}); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.body)), ModTime: mtime}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Build creates and signs a release.
func Build(s Signer, spec ReleaseSpec) (Built, error) {
	spec, v, err := spec.withDefaults()
	if err != nil {
		return Built{}, err
	}
	files := map[string][]byte{}
	m := lib.Manifest{
		Schema: lib.SchemaVersion, Product: lib.Product, Version: spec.Version, Channel: spec.Channel,
		ReleasedAt: spec.ReleasedAt, NotesURL: "https://github.com/onuragtas/openlog/releases/tag/v" + spec.Version,
		Compatibility: lib.Compatibility{
			OldestSupportedAgent: spec.OldestSupportedAgent, MinUpgradeFrom: spec.MinUpgradeFrom, RollbackFloor: spec.RollbackFloor,
		},
		Artifacts: []lib.Artifact{},
	}
	for _, p := range spec.Platforms {
		goos, arch, ok := strings.Cut(p, "/")
		if !ok {
			return Built{}, fmt.Errorf("platform %q: want os/arch", p)
		}
		body, err := Tarball(spec.Version, goos, arch)
		if err != nil {
			return Built{}, err
		}
		name := ArtifactName(spec.Version, goos, arch)
		sum := sha256.Sum256(body)
		files[name] = body
		m.Artifacts = append(m.Artifacts, lib.Artifact{
			Component: lib.ComponentInfraAgent, OS: goos, Arch: arch, Format: lib.FormatTarGz, Name: name,
			URL: spec.BaseURL + "/v" + spec.Version + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
		})
		if spec.PHPAgent {
			php := append([]byte("php agent "+spec.Version+" "), body...)
			phpName := PHPArtifactName(spec.Version, goos, arch)
			phpSum := sha256.Sum256(php)
			files[phpName] = php
			m.Artifacts = append(m.Artifacts, lib.Artifact{
				Component: lib.ComponentPHPAgent, OS: goos, Arch: arch, Format: lib.FormatTarGz, Name: phpName,
				URL: spec.BaseURL + "/v" + spec.Version + "/" + phpName, SHA256: hex.EncodeToString(phpSum[:]), Size: int64(len(php)),
			})
		}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Built{}, err
	}
	sig := s.Sign(raw)
	files["manifest.json"], files["manifest.json.sig"] = raw, sig
	parsed, keyID, err := lib.VerifyManifest(raw, sig, []ed25519.PublicKey{s.PublicKey()})
	if err != nil {
		return Built{}, err
	}
	_ = v
	return Built{Release: &catalog.Release{Version: parsed.ParsedVersion(), Manifest: parsed, Raw: raw, Signature: sig, KeyID: keyID}, Files: files}, nil
}

// Snapshot builds a catalog snapshot from specs.
func Snapshot(s Signer, specs ...ReleaseSpec) (*catalog.Snapshot, error) {
	var rels []*catalog.Release
	for _, spec := range specs {
		b, err := Build(s, spec)
		if err != nil {
			return nil, err
		}
		rels = append(rels, b.Release)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return catalog.NewSnapshot(rels, now, now), nil
}

// Index builds and signs index.json for specs.
func Index(s Signer, specs []ReleaseSpec, generatedAt time.Time) (raw, sig []byte, err error) {
	idx := lib.Index{Schema: lib.SchemaVersion, Product: lib.Product, GeneratedAt: generatedAt.UTC(),
		Channels: map[string][]lib.IndexEntry{lib.ChannelStable: {}, lib.ChannelBeta: {}}}
	for _, spec := range specs {
		spec, _, err := spec.withDefaults()
		if err != nil {
			return nil, nil, err
		}
		idx.Channels[spec.Channel] = append(idx.Channels[spec.Channel], lib.IndexEntry{
			Version: spec.Version, ManifestURL: spec.BaseURL + "/v" + spec.Version + "/manifest.json",
		})
	}
	raw, err = json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return raw, s.Sign(raw), nil
}

// WriteMirror writes a mirror directory: index.json(.sig) and v<version>/{manifest.json(.sig), tarballs}.
func WriteMirror(dir string, s Signer, specs []ReleaseSpec, generatedAt time.Time) error {
	for _, spec := range specs {
		b, err := Build(s, spec)
		if err != nil {
			return err
		}
		rd := filepath.Join(dir, "v"+b.Release.Manifest.Version)
		if err := os.MkdirAll(rd, 0o755); err != nil {
			return err
		}
		for name, body := range b.Files {
			if err := os.WriteFile(filepath.Join(rd, name), body, 0o644); err != nil {
				return err
			}
		}
	}
	raw, sig, err := Index(s, specs, generatedAt)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json.sig"), sig, 0o644)
}

// WriteKeysFile writes a trusted keys file (OPENLOG_RELEASE_TRUSTED_KEYS_FILE).
func WriteKeysFile(path string, signers ...Signer) error {
	var b strings.Builder
	b.WriteString("# openlog test release keys — never trust these in production\n")
	for _, s := range signers {
		b.WriteString(s.PublicKeyBase64() + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
