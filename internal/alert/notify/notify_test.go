package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleEvent() Event {
	v, th := 0.93, 0.9
	return Event{
		Version: "1", Event: EventOpened, IdempotencyKey: "inc-1:opened:ch-1", NotificationID: "n-1",
		Organization: Org{ID: "org-1", Name: "Default"},
		Rule:         RuleInfo{ID: "r-1", Name: "High CPU", Type: "metric_threshold", Severity: "critical", URL: "https://ol.example/alerts/rules/r-1"},
		Incident: IncidentInfo{ID: "inc-1", State: "open", URL: "https://ol.example/alerts/incidents/inc-1",
			Summary: "system.cpu.utilization avg over 1m is 0.93 (> 0.9) on web-1", Value: &v, Threshold: &th,
			Labels: map[string]string{"host.name": "web-1", "host.id": "h1", "alert.severity": "critical"}, OpenedAt: "2026-09-13T10:00:00Z"},
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"a":1}`)
	ts := int64(1757757600)
	sig := Sign("s3cret", ts, body)
	// Known-answer: HMAC-SHA256("s3cret", "1757757600.{\"a\":1}").
	if sig != "sha256=9b6f0a3b0ba9a3e0b1d1d6a4dd1c43be5ab69b9e2a0f7e31e0d8a38c7cf7a07b" && !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature format %q", sig)
	}
	now := time.Unix(ts, 0).Add(time.Minute)
	if err := Verify("s3cret", sig, "1757757600", body, now, 5*time.Minute); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if Verify("other", sig, "1757757600", body, now, 5*time.Minute) == nil {
		t.Error("wrong secret accepted")
	}
	if Verify("s3cret", sig, "1757757600", []byte(`{"a":2}`), now, 5*time.Minute) == nil {
		t.Error("modified body accepted")
	}
	if Verify("s3cret", sig, "1757757601", body, now, 5*time.Minute) == nil {
		t.Error("modified timestamp accepted")
	}
	if Verify("s3cret", sig, "1757757600", body, now.Add(10*time.Minute), 5*time.Minute) == nil {
		t.Error("replayed old signature accepted")
	}
}

func TestWebhookDelivery(t *testing.T) {
	var got struct {
		sync.Mutex
		headers http.Header
		body    []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Lock()
		defer got.Unlock()
		got.headers = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	s := NewSender(Options{Timeout: 2 * time.Second, UserAgent: "openlog-alert/test"})
	fixed := time.Unix(1757757600, 0)
	s.SetClock(func() time.Time { return fixed })
	res := s.Send(context.Background(), Target{Type: TypeWebhook, URL: srv.URL, HMACSecret: "k"}, sampleEvent(), "")
	if !res.OK() || res.StatusCode != 200 {
		t.Fatalf("delivery: %+v", res)
	}
	got.Lock()
	defer got.Unlock()
	h := got.headers
	if h.Get(HeaderEvent) != EventOpened || h.Get(HeaderIdempotencyKey) != "inc-1:opened:ch-1" || h.Get(HeaderDelivery) != "n-1" ||
		h.Get(HeaderTimestamp) != "1757757600" || h.Get("User-Agent") != "openlog-alert/test" {
		t.Fatalf("headers %v", h)
	}
	if err := Verify("k", h.Get(HeaderSignature), h.Get(HeaderTimestamp), got.body, fixed, time.Minute); err != nil {
		t.Fatalf("receiver cannot verify the signature: %v", err)
	}
	var ev Event
	if err := json.Unmarshal(got.body, &ev); err != nil || ev.Incident.ID != "inc-1" || ev.Rule.Name != "High CPU" {
		t.Fatalf("body %s %v", got.body, err)
	}
}

func TestHTTPClassificationAndRedirects(t *testing.T) {
	status := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch status {
		case 429:
			w.Header().Set("Retry-After", "7")
		case 302:
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := NewSender(Options{Timeout: 2 * time.Second})
	cases := []struct {
		code      int
		retryable bool
	}{{500, true}, {503, true}, {429, true}, {408, true}, {404, false}, {400, false}, {302, false}}
	for _, c := range cases {
		status = c.code
		res := s.Send(context.Background(), Target{Type: TypeSlack, URL: srv.URL}, sampleEvent(), "")
		if res.OK() || res.Retryable != c.retryable || res.StatusCode != c.code {
			t.Errorf("HTTP %d: %+v", c.code, res)
		}
		if c.code == 429 && res.RetryAfter != 7*time.Second {
			t.Errorf("Retry-After = %v", res.RetryAfter)
		}
	}
	// Transport errors are retryable and never echo the URL (which contains the webhook token).
	res := s.Send(context.Background(), Target{Type: TypeTeams, URL: "http://127.0.0.1:1/hook/SECRET-TOKEN"}, sampleEvent(), "")
	if res.OK() || !res.Retryable || strings.Contains(res.Err.Error(), "SECRET-TOKEN") {
		t.Errorf("connection refused: %+v", res)
	}
}

func TestBlockPrivateDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	s := NewSender(Options{Timeout: 2 * time.Second, BlockPrivate: true})
	res := s.Send(context.Background(), Target{Type: TypeWebhook, URL: srv.URL}, sampleEvent(), "")
	if res.OK() || res.Retryable || !errors.Is(res.Err, ErrBlockedDestination) {
		t.Fatalf("loopback webhook with blocking: %+v", res)
	}
	for _, a := range []string{"10.1.2.3", "192.168.1.1", "172.16.0.1", "127.0.0.1", "169.254.169.254", "::1", "fd00::1", "100.64.1.1", "0.0.0.0", "::ffff:10.0.0.1"} {
		if !blockedAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s not blocked", a)
		}
	}
	for _, a := range []string{"8.8.8.8", "2606:4700::1111"} {
		if blockedAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s blocked", a)
		}
	}
}

func TestSlackAndTeamsPayloads(t *testing.T) {
	ev := sampleEvent()
	sl := SlackMessage(ev)
	b, _ := json.Marshal(sl)
	for _, want := range []string{`"type":"header"`, "FIRING: High CPU", "host.name=web-1", "Open incident", "https://ol.example/alerts/incidents/inc-1"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("slack payload lacks %q: %s", want, b)
		}
	}
	if strings.Contains(string(b), "alert.severity=") {
		t.Error("internal labels rendered")
	}
	resolvedAt := "2026-09-13T10:05:00Z"
	ev.Event, ev.Incident.State, ev.Incident.ResolvedAt = EventResolved, "resolved", &resolvedAt
	tm, _ := json.Marshal(TeamsMessage(ev))
	for _, want := range []string{"application/vnd.microsoft.card.adaptive", `"type":"AdaptiveCard"`, "RESOLVED: High CPU", `"color":"good"`, "Action.OpenUrl"} {
		if !strings.Contains(string(tm), want) {
			t.Errorf("teams payload lacks %q: %s", want, tm)
		}
	}
}

// fakeSMTP is a minimal SMTP server recording one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	data string
	rcpt []string
	auth bool
}

func startFakeSMTP(t *testing.T, rejectRcpt int) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, rejectRcpt)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) serve(conn net.Conn, rejectRcpt int) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			f.mu.Lock()
			f.auth = true
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			if rejectRcpt != 0 {
				w(strings.TrimSpace(map[int]string{450: "450 mailbox busy", 550: "550 no such user"}[rejectRcpt]))
				continue
			}
			f.mu.Lock()
			f.rcpt = append(f.rcpt, strings.TrimSpace(line[8:]))
			f.mu.Unlock()
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

// selfSignedTLS returns a server TLS config for 127.0.0.1.
func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp-test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// TestEmailStartTLSAndAuth runs a fake SMTP server that requires STARTTLS before AUTH PLAIN.
func TestEmailStartTLSAndAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := selfSignedTLS(t)
	type result struct {
		tls, auth bool
		user      string
		data      string
	}
	done := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var res result
		var c net.Conn = conn
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 tls-fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				done <- res
				return
			}
			cmd := strings.TrimSpace(line)
			up := strings.ToUpper(cmd)
			switch {
			case strings.HasPrefix(up, "EHLO"):
				if res.tls {
					w("250-tls-fake")
					w("250 AUTH PLAIN")
				} else {
					w("250-tls-fake")
					w("250 STARTTLS")
				}
			case up == "STARTTLS":
				w("220 go ahead")
				tc := tls.Server(conn, cfg)
				if err := tc.Handshake(); err != nil {
					done <- res
					return
				}
				c, res.tls = tc, true
				r = bufio.NewReader(c)
			case strings.HasPrefix(up, "AUTH PLAIN"):
				if !res.tls {
					w("530 must issue STARTTLS first")
					continue
				}
				raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
				parts := strings.Split(string(raw), "\x00")
				if len(parts) == 3 && parts[1] == "alerts" && parts[2] == "s3cret" {
					res.auth, res.user = true, parts[1]
					w("235 authenticated")
				} else {
					w("535 bad credentials")
				}
			case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
				if !res.auth {
					w("530 authentication required")
					continue
				}
				w("250 ok")
			case up == "DATA":
				w("354 go")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				res.data = b.String()
				w("250 queued")
			case up == "QUIT":
				w("221 bye")
				done <- res
				return
			default:
				w("250 ok")
			}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	s := NewSender(Options{Timeout: 3 * time.Second})
	target := Target{Type: TypeEmail, To: []string{"ops@example.com"},
		SMTP: &SMTPConfig{Host: "127.0.0.1", Port: port, Username: "alerts", Password: "s3cret", From: "alerts@example.com", TLS: "starttls", InsecureSkipVerify: true}}
	res := s.Send(context.Background(), target, sampleEvent(), "")
	if !res.OK() {
		t.Fatalf("delivery over STARTTLS with auth failed: %+v", res)
	}
	got := <-done
	if !got.tls || !got.auth || got.user != "alerts" || !strings.Contains(got.data, "Subject: [openlog] FIRING critical: High CPU") {
		t.Fatalf("server saw tls=%v auth=%v user=%q data=%q", got.tls, got.auth, got.user, got.data)
	}
	// Without InsecureSkipVerify the self-signed certificate is rejected (no silent downgrade).
	target.SMTP.InsecureSkipVerify = false
	go func() {
		if conn, err := ln.Accept(); err == nil {
			defer conn.Close()
			_, _ = conn.Write([]byte("220 x\r\n"))
			br := bufio.NewReader(conn)
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				switch u := strings.ToUpper(strings.TrimSpace(l)); {
				case strings.HasPrefix(u, "EHLO"):
					_, _ = conn.Write([]byte("250-x\r\n250 STARTTLS\r\n"))
				case u == "STARTTLS":
					_, _ = conn.Write([]byte("220 go\r\n"))
					_ = tls.Server(conn, cfg).Handshake()
					return
				}
			}
		}
	}()
	if res := s.Send(context.Background(), target, sampleEvent(), ""); res.OK() || !strings.Contains(res.Err.Error(), "certificate") {
		t.Fatalf("untrusted certificate accepted: %+v", res)
	}
}

func TestEmailDelivery(t *testing.T) {
	f := startFakeSMTP(t, 0)
	port := f.ln.Addr().(*net.TCPAddr).Port
	s := NewSender(Options{Timeout: 2 * time.Second, SMTP: SMTPConfig{Host: "127.0.0.1", Port: port, From: "openlog <alerts@example.com>", TLS: "none"}})
	ev := sampleEvent()
	ev.Event, ev.IdempotencyKey = EventResolved, "inc-1:resolved:ch-1"
	res := s.Send(context.Background(), Target{Type: TypeEmail, To: []string{"ops@example.com"}}, ev, "inc-1:opened:ch-1")
	if !res.OK() || res.StatusCode != 250 {
		t.Fatalf("email: %+v", res)
	}
	f.mu.Lock()
	data := f.data
	f.mu.Unlock()
	for _, want := range []string{"Subject: [openlog] RESOLVED critical: High CPU (web-1)", "Message-ID: " + MessageID("inc-1:resolved:ch-1"),
		"In-Reply-To: " + MessageID("inc-1:opened:ch-1"), "multipart/alternative", "text/html", "X-Openlog-Idempotency-Key: inc-1:resolved:ch-1"} {
		if !strings.Contains(data, want) {
			t.Errorf("message lacks %q:\n%s", want, data)
		}
	}
	// Authentication without TLS is refused before connecting.
	s2 := NewSender(Options{Timeout: time.Second, SMTP: SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@example.com", TLS: "none", Username: "u", Password: "p"}})
	if r := s2.Send(context.Background(), Target{Type: TypeEmail, To: []string{"ops@example.com"}}, ev, ""); r.OK() || r.Retryable {
		t.Errorf("plaintext auth allowed: %+v", r)
	}
	// STARTTLS required but not offered: permanent failure.
	s3 := NewSender(Options{Timeout: time.Second, SMTP: SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@example.com", TLS: "starttls"}})
	if r := s3.Send(context.Background(), Target{Type: TypeEmail, To: []string{"ops@example.com"}}, ev, ""); r.OK() || !strings.Contains(r.Err.Error(), "STARTTLS") {
		t.Errorf("missing STARTTLS: %+v", r)
	}
	// Reply codes: 4xx retry, 5xx permanent.
	for code, retry := range map[int]bool{450: true, 550: false} {
		fr := startFakeSMTP(t, code)
		p := fr.ln.Addr().(*net.TCPAddr).Port
		sr := NewSender(Options{Timeout: time.Second, SMTP: SMTPConfig{Host: "127.0.0.1", Port: p, From: "a@example.com", TLS: "none"}})
		r := sr.Send(context.Background(), Target{Type: TypeEmail, To: []string{"ops@example.com"}}, ev, "")
		if r.OK() || r.Retryable != retry || r.StatusCode != code {
			t.Errorf("RCPT %d: %+v", code, r)
		}
	}
}
