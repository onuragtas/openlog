package release

import (
	"os"
	"path/filepath"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

func TestTrustedKeys(t *testing.T) {
	pub1, _, _ := lib.GenerateKey()
	pub2, _, _ := lib.GenerateKey()
	old := trustedKeys
	defer func() { trustedKeys = old }()

	trustedKeys = ""
	keys, err := TrustedKeys("")
	if err != nil || len(keys) != 0 {
		t.Fatalf("no keys: %v %v", keys, err)
	}

	trustedKeys = pub1 + ", "
	file := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(file, []byte("# test\n\n"+pub2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err = TrustedKeys(file)
	if err != nil || len(keys) != 2 || CompiledKeyCount() != 1 {
		t.Fatalf("keys: %d %v", len(keys), err)
	}

	if _, err := TrustedKeys(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file accepted")
	}
	trustedKeys = "not-base64!"
	if _, err := TrustedKeys(""); err == nil {
		t.Error("bad compiled key accepted")
	}
}
