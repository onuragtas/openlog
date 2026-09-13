//go:build autoupdate

// Package autoupdate is the acceptance test of the Compose auto-updater
// (docs/plan/09-releases-updates.md §7: "Compose kurulumu vA → vB otomatik güncellenir: migration
// çalışır, veri korunur, bozuk imajda geri döner"):
//
//  1. a local registry holds images 0.9.0, 0.9.1 (with test-only migrations) and a broken 0.9.2
//     whose allinone never becomes ready; a local signed release index lists them;
//
//  2. the deploy/compose stack runs 0.9.0 (project openlog-updtest) and receives data;
//
//  3. openlog-updater (auto) updates to 0.9.1: backup, migrations, data still queryable;
//
//  4. 0.9.2 is published: the update fails its health check and is rolled back to 0.9.1.
//
//     make updater-acceptance   (go test -tags autoupdate -v -count=1 -timeout 40m ./test/autoupdate)
//     UPDTEST_KEEP=1            keep the stack and registry afterwards
package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/test/stack"
)

const (
	registry = "localhost:25000/openlog"
	apiKey   = "ola_updtest-api-key"
	license  = "updtest-license-key"
)

type updaterStatus struct {
	State          string   `json:"state"`
	Message        string   `json:"message"`
	Error          string   `json:"error"`
	CurrentVersion string   `json:"current_version"`
	TargetVersion  string   `json:"target_version"`
	BackupFile     string   `json:"backup_file"`
	FailedVersions []string `json:"failed_versions"`
	Steps          []struct {
		Name, Status, Detail string
	} `json:"steps"`
	// The next check (every 10 s) replaces State (e.g. with up_to_date); History keeps the outcome.
	History []struct {
		From, To, Result, Error string
	} `json:"history"`
}

// lastResult returns the result of the newest finished update to version to.
func (s updaterStatus) lastResult(to string) string {
	if len(s.History) > 0 && s.History[0].To == to {
		return s.History[0].Result
	}
	return ""
}

// releaseSite serves a signed index and manifests.
type releaseSite struct {
	mu    sync.Mutex
	files map[string][]byte
	priv  []byte
	base  string
}

func (s *releaseSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b, ok := s.files[r.URL.Path]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(b)
}

func TestComposeAutoUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	root := stack.RepoRoot(t)
	work, err := os.MkdirTemp("", "openlog-updtest-")
	if err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(work, "backups")
	_ = os.MkdirAll(backups, 0o777)

	// Signing key and release server reachable from containers as host.docker.internal.
	pub, seed, err := lib.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := lib.PrivateKeyFromSeed(seed)
	if err := os.WriteFile(filepath.Join(work, "release-keys"), []byte("# updtest key\n"+pub+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	site := &releaseSite{files: map[string][]byte{}, base: fmt.Sprintf("http://host.docker.internal:%d", ln.Addr().(*net.TCPAddr).Port)}
	srv := &http.Server{Handler: site}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	publish := func(path string, body []byte) {
		site.mu.Lock()
		defer site.mu.Unlock()
		site.files[path] = body
		site.files[path+".sig"] = []byte(lib.SignatureLine(body, priv) + "\n")
	}

	envFile := filepath.Join(root, "test/autoupdate/updtest.env")
	c := &stack.Compose{
		Project: "openlog-updtest",
		Files:   []string{filepath.Join(root, "deploy/compose/docker-compose.yml"), filepath.Join(root, "test/autoupdate/docker-compose.override.yml")},
		EnvFile: envFile,
		Env:     []string{"UPDTEST_WORK=" + work, "OPENLOG_UPDATER_BACKUP_DIR=" + backups, "OPENLOG_RELEASE_INDEX_URL=" + site.base + "/index.json"},
		Dir:     root,
	}
	t.Cleanup(func() {
		if out, err := c.Run(context.Background(), "--profile", "updater", "logs", "--no-color", "openlog-updater"); err == nil {
			_ = os.WriteFile(filepath.Join(work, "updater.log"), []byte(out), 0o644)
			t.Logf("updater log (%s):\n%s", filepath.Join(work, "updater.log"), filterUpdaterLog(out))
		}
		if stack.Keep("UPDTEST_KEEP") {
			t.Logf("UPDTEST_KEEP=1: keeping project openlog-updtest, work dir %s", work)
			return
		}
		_, _ = c.Run(context.Background(), "--profile", "updater", "down", "-v", "--remove-orphans")
		_, _ = stack.Run(context.Background(), "", nil, "docker", "image", "rm", "-f", registry+":0.9.0", registry+":0.9.1", registry+":0.9.2")
		_ = os.RemoveAll(work)
	})
	_, _ = c.Run(ctx, "--profile", "updater", "down", "-v", "--remove-orphans")

	// 1. Registry and images.
	c.MustRun(ctx, t, "up", "-d", "registry")
	stack.Eventually(t, time.Minute, time.Second, "registry", func() error {
		resp, err := http.Get("http://127.0.0.1:25000/v2/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	})
	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: registry + ":0.9.0", Version: "0.9.0"})
	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: registry + ":0.9.1", Version: "0.9.1", Tags: "openlog_testmigrations"})
	brokenDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(brokenDir, "openlog-allinone"), []byte("#!/bin/sh\necho 'broken test release: never ready' >&2\nexec sleep 3600\n"), 0o755)
	_ = os.WriteFile(filepath.Join(brokenDir, "Dockerfile"), []byte("FROM "+registry+":0.9.1\nCOPY --chmod=0755 openlog-allinone /usr/local/bin/openlog-allinone\n"), 0o644)
	if _, err := stack.Run(ctx, brokenDir, nil, "docker", "build", "-q", "-t", registry+":0.9.2", "."); err != nil {
		t.Fatal(err)
	}
	digests := map[string]string{}
	for _, v := range []string{"0.9.0", "0.9.1", "0.9.2"} {
		if _, err := stack.Run(ctx, "", nil, "docker", "push", "-q", registry+":"+v); err != nil {
			t.Fatal(err)
		}
		out, err := stack.Run(ctx, "", nil, "docker", "inspect", "--format", "{{range .RepoDigests}}{{println .}}{{end}}", registry+":"+v)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range strings.Fields(out) {
			if strings.HasPrefix(d, registry+"@sha256:") {
				digests[v] = d
			}
		}
		if digests[v] == "" {
			t.Fatalf("no registry digest for %s: %q", v, out)
		}
		t.Logf("pushed %s", digests[v])
	}
	manifest := func(v, minFrom string) {
		m := map[string]any{
			"schema": 1, "product": "openlog", "version": v, "channel": "stable", "released_at": time.Now().UTC().Format(time.RFC3339),
			"notes_url":     "https://example.test/notes/v" + v,
			"compatibility": map[string]string{"min_upgrade_from": minFrom, "rollback_floor": minFrom},
			"artifacts":     []any{}, "images": map[string]string{"openlog": digests[v]},
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		publish("/v"+v+"/manifest.json", b)
	}
	index := func(versions ...string) {
		var entries []map[string]string
		for i := len(versions) - 1; i >= 0; i-- {
			entries = append(entries, map[string]string{"version": versions[i], "manifest_url": site.base + "/v" + versions[i] + "/manifest.json"})
		}
		b, _ := json.Marshal(map[string]any{"schema": 1, "product": "openlog", "generated_at": time.Now().UTC(), "channels": map[string]any{"stable": entries}})
		publish("/index.json", b)
	}
	manifest("0.9.0", "0.8.0")
	manifest("0.9.1", "0.9.0")
	manifest("0.9.2", "0.9.1")
	index("0.9.0")

	// 2. Stack on 0.9.0.
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("OPENLOG_IMAGE="+digests["0.9.0"]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.Env = append(c.Env, "OPENLOG_IMAGE="+digests["0.9.0"], "OPENLOG_UPDATER_IMAGE="+digests["0.9.0"])
	start := time.Now()
	c.MustRun(ctx, t, "up", "-d", "--wait", "--wait-timeout", "400", "openlog")
	t.Logf("stack 0.9.0 up in %s", time.Since(start).Round(time.Second))
	api := stack.API{Base: "http://127.0.0.1:25080", Key: apiKey}
	loadgen := stack.BuildHostTool(ctx, t, "./cmd/openlog-loadgen")
	if out, err := exec.CommandContext(ctx, loadgen, "-endpoint", "http://127.0.0.1:25318", "-license-key", license, "-hosts", "4",
		"-host-prefix", "before-update", "-duration", "15s", "-interval", "5s", "-logs-per-sec", "20", "-spans-per-sec", "20").CombinedOutput(); err != nil {
		t.Fatalf("loadgen: %v\n%s", err, out)
	}
	stack.Eventually(t, 2*time.Minute, 3*time.Second, "hosts on 0.9.0", func() error {
		names, err := api.HostNames(ctx)
		if err == nil && !stack.HasPrefix(names, "before-update-") {
			err = fmt.Errorf("hosts %v", names)
		}
		return err
	})
	hostsBefore, _ := api.HostNames(ctx)
	t.Logf("0.9.0 serves %d hosts", len(hostsBefore))

	// 3. Publish 0.9.1 and start the updater in auto mode.
	index("0.9.0", "0.9.1")
	c.MustRun(ctx, t, "--profile", "updater", "up", "-d", "openlog-updater")
	st := waitUpdater(ctx, t, api, 8*time.Minute, func(s updaterStatus, v string) bool {
		return s.lastResult("0.9.1") == "succeeded" && s.CurrentVersion == "0.9.1" && strings.HasPrefix(v, "0.9.1")
	})
	logSteps(t, "update 0.9.0 -> 0.9.1", st)
	names, err := api.HostNames(ctx)
	if err != nil || len(names) < len(hostsBefore) || !stack.HasPrefix(names, "before-update-") {
		t.Fatalf("data not preserved: %v %v", names, err)
	}
	t.Logf("0.9.1 serves %d hosts (before: %d)", len(names), len(hostsBefore))
	if pg := c.PSQL(ctx, t, "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations WHERE version >= 9000"); !strings.HasPrefix(pg, "9001") {
		t.Errorf("0.9.1 migrations not applied: %q", pg)
	} else {
		t.Logf("test migrations applied by the update: %s", pg)
	}
	if fi, err := os.Stat(st.BackupFile); err != nil && !strings.HasPrefix(st.BackupFile, "/backups/") {
		t.Errorf("backup %q: %v", st.BackupFile, err)
	} else if matches, _ := filepath.Glob(filepath.Join(backups, "openlog-*-0.9.0.dump")); len(matches) != 1 {
		t.Errorf("backups in %s: %v (%v)", backups, matches, fi)
	} else if info, _ := os.Stat(matches[0]); info.Size() == 0 {
		t.Errorf("empty backup %s", matches[0])
	} else {
		t.Logf("backup %s (%d bytes)", filepath.Base(matches[0]), info.Size())
	}
	if b, _ := os.ReadFile(filepath.Join(work, ".env")); !strings.Contains(string(b), "OPENLOG_IMAGE="+digests["0.9.1"]) {
		t.Errorf(".env not updated: %s", b)
	}
	if out, _ := stack.Run(ctx, "", nil, "docker", "inspect", "--format", "{{.Config.Image}}", "openlog-updtest-openlog-1"); strings.TrimSpace(out) != digests["0.9.1"] {
		t.Errorf("openlog container image %q", out)
	}

	// 4. Broken 0.9.2 is rolled back to 0.9.1.
	index("0.9.0", "0.9.1", "0.9.2")
	st = waitUpdater(ctx, t, api, 10*time.Minute, func(s updaterStatus, _ string) bool {
		r := s.lastResult("0.9.2")
		return r == "rolled_back" || r == "rollback_failed"
	})
	logSteps(t, "update 0.9.1 -> 0.9.2 (broken)", st)
	if r := st.lastResult("0.9.2"); r != "rolled_back" {
		t.Fatalf("result %s: %s", r, st.History[0].Error)
	}
	if !contains(st.FailedVersions, "0.9.2") {
		t.Errorf("0.9.2 not remembered as failed: %v", st.FailedVersions)
	}
	t.Logf("rollback error recorded: %s", st.History[0].Error)
	var v stack.Version
	if _, err := api.Get(ctx, "/api/v1/version", &v); err != nil || !strings.HasPrefix(v.Version, "0.9.1") {
		t.Fatalf("after rollback: version %q %v", v.Version, err)
	}
	if names, err := api.HostNames(ctx); err != nil || !stack.HasPrefix(names, "before-update-") {
		t.Fatalf("data after rollback: %v %v", names, err)
	}
	if out, _ := stack.Run(ctx, "", nil, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project=openlog-updtest", "--format", "{{.Names}} {{.Image}} {{.Status}}"); strings.Contains(out, "pre-update") || strings.Contains(out, "updater-migrate") {
		t.Errorf("leftover containers:\n%s", out)
	} else {
		t.Logf("containers after rollback:\n%s", out)
	}
	audit := c.PSQL(ctx, t, "SELECT string_agg(action || ':' || target_id, ', ' ORDER BY id) FROM audit_log WHERE actor_email = 'openlog-updater'")
	t.Logf("audit log: %s", audit)
	if !strings.Contains(audit, "updater.update_succeeded:0.9.1") || !strings.Contains(audit, "updater.update_rolled_back:0.9.2") {
		t.Errorf("audit log %q", audit)
	}
}

func waitUpdater(ctx context.Context, t *testing.T, api stack.API, timeout time.Duration, done func(updaterStatus, string) bool) updaterStatus {
	t.Helper()
	var st updaterStatus
	last := ""
	stack.Eventually(t, timeout, 3*time.Second, "updater", func() error {
		var v stack.Version
		if _, err := api.Get(ctx, "/api/v1/version", &v); err != nil {
			return err // the api is briefly down while the container is recreated
		}
		st = updaterStatus{}
		if len(v.Updater) > 0 && string(v.Updater) != "null" {
			_ = json.Unmarshal(v.Updater, &st)
		}
		if line := fmt.Sprintf("version=%s updater=%s target=%s step=%s", v.Version, st.State, st.TargetVersion, lastStep(st)); line != last {
			t.Log(line)
			last = line
		}
		if done(st, v.Version) {
			return nil
		}
		return fmt.Errorf("version %s, updater %s %s", v.Version, st.State, st.Error)
	})
	return st
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func lastStep(st updaterStatus) string {
	if len(st.Steps) == 0 {
		return ""
	}
	s := st.Steps[len(st.Steps)-1]
	return s.Name + ":" + s.Status
}

func logSteps(t *testing.T, title string, st updaterStatus) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%s: state=%s current=%s target=%s backup=%s\n", title, st.State, st.CurrentVersion, st.TargetVersion, st.BackupFile)
	for _, s := range st.Steps {
		fmt.Fprintf(&b, "  %-16s %-7s %s\n", s.Name, s.Status, s.Detail)
	}
	if st.Error != "" {
		fmt.Fprintf(&b, "  error: %s\n", st.Error)
	}
	t.Log(b.String())
}

func filterUpdaterLog(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"msg"`) && !strings.Contains(l, "admin server listening") {
			if i := strings.Index(l, "{"); i >= 0 {
				l = l[i:]
			}
			keep = append(keep, "  "+l)
		}
	}
	return strings.Join(keep, "\n")
}
