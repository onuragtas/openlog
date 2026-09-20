// Package apache implements the Apache HTTP Server integration: the machine-readable mod_status page
// (`/server-status?auto`), emitting the metrics of the OpenTelemetry Collector apachereceiver
// (semantic-conventions §6.11).
//
// Like nginx (§6.3) the agent never edits the server's configuration: it probes the usual status paths on
// the ports discovery found and reports needs_configuration with the snippet to add when none answers.
package apache

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
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// ProbePaths are the mod_status locations tried, in order. `?auto` asks for the key/value form.
var ProbePaths = []string{"/server-status?auto", "/status?auto", "/apache-status?auto", "/httpd-status?auto"}

// maxBody bounds a status page; the auto form of a busy server is a few kilobytes.
const maxBody = 1 << 20

// memoURL remembers the page found for an endpoint across collector restarts (Instance.Memo).
const memoURL = "apache.status."

// Integration is the Apache integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationApache }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: 80}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	port := 80
	if len(inst.Endpoints) > 0 {
		if p := inst.Endpoints[0].Port(); p > 0 {
			port = p
		}
	}
	return `# Apache exposes its metrics through mod_status. Enable the module and the handler on an address the
# agent can reach (a2enmod status on Debian/Ubuntu; the module is built in on RHEL), then reload Apache:
<Location "/server-status">
    SetHandler server-status
    Require local
</Location>
ExtendedStatus On
# Then the agent finds http://127.0.0.1:` + strconv.Itoa(port) + `/server-status?auto by itself, or point it at the page in openlog
# (host → Integrations → Apache) or in config.yaml:
integrations:
  apache:
    endpoint: http://127.0.0.1:` + strconv.Itoa(port) + `/server-status?auto`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	c := &collector{inst: inst}
	switch ep.Network {
	case "url":
		c.url = ep.Address
	case "tcp":
		if !inst.Target.AutoEnable {
			return nil, integrations.NeedsConfiguration("mod_status URL not configured (auto_enable is off for this rule)", true)
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
	url      string // the status page; empty until probed
	hostPort string // probe target when url is empty
}

func (c *collector) Close() { c.client.CloseIdleConnections() }

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.url == "" {
		u, body, err := c.probe(ctx)
		if err != nil {
			return err
		}
		c.url = u
		Record(b, Parse(body))
		return nil
	}
	body, err := c.fetch(ctx, c.url)
	if err != nil {
		return err
	}
	st := Parse(body)
	if len(st) == 0 {
		return fmt.Errorf("GET %s: not a mod_status page (add ?auto): %w", c.url, integrations.ErrTryNext)
	}
	Record(b, st)
	return nil
}

// probe tries the usual status paths on the endpoint, http first and then https, and keeps what answered.
// Certificates are not verified on loopback only: a status page is public information, but a remote
// address keeps verification.
func (c *collector) probe(ctx context.Context) (string, string, error) {
	client := c.client
	httpsClient := c.client
	if isLoopback(c.hostPort) {
		httpsClient = &http.Client{
			Transport:     &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			CheckRedirect: c.client.CheckRedirect,
		}
	}
	key := memoURL + c.hostPort
	if u := c.inst.Memo.Get(key); u != "" {
		if body, err := c.fetch(ctx, u); err == nil && len(Parse(body)) > 0 {
			return u, body, nil
		}
		c.inst.Memo.Set(key, "")
	}
	reachable := false
	for _, scheme := range []string{"http", "https"} {
		cl := client
		if scheme == "https" {
			cl = httpsClient
		}
		for _, p := range ProbePaths {
			u := scheme + "://" + c.hostPort + p
			body, err := fetch(ctx, cl, u)
			if err == nil {
				reachable = true
				if len(Parse(body)) > 0 {
					c.client = cl
					c.inst.Memo.Set(key, u)
					c.inst.Log.Info("apache status page found", "url", u)
					return u, body, nil
				}
				continue
			}
			if !integrations.IsUnreachable(err) {
				reachable = true
			}
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
		}
	}
	if !reachable {
		return "", "", fmt.Errorf("%w: no HTTP answer on %s", integrations.ErrUnreachable, c.hostPort)
	}
	return "", "", integrations.NeedsConfiguration("no mod_status page found on "+c.hostPort+
		" (tried "+strings.Join(ProbePaths, ", ")+"); enable mod_status or set the endpoint", false)
}

func (c *collector) fetch(ctx context.Context, url string) (string, error) {
	return fetch(ctx, c.client, url)
}

func fetch(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			err = ue.Unwrap()
		}
		return "", fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("GET %s: HTTP %s: %w", url, resp.Status, integrations.ErrTryNext)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func isLoopback(hostPort string) bool {
	h, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		h = hostPort
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Parse reads the `?auto` form: one "Key: value" per line. A page without the fields mod_status always
// emits (Total Accesses, Uptime) is not a status page and yields no values.
func Parse(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if _, ok := out["Total Accesses"]; !ok {
		return nil
	}
	if _, ok := out["Uptime"]; !ok {
		return nil
	}
	return out
}

// scoreboardStates maps a scoreboard character to the worker state the receiver reports.
var scoreboardStates = map[byte]string{
	'_': "waiting", 'S': "starting", 'R': "reading", 'W': "sending", 'K': "keepalive",
	'D': "dnslookup", 'C': "closing", 'L': "logging", 'G': "finishing", 'I': "idle_cleanup", '.': "open",
}

// Record emits the metrics of one parsed status page.
func Record(b *integrations.Batch, st map[string]string) {
	if len(st) == 0 {
		return
	}
	s := b.Resource()
	if v := st["ServerVersion"]; v != "" {
		b.SetResourceAttr(otlputil.Str("apache.server.version", v))
	}
	if v, ok := num(st, "Uptime"); ok {
		s.SumInt("apache.uptime", "s", true, v)
		b.SetStartTime(b.Now().Add(-time.Duration(v) * time.Second))
	}
	if v, ok := num(st, "Total Accesses"); ok {
		s.SumInt("apache.requests", "{requests}", true, v)
	}
	if v, ok := num(st, "Total kBytes"); ok {
		s.SumInt("apache.traffic", "By", true, v*1024)
	}
	if v, ok := num(st, "BusyWorkers"); ok {
		s.SumInt("apache.workers", "{workers}", false, v, otlputil.Str("state", "busy"))
	}
	if v, ok := num(st, "IdleWorkers"); ok {
		s.SumInt("apache.workers", "{workers}", false, v, otlputil.Str("state", "idle"))
	}
	if v, ok := num(st, "ConnsTotal"); ok {
		s.SumInt("apache.current_connections", "{connections}", false, v)
	}
	for _, kv := range []struct{ key, state string }{
		{"ConnsAsyncWriting", "writing"}, {"ConnsAsyncKeepAlive", "keep-alive"}, {"ConnsAsyncClosing", "closing"},
	} {
		if v, ok := num(st, kv.key); ok {
			s.SumInt("apache.connections.async", "{connections}", false, v, otlputil.Str("state", kv.state))
		}
	}
	if v, ok := float(st, "CPULoad"); ok {
		s.GaugeDouble("apache.cpu.load", "%", v)
	}
	for _, kv := range []struct{ key, state string }{
		{"CPUUser", "user"}, {"CPUSystem", "system"}, {"CPUChildrenUser", "children_user"}, {"CPUChildrenSystem", "children_system"},
	} {
		if v, ok := float(st, kv.key); ok {
			s.SumDouble("apache.cpu.time", "s", true, v, otlputil.Str("level", levelOf(kv.state)), otlputil.Str("mode", modeOf(kv.state)))
		}
	}
	for i, key := range []string{"Load1", "Load5", "Load15"} {
		if v, ok := float(st, key); ok {
			s.GaugeDouble("apache.load."+[]string{"1", "5", "15"}[i], "%", v)
		}
	}
	if v, ok := num(st, "Total Duration"); ok {
		s.SumInt("apache.request.time", "ms", true, v)
	}
	if sb := st["Scoreboard"]; sb != "" {
		counts := map[string]int64{}
		for i := 0; i < len(sb); i++ {
			state, ok := scoreboardStates[sb[i]]
			if !ok {
				state = "unknown"
			}
			counts[state]++
		}
		for _, state := range integrations.SortedKeys(counts) {
			s.SumInt("apache.scoreboard", "{workers}", false, counts[state], otlputil.Str("state", state))
		}
	}
}

// levelOf and modeOf split the receiver's two attributes out of a CPU counter name.
func levelOf(state string) string {
	if strings.HasPrefix(state, "children_") {
		return "children"
	}
	return "self"
}

func modeOf(state string) string {
	if strings.HasSuffix(state, "system") {
		return "system"
	}
	return "user"
}

func num(st map[string]string, key string) (int64, bool) {
	v, ok := st[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

func float(st map[string]string, key string) (float64, bool) {
	v, ok := st[key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f, err == nil
}
