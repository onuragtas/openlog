package sso

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// maxIdPResponse bounds discovery documents, JWKS, token responses and SAML metadata.
const maxIdPResponse = 2 << 20

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr reports whether a is a public unicast address (no loopback, private, link-local — cloud metadata
// endpoints — unspecified, multicast or CGNAT address).
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast() &&
		!a.IsInterfaceLocalMulticast() && !a.IsMulticast() && !a.IsUnspecified() && !cgnat.Contains(a)
}

// errBlockedAddress is returned when an IdP URL resolves to a non-public address and private networks are not allowed.
var errBlockedAddress = errors.New("identity provider address is not a public address (OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS)")

// NewHTTPClient returns the client for requests to identity providers. Organization admins choose these URLs,
// so unless allowPrivate every connection is checked after DNS resolution (no DNS rebinding window) and only
// public addresses may be dialled; bodies are limited to 2 MiB and redirects to 5.
func NewHTTPClient(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if !allowPrivate {
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
	tr := &http.Transport{
		Proxy:                 nil, // a proxy would dial on our behalf and bypass the address check
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: limitTransport{tr},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return checkIdPURL(req.URL, allowPrivate)
		},
	}
}

type limitTransport struct{ rt http.RoundTripper }

func (t limitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.rt.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	res.Body = limitedBody{io.LimitReader(res.Body, maxIdPResponse), res.Body}
	return res, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

// checkIdPURL requires https (http only when private networks are allowed, e.g. a Keycloak in the same cluster)
// and rejects credentials in the URL.
func checkIdPURL(u *url.URL, allowPrivate bool) error {
	switch {
	case u == nil || u.Host == "":
		return errors.New("an absolute URL is required")
	case u.User != nil:
		return errors.New("the URL must not contain credentials")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && allowPrivate:
		return nil
	case u.Scheme == "http":
		return errors.New("the URL must use https")
	default:
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
}

// ParseIdPURL validates an IdP URL entered by an administrator.
func ParseIdPURL(raw string, allowPrivate bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2048 {
		return nil, errors.New("the URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if err := checkIdPURL(u, allowPrivate); err != nil {
		return nil, err
	}
	if u.Fragment != "" {
		return nil, errors.New("the URL must not contain a fragment")
	}
	return u, nil
}

// fetch GETs u with the IdP client and returns the body of a 200 response.
func fetch(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return b, nil
}
