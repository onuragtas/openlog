package mail

import (
	"bufio"
	"context"
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// fakeSMTP is a minimal plain SMTP server that records one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	rcpt []string
	data string
	done chan struct{}
}

func startFake(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, done: make(chan struct{})}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(f.done)
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
		w("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			up := strings.ToUpper(cmd)
			switch {
			case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
				w("250 fake")
			case strings.HasPrefix(up, "MAIL FROM:"):
				f.mu.Lock()
				f.from = cmd[10:]
				f.mu.Unlock()
				w("250 ok")
			case strings.HasPrefix(up, "RCPT TO:"):
				f.mu.Lock()
				f.rcpt = append(f.rcpt, cmd[8:])
				f.mu.Unlock()
				w("250 ok")
			case up == "DATA":
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
			case up == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return f
}

func TestSendPlain(t *testing.T) {
	f := startFake(t)
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	s, err := New(Config{Host: host, Port: p, From: "openlog <noreply@example.com>", TLS: "none", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), auth.Mail{To: "ada@example.com", Subject: "Davet: Örnek", Text: "hello link https://ol.example/invite#token=oli_x", HTML: "<p>hello</p>"})
	if err != nil {
		t.Fatal(err)
	}
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "<noreply@example.com>" || len(f.rcpt) != 1 || f.rcpt[0] != "<ada@example.com>" {
		t.Fatalf("envelope from=%q rcpt=%q", f.from, f.rcpt)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(f.data)))
	for _, want := range []string{"To: ada@example.com", "Subject: =?utf-8?q?", "multipart/alternative", "https://ol.example/invite#token=oli_x", "<p>hello</p>"} {
		if !strings.Contains(f.data, want) && !strings.Contains(string(body), want) {
			t.Errorf("message lacks %q:\n%s", want, f.data)
		}
	}
}

func TestValidateAndRecipient(t *testing.T) {
	for _, c := range []Config{
		{Port: 25, From: "a@b.c"},
		{Host: "h", Port: 0, From: "a@b.c"},
		{Host: "h", Port: 25, From: "a@b.c", TLS: "ssl"},
		{Host: "h", Port: 25, From: "a@b.c", TLS: "none", Username: "u"},
		{Host: "h", Port: 25, From: "not an address"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("config %+v accepted", c)
		}
	}
	s, err := New(Config{Host: "127.0.0.1", Port: 1, From: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), auth.Mail{To: "x@y.z\r\nBcc: evil@example.com"}); err == nil {
		t.Fatal("header injection in recipient accepted")
	}
}
