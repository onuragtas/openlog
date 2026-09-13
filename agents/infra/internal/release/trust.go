// Package release wires the shared release library (libs/release) into the agent: the trusted
// release signing keys of this build. It mirrors the backend's internal/release package.
package release

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"

	lib "github.com/onuragtas/openlog/libs/release"
)

// trustedKeys is set at build time:
// -X github.com/onuragtas/openlog/agents/infra/internal/release.trustedKeys=<b64>,<b64>
var trustedKeys = ""

// TrustedKeys returns the compiled-in keys plus the keys from keysFile (one base64 key per line,
// "#" comments allowed). An empty result means this build cannot verify releases.
func TrustedKeys(keysFile string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, s := range strings.Split(trustedKeys, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		k, err := lib.ParsePublicKey(s)
		if err != nil {
			return nil, fmt.Errorf("compiled-in release key: %w", err)
		}
		keys = append(keys, k)
	}
	if keysFile == "" {
		return keys, nil
	}
	f, err := os.Open(keysFile)
	if err != nil {
		return nil, fmt.Errorf("release keys file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, err := lib.ParsePublicKey(line)
		if err != nil {
			return nil, fmt.Errorf("release keys file %s: %w", keysFile, err)
		}
		keys = append(keys, k)
	}
	return keys, sc.Err()
}

// CompiledKeyCount returns how many keys were compiled in (for -version output).
func CompiledKeyCount() int {
	n := 0
	for _, s := range strings.Split(trustedKeys, ",") {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}
