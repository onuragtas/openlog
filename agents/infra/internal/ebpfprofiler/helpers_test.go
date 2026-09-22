package ebpfprofiler

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

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
