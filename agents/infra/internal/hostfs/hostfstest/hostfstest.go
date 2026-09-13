// Package hostfstest builds fixture host trees for tests.
package hostfstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// SymlinkPrefix marks a file value that should become a symlink to the rest
// of the string (e.g. "symlink:/usr/bin/redis-server").
const SymlinkPrefix = "symlink:"

// DirMarker as a value creates an empty directory.
const DirMarker = "<dir>"

// Build creates the given files under a temp dir and returns an FS rooted there.
func Build(t testing.TB, files map[string]string) *hostfs.FS {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.Clean("/"+p))
		if content == DirMarker {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, SymlinkPrefix); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hostfs.New(root)
}
