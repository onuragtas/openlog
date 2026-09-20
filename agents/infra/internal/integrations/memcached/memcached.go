// Package memcached implements the Memcached integration: the text protocol's `stats` command over TCP or a
// unix socket, emitting the metrics of the OpenTelemetry Collector memcachedreceiver
// (semantic-conventions §6.10).
package memcached

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// maxStatLines bounds one `stats` answer; a healthy server sends about 60 lines.
const maxStatLines = 500

// Integration is the Memcached integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationMemcached }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{
		DefaultPort: 11211,
		UnixSockets: []string{"/run/memcached/memcached.sock", "/var/run/memcached/memcached.sock", "/tmp/memcached.sock"},
	}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	ep := "127.0.0.1:11211"
	if len(inst.Endpoints) > 0 {
		ep = inst.Endpoints[0].Display
	}
	return `# Memcached needs no credentials: the agent sends the text protocol's "stats" command. Make sure the
# server listens on an address the agent can reach (memcached -l 127.0.0.1) and, if SASL is enabled
# (-S), that it is not required for stats. To point the agent at another endpoint, in config.yaml:
integrations:
  memcached:
    instances:
      - match: { endpoint: "` + ep + `" }
        endpoint: "` + ep + `"`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	if ep.Network != "tcp" && ep.Network != "unix" {
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	return &collector{inst: inst, ep: ep}, nil
}

type collector struct {
	inst *integrations.Instance
	ep   integrations.Endpoint
	conn net.Conn
	rd   *bufio.Reader
}

func (c *collector) Close() {
	if c.conn != nil {
		c.conn.Close()
		c.conn, c.rd = nil, nil
	}
}

func (c *collector) dial(ctx context.Context) error {
	d := net.Dialer{Timeout: c.inst.Timeout}
	conn, err := d.DialContext(ctx, c.ep.Network, c.ep.Address)
	if err != nil {
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	c.conn, c.rd = conn, bufio.NewReader(conn)
	return nil
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.conn == nil {
		if err := c.dial(ctx); err != nil {
			return err
		}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Now().Add(c.inst.Timeout))
	}
	stats, err := c.stats(ctx)
	if err != nil {
		c.Close() // a broken connection is re-opened next time
		return err
	}
	Record(b, stats)
	return nil
}

// stats sends `stats` and reads the STAT lines until END.
func (c *collector) stats(_ context.Context) (map[string]string, error) {
	if _, err := c.conn.Write([]byte("stats\r\n")); err != nil {
		return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	out := make(map[string]string, 64)
	for i := 0; i < maxStatLines; i++ {
		line, err := c.rd.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "END":
			if len(out) == 0 {
				return nil, integrations.NotAvailable("the server answered no statistics")
			}
			return out, nil
		case strings.HasPrefix(line, "ERROR"), strings.HasPrefix(line, "CLIENT_ERROR"), strings.HasPrefix(line, "SERVER_ERROR"):
			// SASL turned on for every command answers an error here rather than closing the connection.
			return nil, integrations.NeedsConfiguration("the server refused the stats command ("+line+"); SASL authentication is not supported", true)
		case strings.HasPrefix(line, "STAT "):
			k, v, ok := strings.Cut(line[len("STAT "):], " ")
			if ok {
				out[k] = v
			}
		}
	}
	return nil, fmt.Errorf("stats: more than %d lines", maxStatLines)
}

// Names and units below are the memcachedreceiver's, so a dashboard written for it works unchanged.
// Record emits the metrics of one `stats` answer.
func Record(b *integrations.Batch, stats map[string]string) {
	s := b.Resource()
	if v, ok := num(stats, "uptime"); ok {
		s.SumInt("memcached.uptime", "s", true, v)
		b.SetStartTime(b.Now().Add(-time.Duration(v) * time.Second))
	}
	intSum := func(key, metric, unit string, monotonic bool, attrs ...*commonpb.KeyValue) {
		if v, ok := num(stats, key); ok {
			s.SumInt(metric, unit, monotonic, v, attrs...)
		}
	}
	intSum("bytes", "memcached.bytes", "By", false)
	intSum("curr_connections", "memcached.connections.current", "{connections}", false)
	intSum("total_connections", "memcached.connections.total", "{connections}", true)
	intSum("curr_items", "memcached.current_items", "{items}", false)
	intSum("evictions", "memcached.evictions", "{evictions}", true)
	intSum("threads", "memcached.threads", "{threads}", false)
	intSum("bytes_read", "memcached.network", "By", true, otlputil.Str("direction", "received"))
	intSum("bytes_written", "memcached.network", "By", true, otlputil.Str("direction", "sent"))
	for _, cmd := range []string{"get", "set", "flush", "touch"} {
		intSum("cmd_"+cmd, "memcached.commands", "{commands}", true, otlputil.Str("command", cmd))
	}
	for _, st := range []string{"system", "user"} {
		if v, ok := float(stats, "rusage_"+st); ok {
			s.SumDouble("memcached.cpu.usage", "s", true, v, otlputil.Str("state", st))
		}
	}
	// Hits and misses per operation, and the ratio the receiver derives from them.
	for _, op := range []struct{ name, hit, miss string }{
		{"get", "get_hits", "get_misses"},
		{"increment", "incr_hits", "incr_misses"},
		{"decrement", "decr_hits", "decr_misses"},
		{"delete", "delete_hits", "delete_misses"},
	} {
		hits, okH := num(stats, op.hit)
		misses, okM := num(stats, op.miss)
		if !okH && !okM {
			continue
		}
		s.SumInt("memcached.operations", "{operations}", true, hits, otlputil.Str("operation", op.name), otlputil.Str("type", "hit"))
		s.SumInt("memcached.operations", "{operations}", true, misses, otlputil.Str("operation", op.name), otlputil.Str("type", "miss"))
		if total := hits + misses; total > 0 {
			s.GaugeDouble("memcached.operation_hit_ratio", "%", float64(hits)/float64(total)*100, otlputil.Str("operation", op.name))
		}
	}
}

func num(stats map[string]string, key string) (int64, bool) {
	v, ok := stats[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		// Some counters are unsigned 64-bit and wrap past int64 (bytes_read on a long-lived server).
		if u, uerr := strconv.ParseUint(v, 10, 64); uerr == nil {
			return int64(u & (1<<63 - 1)), true
		}
		return 0, false
	}
	return n, true
}

func float(stats map[string]string, key string) (float64, bool) {
	v, ok := stats[key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}
