package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes bounds one API response held in memory (and, through the tool result, a model's context).
const maxResponseBytes = 8 << 20

// Creds are the credentials one tool call acts as: an openlog API key and, for users of several
// organizations, the organization it selects (docs/contracts/api.md "Authentication"). They are the only
// thing that decides which tenant's data a call reads; the server never resolves a tenant itself.
type Creds struct {
	// Key is the API key sent as "Authorization: Bearer <key>".
	Key string
	// OrgID is sent as X-Openlog-Org-Id; empty = the key's organization.
	OrgID string
}

// APIError is a non-2xx answer of the API, carrying its error shape
// (docs/contracts/api.md: {"error": {"code", "message"}}).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "openlog rejected the API key (" + e.Message + "); check OPENLOG_MCP_API_KEY or the Authorization header"
	case http.StatusForbidden:
		return "openlog refused the request (" + e.Message + "); API keys are read-only viewers of their organization"
	}
	return fmt.Sprintf("openlog API error %d (%s): %s", e.Status, e.Code, e.Message)
}

// Client talks to one openlog API. It is safe for concurrent use.
type Client struct {
	base string
	hc   *http.Client
	// key and org are the configured fallback credentials (OPENLOG_MCP_API_KEY, OPENLOG_MCP_ORG_ID).
	key, org string
}

// NewClient creates the API client of cfg.
func NewClient(cfg Config) *Client {
	return &Client{
		base: cfg.APIURL,
		hc:   &http.Client{Timeout: cfg.Timeout},
		key:  cfg.APIKey,
		org:  cfg.OrgID,
	}
}

// Creds completes a request's credentials with the configured fallbacks. An empty result means the caller
// supplied no key and none is configured: the request must be refused instead of being sent unauthenticated.
func (c *Client) Creds(key, org string) Creds {
	if key == "" {
		key, org = c.key, c.org
	}
	if org == "" {
		org = c.org
	}
	return Creds{Key: strings.TrimSpace(key), OrgID: strings.TrimSpace(org)}
}

// Get reads path (e.g. "/api/v1/apm/services") with the query parameters q.
func (c *Client) Get(ctx context.Context, cr Creds, path string, q url.Values) (json.RawMessage, error) {
	return c.do(ctx, cr, http.MethodGet, path, q, nil)
}

// Post sends body as JSON to path.
func (c *Client) Post(ctx context.Context, cr Creds, path string, body any) (json.RawMessage, error) {
	return c.do(ctx, cr, http.MethodPost, path, nil, body)
}

func (c *Client) do(ctx context.Context, cr Creds, method, path string, q url.Values, body any) (json.RawMessage, error) {
	if cr.Key == "" {
		return nil, &APIError{Status: http.StatusUnauthorized, Code: "unauthenticated", Message: "no API key for this call"}
	}
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding the request body: %w", err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	// The key travels as a Bearer token, exactly as for any other API client; the API resolves the
	// organization from it and scopes every ClickHouse query to that tenant.
	req.Header.Set("Authorization", "Bearer "+cr.Key)
	req.Header.Set("Accept", "application/json")
	if cr.OrgID != "" {
		req.Header.Set("X-Openlog-Org-Id", cr.OrgID)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the openlog API at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, data)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("the openlog API answered %d with a body that is not JSON (is %s the API base URL?)", resp.StatusCode, c.base)
	}
	return json.RawMessage(data), nil
}

// apiError maps a non-2xx body to an APIError, keeping the API's own code and message when it has one.
func apiError(status int, body []byte) error {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	e := &APIError{Status: status, Code: "internal", Message: http.StatusText(status)}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Code != "" {
		e.Code, e.Message = parsed.Error.Code, parsed.Error.Message
	}
	return e
}

// Ping checks that the API is reachable. It uses the public GET /api/v1/auth/config, so it needs no key
// and reports the API's health rather than a key's validity (readiness check of the http transport).
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/auth/config", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the openlog API at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the openlog API at %s answered %d", c.base, resp.StatusCode)
	}
	return nil
}
