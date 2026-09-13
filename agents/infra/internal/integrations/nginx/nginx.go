// Package nginx implements the nginx integration: it reads the stub_status page
// (ngx_http_stub_status_module) and emits the metrics of the OpenTelemetry
// Collector nginxreceiver (semantic-conventions §6.3).
package nginx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// ProbePaths are tried on every discovered port when the rule has auto_enable.
var ProbePaths = []string{"/nginx_status", "/stub_status", "/status", "/basic_status", "/server_status"}

// Integration is the nginx integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationNginx }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: 80}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	port := 80
	for _, e := range inst.Endpoints {
		if e.Network == "tcp" {
			port = e.Port()
			break
		}
	}
	return fmt.Sprintf(`# nginx: expose stub_status on loopback, e.g.
#   server { listen 127.0.0.1:%d; location = /nginx_status { stub_status; allow 127.0.0.1; deny all; } }
integrations:
  nginx:
    instances:
      - match: { port: %d }
        endpoint: http://127.0.0.1:%d/nginx_status`, port, port, port)
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	c := &collector{inst: inst}
	switch ep.Network {
	case "url":
		c.url = ep.Address
	case "tcp":
		if !inst.Target.AutoEnable {
			return nil, integrations.NeedsConfiguration("stub_status URL not configured (auto_enable is off for this rule)", true)
		}
		c.hostPort = ep.Address
	default:
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	tr := &http.Transport{DisableKeepAlives: false, MaxIdleConns: 1, TLSClientConfig: &tls.Config{}}
	if t := inst.Settings.TLS; t != nil {
		tr.TLSClientConfig.InsecureSkipVerify = t.InsecureSkipVerify
		tr.TLSClientConfig.ServerName = t.ServerName
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return nil, integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			tr.TLSClientConfig.RootCAs = pool
		}
	}
	c.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

type collector struct {
	inst     *integrations.Instance
	client   *http.Client
	url      string // known stub_status URL
	hostPort string // probe target when url is empty
}

func (c *collector) Close() { c.client.CloseIdleConnections() }

func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.url == "" {
		u, err := c.probe(ctx)
		if err != nil {
			return err
		}
		c.url = u
	}
	st, err := fetch(ctx, c.client, c.url)
	if err != nil {
		return err
	}
	Record(b, st)
	return nil
}

// probe finds the stub_status page on the endpoint: plain HTTP first, then HTTPS
// without certificate verification (probing only reads the public status format).
func (c *collector) probe(ctx context.Context) (string, error) {
	insecure := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: c.client.CheckRedirect,
	}
	reachable := false
	for _, scheme := range []string{"http", "https"} {
		cl := c.client
		if scheme == "https" {
			cl = insecure
		}
		for _, p := range ProbePaths {
			u := scheme + "://" + c.hostPort + p
			_, err := fetch(ctx, cl, u)
			if err == nil {
				c.inst.Log.Info("nginx stub_status found", "url", u)
				if scheme == "https" {
					c.client = insecure
				}
				return u, nil
			}
			if errors.Is(err, integrations.ErrUnreachable) {
				break // this scheme does not work on the port
			}
			reachable = true
		}
	}
	if !reachable {
		return "", fmt.Errorf("%s: %w", c.hostPort, integrations.ErrUnreachable)
	}
	return "", fmt.Errorf("no stub_status page on %s (tried %s): %w", c.hostPort, strings.Join(ProbePaths, ", "), integrations.ErrTryNext)
}

var errNotStatus = errors.New("response is not a stub_status page")

func fetch(ctx context.Context, client *http.Client, url string) (Stats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Stats{}, err
	}
	req.Header.Set("User-Agent", "openlog-infra-agent")
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") {
			return Stats{}, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
		}
		return Stats{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Stats{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Stats{}, fmt.Errorf("GET %s: HTTP %d: %w", url, resp.StatusCode, errNotStatus)
	}
	st, err := Parse(string(body))
	if err != nil {
		return Stats{}, fmt.Errorf("GET %s: %w", url, err)
	}
	return st, nil
}

// Stats is a parsed stub_status page.
type Stats struct {
	Active, Accepted, Handled, Requests, Reading, Writing, Waiting int64
}

// Parse parses a stub_status body:
//
//	Active connections: 291
//	server accepts handled requests
//	 16630948 16630948 31070465
//	Reading: 6 Writing: 179 Waiting: 106
func Parse(body string) (Stats, error) {
	var st Stats
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 4 {
		return st, errNotStatus
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 || f[0] != "Active" || f[1] != "connections:" {
		return st, errNotStatus
	}
	var err error
	num := func(s string) int64 {
		v, e := strconv.ParseInt(s, 10, 64)
		if e != nil && err == nil {
			err = errNotStatus
		}
		return v
	}
	st.Active = num(f[2])
	f = strings.Fields(lines[2])
	if len(f) != 3 {
		return st, errNotStatus
	}
	st.Accepted, st.Handled, st.Requests = num(f[0]), num(f[1]), num(f[2])
	f = strings.Fields(lines[3])
	if len(f) != 6 || f[0] != "Reading:" || f[2] != "Writing:" || f[4] != "Waiting:" {
		return st, errNotStatus
	}
	st.Reading, st.Writing, st.Waiting = num(f[1]), num(f[3]), num(f[5])
	return st, err
}

// Record emits the nginxreceiver metrics.
func Record(b *integrations.Batch, st Stats) {
	s := b.Resource()
	s.SumInt("nginx.requests", "{requests}", true, st.Requests)
	s.SumInt("nginx.connections_accepted", "{connections}", true, st.Accepted)
	s.SumInt("nginx.connections_handled", "{connections}", true, st.Handled)
	for _, p := range []struct {
		state string
		v     int64
	}{{"active", st.Active}, {"reading", st.Reading}, {"writing", st.Writing}, {"waiting", st.Waiting}} {
		s.SumInt("nginx.connections_current", "{connections}", false, p.v, otlputil.Str("state", p.state))
	}
}
