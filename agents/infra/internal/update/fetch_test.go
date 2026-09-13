package update

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExtractTarGz(t *testing.T) {
	const top = "openlog-infra-agent_1.0.0_linux_amd64"
	big := strings.Repeat("x", 600)
	cases := []struct {
		name    string
		entries []tarEntry
		max     int64
		want    string // "" = ok
	}{
		{name: "valid", entries: []tarEntry{
			{name: top + "/", typ: tar.TypeDir},
			{name: top + "/openlog-infra-agent", typ: tar.TypeReg, body: "bin", mode: 0o755},
			{name: top + "/packaging/systemd/unit", typ: tar.TypeReg, body: "u"},
		}},
		{name: "valid without explicit dir entries", entries: []tarEntry{
			{name: "./" + top + "/openlog-infra-agent", typ: tar.TypeReg, body: "bin"},
		}},
		{name: "absolute path", entries: []tarEntry{{name: "/etc/cron.d/evil", typ: tar.TypeReg, body: "x"}}, want: "absolute path"},
		{name: "dot dot", entries: []tarEntry{{name: top + "/../../etc/passwd", typ: tar.TypeReg, body: "x"}}, want: "contains .."},
		{name: "dot dot that cleans inside", entries: []tarEntry{{name: top + "/a/../openlog-infra-agent", typ: tar.TypeReg, body: "x"}}, want: "contains .."},
		{name: "symlink", entries: []tarEntry{
			{name: top + "/", typ: tar.TypeDir},
			{name: top + "/openlog-infra-agent", typ: tar.TypeSymlink, linkname: "/bin/sh"},
		}, want: "symlink"},
		{name: "hard link", entries: []tarEntry{
			{name: top + "/x", typ: tar.TypeLink, linkname: "/etc/shadow"},
		}, want: "hard link"},
		{name: "char device", entries: []tarEntry{{name: top + "/null", typ: tar.TypeChar}}, want: "device file"},
		{name: "block device", entries: []tarEntry{{name: top + "/sda", typ: tar.TypeBlock}}, want: "device file"},
		{name: "fifo", entries: []tarEntry{{name: top + "/fifo", typ: tar.TypeFifo}}, want: "device file"},
		{name: "file outside top dir", entries: []tarEntry{
			{name: top + "/openlog-infra-agent", typ: tar.TypeReg, body: "bin"},
			{name: "other/file", typ: tar.TypeReg, body: "x"},
		}, want: "outside the expected directory"},
		{name: "top dir name prefix trick", entries: []tarEntry{
			{name: top + "-evil/openlog-infra-agent", typ: tar.TypeReg, body: "x"},
		}, want: "outside the expected directory"},
		{name: "file at top level", entries: []tarEntry{{name: "openlog-infra-agent", typ: tar.TypeReg, body: "x"}}, want: "outside the expected directory"},
		{name: "top entry is a file", entries: []tarEntry{{name: top, typ: tar.TypeReg, body: "x"}}, want: "not a directory"},
		{name: "duplicate file", entries: []tarEntry{
			{name: top + "/openlog-infra-agent", typ: tar.TypeReg, body: "a"},
			{name: top + "/openlog-infra-agent", typ: tar.TypeReg, body: "b"},
		}, want: "file exists"},
		{name: "total size cap", max: 1000, entries: []tarEntry{
			{name: top + "/a", typ: tar.TypeReg, body: big},
			{name: top + "/b", typ: tar.TypeReg, body: big},
		}, want: "exceeds"},
		{name: "backslash", entries: []tarEntry{{name: top + "\\..\\x", typ: tar.TypeReg, body: "x"}}, want: "invalid path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "a.tar.gz")
			if err := os.WriteFile(archive, buildTarGz(t, c.entries), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "out")
			max := c.max
			if max == 0 {
				max = MaxExtractBytes
			}
			err := ExtractTarGz(archive, dest, top, max)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				fi, err := os.Stat(filepath.Join(dest, "openlog-infra-agent"))
				if err != nil {
					t.Fatal(err)
				}
				if c.name == "valid" && fi.Mode().Perm() != 0o755 {
					t.Errorf("mode %v", fi.Mode())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if RuleOf(err) != 6 {
				t.Errorf("rule %d", RuleOf(err))
			}
			if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
				t.Error("destination left behind after a rejected archive")
			}
			if _, statErr := os.Stat(filepath.Join(dir, "etc")); !os.IsNotExist(statErr) {
				t.Error("file written outside the destination")
			}
		})
	}
}

func TestExtractRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	os.WriteFile(archive, []byte("not gzip"), 0o600)
	if err := ExtractTarGz(archive, filepath.Join(dir, "out"), "x", 1<<20); err == nil || RuleOf(err) != 6 {
		t.Fatalf("err %v", err)
	}
}

func TestDownload(t *testing.T) {
	body := []byte("release archive bytes")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("openlog-license-key")
		switch r.URL.Path {
		case "/ok":
			w.Write(body)
		case "/chunked": // no Content-Length, longer than announced by the manifest
			w.Header().Set("Transfer-Encoding", "chunked")
			w.(http.Flusher).Flush()
			w.Write(append(body, []byte("-and-more")...))
		case "/404":
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, ".1.0.0.partial")
	ctx := context.Background()

	h := http.Header{}
	h.Set("openlog-license-key", "k")
	if err := Download(ctx, srv.Client(), srv.URL+"/ok", h, dest, int64(len(body)), sha); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(body) || gotKey != "k" {
		t.Fatalf("content %q key %q", b, gotKey)
	}

	for _, c := range []struct {
		name, path string
		size       int64
		sha        string
		want       string
	}{
		{"sha mismatch", "/ok", int64(len(body)), strings.Repeat("0", 64), "sha256 mismatch"},
		{"size mismatch (content-length)", "/ok", int64(len(body)) + 1, sha, "size mismatch"},
		{"size cap (body longer)", "/chunked", int64(len(body)), sha, "size mismatch"},
		{"too large", "/ok", MaxArchiveBytes + 1, sha, "outside"},
		{"http error", "/404", int64(len(body)), sha, "HTTP 404"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := Download(ctx, srv.Client(), srv.URL+c.path, nil, dest, c.size, c.sha)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Error("partial file kept after failure")
			}
		})
	}
}

func TestSwitchCurrentAndPrune(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"0.8.0", "0.9.0", "0.9.1"} {
		installVersion(t, root, v, "exit 0", nil)
	}
	os.WriteFile(filepath.Join(root, "versions", ".1.0.0.partial"), []byte("x"), 0o600)
	if err := SwitchCurrent(root, "0.9.0"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchCurrent(root, "0.9.1"); err != nil {
		t.Fatal(err)
	}
	if d, err := CurrentDir(root); err != nil || d != "0.9.1" {
		t.Fatalf("current %q %v", d, err)
	}
	if link, _ := os.Readlink(filepath.Join(root, "current")); link != filepath.Join("versions", "0.9.1") {
		t.Errorf("link target %q is not relative", link)
	}
	if err := SwitchCurrent(root, "2.0.0"); err == nil {
		t.Error("switched to a missing version")
	}
	if d, _ := CurrentDir(root); d != "0.9.1" {
		t.Error("failed switch changed current")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".current") {
			t.Errorf("temp symlink left: %s", e.Name())
		}
	}
	removed, err := Prune(root, "0.9.1", "0.9.0", "")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	if !slices.Equal(removed, []string{".1.0.0.partial", "0.8.0"}) {
		t.Errorf("removed %v", removed)
	}
}

func TestRunSelfTest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, script string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755)
		return p
	}
	ok := write("ok", `[ "$1" = "-self-test" ] && [ "$2" = "-config" ] && [ "$3" = "/etc/x.yaml" ] || exit 9`)
	if err := RunSelfTest(context.Background(), ok, "/etc/x.yaml", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	bad := write("bad", "echo broken config >&2; exit 1")
	if err := RunSelfTest(context.Background(), bad, "", 5*time.Second); err == nil || RuleOf(err) != 7 || !strings.Contains(err.Error(), "broken config") {
		t.Fatalf("err %v", err)
	}
	slow := write("slow", "exec sleep 10")
	start := time.Now()
	if err := RunSelfTest(context.Background(), slow, "", 200*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout not enforced")
	}
}
