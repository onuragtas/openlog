// Command openlog-renderer renders dashboard widgets of scheduled reports to PNG images with headless Chromium
// (D-097, docs/operations/reports.md). It serves an internal, token-authenticated HTTP API for the api leader and
// loads only the web app's print view on OPENLOG_RENDERER_UI_ORIGIN. Shipped as its own image
// (ghcr.io/onuragtas/openlog-renderer) so the main image carries no browser.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/renderer"
	"github.com/onuragtas/openlog/internal/renderer/chrome"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-renderer %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-renderer", run)
}

func run(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	rc := cfg.Renderer
	if rc.Token == "" {
		return errors.New("OPENLOG_RENDERER_TOKEN is required")
	}
	if rc.UIOrigin == "" {
		return errors.New("OPENLOG_RENDERER_UI_ORIGIN is required (the web UI origin the browser loads, e.g. http://openlog-api:8080)")
	}
	chromium := rc.ChromiumPath
	if chromium == "" {
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "headless-shell"} {
			if p, err := exec.LookPath(name); err == nil {
				chromium = p
				break
			}
		}
	}
	if chromium == "" {
		return errors.New("no Chromium binary found; set OPENLOG_RENDERER_CHROMIUM_PATH")
	}
	if rc.NoSandbox {
		log.Warn("Chromium sandbox disabled (OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX): run only in a locked-down container (non-root, read-only root filesystem, no capabilities, egress to the UI origin only)")
	}
	browser, err := chrome.New(chrome.Options{ExecPath: chromium, NoSandbox: rc.NoSandbox, Origin: rc.UIOrigin, Log: log})
	if err != nil {
		return fmt.Errorf("OPENLOG_RENDERER_UI_ORIGIN: %w", err)
	}
	api, err := renderer.NewServer(renderer.ServerOptions{Token: rc.Token, Origin: rc.UIOrigin, MaxConcurrency: rc.MaxConcurrency,
		QueueTimeout: rc.QueueTimeout, RenderTimeout: rc.RenderTimeout, MaxImageBytes: int(rc.MaxImageBytes), Browser: browser,
		Log: log, Registerer: adm.Registry()})
	if err != nil {
		return err
	}
	tlsCfg, err := serverTLS(rc)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: rc.Addr, Handler: api.Handler(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: rc.QueueTimeout + rc.RenderTimeout + 30*time.Second, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 16 << 10}
	ln, err := net.Listen("tcp", rc.Addr)
	if err != nil {
		return err
	}
	adm.AddCheck("listener", admin.ListenerCheck(rc.Addr))
	adm.AddCheck("chromium", func(context.Context) error {
		_, err := os.Stat(chromium)
		return err
	})
	log.Info("renderer listening", "addr", rc.Addr, "ui_origin", rc.UIOrigin, "chromium", chromium, "tls", tlsCfg != nil,
		"mtls", tlsCfg != nil && tlsCfg.ClientAuth == tls.RequireAndVerifyClientCert, "max_concurrency", rc.MaxConcurrency)

	errc := make(chan error, 1)
	go func() {
		if tlsCfg != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), rc.RenderTimeout+5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// serverTLS: the renderer's certificate and, with a CA, required client certificates (mTLS).
func serverTLS(rc config.Renderer) (*tls.Config, error) {
	if rc.TLSCertFile == "" {
		if rc.TLSCAFile != "" {
			return nil, errors.New("OPENLOG_RENDERER_TLS_CA_FILE requires OPENLOG_RENDERER_TLS_CERT_FILE and _KEY_FILE on the renderer")
		}
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(rc.TLSCertFile, rc.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("OPENLOG_RENDERER_TLS_CERT_FILE: %w", err)
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if rc.TLSCAFile != "" {
		pool, err := config.LoadCertPool(rc.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_RENDERER_TLS_CA_FILE: %w", err)
		}
		c.ClientCAs, c.ClientAuth = pool, tls.RequireAndVerifyClientCert
	}
	return c, nil
}
