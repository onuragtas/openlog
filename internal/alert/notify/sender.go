package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// Channel types.
const (
	TypeSlack     = "slack"
	TypeTeams     = "teams"
	TypeWebhook   = "webhook"
	TypeEmail     = "email"
	TypePagerDuty = "pagerduty"
	TypeOpsgenie  = "opsgenie"
)

// SMTPConfig is an SMTP server (global OPENLOG_SMTP_* or a channel override).
type SMTPConfig struct {
	Host               string
	Port               int
	Username           string
	Password           string
	From               string
	TLS                string // starttls (default), tls, none
	InsecureSkipVerify bool
}

// Target is a decrypted channel ready for delivery.
type Target struct {
	Type       string
	URL        string
	HMACSecret string
	To         []string
	SMTP       *SMTPConfig // channel override; nil = global server
	// Key is the PagerDuty integration key or the Opsgenie API key of an on-call channel.
	Key       string
	PagerDuty *PagerDutyConfig
	Opsgenie  *OpsgenieConfig
}

// Result is the outcome of one delivery attempt.
type Result struct {
	StatusCode int // HTTP status or SMTP reply code (0 = no response)
	Err        error
	Retryable  bool
	RetryAfter time.Duration
	Duration   time.Duration
}

// OK reports success.
func (r Result) OK() bool { return r.Err == nil }

// Options configure a Sender.
type Options struct {
	Timeout      time.Duration
	BlockPrivate bool
	UserAgent    string
	SMTP         SMTPConfig
}

// Sender delivers notifications to every channel type.
type Sender struct {
	opts   Options
	client *http.Client
	dialer *net.Dialer
	now    func() time.Time
	// Base URLs of the on-call providers; empty = the public endpoint of the channel's region.
	pagerDutyURL string
	opsgenieURL  string
}

// ErrBlockedDestination is returned when OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS refuses an address.
var ErrBlockedDestination = errors.New("destination address is not allowed (private, loopback or link-local)")

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blockedAddr reports addresses refused when private destinations are blocked.
func blockedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

// NewSender creates a sender. With BlockPrivate, every connection (HTTP and SMTP) is checked at dial time, after
// DNS resolution, so DNS rebinding cannot reach internal addresses.
func NewSender(o Options) *Sender {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = "openlog-alert"
	}
	d := &net.Dialer{Timeout: o.Timeout, KeepAlive: 30 * time.Second}
	if o.BlockPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return ErrBlockedDestination
			}
			if blockedAddr(ap.Addr()) {
				return ErrBlockedDestination
			}
			return nil
		}
	}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   o.Timeout,
		ResponseHeaderTimeout: o.Timeout,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if !o.BlockPrivate {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &Sender{
		opts:   o,
		dialer: d,
		now:    time.Now,
		client: &http.Client{
			Transport: tr,
			Timeout:   o.Timeout,
			// Redirects are not followed: they could leak signed bodies to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// SetClock overrides the clock (tests).
func (s *Sender) SetClock(now func() time.Time) { s.now = now }

// SetProviderEndpoints overrides the PagerDuty and Opsgenie base URLs (tests).
func (s *Sender) SetProviderEndpoints(pagerDuty, opsgenie string) {
	s.pagerDutyURL, s.opsgenieURL = pagerDuty, opsgenie
}

// Send delivers ev to t. threadKey is the idempotency key of the opening notification (e-mail threading).
func (s *Sender) Send(ctx context.Context, t Target, ev Event, threadKey string) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout+2*time.Second)
	defer cancel()
	var r Result
	switch t.Type {
	case TypeSlack:
		r = s.postJSON(ctx, t.URL, SlackMessage(ev), nil)
	case TypeTeams:
		r = s.postJSON(ctx, t.URL, TeamsMessage(ev), nil)
	case TypeWebhook:
		r = s.sendWebhook(ctx, t, ev)
	case TypeEmail:
		r = s.sendEmail(ctx, t, ev, threadKey)
	case TypePagerDuty:
		r = s.sendPagerDuty(ctx, t, ev)
	case TypeOpsgenie:
		r = s.sendOpsgenie(ctx, t, ev)
	default:
		r = Result{Err: fmt.Errorf("unknown channel type %q", t.Type)}
	}
	r.Duration = time.Since(start)
	return r
}

// classifyHTTP maps a response status to a result.
func classifyHTTP(resp *http.Response, now time.Time) Result {
	r := Result{StatusCode: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return r
	}
	r.Err = fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooEarly,
		resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		r.Retryable = true
		r.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), now)
	}
	return r
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return min(time.Duration(secs)*time.Second, time.Hour)
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return min(t.Sub(now), time.Hour)
	}
	return 0
}

// networkResult classifies transport errors: blocked destinations are permanent, everything else is retried.
func networkResult(err error) Result {
	if errors.Is(err, ErrBlockedDestination) {
		return Result{Err: ErrBlockedDestination}
	}
	return Result{Err: err, Retryable: true}
}
