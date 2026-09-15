//go:build autoupdate

// Package autoupdate is the acceptance test of the Compose auto-updater
// (docs/plan/09-releases-updates.md §7: "Compose kurulumu vA → vB otomatik güncellenir: migration
// çalışır, veri korunur, bozuk imajda geri döner"):
//
//  1. a local registry holds images FROM (0.9.0 built from the working tree, or a published release with
//     UPDTEST_FROM_IMAGE), 0.9.1 (with the test-only expand + contract migrations 9001/9002) and a broken 0.9.2
//     (one more test-only expand migration, 9003, and an allinone that never becomes ready); a local signed release
//     index lists them;
//
//  2. the deploy/compose stack runs FROM (project openlog-updtest) and receives data;
//
//  3. openlog-updater (auto) updates to 0.9.1: backup, expand migrations, recreate, health, contract migrations;
//     data and queries still work;
//
//  4. 0.9.2 is published: its expand migration applies, the health check fails and the containers are rolled back
//     to 0.9.1, which keeps serving and ingesting with the expanded schema; the status says why.
//
//     make updater-acceptance   (go test -tags autoupdate -v -count=1 -timeout 40m ./test/autoupdate)
//     UPDTEST_KEEP=1            keep the stack and registry afterwards
//     UPDTEST_FROM_IMAGE=ghcr.io/onuragtas/openlog:0.1.9
//     ...                       start from a published release: the update to 0.9.1 then also applies every real
//     ...                       migration added since that release (its own openlog-updater runs the update,
//     ...                       UPDTEST_UPDATER=to uses the 0.9.1 updater instead)
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
	"strconv"
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
	nextBase = "openlog-updtest-next-base:0.9.2"
)

type updaterStatus struct {
	State          string   `json:"state"`
	Message        string   `json:"message"`
	MessageCode    string   `json:"message_code"` // for the UI's translation; absent in updaters before it existed
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
	UpdaterVersion string `json:"updater_version"`
	SelfUpdate     *struct {
		TargetVersion string `json:"target_version"`
		State         string `json:"state"`
		Error         string `json:"error"`
	} `json:"self_update"`
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

	fromImage := os.Getenv("UPDTEST_FROM_IMAGE")
	from := "0.9.0"
	if fromImage != "" {
		if _, err := stack.Run(ctx, "", nil, "docker", "pull", "-q", fromImage); err != nil {
			t.Fatal(err)
		}
		from = imageVersion(ctx, t, fromImage)
		t.Logf("starting from published image %s (version %s)", fromImage, from)
	}
	versions := []string{from, "0.9.1", "0.9.2"}

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
		rm := []string{"image", "rm", "-f", nextBase}
		for _, v := range versions {
			rm = append(rm, registry+":"+v)
		}
		_, _ = stack.Run(context.Background(), "", nil, "docker", rm...)
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
	if fromImage == "" {
		stack.BuildImage(ctx, t, stack.ImageSpec{Ref: registry + ":" + from, Version: from})
	} else if _, err := stack.Run(ctx, "", nil, "docker", "tag", fromImage, registry+":"+from); err != nil {
		t.Fatal(err)
	}
	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: registry + ":0.9.1", Version: "0.9.1", Tags: "openlog_testmigrations"})
	// The broken release: a real 0.9.2 build with one more expand migration (applied by the updater's migrate step
	// before the health check), whose allinone never becomes ready.
	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: nextBase, Version: "0.9.2", Tags: "openlog_testmigrations openlog_testmigrations_next"})
	brokenDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(brokenDir, "openlog-allinone"), []byte("#!/bin/sh\necho 'broken test release: never ready' >&2\nexec sleep 3600\n"), 0o755)
	_ = os.WriteFile(filepath.Join(brokenDir, "Dockerfile"), []byte("FROM "+nextBase+"\nCOPY --chmod=0755 openlog-allinone /usr/local/bin/openlog-allinone\n"), 0o644)
	if _, err := stack.Run(ctx, brokenDir, nil, "docker", "build", "-q", "-t", registry+":0.9.2", "."); err != nil {
		t.Fatal(err)
	}
	digests := map[string]string{}
	for _, v := range versions {
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
	manifest(from, from)
	manifest("0.9.1", from)
	manifest("0.9.2", "0.9.1")
	index(from)

	// 2. Stack on FROM.
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("OPENLOG_IMAGE="+digests[from]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	updaterImage := digests[from]
	if os.Getenv("UPDTEST_UPDATER") == "to" {
		updaterImage = digests["0.9.1"]
	}
	c.Env = append(c.Env, "OPENLOG_IMAGE="+digests[from], "OPENLOG_UPDATER_IMAGE="+updaterImage)
	start := time.Now()
	c.MustRun(ctx, t, "up", "-d", "--wait", "--wait-timeout", "400", "openlog")
	t.Logf("stack %s up in %s", from, time.Since(start).Round(time.Second))
	api := stack.API{Base: "http://127.0.0.1:25080", Key: apiKey}
	loadgen := stack.BuildHostTool(ctx, t, "./cmd/openlog-loadgen")
	sendLoad(ctx, t, loadgen, "before-update")
	stack.Eventually(t, 2*time.Minute, 3*time.Second, "hosts on "+from, func() error {
		names, err := api.HostNames(ctx)
		if err == nil && !stack.HasPrefix(names, "before-update-") {
			err = fmt.Errorf("hosts %v", names)
		}
		return err
	})
	hostsBefore, _ := api.HostNames(ctx)
	var rowsBefore map[string]uint64
	stack.Eventually(t, 2*time.Minute, 3*time.Second, "telemetry rows on "+from, func() error {
		rowsBefore = telemetryRows(ctx, t, c)
		for table, n := range rowsBefore {
			if n == 0 {
				return fmt.Errorf("no %s rows yet", table)
			}
		}
		return nil
	})
	pgBefore := c.PSQL(ctx, t, "SELECT count(*) || ' max ' || max(version) FROM schema_migrations")
	chBefore := c.ClickHouse(ctx, t, "SELECT toString(count()) || ' max ' || toString(max(version)) FROM openlog.schema_migrations")
	t.Logf("%s serves %d hosts; telemetry rows %v; migrations postgres %s, clickhouse %s", from, len(hostsBefore), rowsBefore, pgBefore, chBefore)

	// 3. Publish 0.9.1 and start the updater in auto mode.
	index(from, "0.9.1")
	c.MustRun(ctx, t, "--profile", "updater", "up", "-d", "openlog-updater")
	st := waitUpdater(ctx, t, api, 8*time.Minute, func(s updaterStatus, v string) bool {
		return s.lastResult("0.9.1") == "succeeded" && s.CurrentVersion == "0.9.1" && strings.HasPrefix(v, "0.9.1")
	})
	logSteps(t, "update "+from+" -> 0.9.1", st)
	for _, want := range []string{"backup", "pull", "migrate", "recreate", "health", "cleanup", "contract-migrate"} {
		if s := stepStatus(st, want); s != "ok" {
			t.Errorf("step %s: %q", want, s)
		}
	}
	names, err := api.HostNames(ctx)
	if err != nil || len(names) < len(hostsBefore) || !stack.HasPrefix(names, "before-update-") {
		t.Fatalf("data not preserved: %v %v", names, err)
	}
	t.Logf("0.9.1 serves %d hosts (before: %d)", len(names), len(hostsBefore))
	assertRowsKept(ctx, t, c, "after the update", rowsBefore)
	assertQueries(ctx, t, api, "0.9.1")
	// 9001 (expand) runs in the migrate step, 9002 (contract, requires 0.9.1) only after the old container stopped.
	assertTestMigrations(ctx, t, c, "9001,9002")
	t.Logf("migrations after the update: postgres %s (before %s), clickhouse %s (before %s)",
		c.PSQL(ctx, t, "SELECT count(*) || ' max ' || max(version) FROM schema_migrations WHERE version < 9000"), pgBefore,
		c.ClickHouse(ctx, t, "SELECT toString(count()) || ' max ' || toString(max(version)) FROM openlog.schema_migrations WHERE version < 9000"), chBefore)
	if fromImage != "" {
		// The update from a published release applies the real migrations added since then.
		newPG := c.PSQL(ctx, t, "SELECT coalesce(string_agg(version::text, ',' ORDER BY version), '') FROM schema_migrations WHERE version < 9000 AND version > "+
			strings.Fields(pgBefore)[2])
		newCH := c.ClickHouse(ctx, t, "SELECT arrayStringConcat(arraySort(groupUniqArray(toString(version))), ',') FROM openlog.schema_migrations WHERE version < 9000 AND version > "+
			strings.Fields(chBefore)[2])
		t.Logf("real migrations applied by the update from %s: postgres [%s], clickhouse [%s]", from, newPG, newCH)
		if newPG == "" && newCH == "" {
			t.Errorf("no real migration applied by the update from %s", from)
		}
	}
	if fi, err := os.Stat(st.BackupFile); err != nil && !strings.HasPrefix(st.BackupFile, "/backups/") {
		t.Errorf("backup %q: %v", st.BackupFile, err)
	} else if matches, _ := filepath.Glob(filepath.Join(backups, "openlog-*-"+from+".dump")); len(matches) != 1 {
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
	// 3b. The updater (FROM built from this tree, older than 0.9.1) replaces its own container with 0.9.1 (D-120) and
	// the new updater performs step 4. A published FROM release predates the self-update; UPDTEST_UPDATER=to already
	// runs 0.9.1.
	if fromImage == "" && os.Getenv("UPDTEST_UPDATER") != "to" {
		st = waitUpdater(ctx, t, api, 4*time.Minute, func(s updaterStatus, _ string) bool {
			return s.SelfUpdate != nil && s.SelfUpdate.TargetVersion == "0.9.1" && s.SelfUpdate.State != "running"
		})
		logSteps(t, "self-update of openlog-updater", st)
		if st.SelfUpdate.State != "succeeded" || stepStatus(st, "self-update") != "ok" || st.UpdaterVersion != "0.9.1" {
			t.Fatalf("self-update %+v, step %q, updater_version %q", st.SelfUpdate, stepStatus(st, "self-update"), st.UpdaterVersion)
		}
		out, _ := stack.Run(ctx, "", nil, "docker", "inspect", "--format",
			`{{.Config.Image}} {{.HostConfig.RestartPolicy.Name}} {{index .Config.Labels "com.docker.compose.service"}}`, "openlog-updtest-openlog-updater-1")
		if got, want := strings.TrimSpace(out), digests["0.9.1"]+" unless-stopped openlog-updater"; got != want {
			t.Errorf("updater container after the self-update: %q, want %q", got, want)
		}
		if out, _ := stack.Run(ctx, "", nil, "docker", "ps", "-a", "--filter", "name=self-update", "--format", "{{.Names}}"); strings.TrimSpace(out) != "" {
			t.Errorf("leftover handover containers: %s", out)
		}
	}
	rowsUpdated := telemetryRows(ctx, t, c)

	// 4. Broken 0.9.2 (expand migration 9003 applied, never healthy) is rolled back to 0.9.1.
	index(from, "0.9.1", "0.9.2")
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
	if s := stepStatus(st, "migrate"); s != "ok" {
		t.Errorf("migrate step of 0.9.2: %q", s)
	}
	if st.History[0].Error == "" {
		t.Error("rollback without a recorded error")
	}
	t.Logf("rollback error recorded: %s", st.History[0].Error)
	var v stack.Version
	if _, err := api.Get(ctx, "/api/v1/version", &v); err != nil || !strings.HasPrefix(v.Version, "0.9.1") {
		t.Fatalf("after rollback: version %q %v", v.Version, err)
	}
	if names, err := api.HostNames(ctx); err != nil || !stack.HasPrefix(names, "before-update-") {
		t.Fatalf("data after rollback: %v %v", names, err)
	}
	assertRowsKept(ctx, t, c, "after the rollback", rowsUpdated)
	// The expand migration of the failed release stays; 0.9.1 keeps working with it.
	assertTestMigrations(ctx, t, c, "9001,9002,9003")
	c.PSQL(ctx, t, "INSERT INTO mixedversion_probe (note) VALUES ('written like release 0.9.1 after the rollback')")
	assertQueries(ctx, t, api, "0.9.1")
	sendLoad(ctx, t, loadgen, "after-rollback")
	stack.Eventually(t, 2*time.Minute, 3*time.Second, "0.9.1 ingests after the rollback", func() error {
		names, err := api.HostNames(ctx)
		if err == nil && !stack.HasPrefix(names, "after-rollback-") {
			err = fmt.Errorf("no after-rollback-* host in %d hosts", len(names))
		}
		return err
	})
	if out, _ := stack.Run(ctx, "", nil, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project=openlog-updtest", "--format", "{{.Names}} {{.Image}} {{.Status}}"); strings.Contains(out, "pre-update") || strings.Contains(out, "updater-migrate") || strings.Contains(out, "self-update") {
		t.Errorf("leftover containers:\n%s", out)
	} else {
		t.Logf("containers after rollback:\n%s", out)
	}
	// What Settings -> Organization -> Version shows (GET /api/v1/version .updater): the rolled back state with its
	// error, then (next check) up to date with the reason why 0.9.2 is not offered again.
	st = waitUpdater(ctx, t, api, 2*time.Minute, func(s updaterStatus, _ string) bool {
		return s.State == "up_to_date" && strings.Contains(s.Message, "0.9.2: failed before on this installation")
	})
	t.Logf("updater status after the rollback: state=%s message=%q history[0]=%+v", st.State, st.Message, st.History[0])
	audit := c.PSQL(ctx, t, "SELECT string_agg(action || ':' || target_id, ', ' ORDER BY id) FROM audit_log WHERE actor_email = 'openlog-updater'")
	t.Logf("audit log: %s", audit)
	if !strings.Contains(audit, "updater.update_succeeded:0.9.1") || !strings.Contains(audit, "updater.update_rolled_back:0.9.2") {
		t.Errorf("audit log %q", audit)
	}
}

// imageVersion returns the version a published image reports (openlog-allinone -version).
func imageVersion(ctx context.Context, t *testing.T, image string) string {
	t.Helper()
	out, err := stack.Run(ctx, "", nil, "docker", "run", "--rm", "--entrypoint", "/usr/local/bin/openlog-allinone", image, "-version")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		t.Fatalf("%s -version: %q", image, out)
	}
	return f[1]
}

func sendLoad(ctx context.Context, t *testing.T, loadgen, prefix string) {
	t.Helper()
	if out, err := exec.CommandContext(ctx, loadgen, "-endpoint", "http://127.0.0.1:25318", "-license-key", license, "-hosts", "4",
		"-host-prefix", prefix, "-duration", "15s", "-interval", "5s", "-logs-per-sec", "20", "-spans-per-sec", "20").CombinedOutput(); err != nil {
		t.Fatalf("loadgen %s: %v\n%s", prefix, err, out)
	}
}

// telemetryRows counts the rows of the main telemetry tables (ClickHouse).
func telemetryRows(ctx context.Context, t *testing.T, c *stack.Compose) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for _, table := range []string{"logs", "spans", "metrics"} {
		s := c.ClickHouse(ctx, t, "SELECT count() FROM openlog."+table)
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("count %s: %q", table, s)
		}
		out[table] = n
	}
	return out
}

func assertRowsKept(ctx context.Context, t *testing.T, c *stack.Compose, when string, before map[string]uint64) {
	t.Helper()
	after := telemetryRows(ctx, t, c)
	for table, n := range before {
		if after[table] < n || n == 0 {
			t.Errorf("%s: %s rows %d, before %d", when, table, after[table], n)
		}
	}
	t.Logf("telemetry rows %s: %v (before %v)", when, after, before)
}

// assertQueries runs the queries the UI's main pages use through the API of the running version.
func assertQueries(ctx context.Context, t *testing.T, api stack.API, want string) {
	t.Helper()
	var v stack.Version
	if _, err := api.Get(ctx, "/api/v1/version", &v); err != nil || !strings.HasPrefix(v.Version, want) {
		t.Fatalf("version %q %v, want %s", v.Version, err, want)
	}
	var logs struct {
		Logs []json.RawMessage `json:"logs"`
	}
	if _, err := api.Get(ctx, "/api/v1/logs?limit=10", &logs); err != nil {
		t.Errorf("logs query on %s: %v", want, err)
	} else if len(logs.Logs) == 0 {
		t.Errorf("logs query on %s returned no logs", want)
	}
	if _, err := api.Get(ctx, "/api/v1/apm/services", nil); err != nil {
		t.Errorf("apm services on %s: %v", want, err)
	}
}

func assertTestMigrations(ctx context.Context, t *testing.T, c *stack.Compose, want string) {
	t.Helper()
	pg := c.PSQL(ctx, t, "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations WHERE version >= 9000")
	ch := c.ClickHouse(ctx, t, "SELECT arrayStringConcat(arraySort(groupUniqArray(toString(version))), ',') FROM openlog.schema_migrations WHERE version >= 9000")
	if pg != want || ch != want {
		t.Errorf("test migrations: postgres %q, clickhouse %q, want %q", pg, ch, want)
		return
	}
	t.Logf("test migrations applied: %s (postgres and clickhouse)", want)
}

func stepStatus(st updaterStatus, name string) string {
	for _, s := range st.Steps {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
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
