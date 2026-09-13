//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testUI runs the Playwright suite web/e2e-stack against this stack's embedded web UI
// (E2E_UI=1). node_modules never live in the repository checkout: web/ is copied to
// E2E_UI_WORKDIR (default $TMPDIR/openlog-e2e-ui) and `npm ci` runs only when package-lock.json
// changed. Requires Node >= 20.19; Chromium is installed by `npx playwright install chromium`.
func testUI(t *testing.T) {
	if os.Getenv("E2E_UI") != "1" {
		t.Skip("E2E_UI!=1: skipping the Playwright UI phase")
	}
	work := os.Getenv("E2E_UI_WORKDIR")
	if work == "" {
		work = filepath.Join(os.TempDir(), "openlog-e2e-ui")
	}
	web := filepath.Join(work, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(timeout time.Duration, dir string, extraEnv []string, name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), extraEnv...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		start := time.Now()
		err := cmd.Run()
		t.Logf("%s %s: %s (%v)", name, strings.Join(args, " "), time.Since(start).Round(100*time.Millisecond), err)
		return buf.String(), err
	}

	if out, err := run(2*time.Minute, repoRoot, nil, "rsync", "-a", "--delete",
		"--exclude", "node_modules", "--exclude", "dist", "--exclude", "test-results", "--exclude", "test-results-stack",
		"--exclude", "playwright-report", filepath.Join(repoRoot, "web")+"/", web+"/"); err != nil {
		t.Fatalf("copy web/: %v\n%s", err, out)
	}
	lock, _ := os.ReadFile(filepath.Join(web, "package-lock.json"))
	stampFile := filepath.Join(work, "package-lock.installed")
	stamp, _ := os.ReadFile(stampFile)
	if _, err := os.Stat(filepath.Join(web, "node_modules", ".bin", "playwright")); err != nil || !bytes.Equal(lock, stamp) {
		if out, err := run(10*time.Minute, web, nil, "npm", "ci", "--no-audit", "--no-fund"); err != nil {
			t.Fatalf("npm ci: %v\n%s", err, tail(out, 40))
		}
		_ = os.WriteFile(stampFile, lock, 0o644)
	}
	if out, err := run(10*time.Minute, web, nil, "npx", "playwright", "install", "chromium"); err != nil {
		t.Fatalf("playwright install: %v\n%s", err, tail(out, 40))
	}

	marker := "/e2e-log/"
	if logMarkerUsed {
		marker = logMarker()
	}
	uiEnv := []string{
		"E2E_BASE_URL=http://127.0.0.1:" + env["OPENLOG_API_PORT"],
		"E2E_OWNER_EMAIL=" + env["OPENLOG_BOOTSTRAP_OWNER_EMAIL"],
		"E2E_OWNER_PASSWORD=" + env["OPENLOG_BOOTSTRAP_OWNER_PASSWORD"],
		"E2E_TARGET_ID=" + targetID(),
		"E2E_TARGET_NAME=" + targetName,
		"E2E_PLAIN_NAME=" + plainName,
		"E2E_LICENSE_KEY_PREFIX=" + displayPrefix(env["E2E_LICENSE_KEY"]),
		"E2E_LOG_MARKER=" + marker,
		"E2E_UI_OUTPUT=" + filepath.Join(work, "test-results-stack"),
	}
	out, err := run(10*time.Minute, web, uiEnv, "npx", "playwright", "test", "-c", "playwright.stack.config.ts")
	t.Logf("playwright output:\n%s", tail(out, 60))
	if err != nil {
		t.Errorf("playwright UI suite failed (traces/screenshots in %s)", filepath.Join(work, "test-results-stack"))
	}
}

// displayPrefix mirrors auth.DisplayPrefix for operator-chosen (bootstrap) keys.
func displayPrefix(secret string) string {
	return secret[:min(8, len(secret)/2)]
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
