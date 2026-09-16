package synthetics

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Error kinds of a failed run. They are LowCardinality in ClickHouse and an attribute of the emitted metrics,
// so a rule can alert on "connection failures" without parsing the message.
const (
	ErrorNone      = ""
	ErrorDNS       = "dns"
	ErrorConnect   = "connect"
	ErrorTLS       = "tls"
	ErrorTimeout   = "timeout"
	ErrorBlocked   = "blocked"   // the address is not public and private networks are not allowed
	ErrorRedirect  = "redirect"  // too many redirects, or a redirect to an unusable URL
	ErrorStatus    = "status"    // the response status is not one of expected_status
	ErrorAssertion = "assertion" // the body assertion failed
	ErrorBody      = "body"      // the response exceeded the size cap or could not be read
	ErrorRequest   = "request"   // the request could not be built or sent
)

// Guard limits of every run; the configuration (OPENLOG_SYNTHETICS_*) overrides the defaults.
const (
	DefaultMaxResponseBytes int64 = 1 << 20
	DefaultMaxRedirects           = 5
)

// errBlockedAddress is returned when a check's address is not public and private networks are not allowed.
var errBlockedAddress = errors.New("the address is not a public address (OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS)")

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr reports whether a is a public unicast address (no loopback, private, link-local — cloud metadata
// endpoints — unspecified, multicast or CGNAT address), like the SSO client (internal/sso/httpclient.go).
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast() &&
		!a.IsInterfaceLocalMulticast() && !a.IsMulticast() && !a.IsUnspecified() && !cgnat.Contains(a)
}

// CheckerOptions configure a Checker.
type CheckerOptions struct {
	// AllowPrivateNetworks lets checks reach private, loopback and link-local addresses
	// (OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS). Members of an organization choose the URLs, so unless it is
	// set every connection is checked after DNS resolution (no DNS rebinding window).
	AllowPrivateNetworks bool
	// MaxResponseBytes caps the body read of one run (default DefaultMaxResponseBytes).
	MaxResponseBytes int64
	// MaxRedirects caps the redirects one run follows (default DefaultMaxRedirects).
	MaxRedirects int
	// UserAgent identifies the checker to the target.
	UserAgent string
	// Transport replaces the guarded transport (tests only).
	Transport http.RoundTripper
	Now       func() time.Time
}

// Checker runs one check and turns the response into a Result. It is safe for concurrent use.
type Checker struct {
	opts CheckerOptions

	once   sync.Once
	client *http.Client
}

// NewChecker creates a checker with the guarded HTTP client.
func NewChecker(o CheckerOptions) *Checker {
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	if o.UserAgent == "" {
		o.UserAgent = "openlog-synthetics"
	}
	return &Checker{opts: o}
}

func (c *Checker) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

// httpClient builds the client on first use: no proxy (a proxy would dial on our behalf and bypass the
// address check), the address guard on every connection, and a redirect cap that re-validates every hop.
func (c *Checker) httpClient() *http.Client {
	c.once.Do(func() {
		rt := c.opts.Transport
		if rt == nil {
			dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			if !c.opts.AllowPrivateNetworks {
				dialer.Control = func(_, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					a, err := netip.ParseAddr(host)
					if err != nil || !publicAddr(a) {
						return errBlockedAddress
					}
					return nil
				}
			}
			rt = &http.Transport{
				Proxy:                 nil,
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				// A check is a fresh request every interval; keeping connections would hide connect and TLS time.
				DisableKeepAlives: true,
				TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			}
		}
		c.client = &http.Client{
			Transport: rt,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= c.opts.MaxRedirects {
					return fmt.Errorf("more than %d redirects", c.opts.MaxRedirects)
				}
				return checkRedirectURL(req.URL)
			},
		}
	})
	return c.client
}

func checkRedirectURL(u *url.URL) error {
	if u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("redirect to an unsupported URL")
	}
	if u.User != nil {
		return errors.New("redirect to a URL with credentials")
	}
	return nil
}

// timings collects the phases of one run through httptrace. Only the first connection is measured: after a
// redirect the following requests open their own connection and would overwrite the timings of the first.
type timings struct {
	mu                  sync.Mutex
	start               time.Time
	dnsStart, dnsDone   time.Time
	connStart, connDone time.Time
	tlsStart, tlsDone   time.Time
	firstByte           time.Time
}

// first records now in dst unless an earlier connection of the same run already filled it.
func (t *timings) first(dst *time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if dst.IsZero() {
		*dst = time.Now()
	}
}

func (t *timings) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { t.first(&t.dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { t.first(&t.dnsDone) },
		ConnectStart:         func(string, string) { t.first(&t.connStart) },
		ConnectDone:          func(string, string, error) { t.first(&t.connDone) },
		TLSHandshakeStart:    func() { t.first(&t.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.first(&t.tlsDone) },
		GotFirstResponseByte: func() { t.first(&t.firstByte) },
	}
}

// ms returns the milliseconds between two trace points, 0 when the phase did not happen (a cached DNS answer,
// a plain http target, a request that failed before the response).
func ms(from, to time.Time) float64 {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0
	}
	return float64(to.Sub(from)) / float64(time.Millisecond)
}

func (t *timings) fill(r *Result, end time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.DNSMs = ms(t.dnsStart, t.dnsDone)
	r.ConnectMs = ms(t.connStart, t.connDone)
	r.TLSMs = ms(t.tlsStart, t.tlsDone)
	r.FirstByteMs = ms(t.start, t.firstByte)
	r.DurationMs = ms(t.start, end)
}

// Run performs one run of the check from location and never returns an error: a failure is a Result with
// Success false and an ErrorKind. The context bounds the whole run (the check's own timeout is applied on
// top of it).
func (c *Checker) Run(ctx context.Context, due Due) Result {
	chk := due.Check
	// The definition is copied onto the result: the stored row and the emitted metrics describe the run
	// without joining PostgreSQL, and keep describing it after the check was edited or deleted.
	r := Result{CheckID: chk.ID, TenantID: due.TenantID, Location: due.Location, Name: chk.Name,
		URL: chk.URL, Method: chk.Method, At: c.now().UTC()}

	rctx, cancel := context.WithTimeout(ctx, chk.Timeout())
	defer cancel()
	t := &timings{start: time.Now()}
	req, err := c.request(httptrace.WithClientTrace(rctx, t.trace()), chk)
	if err != nil {
		t.fill(&r, time.Now())
		return r.fail(ErrorRequest, err.Error())
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		t.fill(&r, time.Now())
		kind, msg := classifyError(rctx, err)
		return r.fail(kind, msg)
	}
	defer res.Body.Close()
	r.StatusCode = res.StatusCode

	// The body is read (and capped) even without an assertion: it measures the response and frees the
	// connection; a body over the cap fails the run instead of being silently truncated.
	body, err := io.ReadAll(io.LimitReader(res.Body, c.opts.MaxResponseBytes+1))
	r.ResponseBytes = int64(len(body))
	end := time.Now()
	t.fill(&r, end)
	switch {
	case err != nil:
		kind, msg := classifyError(rctx, err)
		if kind == ErrorRequest {
			kind = ErrorBody
		}
		return r.fail(kind, msg)
	case int64(len(body)) > c.opts.MaxResponseBytes:
		r.ResponseBytes = c.opts.MaxResponseBytes
		return r.fail(ErrorBody, fmt.Sprintf("the response is larger than %d bytes", c.opts.MaxResponseBytes))
	case !chk.ExpectsStatus(res.StatusCode):
		return r.fail(ErrorStatus, fmt.Sprintf("HTTP %d, expected %s", res.StatusCode, joinInts(chk.ExpectedStatus)))
	}
	if msg := assert(chk.Input, body); msg != "" {
		return r.fail(ErrorAssertion, msg)
	}
	r.Success = true
	return r
}

func (c *Checker) request(ctx context.Context, chk Check) (*http.Request, error) {
	if _, err := ParseTargetURL(chk.URL); err != nil {
		return nil, err
	}
	var body io.Reader
	if chk.HasBody() && chk.Body != "" {
		body = strings.NewReader(chk.Body)
	}
	req, err := http.NewRequestWithContext(ctx, chk.Method, chk.URL, body)
	if err != nil {
		return nil, errors.New("invalid request")
	}
	for k, v := range chk.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	return req, nil
}

// classifyError maps a transport error to an error kind and a message without the URL (the message is shown
// next to the check, which already names its URL).
func classifyError(ctx context.Context, err error) (string, string) {
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	switch {
	case errors.Is(err, errBlockedAddress):
		return ErrorBlocked, errBlockedAddress.Error()
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return ErrorTimeout, "the request timed out"
	case strings.Contains(msg, "redirect"):
		return ErrorRedirect, msg
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return ErrorDNS, "the host could not be resolved"
	}
	var te *tls.CertificateVerificationError
	if errors.As(err, &te) || strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") {
		return ErrorTLS, msg
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return ErrorConnect, msg
	}
	return ErrorRequest, msg
}

// assert evaluates the body assertion and returns the failure message ("" when it holds).
func assert(in Input, body []byte) string {
	switch in.AssertionType {
	case AssertContains:
		if !strings.Contains(string(body), in.AssertionValue) {
			return "the response does not contain " + strconv.Quote(in.AssertionValue)
		}
	case AssertNotContains:
		if strings.Contains(string(body), in.AssertionValue) {
			return "the response contains " + strconv.Quote(in.AssertionValue)
		}
	case AssertJSONPath:
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return "the response is not JSON"
		}
		got, ok := JSONPath(doc, in.AssertionPath)
		switch {
		case !ok:
			return "the response has no " + in.AssertionPath
		case in.AssertionValue != "" && got != in.AssertionValue:
			return in.AssertionPath + " is " + strconv.Quote(got) + ", expected " + strconv.Quote(in.AssertionValue)
		}
	}
	return ""
}

func joinInts(codes []int) string {
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ", ")
}
