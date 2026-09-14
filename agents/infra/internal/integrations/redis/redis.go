// Package redis implements the Redis integration: INFO over TCP, TLS or a unix
// socket with optional AUTH (password or ACL user), emitting the metrics of the
// OpenTelemetry Collector redisreceiver (semantic-conventions §6.4).
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// Integration is the Redis integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationRedis }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{
		DefaultPort: 6379,
		SkipPort:    func(p int) bool { return p == 16379 }, // cluster bus
		UnixSockets: []string{"/run/redis/redis-server.sock", "/run/redis/redis.sock", "/var/run/redis/redis-server.sock", "/var/run/redis/redis.sock", "/tmp/redis.sock"},
	}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	ep := "127.0.0.1:6379"
	if len(inst.Endpoints) > 0 {
		ep = inst.Endpoints[0].Display
	}
	return `# Redis requires AUTH. Enter the password (and the ACL user, if any) in openlog
# (host → Integrations → Redis), or in config.yaml:
integrations:
  redis:
    instances:
      - match: { endpoint: "` + ep + `" }
        username: openlog            # optional ACL user (Redis 6+)
        password: env:OPENLOG_REDIS_PASSWORD   # or file:/etc/openlog-infra-agent/redis.password`
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
	if t := c.inst.Settings.TLS; t != nil && t.Enabled {
		cfg := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName}
		if cfg.ServerName == "" && c.ep.Network == "tcp" {
			cfg.ServerName = c.ep.Host()
		}
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				conn.Close()
				return integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			cfg.RootCAs = pool
		}
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("tls handshake: %w", err)
		}
		conn = tc
	}
	c.conn, c.rd = conn, bufio.NewReader(conn)
	password, err := c.inst.Password()
	if err != nil {
		c.Close()
		return integrations.NeedsConfiguration("password: "+err.Error(), false)
	}
	if password != "" {
		args := []string{"AUTH", password}
		if u := c.inst.Settings.Username; u != "" {
			args = []string{"AUTH", u, password}
		}
		if _, err := c.do(ctx, args...); err != nil {
			c.Close()
			return fmt.Errorf("authentication failed: %w", err)
		}
	}
	return nil
}

// ServerError is a RESP error reply.
type ServerError string

func (e ServerError) Error() string { return string(e) }

func (c *collector) do(ctx context.Context, args ...string) (string, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Now().Add(c.inst.Timeout))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(c.conn, sb.String()); err != nil {
		return "", err
	}
	return ReadReply(c.rd)
}

// ReadReply reads a simple string, error, integer or bulk string reply.
func ReadReply(rd *bufio.Reader) (string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", ServerError(line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n > 16<<20 {
			return "", fmt.Errorf("redis: bad bulk length %q", line)
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", fmt.Errorf("redis: unexpected reply type %q", line[0])
}

func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.conn == nil {
		if err := c.dial(ctx); err != nil {
			return classify(err, c.inst)
		}
	}
	body, err := c.do(ctx, "INFO")
	if err != nil {
		c.Close()
		return classify(err, c.inst)
	}
	info := ParseInfo(body)
	if err := Record(b, info); err != nil {
		return err
	}
	if info["cluster_enabled"] != "1" {
		return nil
	}
	return c.collectCluster(ctx, b)
}

// collectCluster adds CLUSTER INFO metrics and the node id (CLUSTER NODES).
// Failures (e.g. an ACL user without +cluster) make the collection partial.
func (c *collector) collectCluster(ctx context.Context, b *integrations.Batch) error {
	ci, err := c.do(ctx, "CLUSTER", "INFO")
	if err != nil {
		return c.clusterError(err)
	}
	nodes, nerr := c.do(ctx, "CLUSTER", "NODES")
	if nerr != nil {
		nodes = ""
	}
	RecordCluster(b, ParseInfo(ci), nodes)
	if nerr != nil {
		return c.clusterError(nerr)
	}
	return nil
}

func (c *collector) clusterError(err error) error {
	var se ServerError
	switch {
	case !errors.As(err, &se):
		c.Close() // the connection state is unknown; reconnect next time
		return integrations.Partial(fmt.Errorf("cluster metrics: %v", err))
	case strings.HasPrefix(string(se), "NOPERM"):
		return integrations.Partial(fmt.Errorf("cluster metrics: permission denied (the ACL user needs +cluster|info and +cluster|nodes): %s", se))
	}
	return integrations.Partial(fmt.Errorf("cluster metrics: %s", se))
}

// clusterMetrics maps CLUSTER INFO fields to redisreceiver metrics (names of
// its metadata.yaml; the receiver itself does not run CLUSTER INFO).
var clusterMetrics = []struct {
	field, name, unit string
	sum               bool
}{
	{"cluster_slots_assigned", "redis.cluster.slots_assigned", "{slot}", false},
	{"cluster_slots_ok", "redis.cluster.slots_ok", "{slot}", false},
	{"cluster_slots_pfail", "redis.cluster.slots_pfail", "{slot}", false},
	{"cluster_slots_fail", "redis.cluster.slots_fail", "{slot}", false},
	{"cluster_known_nodes", "redis.cluster.known_nodes", "{node}", false},
	{"cluster_size", "redis.cluster.node.count", "{node}", false},
	{"cluster_stats_messages_sent", "redis.cluster.stats_messages_sent", "{message}", true},
	{"cluster_stats_messages_received", "redis.cluster.stats_messages_received", "{message}", true},
	{"total_cluster_links_buffer_limit_exceeded", "redis.cluster.links_buffer_limit_exceeded.count", "{count}", true},
}

// RecordCluster emits cluster metrics from CLUSTER INFO fields and sets the
// resource attribute redis.cluster.node.id from a CLUSTER NODES reply ("" skips it).
func RecordCluster(b *integrations.Batch, ci map[string]string, nodes string) {
	s := b.Resource()
	if st, ok := ci["cluster_state"]; ok {
		v, label := int64(0), "fail"
		if st == "ok" {
			v, label = 1, "ok"
		}
		s.GaugeInt("redis.cluster.state", "{state}", v, otlputil.Str("cluster_state", label))
	}
	for _, m := range clusterMetrics {
		n, err := strconv.ParseInt(ci[m.field], 10, 64)
		if err != nil {
			continue
		}
		if m.sum {
			s.SumInt(m.name, m.unit, true, n)
		} else {
			s.GaugeInt(m.name, m.unit, n)
		}
	}
	if id := MyClusterNodeID(nodes); id != "" {
		b.SetResourceAttr(otlputil.Str("redis.cluster.node.id", id))
	}
}

// MyClusterNodeID returns the id of the "myself" line of a CLUSTER NODES reply.
func MyClusterNodeID(nodes string) string {
	for _, line := range strings.Split(nodes, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && slices.Contains(strings.Split(f[2], ","), "myself") {
			return f[0]
		}
	}
	return ""
}

func classify(err error, inst *integrations.Instance) error {
	var se ServerError
	if !errors.As(err, &se) {
		return err
	}
	msg := string(se)
	switch {
	case strings.HasPrefix(msg, "NOAUTH"):
		if inst.Settings.Password == "" {
			return integrations.NeedsConfiguration("authentication required (NOAUTH): configure integrations.redis.password", false)
		}
		return fmt.Errorf("authentication required (NOAUTH)")
	case strings.HasPrefix(msg, "WRONGPASS"), strings.Contains(msg, "invalid password"), strings.Contains(msg, "without any password configured"):
		return fmt.Errorf("authentication failed: %s", msg)
	case strings.HasPrefix(msg, "NOPERM"):
		return fmt.Errorf("permission denied (the ACL user needs +info): %s", msg)
	}
	return err
}

// ParseInfo parses an INFO reply into key/value pairs.
func ParseInfo(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}

type kind int

const (
	sumInt kind = iota
	sumIntNonMono
	gaugeInt
	gaugeDouble
)

type metricDef struct {
	name, unit string
	kind       kind
}

// infoMetrics maps INFO fields to redisreceiver metrics (receiver dataPointRecorders).
var infoMetrics = map[string]metricDef{
	"blocked_clients":                 {"redis.clients.blocked", "{client}", sumIntNonMono},
	"client_recent_max_input_buffer":  {"redis.clients.max_input_buffer", "By", gaugeInt},
	"client_recent_max_output_buffer": {"redis.clients.max_output_buffer", "By", gaugeInt},
	"connected_clients":               {"redis.clients.connected", "{client}", sumIntNonMono},
	"connected_slaves":                {"redis.slaves.connected", "{replica}", sumIntNonMono},
	"evicted_keys":                    {"redis.keys.evicted", "{key}", sumInt},
	"expired_keys":                    {"redis.keys.expired", "{event}", sumInt},
	"instantaneous_ops_per_sec":       {"redis.commands", "{ops}/s", gaugeInt},
	"keyspace_hits":                   {"redis.keyspace.hits", "{hit}", sumInt},
	"keyspace_misses":                 {"redis.keyspace.misses", "{miss}", sumInt},
	"latest_fork_usec":                {"redis.latest_fork", "us", gaugeInt},
	"master_repl_offset":              {"redis.replication.offset", "By", gaugeInt},
	"maxmemory":                       {"redis.maxmemory", "By", gaugeInt},
	"mem_fragmentation_ratio":         {"redis.memory.fragmentation_ratio", "1", gaugeDouble},
	"rdb_changes_since_last_save":     {"redis.rdb.changes_since_last_save", "{change}", sumIntNonMono},
	"rejected_connections":            {"redis.connections.rejected", "{connection}", sumInt},
	"repl_backlog_first_byte_offset":  {"redis.replication.backlog_first_byte_offset", "By", gaugeInt},
	"slave_repl_offset":               {"redis.replication.replica_offset", "By", gaugeInt},
	"total_commands_processed":        {"redis.commands.processed", "{command}", sumInt},
	"total_connections_received":      {"redis.connections.received", "{connection}", sumInt},
	"total_net_input_bytes":           {"redis.net.input", "By", sumInt},
	"total_net_output_bytes":          {"redis.net.output", "By", sumInt},
	"uptime_in_seconds":               {"redis.uptime", "s", sumInt},
	"used_memory":                     {"redis.memory.used", "By", gaugeInt},
	"used_memory_lua":                 {"redis.memory.lua", "By", gaugeInt},
	"used_memory_peak":                {"redis.memory.peak", "By", gaugeInt},
	"used_memory_rss":                 {"redis.memory.rss", "By", gaugeInt},
}

var cpuStates = map[string]string{
	"used_cpu_sys": "sys", "used_cpu_sys_children": "sys_children", "used_cpu_sys_main_thread": "sys_main_thread",
	"used_cpu_user": "user", "used_cpu_user_children": "user_children", "used_cpu_user_main_thread": "user_main_thread",
}

// Record emits redisreceiver metrics from parsed INFO fields.
func Record(b *integrations.Batch, info map[string]string) error {
	up, err := strconv.ParseInt(info["uptime_in_seconds"], 10, 64)
	if err != nil {
		return fmt.Errorf("INFO reply without uptime_in_seconds")
	}
	b.SetStartTime(b.Now().Add(-time.Duration(up) * time.Second))
	v := info["redis_version"]
	if v == "" {
		v = "unknown"
	}
	b.SetResourceAttr(otlputil.Str("redis.version", v))
	s := b.Resource()
	for _, k := range integrations.SortedKeys(info) {
		val := info[k]
		if d, ok := infoMetrics[k]; ok {
			switch d.kind {
			case gaugeDouble:
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					s.GaugeDouble(d.name, d.unit, f)
				}
			default:
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					continue
				}
				switch d.kind {
				case sumInt:
					s.SumInt(d.name, d.unit, true, n)
				case sumIntNonMono:
					s.SumInt(d.name, d.unit, false, n)
				case gaugeInt:
					s.GaugeInt(d.name, d.unit, n)
				}
			}
			continue
		}
		if state, ok := cpuStates[k]; ok {
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				s.SumDouble("redis.cpu.time", "s", true, f, otlputil.Str("state", state))
			}
		}
	}
	for db := 0; db < 16; db++ {
		ks, ok := info["db"+strconv.Itoa(db)]
		if !ok {
			continue
		}
		fields := map[string]int64{}
		for _, pair := range strings.Split(ks, ",") {
			k, v, _ := strings.Cut(pair, "=")
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				fields[k] = n
			}
		}
		attr := otlputil.Str("db", strconv.Itoa(db))
		s.GaugeInt("redis.db.keys", "{key}", fields["keys"], attr)
		s.GaugeInt("redis.db.expires", "{key}", fields["expires"], attr)
		s.GaugeInt("redis.db.avg_ttl", "ms", fields["avg_ttl"], attr)
	}
	if n, err := strconv.ParseInt(info["cluster_enabled"], 10, 64); err == nil {
		s.GaugeInt("redis.cluster.cluster_enabled", "1", n)
	}
	if role, ok := info["role"]; ok {
		r := "replica"
		if role == "master" {
			r = "primary"
		}
		s.SumInt("redis.role", "{role}", false, 1, otlputil.Str("role", r))
	}
	return nil
}
