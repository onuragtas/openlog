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
	t.Cleanup(func() { trustedKeys = old })

	trustedKeys = ""
	if keys, err := TrustedKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("no keys: %v, %v", keys, err)
	}

	trustedKeys = " " + pub1 + " ,"
	file := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(file, []byte("# test key\n\n"+pub2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := TrustedKeys(file)
	if err != nil || len(keys) != 2 {
		t.Fatalf("compiled + file: %d keys, %v", len(keys), err)
	}

	trustedKeys = "not-base64!"
	if _, err := TrustedKeys(""); err == nil {
		t.Error("invalid compiled key accepted")
	}
	trustedKeys = ""
	if _, err := TrustedKeys(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file accepted")
	}
}
