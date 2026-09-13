//go:build mixedversion

// Package mixedversion verifies that backend releases N and N+1 run side by side against the same
// stores (docs/contracts/releases-updates.md §5, §6):
//
//  1. release N (0.9.0) runs; data is ingested;
//
//  2. openlog-migrate of N+1 (0.9.1, with test-only expand + contract migrations) runs: the expand
//     migrations apply, the contract migrations requiring 0.9.1 are skipped while N heartbeats;
//
//  3. N and N+1 allinone, api and ingest run together; data sent to both is queryable through both;
//
//  4. after N stops, the contract migrations apply.
//
//     make mixed-version        (go test -tags mixedversion -v -count=1 -timeout 30m ./test/mixedversion)
//     MIXED_KEEP=1              keep the compose project openlog-mixedtest afterwards
package mixedversion

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/version"
	"github.com/onuragtas/openlog/test/stack"
)

const (
	imageN  = "openlog-mixedtest:0.9.0"
	imageN1 = "openlog-mixedtest:0.9.1"
	apiKey  = "ola_mixed-api-key"
	license = "mixed-license-key"
)

func TestMixedVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	root := stack.RepoRoot(t)

	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: imageN, Version: "0.9.0"})
	stack.BuildImage(ctx, t, stack.ImageSpec{Ref: imageN1, Version: "0.9.1", Tags: "openlog_testmigrations"})
	loadgen := stack.BuildHostTool(ctx, t, "./cmd/openlog-loadgen")

	c := &stack.Compose{
		Project: "openlog-mixedtest",
		Files:   []string{filepath.Join(root, "test/mixedversion/docker-compose.yml")},
		Env:     []string{"MIXED_IMAGE_N=" + imageN, "MIXED_IMAGE_N1=" + imageN1},
		Dir:     root,
	}
	t.Cleanup(func() {
		if t.Failed() {
			if out, err := c.Run(context.Background(), "--profile", "n1", "logs", "--no-color", "--tail", "80", "openlog-n", "openlog-n1", "api-n1", "ingest-n1"); err == nil {
				t.Logf("service logs:\n%s", out)
			}
		}
		if stack.Keep("MIXED_KEEP") {
			t.Log("MIXED_KEEP=1: keeping compose project openlog-mixedtest")
			return
		}
		_, _ = c.Run(context.Background(), "--profile", "n1", "down", "-v", "--remove-orphans")
		_, _ = stack.Run(context.Background(), "", nil, "docker", "image", "rm", imageN, imageN1)
	})
	_, _ = c.Run(ctx, "--profile", "n1", "down", "-v", "--remove-orphans")

	// 1. Release N.
	start := time.Now()
	c.MustRun(ctx, t, "up", "-d", "--wait", "--wait-timeout", "300", "openlog-n")
	t.Logf("release N up in %s", time.Since(start).Round(time.Second))
	apiN := stack.API{Base: "http://127.0.0.1:28080", Key: apiKey}
	assertVersion(ctx, t, apiN, "0.9.0")
	sendLoad(ctx, t, loadgen, "http://127.0.0.1:28318", "mixn", 15*time.Second)

	// 2. N+1 migrate while N runs: expand applies, contract waits.
	plan := c.MustRun(ctx, t, "--profile", "n1", "run", "--rm", "migrate-n1", "-plan")
	t.Logf("openlog-migrate -plan (N+1, N running):\n%s", planLines(plan))
	for _, want := range []string{
		`postgres\s+9001\s+mixedversion_expand\s+expand\s+apply`,
		`postgres\s+9002\s+mixedversion_contract\s+contract \(>= 0\.9\.1\)\s+skip\s+openlog-allinone \S+ runs 0\.9\.0\S* < 0\.9\.1`,
		`clickhouse\s+9002\s+mixedversion_contract\s+contract \(>= 0\.9\.1\)\s+skip`,
	} {
		if !regexp.MustCompile(want).MatchString(plan) {
			t.Errorf("plan does not match %q", want)
		}
	}
	c.MustRun(ctx, t, "--profile", "n1", "run", "--rm", "migrate-n1")
	assertMigrations(ctx, t, c, true, false)
	if _, err := apiN.HostNames(ctx); err != nil {
		t.Fatalf("release N broken after N+1 expand migrations: %v", err)
	}

	// 3. N and N+1 side by side.
	start = time.Now()
	c.MustRun(ctx, t, "--profile", "n1", "up", "-d", "--wait", "--wait-timeout", "300", "openlog-n1", "api-n1", "ingest-n1")
	t.Logf("release N+1 services up in %s", time.Since(start).Round(time.Second))
	apiN1 := stack.API{Base: "http://127.0.0.1:28081", Key: apiKey}
	apiN1Only := stack.API{Base: "http://127.0.0.1:28082", Key: apiKey}
	assertVersion(ctx, t, apiN, "0.9.0")
	assertVersion(ctx, t, apiN1, "0.9.1")
	assertVersion(ctx, t, apiN1Only, "0.9.1")
	if v := ingestVersion(ctx, t, "http://127.0.0.1:28320"); v != "0.9.1+test" && v != "0.9.1" {
		t.Errorf("ingest N+1 version header %q", v)
	}
	stack.Eventually(t, 90*time.Second, 3*time.Second, "heartbeats of both versions", func() error {
		out := c.PSQL(ctx, t, "SELECT component || '=' || version FROM component_heartbeats WHERE last_seen > now() - interval '5 minutes' ORDER BY 1")
		t.Logf("live instances: %s", strings.ReplaceAll(out, "\n", ", "))
		for _, want := range []string{"openlog-allinone=0.9.0", "openlog-allinone=0.9.1", "openlog-api=0.9.1", "openlog-ingest=0.9.1"} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("missing %s", want)
			}
		}
		return nil
	})

	var wg sync.WaitGroup
	for endpoint, prefix := range map[string]string{
		"http://127.0.0.1:28318": "mixn2", // N allinone
		"http://127.0.0.1:28319": "mixa1", // N+1 allinone
		"http://127.0.0.1:28320": "mixi1", // N+1 ingest
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendLoad(ctx, t, loadgen, endpoint, prefix, 20*time.Second)
		}()
	}
	wg.Wait()
	for name, api := range map[string]stack.API{"N allinone": apiN, "N+1 allinone": apiN1, "N+1 api": apiN1Only} {
		stack.Eventually(t, 2*time.Minute, 3*time.Second, "hosts through "+name, func() error {
			names, err := api.HostNames(ctx)
			if err != nil {
				return err
			}
			for _, p := range []string{"mixn-", "mixn2-", "mixa1-", "mixi1-"} {
				if !stack.HasPrefix(names, p) {
					return fmt.Errorf("no host %s* in %d hosts", p, len(names))
				}
			}
			return nil
		})
		if _, err := api.Get(ctx, "/api/v1/logs?limit=10", nil); err != nil {
			t.Errorf("logs through %s: %v", name, err)
		}
	}
	t.Log("data from N and N+1 ingest is queryable through N and N+1 api")

	// 4. Contract migrations wait for N to stop.
	c.MustRun(ctx, t, "--profile", "n1", "run", "--rm", "migrate-n1")
	assertMigrations(ctx, t, c, true, false)
	c.MustRun(ctx, t, "stop", "openlog-n") // graceful stop removes N's heartbeat row
	plan = c.MustRun(ctx, t, "--profile", "n1", "run", "--rm", "migrate-n1", "-plan")
	t.Logf("openlog-migrate -plan (N stopped):\n%s", planLines(plan))
	if !regexp.MustCompile(`postgres\s+9002\s+mixedversion_contract\s+contract \(>= 0\.9\.1\)\s+apply\s+all \d+ live instances >= 0\.9\.1`).MatchString(plan) {
		t.Errorf("contract migration not planned after N stopped")
	}
	c.MustRun(ctx, t, "--profile", "n1", "run", "--rm", "migrate-n1")
	assertMigrations(ctx, t, c, true, true)
	if _, err := apiN1.HostNames(ctx); err != nil {
		t.Errorf("N+1 after contract migration: %v", err)
	}

	logs := c.MustRun(ctx, t, "--profile", "n1", "logs", "--no-color", "openlog-n", "openlog-n1", "api-n1", "ingest-n1")
	var errorsFound []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"level":"ERROR"`) {
			errorsFound = append(errorsFound, l)
		}
	}
	if len(errorsFound) > 0 {
		t.Errorf("%d ERROR log lines, e.g.:\n%s", len(errorsFound), strings.Join(errorsFound[:min(5, len(errorsFound))], "\n"))
	}
}

func assertVersion(ctx context.Context, t *testing.T, api stack.API, want string) {
	t.Helper()
	var v stack.Version
	resp, err := api.Get(ctx, "/api/v1/version", &v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.Version, want) || !strings.HasPrefix(resp.Header.Get(version.Header), want) {
		t.Fatalf("%s: version %q header %q, want %s", api.Base, v.Version, resp.Header.Get(version.Header), want)
	}
}

func ingestVersion(ctx context.Context, t *testing.T, base string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get(version.Header)
}

func sendLoad(ctx context.Context, t *testing.T, loadgen, endpoint, prefix string, d time.Duration) {
	t.Helper()
	cmd := exec.CommandContext(ctx, loadgen, "-endpoint", endpoint, "-license-key", license, "-hosts", "3",
		"-host-prefix", prefix, "-duration", d.String(), "-interval", "5s", "-logs-per-sec", "20", "-spans-per-sec", "20")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("loadgen %s -> %s: %v\n%s", prefix, endpoint, err, out)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	t.Logf("loadgen %s -> %s: %s", prefix, endpoint, lines[len(lines)-1])
}

func assertMigrations(ctx context.Context, t *testing.T, c *stack.Compose, expand, contract bool) {
	t.Helper()
	pg := c.PSQL(ctx, t, "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations WHERE version >= 9000")
	ch := c.ClickHouse(ctx, t, "SELECT arrayStringConcat(arraySort(groupUniqArray(toString(version))), ',') FROM openlog.schema_migrations WHERE version >= 9000")
	want := ""
	if expand {
		want = "9001"
	}
	if contract {
		want += ",9002"
	}
	if pg != want || ch != want {
		t.Fatalf("test migrations applied: postgres=%q clickhouse=%q, want %q", pg, ch, want)
	}
	t.Logf("test migrations applied: postgres=%s clickhouse=%s", pg, ch)
}

func planLines(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if l == "" || strings.HasPrefix(l, "{") {
			continue
		}
		keep = append(keep, "  "+l)
	}
	return strings.Join(keep, "\n")
}
