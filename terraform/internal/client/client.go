// Package client is the HTTP client of the openlog management API (docs/contracts/api.md). It speaks the
// documented authentication (an API key as a Bearer token, the organization in X-Openlog-Org-Id), decodes the
// documented error shape ({"error": {"code", "message"}}) into *Error with the field path the API reports, and
// exposes the CRUD calls the Terraform provider needs.
//
// Layout: client.go (transport, auth, errors), alertrules.go, channels.go, routingrules.go, slos.go and
// dashboards.go (one file per API resource, request and response shapes of internal/api).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxResponseBytes bounds one API response held in memory. Dashboard documents are the largest objects the
// provider reads; the API itself accepts at most 4 MiB of dashboard body (internal/api/dashboards.go).
const maxResponseBytes = 8 << 20

// DefaultTimeout is the per-request timeout when the provider configures none.
const DefaultTimeout = 30 * time.Second

// Config configures a Client.
type Config struct {
	// Endpoint is the base URL of the openlog API, without /api/v1 (e.g. https://openlog.example.com).
	Endpoint string
	// APIKey is sent as "Authorization: Bearer <key>" (ola_…).
	APIKey string
	// OrgID selects the organization for users of several organizations (X-Openlog-Org-Id); empty = the
	// key's own organization.
	OrgID string
	// Timeout bounds one request; zero means DefaultTimeout.
	Timeout time.Duration
	// UserAgent identifies the provider build in the API's access log.
	UserAgent string
	// HTTPClient replaces the default transport (tests).
	HTTPClient *http.Client
}

// Client talks to one openlog API. It is safe for concurrent use.
type Client struct {
	base      string
	key, org  string
	userAgent string
	hc        *http.Client
}

// New validates cfg and builds the client.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if base == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("endpoint %q must be an http(s) URL such as https://openlog.example.com", cfg.Endpoint)
	}
	// A base URL that already carries /api/v1 is the most common misconfiguration; the client appends it.
	base = strings.TrimSuffix(base, "/api/v1")
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("api_key is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// Copy so a caller-supplied client keeps its transport but gets the configured timeout.
	c := *hc
	c.Timeout = timeout
	ua := cfg.UserAgent
	if ua == "" {
		ua = "terraform-provider-openlog"
	}
	return &Client{base: base, key: strings.TrimSpace(cfg.APIKey), org: strings.TrimSpace(cfg.OrgID), userAgent: ua, hc: &c}, nil
}

// Endpoint returns the API base URL (diagnostics).
func (c *Client) Endpoint() string { return c.base }

// Error is a non-2xx answer, carrying the API's error shape (docs/contracts/api.md).
type Error struct {
	Status int
	Code   string
	// Message is the API's message. Validation errors start with the field path
	// ("condition.threshold: required"), which Field repeats on its own.
	Message string
	// Field is the field path of a validation error, empty when the message carries none.
	Field string
	// Method and Path are the request this answers (diagnostics).
	Method, Path string
}

func (e *Error) Error() string {
	return fmt.Sprintf("the openlog API answered %d %s for %s %s: %s", e.Status, e.Code, e.Method, e.Path, e.Message)
}

// NotFound reports whether the object does not exist (or belongs to another organization), which Terraform
// treats as "removed outside Terraform".
func (e *Error) NotFound() bool { return e.Status == http.StatusNotFound }

// Conflict reports a 409: a stale version (optimistic concurrency), a limit reached, or a precondition such as
// a missing OPENLOG_SECRETS_KEY.
func (e *Error) Conflict() bool { return e.Status == http.StatusConflict }

// Invalid reports a 400 invalid_argument, whose Field names the offending input.
func (e *Error) Invalid() bool { return e.Status == http.StatusBadRequest }

// Forbidden reports a 403. Writes need a credential the API accepts for writes; today's read-only API keys are
// refused here (docs/operations/terraform.md).
func (e *Error) Forbidden() bool { return e.Status == http.StatusForbidden }

// fieldRe matches the "field.path: message" prefix the API puts in front of validation messages
// (internal/alert ValidationError, internal/slo ValidationError): a dotted path, optionally indexed.
var fieldRe = regexp.MustCompile(`^([a-z][a-zA-Z0-9_]*(?:\[[0-9]+\])?(?:\.[a-zA-Z0-9_.\-]+(?:\[[0-9]+\])?)*): `)

// get reads path into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post sends body and decodes the answer into out.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// put replaces the object at path.
func (c *Client) put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

// delete removes the object at path (204, no body).
func (c *Client) delete(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request body: %w", err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	// The key travels as a Bearer token exactly as for any other API client; the API resolves the
	// organization (and with it the tenant) from the credential, never from the request body.
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.org != "" {
		req.Header.Set("X-Openlog-Org-Id", c.org)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the openlog API at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading the response of %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseError(resp.StatusCode, data, method, path)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("the openlog API answered %s %s with a body this provider cannot read (is %s the API base URL?): %w",
			method, path, c.base, err)
	}
	return nil
}

// parseError maps a non-2xx body to *Error, keeping the API's own code, message and field path.
func parseError(status int, body []byte, method, path string) error {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	e := &Error{Status: status, Code: "internal", Message: strings.TrimSpace(http.StatusText(status)), Method: method, Path: path}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Code != "" {
		e.Code, e.Message = parsed.Error.Code, parsed.Error.Message
	}
	if m := fieldRe.FindStringSubmatch(e.Message); m != nil {
		e.Field = m[1]
	}
	return e
}

// AsError returns err as *Error when the API answered, so callers can branch on the status.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ambiguousName is the error a data source reports when a name matches several objects.
func ambiguousName(kind, name string, n int) error {
	return fmt.Errorf("%d %ss of this organization are named %q; look the one you mean up by id instead", n, kind, name)
}
