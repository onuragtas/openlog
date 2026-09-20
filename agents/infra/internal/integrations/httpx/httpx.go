// Package httpx is the HTTP client the integrations that read a management API share (HAProxy stats,
// RabbitMQ, Elasticsearch): one client per instance built from its TLS settings, a GET with the instance
// timeout, a body limit and errors classified the way the integration framework expects
// (unreachable → next endpoint candidate, 401/403 → needs_configuration).
package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
)

// DefaultMaxBody bounds one answer. Management APIs answer JSON documents of a few hundred kilobytes; the
// limit is what keeps a cluster-wide listing from becoming the agent's memory profile.
const DefaultMaxBody = 8 << 20

// Client builds the HTTP client of an instance. TLS verification follows the instance settings; nothing is
// skipped implicitly, because unlike a status page a management API carries credentials.
func Client(inst *integrations.Instance) (*http.Client, error) {
	tr := &http.Transport{MaxIdleConns: 2, IdleConnTimeout: 2 * inst.Timeout, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if t := inst.Settings.TLS; t != nil {
		tr.TLSClientConfig.InsecureSkipVerify = t.InsecureSkipVerify
		tr.TLSClientConfig.ServerName = t.ServerName
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return nil, integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, integrations.NeedsConfiguration("tls.ca_file "+t.CAFile+": no PEM certificate", true)
			}
			tr.TLSClientConfig.RootCAs = pool
		}
	}
	return &http.Client{Transport: tr, Timeout: inst.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Scheme returns the URL scheme of an instance: https when TLS is on, http otherwise.
func Scheme(s config.InstanceSettings) string {
	if t := s.TLS; t != nil && t.Enabled {
		return "https"
	}
	return "http"
}

// BaseURL turns an endpoint into the base URL of a management API: a configured http(s) URL is used as it
// is, a host:port endpoint gets the scheme of the instance settings and, when the discovered port is the
// service's own port rather than the API's, apiPort.
func BaseURL(inst *integrations.Instance, ep integrations.Endpoint, apiPort int, servicePorts ...int) (string, error) {
	if raw := strings.TrimSpace(inst.Settings.Endpoint); strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", integrations.NeedsConfiguration("endpoint must be an http(s) URL", true)
		}
		return strings.TrimSuffix(raw, "/"), nil
	}
	if ep.Network != "tcp" {
		return "", fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	host, port := ep.Host(), ep.Port()
	for _, p := range servicePorts {
		if port == p && apiPort > 0 {
			port = apiPort // the discovered port is the service's; its API listens elsewhere
			break
		}
	}
	return Scheme(inst.Settings) + "://" + net.JoinHostPort(host, fmt.Sprint(port)), nil
}

// Get fetches url and returns the body, with the instance credentials as basic auth when set.
func Get(ctx context.Context, client *http.Client, inst *integrations.Instance, target string, maxBody int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if u := inst.Settings.Username; u != "" || inst.Settings.Password != "" {
		pw, err := inst.Settings.Password.Resolve()
		if err != nil {
			return nil, integrations.NeedsConfiguration("password: "+err.Error(), false)
		}
		req.SetBasicAuth(u, pw)
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Err != nil {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch resp.StatusCode {
	case http.StatusOK:
		if readErr != nil {
			return nil, readErr
		}
		return body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, integrations.NeedsConfiguration("authentication failed (HTTP "+resp.Status+"): check the user and password", false)
	case http.StatusNotFound:
		// A wrong port or a disabled API answers this; another endpoint candidate may be the right one.
		return nil, fmt.Errorf("GET %s: HTTP %s: %w", target, resp.Status, integrations.ErrTryNext)
	}
	return nil, fmt.Errorf("GET %s: HTTP %s", target, resp.Status)
}

// GetJSON fetches url and decodes the body into v.
func GetJSON(ctx context.Context, client *http.Client, inst *integrations.Instance, target string, v any, maxBody int64) error {
	body, err := Get(ctx, client, inst, target, maxBody)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: %w: %v", target, integrations.ErrTryNext, err)
	}
	return nil
}
