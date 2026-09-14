// Package mail sends transactional e-mail (invitations, address verification) through the global SMTP server
// (OPENLOG_SMTP_*, the same server alert e-mail channels use; docs/contracts/config.md).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// Config is an SMTP server.
type Config struct {
	Host               string
	Port               int
	Username           string
	Password           string
	From               string // "openlog <noreply@example.com>"
	TLS                string // starttls (default), tls (implicit), none
	InsecureSkipVerify bool   // testing only
	Timeout            time.Duration
}

// Validate checks the configuration.
func (c Config) Validate() error {
	if c.Host == "" {
		return errors.New("SMTP host is not configured")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("SMTP port must be 1-65535")
	}
	switch c.TLS {
	case "", "starttls", "tls", "none":
	default:
		return errors.New("SMTP tls must be starttls, tls or none")
	}
	if c.TLS == "none" && c.Username != "" {
		return errors.New("SMTP authentication requires TLS")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return errors.New("SMTP from address is invalid")
	}
	return nil
}

// Sender implements auth.Mailer.
type Sender struct {
	cfg  Config
	from *mail.Address
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	now  func() time.Time
}

var _ auth.Mailer = (*Sender)(nil)

// New returns a sender for cfg.
func New(cfg Config) (*Sender, error) {
	if cfg.TLS == "" {
		cfg.TLS = "starttls"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	from, _ := mail.ParseAddress(cfg.From)
	d := &net.Dialer{Timeout: cfg.Timeout}
	return &Sender{cfg: cfg, from: from, dial: d.DialContext, now: time.Now}, nil
}

// Message renders an RFC 5322 multipart/alternative message.
func Message(from, to, subject, text, htmlBody string, now time.Time) []byte {
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	boundary := random("openlog-")
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+random("")+"@openlog>")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\nContent-Type: " + ctype + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		_, _ = w.Write([]byte(body))
		_ = w.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", text)
	if htmlBody != "" {
		part("text/html", htmlBody)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func random(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

// Send delivers m (auth.Mailer).
func (s *Sender) Send(ctx context.Context, m auth.Mail) error {
	rcpt, err := mail.ParseAddress(m.To)
	if err != nil || strings.ContainsAny(m.To, "\r\n") {
		return fmt.Errorf("invalid recipient %q", m.To)
	}
	msg := Message(s.from.String(), rcpt.Address, m.Subject, m.Text, m.HTML, s.now())
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.cfg.InsecureSkipVerify} //nolint:gosec // opt-in for self-signed test servers
	if s.cfg.TLS == "tls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if s.cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not offer STARTTLS (set OPENLOG_SMTP_TLS=none only on trusted networks)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	_ = c.Quit()
	return nil
}
