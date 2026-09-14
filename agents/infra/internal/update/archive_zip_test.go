package update

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

type zipEntry struct {
	name string
	mode os.FileMode
	body string
}

func writeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestArtifactFormat(t *testing.T) {
	if ArtifactFormat("windows") != lib.FormatZip || ArtifactFormat("linux") != lib.FormatTarGz || ArtifactFormat("darwin") != lib.FormatTarGz {
		t.Error("unexpected artifact formats")
	}
}

func TestExtractZip(t *testing.T) {
	top := TopDir("1.2.3", "windows", "amd64")
	archive := writeZip(t, []zipEntry{
		{top + "/", os.ModeDir | 0o755, ""},
		{top + "/openlog-infra-agent.exe", 0o755, "MZ"},
		{top + "/packaging/config.example.yaml", 0o644, "license_key: \"\"\n"},
	})
	dest := filepath.Join(t.TempDir(), "x")
	if err := ExtractArchive(lib.FormatZip, archive, dest, top, 1<<20); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "packaging", "config.example.yaml"))
	if err != nil || !strings.Contains(string(b), "license_key") {
		t.Fatalf("extracted config = %q, %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "openlog-infra-agent.exe")); err != nil || fi.Size() != 2 {
		t.Fatalf("exe: %v", err)
	}

	for name, c := range map[string]struct {
		entries []zipEntry
		max     int64
		want    string
	}{
		"traversal": {[]zipEntry{{top + "/../evil", 0o644, "x"}}, 1 << 20, ".."},
		"outside":   {[]zipEntry{{"other/file", 0o644, "x"}}, 1 << 20, "outside"},
		"absolute":  {[]zipEntry{{"/" + top + "/file", 0o644, "x"}}, 1 << 20, "absolute"},
		"backslash": {[]zipEntry{{top + `\file`, 0o644, "x"}}, 1 << 20, "invalid path"},
		"symlink":   {[]zipEntry{{top + "/link", os.ModeSymlink | 0o777, "/etc/passwd"}}, 1 << 20, "symlink"},
		"too large": {[]zipEntry{{top + "/big", 0o644, strings.Repeat("a", 100)}}, 10, "too large"},
	} {
		dest := filepath.Join(t.TempDir(), "x")
		err := ExtractZip(writeZip(t, c.entries), dest, top, c.max)
		if err == nil || !strings.Contains(err.Error(), c.want) || RuleOf(err) != 6 {
			t.Errorf("%s: err = %v (rule %d), want rule 6 containing %q", name, err, RuleOf(err), c.want)
		}
		if _, statErr := os.Stat(dest); statErr == nil {
			t.Errorf("%s: destination left behind", name)
		}
	}
}
