package renderer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes bounds a render response (MaxImages PNGs, base64).
const maxResponseBytes = 64 << 20

// Client calls the render API of openlog-renderer (api side).
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewClient returns a client for baseURL (OPENLOG_RENDERER_URL). tlsConfig (optional) carries the CA and client
// certificate for mTLS; timeout bounds a whole call.
func NewClient(baseURL, token string, tlsConfig *tls.Config, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("OPENLOG_RENDERER_URL must be an http(s) URL, got %q", baseURL)
	}
	if len(token) < 32 {
		return nil, errors.New("OPENLOG_RENDERER_TOKEN must be at least 32 characters")
	}
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // internal service: never through an egress proxy
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig
	}
	return &Client{endpoint: strings.TrimRight(baseURL, "/") + "/v1/render", token: token,
		http: &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Render requests the images of a print page.
func (c *Client) Render(ctx context.Context, req Request) (*Response, error) {
	if err := req.Normalize(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("renderer: response too large")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("renderer: HTTP %d: %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("renderer: HTTP %d", resp.StatusCode)
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("renderer: invalid response: %w", err)
	}
	return &out, nil
}
