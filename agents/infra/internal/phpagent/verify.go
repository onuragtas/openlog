package phpagent

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	lib "github.com/onuragtas/openlog/libs/release"
)

// ErrNoTrustedKeys is reported by builds without release keys.
var ErrNoTrustedKeys = errors.New("no trusted release keys")

// Verified is a PHP agent release that passed Verify.
type Verified struct {
	Version  lib.Version
	Manifest *lib.Manifest
	Artifact lib.Artifact
}

// Verify applies the agent update rules to a PHP agent release (releases-updates.md §3, php-agent.md §7.3 step 3):
// signature by a trusted key, product/schema, manifest version = target, a php-agent tar.gz artifact for the platform,
// and for a downgrade target >= rollback_floor of the installed version's manifest. installed returns the manifest
// kept in the installed version directory, or nil when nothing is installed.
func Verify(manifest, signature []byte, trusted []ed25519.PublicKey, target, osName, arch string, installed func() (*lib.Manifest, error)) (*Verified, error) {
	if len(trusted) == 0 {
		return nil, ErrNoTrustedKeys
	}
	if _, err := lib.Verify(manifest, signature, trusted); err != nil {
		return nil, fmt.Errorf("manifest signature invalid: %w", err)
	}
	m, err := lib.ParseManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	tv, err := lib.ParseVersion(target)
	if err != nil {
		return nil, fmt.Errorf("invalid target version: %w", err)
	}
	if m.Version != tv.String() {
		return nil, fmt.Errorf("manifest version %s does not match target version %s", m.Version, tv)
	}
	art, ok := m.Artifact(lib.ComponentPHPAgent, osName, arch, lib.FormatTarGz)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s %s/%s %s artifact", m.Version, lib.ComponentPHPAgent, osName, arch, lib.FormatTarGz)
	}
	if installed != nil {
		cur, err := installed()
		if err != nil {
			return nil, fmt.Errorf("manifest of the installed PHP agent: %w", err)
		}
		if cur != nil && lib.Compare(tv, cur.ParsedVersion()) < 0 {
			if cur.Compatibility.RollbackFloor == "" {
				return nil, fmt.Errorf("downgrade to %s refused: rollback_floor of installed %s unknown", tv, cur.Version)
			}
			floor, _ := lib.ParseVersion(cur.Compatibility.RollbackFloor)
			if lib.Compare(tv, floor) < 0 {
				return nil, fmt.Errorf("downgrade to %s refused: below rollback_floor %s of installed %s", tv, floor, cur.Version)
			}
		}
	}
	return &Verified{Version: tv, Manifest: m, Artifact: art}, nil
}
