package config

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	// Write to a temp file and rename, like a Secret volume update (no half-written file is read).
	tmp := to + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, to); err != nil {
		t.Fatal(err)
	}
}

func certSerial(t *testing.T, file string) string {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(file, strings.TrimSuffix(file, ".crt")+".key")
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return c.SerialNumber.String()
}

// tlsServer requires client certificates from either CA and reports the serial of each client certificate.
type tlsServer struct {
	addr    string
	cert    atomic.Pointer[tls.Certificate]
	serials chan string
}

func startTLSServer(t *testing.T, cas []string, cert, key string) *tlsServer {
	t.Helper()
	pool := x509.NewCertPool()
	for _, f := range cas {
		b, _ := os.ReadFile(f)
		pool.AppendCertsFromPEM(b)
	}
	s := &tlsServer{serials: make(chan string, 10)}
	s.setCert(t, cert, key)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.cert.Load(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := c.(*tls.Conn)
			if err := tc.Handshake(); err == nil && len(tc.ConnectionState().PeerCertificates) > 0 {
				s.serials <- tc.ConnectionState().PeerCertificates[0].SerialNumber.String()
			} else {
				s.serials <- "handshake failed"
			}
			tc.Close()
		}
	}()
	return s
}

func (s *tlsServer) setCert(t *testing.T, cert, key string) {
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	s.cert.Store(&pair)
}

func (s *tlsServer) dial(t *testing.T, r *TLSReloader) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := r.DialContext(ctx, 5*time.Second, "tcp", s.addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	select {
	case serial := <-s.serials:
		return serial, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Renewed client certificates and a new CA are used by new connections without a restart (D-048).
func TestTLSReloaderPicksUpRotatedFiles(t *testing.T) {
	old, err := testcerts.Generate(filepath.Join(t.TempDir(), "old"), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := testcerts.Generate(filepath.Join(t.TempDir(), "new"), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ca, crt, key := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	copyFile(t, old.CAFile, ca)
	copyFile(t, old.ClientCert, crt)
	copyFile(t, old.ClientKey, key)

	defer func(v time.Duration) { ReloadCheckInterval = v }(ReloadCheckInterval)
	ReloadCheckInterval = 0

	srv := startTLSServer(t, []string{old.CAFile, renewed.CAFile}, old.ServerCert, old.ServerKey)
	r, err := TLS{Enabled: true, CAFile: ca, CertFile: crt, KeyFile: key, prefix: "OPENLOG_TEST_TLS"}.Reloader()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := srv.dial(t, r); err != nil || got != certSerial(t, old.ClientCert) {
		t.Fatalf("before rotation: client serial %q err %v", got, err)
	}

	// The server moves to a certificate of the new CA: the old CA file no longer verifies it.
	srv.setCert(t, renewed.ServerCert, renewed.ServerKey)
	if _, err := srv.dial(t, r); err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("new server certificate with the old CA file: %v", err)
	}
	<-srv.serials // the server side of the failed handshake

	copyFile(t, renewed.CAFile, ca)
	copyFile(t, renewed.ClientCert, crt)
	copyFile(t, renewed.ClientKey, key)
	if got, err := srv.dial(t, r); err != nil || got != certSerial(t, renewed.ClientCert) {
		t.Fatalf("after rotation: client serial %q err %v; want the renewed certificate", got, err)
	}
}

func TestReloadableKeepsPreviousValueOnBrokenFiles(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	crt, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	copyFile(t, b.ClientCert, crt)
	copyFile(t, b.ClientKey, key)
	tt := TLS{Enabled: true, CertFile: crt, KeyFile: key, prefix: "OPENLOG_TEST_TLS"}
	r, err := NewReloadable(tt.prefix, []string{crt, key}, tt.Config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.now = func() time.Time { return now }
	first := r.Get()

	// Within the interval nothing is re-read.
	copyFile(t, b.ServerCert, crt) // certificate no longer matches the key
	if r.Get() != first {
		t.Fatal("files re-read before the check interval")
	}
	// After the interval: the mismatched pair fails to load, the previous config stays in use.
	now = now.Add(ReloadCheckInterval)
	if r.Get() != first {
		t.Fatal("broken files replaced the working configuration")
	}
	// Completing the rotation (matching key) is picked up on the next check.
	copyFile(t, b.ServerKey, key)
	now = now.Add(ReloadCheckInterval)
	if got := r.Get(); got == first || len(got.Certificates) != 1 {
		t.Fatal("completed rotation not picked up")
	}

	if _, err := NewReloadable("x", []string{filepath.Join(dir, "missing")}, tt.Config); err == nil {
		t.Error("missing file accepted at start-up")
	}
	if r, err := (TLS{}).Reloader(); r != nil || err != nil {
		t.Errorf("disabled TLS: %v %v", r, err)
	}
	if _, err := (TLS{Enabled: true, CAFile: filepath.Join(dir, "missing"), prefix: "OPENLOG_KAFKA_TLS"}).Reloader(); err == nil ||
		!strings.Contains(err.Error(), "OPENLOG_KAFKA_TLS_CA_FILE") {
		t.Errorf("error does not name the variable: %v", err)
	}
}
