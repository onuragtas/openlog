// Package haproxy implements the HAProxy integration: the CSV statistics of the stats page (`;csv`) over
// HTTP or of the runtime API (`show stat`) over its unix socket, emitting the metrics of the OpenTelemetry
// Collector haproxyreceiver (semantic-conventions §6.12).
package haproxy

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/httpx"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// MaxProxies bounds the rows one collection stores: a proxy's frontend, its backend and every server are a
// row each, and a busy load balancer has thousands. Past this the rest is dropped and the collection is
// partial, so the cardinality of a metric is a property of the configuration rather than of the fleet.
const MaxProxies = 500

// maxBody bounds the CSV answer.
const maxBody = 8 << 20

// SocketPaths are the usual runtime API sockets, tried when the endpoint is a socket.
var SocketPaths = []string{"/run/haproxy/admin.sock", "/var/run/haproxy/admin.sock", "/run/haproxy.sock", "/var/lib/haproxy/stats"}

// StatsPaths are the stats page paths tried over HTTP, in order.
var StatsPaths = []string{"/;csv", "/stats;csv", "/haproxy?stats;csv", "/haproxy_stats;csv"}

// Integration is the HAProxy integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationHAProxy }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: 8404, UnixSockets: SocketPaths} // 8404 is the documented stats port
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# HAProxy publishes its statistics on a stats page or on the runtime API socket; the agent reads either.
# A stats page (haproxy.cfg):
frontend stats
    bind 127.0.0.1:8404
    stats enable
    stats uri /
# Or the runtime socket, readable by the openlog-agent user:
global
    stats socket /run/haproxy/admin.sock mode 660 group openlog-agent level operator
# Then set the endpoint in openlog (host → Integrations → HAProxy) or in config.yaml:
integrations:
  haproxy:
    endpoint: http://127.0.0.1:8404/;csv      # or unix:/run/haproxy/admin.sock`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	c := &collector{inst: inst, ep: ep}
	switch ep.Network {
	case "unix":
		c.socket = ep.Address
	case "url", "tcp":
		base, err := httpx.BaseURL(inst, ep, 0)
		if err != nil {
			return nil, err
		}
		if ep.Network == "url" {
			base = ep.Address
		}
		c.base = base
		client, err := httpx.Client(inst)
		if err != nil {
			return nil, err
		}
		c.client = client
	default:
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	return c, nil
}

type collector struct {
	inst   *integrations.Instance
	ep     integrations.Endpoint
	client *http.Client
	base   string // stats page base (http)
	url    string // the stats page that answered CSV
	socket string // runtime API socket (unix)
}

func (c *collector) Close() {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	body, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	rows, err := ParseCSV(body)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return integrations.NotAvailable("the statistics answer has no proxies")
	}
	dropped := Record(b, rows)
	if dropped > 0 {
		return integrations.Partial(fmt.Errorf("%d proxies beyond the limit of %d were not stored", dropped, MaxProxies))
	}
	return nil
}

func (c *collector) fetch(ctx context.Context) ([]byte, error) {
	if c.socket != "" {
		return c.showStat(ctx)
	}
	if c.url != "" {
		return httpx.Get(ctx, c.client, c.inst, c.url, maxBody)
	}
	// The configured URL may already be the CSV page; otherwise the usual paths are tried once.
	candidates := []string{c.base}
	if !strings.Contains(c.base, "csv") {
		candidates = candidates[:0]
		for _, p := range StatsPaths {
			candidates = append(candidates, strings.TrimSuffix(c.base, "/")+p)
		}
	}
	var lastErr error
	for _, u := range candidates {
		body, err := httpx.Get(ctx, c.client, c.inst, u, maxBody)
		if err != nil {
			lastErr = err
			if integrations.IsUnreachable(err) {
				return nil, err
			}
			continue
		}
		if _, perr := ParseCSV(body); perr != nil {
			lastErr = perr
			continue
		}
		c.url = u
		c.inst.Log.Info("haproxy statistics found", "url", u)
		return body, nil
	}
	if lastErr == nil {
		lastErr = integrations.NeedsConfiguration("no CSV statistics page found; set the endpoint to the stats page (…/;csv) or the runtime socket", false)
	}
	return nil, lastErr
}

// showStat reads the runtime API's `show stat`, which answers the same CSV as the stats page.
func (c *collector) showStat(ctx context.Context) ([]byte, error) {
	d := net.Dialer{Timeout: c.inst.Timeout}
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(c.inst.Timeout))
	}
	if _, err := conn.Write([]byte("show stat\n")); err != nil {
		return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	body, err := io.ReadAll(io.LimitReader(conn, maxBody))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, integrations.NeedsConfiguration("the runtime socket answered nothing; the socket needs at least level operator", false)
	}
	return body, nil
}

// Row is one CSV record: the columns keyed by their header name.
type Row map[string]string

// ParseCSV reads the `show stat` / `;csv` answer, whose first line is a comment holding the header.
func ParseCSV(body []byte) ([]Row, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, integrations.NotAvailable("empty statistics answer")
	}
	if !strings.HasPrefix(text, "#") {
		return nil, fmt.Errorf("not the HAProxy CSV format: %w", integrations.ErrTryNext)
	}
	text = strings.TrimPrefix(text, "# ")
	text = strings.TrimPrefix(text, "#")
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("statistics CSV: %w: %v", integrations.ErrTryNext, err)
	}
	if len(records) == 0 {
		return nil, integrations.NotAvailable("empty statistics answer")
	}
	header := records[0]
	out := make([]Row, 0, len(records)-1)
	for _, rec := range records[1:] {
		if len(rec) < 2 {
			continue
		}
		row := make(Row, len(header))
		for i, h := range header {
			if i < len(rec) {
				row[strings.TrimSpace(h)] = rec[i]
			}
		}
		if row["pxname"] == "" {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// proxyType maps the CSV `type` column to the receiver's proxy type.
var proxyType = map[string]string{"0": "frontend", "1": "backend", "2": "server", "3": "listener"}

// Record emits the metrics of the parsed rows and returns how many rows were dropped by MaxProxies.
func Record(b *integrations.Batch, rows []Row) (dropped int) {
	for i, row := range rows {
		if i >= MaxProxies {
			return len(rows) - MaxProxies
		}
		typ := proxyType[row["type"]]
		if typ == "" {
			typ = "unknown"
		}
		s := b.Resource(
			otlputil.Str("haproxy.proxy.name", clip(row["pxname"])),
			otlputil.Str("haproxy.service.name", clip(row["svname"])),
			otlputil.Str("haproxy.proxy.type", typ),
		)
		// status is a word (UP, DOWN, OPEN, MAINT, …), sometimes with a suffix ("UP 2/3"): the first word
		// is stored as a state gauge of 1, so "how many backends are DOWN" is a sum over the attribute.
		if f := strings.Fields(row["status"]); len(f) > 0 {
			s.GaugeInt("haproxy.status", "{status}", 1, otlputil.Str("state", strings.ToLower(f[0])))
		}
		intSum := func(col, metric, unit string, monotonic bool, extra ...*commonpb.KeyValue) {
			if v, ok := num(row, col); ok {
				s.SumInt(metric, unit, monotonic, v, extra...)
			}
		}
		gauge := func(col, metric, unit string, extra ...*commonpb.KeyValue) {
			if v, ok := num(row, col); ok {
				s.GaugeInt(metric, unit, v, extra...)
			}
		}
		intSum("stot", "haproxy.sessions.count", "{sessions}", true)
		gauge("scur", "haproxy.sessions.current", "{sessions}")
		gauge("rate", "haproxy.sessions.rate", "{sessions}/s")
		gauge("slim", "haproxy.sessions.limit", "{sessions}")
		intSum("conn_tot", "haproxy.connections.total", "{connections}", true)
		gauge("conn_rate", "haproxy.connections.rate", "{connections}/s")
		intSum("dcon", "haproxy.connections.denied", "{connections}", true)
		intSum("req_tot", "haproxy.requests.total", "{requests}", true)
		gauge("req_rate", "haproxy.requests.rate", "{requests}/s")
		intSum("dreq", "haproxy.requests.denied", "{requests}", true)
		intSum("ereq", "haproxy.requests.errors", "{requests}", true)
		intSum("bin", "haproxy.bytes", "By", true, otlputil.Str("direction", "received"))
		intSum("bout", "haproxy.bytes", "By", true, otlputil.Str("direction", "sent"))
		intSum("econ", "haproxy.connections.errors", "{connections}", true)
		intSum("eresp", "haproxy.responses.errors", "{responses}", true)
		intSum("wretr", "haproxy.server.retries", "{retries}", true)
		intSum("wredis", "haproxy.server.redispatches", "{redispatches}", true)
		intSum("chkfail", "haproxy.health_check.failures", "{checks}", true)
		gauge("qcur", "haproxy.queue.current", "{requests}")
		gauge("qtime", "haproxy.queue.time", "ms")
		gauge("rtime", "haproxy.response.time", "ms")
		gauge("ctime", "haproxy.connect.time", "ms")
		gauge("ttime", "haproxy.session.time", "ms")
		gauge("act", "haproxy.servers", "{servers}", otlputil.Str("state", "active"))
		gauge("bck", "haproxy.servers", "{servers}", otlputil.Str("state", "backup"))
		for _, code := range []string{"1xx", "2xx", "3xx", "4xx", "5xx", "other"} {
			intSum("hrsp_"+code, "haproxy.responses.count", "{responses}", true, otlputil.Str("status_code", code))
		}
	}
	return 0
}

func num(row Row, col string) (int64, bool) {
	v, ok := row[col]
	if !ok || strings.TrimSpace(v) == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// clip bounds a name; HAProxy allows long proxy names and they become metric attributes.
func clip(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max]
}
