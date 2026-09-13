package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// MessageID is the deterministic Message-ID of the notification with idempotency key key.
func MessageID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "<" + hex.EncodeToString(sum[:])[:32] + "@openlog>"
}

// EmailMessage renders the RFC 5322 message (multipart/alternative) for ev.
func EmailMessage(ev Event, from string, to []string, threadKey string, now time.Time) []byte {
	prefix, _ := title(ev)
	subject := fmt.Sprintf("[openlog] %s %s: %s", prefix, ev.Rule.Severity, ev.Rule.Name)
	if host := ev.Incident.Labels["host.name"]; host != "" {
		subject += " (" + host + ")"
	}
	if ev.Event == EventTest {
		subject = "[openlog] Test notification"
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.UTC().Format(time.RFC1123Z))
	h("Message-ID", MessageID(ev.IdempotencyKey))
	if threadKey != "" && threadKey != ev.IdempotencyKey {
		h("In-Reply-To", MessageID(threadKey))
		h("References", MessageID(threadKey))
	}
	h("MIME-Version", "1.0")
	h("X-Openlog-Event", ev.Event)
	h("X-Openlog-Idempotency-Key", ev.IdempotencyKey)
	boundary := randomBoundary()
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	lines := []string{summaryOrTest(ev), ""}
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, k+": "+v)
		}
	}
	add("Rule", ev.Rule.Name)
	add("Severity", ev.Rule.Severity)
	add("State", ev.Incident.State)
	if ev.Event != EventTest {
		add("Value", fmtFloat(ev.Incident.Value))
		add("Threshold", fmtFloat(ev.Incident.Threshold))
	}
	add("Labels", sortedLabels(ev.Incident.Labels))
	add("Opened", ev.Incident.OpenedAt)
	if ev.Incident.ResolvedAt != nil {
		add("Resolved", *ev.Incident.ResolvedAt)
	}
	add("Incident", ev.Incident.URL)
	add("Runbook", ev.Rule.RunbookURL)
	add("Organization", ev.Organization.Name)
	text := strings.Join(lines, "\r\n") + "\r\n"

	var rows strings.Builder
	for _, l := range lines[2:] {
		k, v, _ := strings.Cut(l, ": ")
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			v = `<a href="` + html.EscapeString(v) + `">` + html.EscapeString(v) + `</a>`
		} else {
			v = html.EscapeString(v)
		}
		rows.WriteString(`<tr><th align="left" style="padding:4px 12px 4px 0;color:#555">` + html.EscapeString(k) + `</th><td style="padding:4px 0">` + v + "</td></tr>")
	}
	color := "#c0392b"
	if ev.Event == EventResolved {
		color = "#1e8449"
	}
	htmlBody := `<!doctype html><html><body style="font-family:system-ui,sans-serif;font-size:14px">` +
		`<h2 style="color:` + color + `;margin:0 0 8px">` + html.EscapeString(prefix+": "+ev.Rule.Name) + `</h2>` +
		`<p>` + html.EscapeString(summaryOrTest(ev)) + `</p><table>` + rows.String() + `</table></body></html>`

	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ctype + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		_, _ = w.Write([]byte(body))
		_ = w.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", text)
	part("text/html", htmlBody)
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func randomBoundary() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "openlog-" + hex.EncodeToString(buf)
}

// ValidateSMTP checks an SMTP configuration.
func ValidateSMTP(c SMTPConfig) error {
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
		return errors.New("SMTP authentication requires TLS (tls none cannot use a username)")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return errors.New("SMTP from address is invalid")
	}
	return nil
}

func (s *Sender) sendEmail(ctx context.Context, t Target, ev Event, threadKey string) Result {
	cfg := s.opts.SMTP
	if t.SMTP != nil && t.SMTP.Host != "" {
		cfg = *t.SMTP
	}
	if cfg.TLS == "" {
		cfg.TLS = "starttls"
	}
	if err := ValidateSMTP(cfg); err != nil {
		return Result{Err: err}
	}
	if len(t.To) == 0 {
		return Result{Err: errors.New("no recipients")}
	}
	from, _ := mail.ParseAddress(cfg.From)
	msg := EmailMessage(ev, from.String(), t.To, threadKey, s.now())

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := s.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return networkResult(err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in for self-signed test servers
	if cfg.TLS == "tls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return Result{Err: fmt.Errorf("smtp tls: %w", err), Retryable: true}
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return smtpResult(err)
	}
	defer c.Close()
	if cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return Result{Err: errors.New("smtp server does not offer STARTTLS (set tls none only on trusted networks)")}
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return Result{Err: fmt.Errorf("smtp starttls: %w", err), Retryable: true}
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return smtpResult(fmt.Errorf("smtp auth: %w", err))
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return smtpResult(err)
	}
	for _, rcpt := range t.To {
		a, err := mail.ParseAddress(rcpt)
		if err != nil {
			return Result{Err: fmt.Errorf("invalid recipient %q", rcpt)}
		}
		if err := c.Rcpt(a.Address); err != nil {
			return smtpResult(err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpResult(err)
	}
	if _, err := w.Write(msg); err != nil {
		return smtpResult(err)
	}
	if err := w.Close(); err != nil {
		return smtpResult(err)
	}
	_ = c.Quit()
	return Result{StatusCode: 250}
}

// smtpResult retries 4xx replies and network errors; 5xx replies are permanent.
func smtpResult(err error) Result {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		return Result{StatusCode: tp.Code, Err: err, Retryable: tp.Code < 500}
	}
	return Result{Err: err, Retryable: true}
}
