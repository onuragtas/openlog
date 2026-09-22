package ebpfprofiler

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

func TestVerifyAcceptsASignedRelease(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "1.2.3", "linux", "amd64", "")
	v, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.2.3", "linux", "amd64", nil)
	if err != nil {
		t.Fatalf("a correctly signed release was refused: %v", err)
	}
	if v.Version.String() != "1.2.3" {
		t.Errorf("version = %s", v.Version)
	}
	if v.Artifact.Component != lib.ComponentEBPFProfiler {
		t.Errorf("artifact component = %q", v.Artifact.Component)
	}
}

// A build with no keys must refuse rather than install whatever it downloaded.
func TestVerifyRefusesWithoutTrustedKeys(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "1.2.3", "linux", "amd64", "")
	if _, err := Verify(mf, signLine(mf, k), nil, "1.2.3", "linux", "amd64", nil); !errors.Is(err, ErrNoTrustedKeys) {
		t.Errorf("error = %v, want ErrNoTrustedKeys", err)
	}
}

// Signed by a key nobody trusts: the whole point of the chain.
func TestVerifyRefusesAnUntrustedSignature(t *testing.T) {
	signer, trusted := newKey(t), newKey(t)
	mf := testManifest(t, "1.2.3", "linux", "amd64", "")
	_, err := Verify(mf, signLine(mf, signer), []ed25519.PublicKey{trusted.pub}, "1.2.3", "linux", "amd64", nil)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("error = %v, want a signature failure", err)
	}
}

// A manifest for another version must not be accepted for this target, however well signed.
func TestVerifyRefusesAVersionMismatch(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "1.2.3", "linux", "amd64", "")
	_, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.2.4", "linux", "amd64", nil)
	if err == nil || !strings.Contains(err.Error(), "does not match target") {
		t.Errorf("error = %v, want a version mismatch", err)
	}
}

// The release may exist and still have nothing for this machine.
func TestVerifyRefusesWhenThePlatformIsMissing(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "1.2.3", "linux", "amd64", "")
	_, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.2.3", "linux", "arm64", nil)
	if err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("error = %v, want a missing artifact", err)
	}
}

// Downgrades are the dangerous direction: they are allowed only down to the installed version's floor.
func TestVerifyDowngradeRules(t *testing.T) {
	k := newKey(t)
	installedWith := func(version, floor string) func() (*lib.Manifest, error) {
		b := testManifest(t, version, "linux", "amd64", floor)
		return func() (*lib.Manifest, error) { return parsed(t, b), nil }
	}

	mf := testManifest(t, "1.0.0", "linux", "amd64", "")
	if _, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.0.0", "linux", "amd64",
		installedWith("2.0.0", "0.9.0")); err != nil {
		t.Errorf("a downgrade above the floor was refused: %v", err)
	}

	if _, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.0.0", "linux", "amd64",
		installedWith("2.0.0", "1.5.0")); err == nil || !strings.Contains(err.Error(), "rollback_floor") {
		t.Errorf("error = %v, want a refusal below the floor", err)
	}

	// No floor recorded means the installed version cannot promise a safe way back.
	if _, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.0.0", "linux", "amd64",
		installedWith("2.0.0", "")); err == nil || !strings.Contains(err.Error(), "rollback_floor") {
		t.Errorf("error = %v, want a refusal with an unknown floor", err)
	}
}

// An upgrade is never blocked by the floor.
func TestVerifyUpgradeIgnoresTheFloor(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "3.0.0", "linux", "amd64", "")
	installed := testManifest(t, "2.0.0", "linux", "amd64", "2.0.0")
	_, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "3.0.0", "linux", "amd64",
		func() (*lib.Manifest, error) { return parsed(t, installed), nil })
	if err != nil {
		t.Errorf("an upgrade was refused: %v", err)
	}
}

// A broken installed manifest is reported, not ignored: silently treating it as "nothing installed" would
// turn a downgrade guard off exactly when it matters.
func TestVerifyPropagatesAnUnreadableInstalledManifest(t *testing.T) {
	k := newKey(t)
	mf := testManifest(t, "1.0.0", "linux", "amd64", "")
	_, err := Verify(mf, signLine(mf, k), []ed25519.PublicKey{k.pub}, "1.0.0", "linux", "amd64",
		func() (*lib.Manifest, error) { return nil, errors.New("unreadable") })
	if err == nil || !strings.Contains(err.Error(), "installed profiler") {
		t.Errorf("error = %v, want the read failure reported", err)
	}
}
