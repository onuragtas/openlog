package config

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// ReloadCheckInterval is how often certificate files are re-read (at most once per interval, on the next
// new connection). Kubernetes updates mounted Secrets within about a minute, cert-manager renews well
// before expiry, so a few seconds are enough (D-048).
var ReloadCheckInterval = 10 * time.Second

// Reloadable holds a value built from files and rebuilds it when their content changes. The check runs
// lazily in Get, at most once per interval; a failed rebuild (half-written or mismatched files during a
// rotation) keeps the previous value and is retried on the next check.
type Reloadable[T any] struct {
	name     string
	files    []string
	build    func() (T, error)
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	val     T
	sum     [sha256.Size]byte
	checked time.Time
	failing bool
}

// NewReloadable builds the initial value; an error here is a configuration error. name is used in logs.
func NewReloadable[T any](name string, files []string, build func() (T, error)) (*Reloadable[T], error) {
	r := &Reloadable[T]{name: name, build: build, interval: ReloadCheckInterval, now: time.Now}
	for _, f := range files {
		if f != "" {
			r.files = append(r.files, f)
		}
	}
	sum, err := r.fingerprint()
	if err != nil {
		return nil, err
	}
	v, err := build()
	if err != nil {
		return nil, err
	}
	r.val, r.sum, r.checked = v, sum, r.now()
	return r, nil
}

func (r *Reloadable[T]) fingerprint() ([sha256.Size]byte, error) {
	h := sha256.New()
	for _, f := range r.files {
		// Read the content, not mtime: Kubernetes swaps Secret volumes through symlinks.
		b, err := os.ReadFile(f)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Get returns the current value, rebuilding it first when the files changed since the last check.
func (r *Reloadable[T]) Get() T {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.files) == 0 || r.now().Sub(r.checked) < r.interval {
		return r.val
	}
	r.checked = r.now()
	sum, err := r.fingerprint()
	if err == nil && sum == r.sum {
		return r.val
	}
	var v T
	if err == nil {
		v, err = r.build()
	}
	if err != nil {
		if !r.failing {
			slog.Warn("certificate reload failed; keeping the previous certificates", "tls", r.name, "err", err)
		}
		r.failing = true
		return r.val
	}
	r.val, r.sum, r.failing = v, sum, false
	slog.Info("certificates reloaded", "tls", r.name)
	return r.val
}

// TLSReloader dials TLS connections with certificates re-read from the configured files (CA bundle, client
// certificate and key). Connections opened before a change keep their certificates; every new connection
// uses the files' current content.
type TLSReloader struct {
	r *Reloadable[*tls.Config]
}

// Reloader returns a TLSReloader, or nil when TLS is disabled. Invalid files are reported like Config.
func (t TLS) Reloader() (*TLSReloader, error) {
	if !t.Enabled {
		return nil, nil
	}
	// Config first: its errors name the variable.
	if _, err := t.Config(); err != nil {
		return nil, err
	}
	r, err := NewReloadable(t.prefix, []string{t.CAFile, t.CertFile, t.KeyFile}, t.Config)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.prefix, err)
	}
	return &TLSReloader{r: r}, nil
}

// Config returns the current client configuration (shared: clone before modifying).
func (t *TLSReloader) Config() *tls.Config { return t.r.Get() }

// DialContext opens a TLS connection to addr with the current certificates. An empty ServerName is set to
// the dialed host, like tls.Dial, so each broker or replica certificate is verified against its own name.
func (t *TLSReloader) DialContext(ctx context.Context, timeout time.Duration, network, addr string) (net.Conn, error) {
	c := t.Config().Clone()
	if c.ServerName == "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		c.ServerName = host
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}, Config: c}
	return d.DialContext(ctx, network, addr)
}
