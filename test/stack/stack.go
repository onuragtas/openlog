// Package stack holds helpers for the Docker-based upgrade tests (test/mixedversion,
// test/autoupdate): versioned test images, docker compose projects and API polling.
package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// RepoRoot returns the repository root (the directory with go.mod).
func RepoRoot(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Run executes a command in dir and returns combined output.
func Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, tail(out.String(), 60))
	}
	return out.String(), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// DockerArch is the architecture of the Docker daemon (amd64, arm64).
func DockerArch(ctx context.Context) (string, error) {
	out, err := Run(ctx, "", nil, "docker", "version", "--format", "{{.Server.Arch}}")
	return strings.TrimSpace(out), err
}

// ImageSpec describes a test image of openlog built from the working tree.
type ImageSpec struct {
	Ref     string // image reference to tag
	Version string // injected product version
	Tags    string // go build tags (e.g. openlog_testmigrations)
}

// BuildImage cross-compiles every backend binary for the daemon's platform with the version
// injected and packs them into a small alpine image. It does not need Node or the Dockerfile's
// build stages, so building two versions takes seconds.
func BuildImage(ctx context.Context, t testing.TB, spec ImageSpec) {
	t.Helper()
	root := RepoRoot(t)
	arch, err := DockerArch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	ld := fmt.Sprintf("-s -w -X github.com/onuragtas/openlog/internal/version.Version=%s -X github.com/onuragtas/openlog/internal/version.Commit=test -X github.com/onuragtas/openlog/internal/version.Date=%s",
		spec.Version, time.Now().UTC().Format(time.RFC3339))
	args := []string{"build", "-trimpath", "-ldflags", ld, "-o", bin + "/"}
	if spec.Tags != "" {
		args = append(args, "-tags", spec.Tags)
	}
	args = append(args, "./cmd/openlog-allinone", "./cmd/openlog-api", "./cmd/openlog-ingest", "./cmd/openlog-processor",
		"./cmd/openlog-migrate", "./cmd/openlog-admin", "./cmd/openlog-loadgen", "./cmd/openlog-updater")
	if _, err := Run(ctx, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}, "go", args...); err != nil {
		t.Fatal(err)
	}
	dockerfile := `FROM alpine:3.22
RUN addgroup -S -g 10001 openlog && adduser -S -D -H -u 10001 -G openlog openlog
COPY bin/ /usr/local/bin/
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/openlog-allinone"]
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, dir, nil, "docker", "build", "-q", "--label", "org.opencontainers.image.version="+spec.Version, "-t", spec.Ref, "."); err != nil {
		t.Fatal(err)
	}
	t.Logf("built image %s (version %s, tags %q)", spec.Ref, spec.Version, spec.Tags)
}

// BuildHostTool builds a command for the host (e.g. openlog-loadgen) and returns its path.
func BuildHostTool(ctx context.Context, t testing.TB, pkg string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), filepath.Base(pkg))
	if _, err := Run(ctx, RepoRoot(t), []string{"CGO_ENABLED=0"}, "go", "build", "-o", out, pkg); err != nil {
		t.Fatal(err)
	}
	return out
}

// Compose runs docker compose for one project.
type Compose struct {
	Project string
	Files   []string
	EnvFile string
	Env     []string
	Dir     string
}

// Args returns the base docker compose arguments.
func (c *Compose) Args(args ...string) []string {
	base := []string{"compose", "-p", c.Project}
	for _, f := range c.Files {
		base = append(base, "-f", f)
	}
	if c.EnvFile != "" {
		base = append(base, "--env-file", c.EnvFile)
	}
	return append(base, args...)
}

// Run executes docker compose with args.
func (c *Compose) Run(ctx context.Context, args ...string) (string, error) {
	return Run(ctx, c.Dir, c.Env, "docker", c.Args(args...)...)
}

// MustRun fails the test on error.
func (c *Compose) MustRun(ctx context.Context, t testing.TB, args ...string) string {
	t.Helper()
	out, err := c.Run(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// PSQL runs a query in the postgres service and returns the unaligned, tuples-only output.
func (c *Compose) PSQL(ctx context.Context, t testing.TB, sql string) string {
	t.Helper()
	return strings.TrimSpace(c.MustRun(ctx, t, "exec", "-T", "postgres", "psql", "-U", "openlog", "-d", "openlog", "-tA", "-c", sql))
}

// ClickHouse runs a query with clickhouse-client in the clickhouse service.
func (c *Compose) ClickHouse(ctx context.Context, t testing.TB, sql string) string {
	t.Helper()
	return strings.TrimSpace(c.MustRun(ctx, t, "exec", "-T", "clickhouse", "clickhouse-client", "--user", "openlog", "--password", "openlog", "-q", sql))
}

// API is a Query API client with a bearer API key.
type API struct {
	Base string
	Key  string
}

// Get performs GET path and decodes JSON into out (when non-nil); it returns the response.
func (a API) Get(ctx context.Context, path string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+path, nil)
	if err != nil {
		return nil, err
	}
	if a.Key != "" {
		req.Header.Set("Authorization", "Bearer "+a.Key)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp, fmt.Errorf("GET %s: %w", path, err)
		}
	}
	return resp, nil
}

// Version is the subset of GET /api/v1/version the tests read.
type Version struct {
	Version     string          `json:"version"`
	UpdateCheck string          `json:"update_check"`
	Updater     json.RawMessage `json:"updater"`
}

// HostNames returns the host names of GET /api/v1/hosts.
func (a API) HostNames(ctx context.Context) ([]string, error) {
	var body struct {
		Hosts []struct {
			HostName string `json:"host_name"`
		} `json:"hosts"`
	}
	if _, err := a.Get(ctx, "/api/v1/hosts?limit=1000", &body); err != nil {
		return nil, err
	}
	var names []string
	for _, h := range body.Hosts {
		names = append(names, h.HostName)
	}
	return names, nil
}

// Eventually polls fn every interval until it returns nil or timeout passes.
func Eventually(t testing.TB, timeout, interval time.Duration, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = fn(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not satisfied within %s: %v", what, timeout, err)
		}
		time.Sleep(interval)
	}
}

// HasPrefix reports whether any name starts with prefix.
func HasPrefix(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// Keep reports whether the environment asks to keep the stack (for debugging).
func Keep(name string) bool { return os.Getenv(name) == "1" }
