package app

import (
	"crypto/tls"
	"fmt"
	"log/slog"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dashboard/report"
	"github.com/onuragtas/openlog/internal/renderer"
)

// reportImages enables PNG widget images in report e-mails when OPENLOG_RENDERER_URL is set (D-097): every api pod
// serves the render endpoints (the renderer's browser reaches any pod), the leader's report job calls the renderer.
// Returns nil without a renderer.
func reportImages(cfg config.Config, srv *api.Server, log *slog.Logger) (report.Imager, error) {
	rc := cfg.Renderer
	if rc.URL == "" {
		return nil, nil
	}
	key := renderer.KeyFromSecret(cfg.RenderSigningSecret())
	if key == nil {
		return nil, fmt.Errorf("OPENLOG_RENDERER_URL: set OPENLOG_KEY_HASH_SECRET or OPENLOG_SECRETS_KEY to sign render tokens")
	}
	tlsCfg, err := rendererClientTLS(rc)
	if err != nil {
		return nil, err
	}
	client, err := renderer.NewClient(rc.URL, rc.Token, tlsCfg, rc.Timeout)
	if err != nil {
		return nil, err
	}
	srv.SetRenderKey(key)
	log.Info("scheduled reports embed PNG widget images", "renderer", rc.URL, "mtls", tlsCfg != nil && len(tlsCfg.Certificates) > 0)
	return &renderer.ReportImages{Renderer: client, Key: key}, nil
}

// rendererClientTLS: the CA that verifies the renderer and the api's client certificate (both optional).
func rendererClientTLS(rc config.Renderer) (*tls.Config, error) {
	if rc.TLSCAFile == "" && rc.TLSCertFile == "" {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if rc.TLSCAFile != "" {
		pool, err := config.LoadCertPool(rc.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_RENDERER_TLS_CA_FILE: %w", err)
		}
		c.RootCAs = pool
	}
	if rc.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(rc.TLSCertFile, rc.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_RENDERER_TLS_CERT_FILE: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	return c, nil
}
