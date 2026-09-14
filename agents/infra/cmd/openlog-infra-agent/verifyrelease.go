package main

// "-verify-release": check a release manifest's Ed25519 signature with the keys trusted by this binary and,
// optionally, an artifact's size and sha256 against it. Installers run it with the already installed (trusted)
// binary before replacing it (D-104).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

func runVerifyRelease(configPath string, explicit bool, manifestPath, artifactPath string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "verify-release: "+format+"\n", a...)
		return 1
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	// release.trusted_keys_file only counts when the configuration can be changed by root/Administrators only.
	_, keys := privilegedConfig(update.HostSys(), configPath, explicit, log)
	if len(keys) == 0 {
		return fail("this binary has no trusted release keys")
	}
	data, err := update.ReadLimited(manifestPath, 1<<20)
	if err != nil {
		return fail("manifest: %v", err)
	}
	sig, err := update.ReadLimited(manifestPath+".sig", 64<<10)
	if err != nil {
		return fail("signature: %v", err)
	}
	m, keyID, err := lib.VerifyManifest(data, sig, keys)
	if err != nil {
		return fail("%s: %v", manifestPath, err)
	}
	if artifactPath != "" {
		name := filepath.Base(artifactPath)
		var art *lib.Artifact
		for i := range m.Artifacts {
			if m.Artifacts[i].Name == name {
				art = &m.Artifacts[i]
			}
		}
		if art == nil {
			return fail("release %s has no artifact %s", m.Version, name)
		}
		f, err := os.Open(artifactPath)
		if err != nil {
			return fail("%v", err)
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(f, art.Size+1))
		if err != nil {
			return fail("%v", err)
		}
		if n != art.Size {
			return fail("%s: size %d does not match the signed manifest (%d)", name, n, art.Size)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != art.SHA256 {
			return fail("%s: sha256 %s does not match the signed manifest (%s)", name, got, art.SHA256)
		}
		fmt.Printf("artifact %s verified: %d bytes, sha256 %s\n", name, n, art.SHA256)
	}
	fmt.Printf("release %s verified (signed by key %s)\n", m.Version, keyID)
	return 0
}
