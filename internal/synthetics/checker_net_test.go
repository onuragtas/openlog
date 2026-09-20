package synthetics

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// checkerForNetwork allows private addresses: every test target is a listener on loopback.
func checkerForNetwork(now time.Time) *Checker {
	return NewChecker(CheckerOptions{AllowPrivateNetworks: true, Now: func() time.Time { return now }})
}

func due(in Input) Due {
	return Due{Check: Check{ID: "c1", Input: in}, TenantID: "t1", Location: LocationLocal}
}

func TestTCPCheckSucceedsOnAnOpenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	in := Input{Name: "port", Type: TypeTCP, Target: ln.Addr().String()}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	r := checkerForNetwork(time.Now()).Run(context.Background(), due(in))
	if !r.Success || r.ErrorKind != "" {
		t.Fatalf("run = %+v", r)
	}
	if r.Type != TypeTCP || r.Target != in.Target || r.Answer == "" {
		t.Errorf("the run must describe what it reached: %+v", r)
	}
}

func TestTCPCheckFailsOnAClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there any more

	in := Input{Name: "port", Type: TypeTCP, Target: addr}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	r := checkerForNetwork(time.Now()).Run(context.Background(), due(in))
	if r.Success || r.ErrorKind != ErrorConnect {
		t.Fatalf("run = %+v, want a connect failure", r)
	}
}

// A tcp check must not reach the network openlog runs in unless that was configured.
func TestTCPCheckHonoursTheAddressGuard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	in := Input{Name: "port", Type: TypeTCP, Target: ln.Addr().String()}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	r := NewChecker(CheckerOptions{}).Run(context.Background(), due(in))
	if r.Success || r.ErrorKind != ErrorBlocked {
		t.Fatalf("run = %+v, want blocked", r)
	}
}

// tlsServer starts a TLS listener whose certificate is valid for 127.0.0.1 in [notBefore, notAfter), and
// returns a pool trusting it — what OPENLOG_SYNTHETICS_CA_FILE is for an installation with its own CA.
func tlsServer(t *testing.T, notBefore, notAfter time.Time) (net.Listener, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "openlog test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// The handshake must complete before the connection goes away, or the client sees a reset
			// instead of the certificate.
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.HandshakeContext(context.Background())
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)
	return ln, pool
}

// A self-signed certificate fails verification, which is a tls failure and not a certificate one: the
// distinction is what tells "the handshake did not happen" from "the certificate is about to expire".
func TestTLSCheckReportsAnUntrustedCertificate(t *testing.T) {
	now := time.Now()
	ln, _ := tlsServer(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	in := Input{Name: "cert", Type: TypeTLS, Target: ln.Addr().String()}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if in.TLSWarningDays != DefaultTLSWarningDays {
		t.Fatalf("tls_warning_days = %d, want the default", in.TLSWarningDays)
	}
	r := checkerForNetwork(now).Run(context.Background(), due(in))
	if r.Success || r.ErrorKind != ErrorTLS {
		t.Fatalf("run = %+v, want a tls failure", r)
	}
}

// With the certificate trusted (as an internal CA would be), its dates are what the check is about.
func TestTLSCheckDates(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		notAfter  time.Duration
		warnDays  int
		wantKind  string
		wantInMsg string
	}{
		{"valid", 90 * 24 * time.Hour, 14, "", ""},
		{"expiring", 3 * 24 * time.Hour, 14, ErrorCertificate, "expires in 2 days"}, // whole days left, rounded down: a warning must never overstate the time left
		{"expired", -time.Hour, 14, ErrorCertificate, "expired on"},
		{"warning off", 3 * 24 * time.Hour, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, pool := tlsServer(t, now.Add(-time.Hour), now.Add(tc.notAfter))
			in := Input{Name: "cert", Type: TypeTLS, Target: ln.Addr().String(), TLSWarningDays: tc.warnDays}
			if err := in.Validate(); err != nil {
				t.Fatal(err)
			}
			in.TLSWarningDays = tc.warnDays // Validate() fills 0 with the default
			c := NewChecker(CheckerOptions{AllowPrivateNetworks: true, RootCAs: pool, Now: func() time.Time { return now }})
			r := c.Run(context.Background(), due(in))
			if r.ErrorKind != tc.wantKind {
				t.Fatalf("error kind = %q (%s), want %q", r.ErrorKind, r.Error, tc.wantKind)
			}
			if tc.wantInMsg != "" && !strings.Contains(r.Error, tc.wantInMsg) {
				t.Errorf("error = %q, want it to contain %q", r.Error, tc.wantInMsg)
			}
			if r.CertExpiresAt.IsZero() {
				t.Error("a completed handshake must record the certificate's notAfter")
			}
			if tc.wantKind == "" && !r.Success {
				t.Errorf("run = %+v, want success", r)
			}
		})
	}
}

func TestDNSCheckResolvesLocalhost(t *testing.T) {
	in := Input{Name: "dns", Type: TypeDNS, Target: "localhost", DNSRecordType: "A"}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	r := checkerForNetwork(time.Now()).Run(context.Background(), due(in))
	if !r.Success {
		t.Skipf("this machine does not resolve localhost to an A record: %+v", r)
	}
	if !strings.Contains(r.Answer, "127.0.0.1") {
		t.Errorf("answer = %q, want the resolved address", r.Answer)
	}
}

func TestDNSCheckFailsOnAnUnexpectedAnswer(t *testing.T) {
	in := Input{Name: "dns", Type: TypeDNS, Target: "localhost", DNSRecordType: "A", DNSExpected: []string{"203.0.113.10"}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	r := checkerForNetwork(time.Now()).Run(context.Background(), due(in))
	if r.Success {
		t.Fatalf("run = %+v, want a record failure", r)
	}
	if r.ErrorKind != ErrorRecord && r.ErrorKind != ErrorDNS {
		t.Fatalf("error kind = %q", r.ErrorKind)
	}
}

func TestMissingRecordsIgnoresCaseAndTrailingDot(t *testing.T) {
	if got := missingRecords([]string{"Example.COM"}, []string{"example.com."}); len(got) != 0 {
		t.Errorf("missing = %v, want none", got)
	}
	if got := missingRecords([]string{"a.example.com", "b.example.com"}, []string{"a.example.com"}); len(got) != 1 || got[0] != "b.example.com" {
		t.Errorf("missing = %v", got)
	}
}

func TestValidateRejectsMixedTargets(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		field string
	}{
		{"tcp without a port", Input{Name: "x", Type: TypeTCP, Target: "example.com"}, "target"},
		{"tcp with a URL", Input{Name: "x", Type: TypeTCP, Target: "https://example.com:443"}, "target"},
		{"tls port out of range", Input{Name: "x", Type: TypeTLS, Target: "example.com:70000"}, "target"},
		{"dns without a name", Input{Name: "x", Type: TypeDNS}, "target"},
		{"dns with an unknown record", Input{Name: "x", Type: TypeDNS, Target: "example.com", DNSRecordType: "SRV"}, "dns_record_type"},
		{"unknown type", Input{Name: "x", Type: "browser"}, "type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			err := in.Validate()
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want a validation error on %q", err, tc.field)
			}
		})
	}
}

// A non-http check must not keep HTTP fields, and an http check must not keep a target: the stored row
// describes one kind of check, and the database repeats that as a constraint.
func TestValidateClearsTheOtherTypesFields(t *testing.T) {
	tcp := Input{Name: "x", Type: TypeTCP, Target: "example.com:6379", URL: "https://example.com", Method: "POST",
		Body: "hello", Headers: map[string]string{"X-Test": "1"}, AssertionType: AssertContains, AssertionValue: "ok"}
	if err := tcp.Validate(); err != nil {
		t.Fatal(err)
	}
	if tcp.URL != "" || tcp.Method != "" || tcp.Body != "" || len(tcp.Headers) != 0 || len(tcp.ExpectedStatus) != 0 {
		t.Errorf("the HTTP fields must be cleared: %+v", tcp)
	}
	if tcp.AssertionType != AssertNone || tcp.AssertionValue != "" {
		t.Errorf("the assertion must be cleared: %+v", tcp)
	}

	http := Input{Name: "x", Type: TypeHTTP, URL: "https://example.com", Target: "example.com:443",
		DNSRecordType: "A", DNSExpected: []string{"1.2.3.4"}, TLSWarningDays: 30}
	if err := http.Validate(); err != nil {
		t.Fatal(err)
	}
	if http.Target != "" || http.DNSRecordType != "" || len(http.DNSExpected) != 0 || http.TLSWarningDays != 0 {
		t.Errorf("the target fields must be cleared: %+v", http)
	}
}
