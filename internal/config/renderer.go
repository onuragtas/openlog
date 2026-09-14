package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Renderer configures PNG widget images in scheduled report e-mails (D-097, docs/operations/reports.md): the api side
// (OPENLOG_RENDERER_URL, _TIMEOUT) and the openlog-renderer service (_ADDR, _UI_ORIGIN, _MAX_CONCURRENCY, …). Both read
// OPENLOG_RENDERER_TOKEN and the optional mTLS files.
type Renderer struct {
	// URL is the render API base URL the api leader calls, e.g. http://openlog-renderer:8090; "" = tables only.
	URL string
	// Token is the shared secret of the render API (≥ 32 characters).
	Token string
	// Timeout bounds rendering one report on the api side.
	Timeout time.Duration
	// TLS files: on openlog-renderer the server certificate and, with CAFile, required client certificates (mTLS); on
	// the api the client certificate and the CA that verifies the renderer.
	TLSCertFile, TLSKeyFile, TLSCAFile string

	// openlog-renderer only.
	Addr           string        // OPENLOG_RENDERER_ADDR
	UIOrigin       string        // OPENLOG_RENDERER_UI_ORIGIN: the only origin the browser may load
	MaxConcurrency int           // OPENLOG_RENDERER_MAX_CONCURRENCY
	RenderTimeout  time.Duration // OPENLOG_RENDERER_RENDER_TIMEOUT
	QueueTimeout   time.Duration // OPENLOG_RENDERER_QUEUE_TIMEOUT
	MaxImageBytes  int64         // OPENLOG_RENDERER_MAX_IMAGE_BYTES
	ChromiumPath   string        // OPENLOG_RENDERER_CHROMIUM_PATH
	NoSandbox      bool          // OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX
}

func loadRenderer(p *parser) Renderer {
	return Renderer{
		URL:            p.str("OPENLOG_RENDERER_URL", ""),
		Token:          p.str("OPENLOG_RENDERER_TOKEN", ""),
		Timeout:        p.duration("OPENLOG_RENDERER_TIMEOUT", 2*time.Minute),
		TLSCertFile:    p.str("OPENLOG_RENDERER_TLS_CERT_FILE", ""),
		TLSKeyFile:     p.str("OPENLOG_RENDERER_TLS_KEY_FILE", ""),
		TLSCAFile:      p.str("OPENLOG_RENDERER_TLS_CA_FILE", ""),
		Addr:           p.str("OPENLOG_RENDERER_ADDR", ":8090"),
		UIOrigin:       p.str("OPENLOG_RENDERER_UI_ORIGIN", ""),
		MaxConcurrency: int(p.int64("OPENLOG_RENDERER_MAX_CONCURRENCY", 2)),
		RenderTimeout:  p.duration("OPENLOG_RENDERER_RENDER_TIMEOUT", 60*time.Second),
		QueueTimeout:   p.duration("OPENLOG_RENDERER_QUEUE_TIMEOUT", 30*time.Second),
		MaxImageBytes:  p.int64("OPENLOG_RENDERER_MAX_IMAGE_BYTES", 1<<20),
		ChromiumPath:   p.str("OPENLOG_RENDERER_CHROMIUM_PATH", ""),
		NoSandbox:      p.bool("OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX", false),
	}
}

// RenderSigningSecret is the server secret render tokens are derived from: OPENLOG_KEY_HASH_SECRET, else
// OPENLOG_SECRETS_KEY. Neither is given to openlog-renderer.
func (c Config) RenderSigningSecret() string {
	if c.KeyHash.Secret != "" {
		return c.KeyHash.Secret
	}
	return c.Alert.SecretsKey
}

func (c Config) validateRenderer() []error {
	r := c.Renderer
	var errs []error
	if r.URL != "" {
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, fmt.Errorf("OPENLOG_RENDERER_URL: must be an http(s) URL, got %q", r.URL))
		}
		if len(r.Token) < 32 {
			errs = append(errs, errors.New("OPENLOG_RENDERER_TOKEN: at least 32 characters are required with OPENLOG_RENDERER_URL"))
		}
		if c.RenderSigningSecret() == "" {
			errs = append(errs, errors.New("OPENLOG_RENDERER_URL: render tokens are signed with OPENLOG_KEY_HASH_SECRET or OPENLOG_SECRETS_KEY; set one of them"))
		}
	}
	if r.Token != "" && len(r.Token) < 32 {
		errs = append(errs, errors.New("OPENLOG_RENDERER_TOKEN: must be at least 32 characters"))
	}
	if r.Timeout < 5*time.Second || r.RenderTimeout < 5*time.Second || r.QueueTimeout < time.Second {
		errs = append(errs, errors.New("OPENLOG_RENDERER_TIMEOUT and OPENLOG_RENDERER_RENDER_TIMEOUT must be at least 5s, OPENLOG_RENDERER_QUEUE_TIMEOUT at least 1s"))
	}
	if r.MaxConcurrency < 1 || r.MaxConcurrency > 64 {
		errs = append(errs, fmt.Errorf("OPENLOG_RENDERER_MAX_CONCURRENCY: must be 1-64, got %d", r.MaxConcurrency))
	}
	if r.MaxImageBytes < 16<<10 || r.MaxImageBytes > 16<<20 {
		errs = append(errs, fmt.Errorf("OPENLOG_RENDERER_MAX_IMAGE_BYTES: must be 16384-16777216, got %d", r.MaxImageBytes))
	}
	if (r.TLSCertFile == "") != (r.TLSKeyFile == "") {
		errs = append(errs, errors.New("OPENLOG_RENDERER_TLS_CERT_FILE and OPENLOG_RENDERER_TLS_KEY_FILE must be set together"))
	}
	return errs
}
