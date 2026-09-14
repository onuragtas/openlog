package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/updatemsg"
)

func tarGz(t *testing.T, entries map[string]string, extra ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(entries[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(entries[n]))
	}
	for _, h := range extra {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestExtractBundleRejectsUnsafeEntries(t *testing.T) {
	ok := map[string]string{"openlog-compose-0.9.1/docker-compose.yml": "services: {}", "openlog-compose-0.9.1/.env.example": "A=1\n"}
	if files, err := extractBundle(tarGz(t, ok), "0.9.1", t.TempDir()); err != nil || !slices.Equal(files, []string{".env.example", "docker-compose.yml"}) {
		t.Fatalf("valid bundle: %v %v", files, err)
	}
	for name, entries := range map[string]map[string]string{
		"traversal":     {"openlog-compose-0.9.1/../evil": "x"},
		"other prefix":  {"openlog-compose-0.9.0/docker-compose.yml": "x"},
		"env":           {"openlog-compose-0.9.1/.env": "SECRET=1"},
		"backups":       {"openlog-compose-0.9.1/backups/x.dump": "x"},
		"missing files": {"openlog-compose-0.9.1/docker-compose.yml": "x"},
	} {
		if _, err := extractBundle(tarGz(t, entries), "0.9.1", t.TempDir()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	link := &tar.Header{Name: "openlog-compose-0.9.1/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}
	if _, err := extractBundle(tarGz(t, ok, link), "0.9.1", t.TempDir()); err == nil {
		t.Error("symlink: expected an error")
	}
}

func TestEnvAdditions(t *testing.T) {
	env := []byte("OPENLOG_API_PORT=8080\nOPENLOG_SAAS_MODE=true\n")
	example := []byte("# c\nOPENLOG_API_PORT=9999\nOPENLOG_NEW=1\n#OPENLOG_COMMENTED=1\nOPENLOG_SECRETS_KEY=dev\nOPENLOG_CLICKHOUSE_PASSWORD=x\nOPENLOG_IMAGE=openlog:dev\nOPENLOG_NEW=2\n")
	if got := envAdditions(env, example); !slices.Equal(got, []string{"OPENLOG_NEW=1"}) {
		t.Errorf("additions %v", got)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallAndRestoreBundle(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"docker-compose.yml": "old", ".env.example": "old", "clickhouse/a.xml": "old-a", ".bundle-version": "0.9.0\n"})
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("OPENLOG_A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &ComposeEngine{Cfg: Config{ComposeDir: dir, EnvFile: envFile}}
	stage := filepath.Join(dir, bundleStagingDir)
	writeFiles(t, stage, map[string]string{"docker-compose.yml": "new", ".env.example": "new", "clickhouse/a.xml": "new-a", "clickhouse/b.xml": "new-b"})
	b := &composeBundle{Version: "0.9.1", Dir: stage, Files: []string{".env.example", "clickhouse/a.xml", "clickhouse/b.xml", "docker-compose.yml"}, EnvAdditions: []string{"OPENLOG_NEW=1"}}
	if err := e.installBundle(b, "0.9.0"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "docker-compose.yml")) != "new" || readFile(t, filepath.Join(dir, "clickhouse/b.xml")) != "new-b" ||
		readFile(t, filepath.Join(dir, bundleVersionFile)) != "0.9.1\n" {
		t.Error("files not installed")
	}
	if readFile(t, filepath.Join(dir, bundlePreviousDir, "docker-compose.yml")) != "old" || readFile(t, filepath.Join(dir, bundlePreviousDir, bundleVersionFile)) != "0.9.0\n" {
		t.Error("previous bundle not kept")
	}
	if got := readFile(t, envFile); got != "OPENLOG_A=1\n# added by openlog-updater from .env.example 0.9.1\nOPENLOG_NEW=1\n" {
		t.Errorf(".env %q", got)
	}
	if fi, _ := os.Stat(envFile); fi.Mode().Perm() != 0o600 {
		t.Errorf(".env mode %v, want 0600", fi.Mode().Perm())
	}
	for _, p := range []string{bundleJournalFile, bundleStagingDir} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}

	// An interrupted replacement (journal present) is undone from .bundle-previous.
	journal := `{"version":"0.9.2","previous":"0.9.1","files":["docker-compose.yml","clickhouse/c.xml"],"existed":["docker-compose.yml"]}`
	writeFiles(t, dir, map[string]string{bundleJournalFile: journal, "docker-compose.yml": "half", "clickhouse/c.xml": "new-c"})
	if err := e.restoreBundle(); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "docker-compose.yml")) != "old" || readFile(t, filepath.Join(dir, bundleVersionFile)) != "0.9.0\n" {
		t.Error("previous files not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "clickhouse/c.xml")); !os.IsNotExist(err) {
		t.Error("a file new in the interrupted bundle was not removed")
	}
	if err := e.restoreBundle(); err != nil {
		t.Errorf("no journal: %v", err)
	}
}

func TestRewriteEnvFileKeepsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(p, []byte("OPENLOG_IMAGE=old\nB=2"), 0o600)
	if err := rewriteEnvFile(p, "OPENLOG_IMAGE", "new"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "OPENLOG_IMAGE=new\nB=2\n" {
		t.Errorf("got %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestPendingChanges(t *testing.T) {
	cur, next := t.TempDir(), t.TempDir()
	oldCompose := "services:\n  openlog:\n    environment: {A: 1}\n    volumes: [\"./clickhouse/x.xml:/x\"]\n  clickhouse:\n    volumes: [\"./clickhouse/a.xml:/a:ro\"]\n  loadgen:\n    profiles: [loadgen]\n"
	newCompose := "services:\n  openlog:\n    environment: {A: 2, B: 3}\n    env_file: [.env]\n    volumes: [\"./clickhouse/x.xml:/x\", \"data-exports:/exports\"]\n  clickhouse:\n    volumes: [\"./clickhouse/a.xml:/a:ro\"]\n  loadgen:\n    profiles: [loadgen]\n    image: other\n  newsvc:\n    image: x\n"
	writeFiles(t, cur, map[string]string{"clickhouse/a.xml": "old"})
	writeFiles(t, next, map[string]string{"clickhouse/a.xml": "new"})
	got, err := pendingChanges([]byte(oldCompose), []byte(newCompose), cur, next, []string{"clickhouse/a.xml", "docker-compose.yml"},
		map[string]bool{"openlog": true, "clickhouse": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []PendingService{{Service: "newsvc", Reason: "definition"}, {Service: "openlog", Reason: "definition"}, {Service: "clickhouse", Reason: "files"}}
	if !slices.Equal(got, want) {
		t.Errorf("pending %+v, want %+v", got, want)
	}
	// Only environment changes: nothing pending.
	envOnly := strings.Replace(oldCompose, "{A: 1}", "{A: 9}", 1)
	if got, _ := pendingChanges([]byte(oldCompose), []byte(envOnly), cur, cur, nil, map[string]bool{"openlog": true}); len(got) != 0 {
		t.Errorf("environment-only change reported: %+v", got)
	}
}

// bundleFixture extends the compose fixture with an install-server.sh layout and a release server.
func bundleFixture(t *testing.T, bundleFiles map[string]string, corrupt bool) (*composeFixture, string) {
	t.Helper()
	f := newComposeFixture(t)
	dir := filepath.Dir(f.envFile)
	oldCompose := `x-openlog-compose-version: "0.9.0"
services:
  openlog:
    image: ${OPENLOG_IMAGE:-openlog:dev}
    environment:
      OPENLOG_POSTGRES_DSN: postgres://openlog@postgres/openlog
`
	writeFiles(t, dir, map[string]string{"docker-compose.yml": oldCompose, ".env.example": "OPENLOG_LOG_LEVEL=info\n", bundleVersionFile: "0.9.0\n"})
	if err := os.WriteFile(f.envFile, []byte("OPENLOG_IMAGE=openlog:dev\nOPENLOG_SAAS_MODE=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.engine.Cfg.ComposeDir = dir
	f.runner.Cfg.ComposeDir = dir
	// The fixture container was created from docker-compose.yml in dir.
	c := f.docker.containers[f.oldID]
	labels := c.c.Config["Labels"].(map[string]any)
	labels["com.docker.compose.project.config_files"] = "/host/openlog-server/docker-compose.yml"
	labels["com.docker.compose.project.working_dir"] = "/host/openlog-server"

	entries := map[string]string{}
	for n, content := range bundleFiles {
		entries["openlog-compose-0.9.1/"+n] = content
	}
	data := tarGz(t, entries)
	sum := sha256.Sum256(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if corrupt {
			_, _ = w.Write(append([]byte{}, data[:len(data)-1]...))
			_, _ = w.Write([]byte{data[len(data)-1] ^ 0xff})
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	f.engine.HTTP = srv.Client()
	art := fmt.Sprintf(`{"component":"compose","os":"any","arch":"any","format":"tar.gz","name":"openlog-compose-0.9.1.tar.gz","url":%q,"sha256":%q,"size":%d}`,
		srv.URL+"/openlog-compose-0.9.1.tar.gz", hex.EncodeToString(sum[:]), len(data))
	f.source.manifests["0.9.1"] = strings.Replace(manifestJSON("0.9.1", "stable", "0.9.0", img091), `"artifacts":[]`, `"artifacts":[`+art+`]`, 1)
	return f, dir
}

func TestComposeUpdateInstallsBundle(t *testing.T) {
	newCompose := `x-openlog-compose-version: "0.9.1"
x-settings: &settings
  OPENLOG_SAAS_MODE: ${OPENLOG_SAAS_MODE:-}
services:
  openlog:
    image: ${OPENLOG_IMAGE:-openlog:dev}
    environment:
      <<: *settings
      OPENLOG_POSTGRES_DSN: postgres://openlog@postgres/openlog
    volumes: ["data-exports:/var/lib/openlog/exports"]
volumes:
  data-exports:
`
	f, dir := bundleFixture(t, map[string]string{"docker-compose.yml": newCompose, ".env.example": "OPENLOG_LOG_LEVEL=info\nOPENLOG_NEW_DEFAULT=x\n"}, false)
	ctx := context.Background()
	if err := f.runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	st := f.store.st
	var steps []string
	for _, s := range st.Steps {
		steps = append(steps, s.Name+"="+s.Status)
	}
	if want := "backup=ok pull=ok compose-bundle=ok migrate=ok recreate=ok health=ok cleanup=ok contract-migrate=ok"; strings.Join(steps, " ") != want {
		t.Fatalf("steps %v (%+v)", steps, st.Steps)
	}
	if readFile(t, filepath.Join(dir, "docker-compose.yml")) != newCompose || readFile(t, filepath.Join(dir, bundleVersionFile)) != "0.9.1\n" {
		t.Error("compose bundle not installed")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, bundlePreviousDir, "docker-compose.yml")), `"0.9.0"`) {
		t.Error("previous compose file not kept")
	}
	env := readFile(t, f.envFile)
	if !strings.Contains(env, "OPENLOG_NEW_DEFAULT=x") || !strings.Contains(env, "OPENLOG_IMAGE="+img091) {
		t.Errorf(".env %q", env)
	}
	// The recreated container got OPENLOG_SAAS_MODE from .env through the new compose file.
	var app ContainerSpec
	for _, c := range f.docker.created {
		if !strings.Contains(c.Name, "migrate") {
			app = c
		}
	}
	if got := envMap(toStrings(app.Config["Env"])); got["OPENLOG_SAAS_MODE"] != "true" || got["OPENLOG_NEW_DEFAULT"] != "" {
		t.Errorf("recreated env %v", got)
	}
	if !strings.Contains(st.Steps[4].Detail, "OPENLOG_SAAS_MODE") {
		t.Errorf("recreate detail %q", st.Steps[4].Detail)
	}
	// The new volume is not applied by the updater: pending until compose recreates the container.
	if st.ComposeChanges == nil || len(st.Notices) != 1 || st.Notices[0].Code != updatemsg.ComposeChangesPending || st.Notices[0].Params["services"] != "openlog" {
		t.Fatalf("pending %+v notices %+v", st.ComposeChanges, st.Notices)
	}
	// docker compose up -d recreated it (new config hash): the notice disappears.
	cur := f.docker.byName("proj-openlog-1")
	cur.c.Config["Labels"].(map[string]any)["com.docker.compose.config-hash"] = "def"
	f.source.channels["stable"] = []string{"0.9.0", "0.9.1"}
	if err := f.runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if f.store.st.ComposeChanges != nil || len(f.store.st.Notices) != 0 {
		t.Errorf("after compose up: %+v %+v", f.store.st.ComposeChanges, f.store.st.Notices)
	}
}

func TestComposeBundleFailures(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{"docker-compose.yml": "services:\n  openlog:\n    image: x\n", ".env.example": ""}
	t.Run("checksum", func(t *testing.T) {
		f, dir := bundleFixture(t, files, true)
		if err := f.runner.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "compose-bundle") {
			t.Fatalf("err %v", err)
		}
		if f.store.st.State != StateFailed || len(f.docker.pulled) != 1 || f.docker.containers[f.oldID] == nil {
			t.Errorf("state %s", f.store.st.State)
		}
		if _, err := os.Stat(filepath.Join(dir, bundleStagingDir)); !os.IsNotExist(err) {
			t.Error("staging directory left behind")
		}
	})
	t.Run("rollback keeps the compose files", func(t *testing.T) {
		f, dir := bundleFixture(t, files, false)
		f.source.manifests["0.9.1"] = strings.Replace(f.source.manifests["0.9.1"], img091, img092, 1) // never ready
		before := readFile(t, filepath.Join(dir, "docker-compose.yml"))
		if err := f.runner.RunOnce(ctx); err == nil {
			t.Fatal("expected failure")
		}
		if f.store.st.State != StateRolledBack || readFile(t, filepath.Join(dir, "docker-compose.yml")) != before || readFile(t, filepath.Join(dir, bundleVersionFile)) != "0.9.0\n" {
			t.Errorf("state %s, compose files changed", f.store.st.State)
		}
		if _, err := os.Stat(filepath.Join(dir, bundleStagingDir)); !os.IsNotExist(err) {
			t.Error("staging directory left behind")
		}
	})
	t.Run("no bundle in the manifest", func(t *testing.T) {
		f, dir := bundleFixture(t, files, false)
		f.source.manifests["0.9.1"] = manifestJSON("0.9.1", "stable", "0.9.0", img091)
		if err := f.runner.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		st := f.store.st
		if st.State != StateSucceeded || !strings.Contains(st.Steps[2].Detail, "no compose bundle") || readFile(t, filepath.Join(dir, bundleVersionFile)) != "0.9.0\n" {
			t.Errorf("status %+v", st.Steps)
		}
		if len(st.Notices) != 1 || st.Notices[0].Code != updatemsg.ComposeOutdatedBundle || st.Notices[0].Params["files_version"] != "0.9.0" {
			t.Errorf("notices %+v", st.Notices)
		}
	})
}

func TestOutdatedNoticeGitInstall(t *testing.T) {
	f := newComposeFixture(t)
	dir := filepath.Dir(f.envFile)
	e := f.engine
	e.Cfg.ComposeDir = dir
	st := &Status{}
	if n := e.Notices(context.Background(), "0.9.1", st); len(n) != 0 {
		t.Errorf("no docker-compose.yml: %+v", n)
	}
	writeFiles(t, dir, map[string]string{"docker-compose.yml": "x-openlog-compose-version: \"0.9.0\"\nservices: {}\n"})
	n := e.Notices(context.Background(), "0.9.1", st)
	if len(n) != 1 || n[0].Code != updatemsg.ComposeOutdated || n[0].Params["files_version"] != "0.9.0" || !strings.Contains(n[0].Message, "git checkout v0.9.1") {
		t.Errorf("notice %+v", n)
	}
	if n := e.Notices(context.Background(), "0.9.0", st); len(n) != 0 {
		t.Errorf("same version: %+v", n)
	}
	if n := e.Notices(context.Background(), "0.0.0-dev+abc", st); len(n) != 0 {
		t.Errorf("dev build: %+v", n)
	}
	// Without a marker the files predate this updater.
	writeFiles(t, dir, map[string]string{"docker-compose.yml": "services: {}\n"})
	e.SelfVersion = "0.9.1"
	if n := e.Notices(context.Background(), "0.9.1", st); len(n) != 1 || n[0].Params["files_version"] != "< 0.9.1" {
		t.Errorf("unmarked: %+v", n)
	}
	if n := e.Notices(context.Background(), "0.9.0", st); len(n) != 0 {
		t.Errorf("unmarked, running older than the updater: %+v", n)
	}
}

func TestPendingFilesClearedByRestart(t *testing.T) {
	f := newComposeFixture(t)
	installed := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	st := &Status{ComposeChanges: &ComposeChanges{Version: "0.9.1", InstalledAt: installed, Services: []PendingService{{Service: "openlog", Reason: "files"}}}}
	c := f.docker.containers[f.oldID]
	c.c.State.StartedAt = installed.Add(-time.Hour).Format(time.RFC3339Nano)
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 1 || st.ComposeChanges == nil {
		t.Fatalf("before restart: %+v", n)
	}
	c.c.State.StartedAt = installed.Add(time.Minute).Format(time.RFC3339Nano)
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("after restart: %+v %+v", n, st.ComposeChanges)
	}
}
