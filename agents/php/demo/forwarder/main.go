// Command forwarder is the standalone PHP agent forwarder for the openlog PHP demo: it receives v1 messages from the
// openlog PHP extension on a unix datagram socket (and optionally UDP), reassembles split messages, converts them to
// OTLP spans and exports batched OTLP/HTTP protobuf to openlog (docs/contracts/php-agent.md §1, §2, §6).
//
// The decoding, reassembly and conversion files are copies of agents/infra/internal/phpforwarder so the demo behaves
// exactly like the infra agent module; this file replaces the infra agent's pipeline with a small exporter.
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const agentVersion = "0.1.0-demo"

type config struct {
	socket        string
	socketMode    fs.FileMode
	udp           string
	endpoint      string
	licenseKey    string
	timeout       time.Duration
	flushInterval time.Duration
	maxPending    int
	maxBatchBytes int
	debug         int // 0 off, 1 message + span summaries, 2 also raw JSON
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		socket:     env("OPENLOG_PHP_SOCKET", "/run/openlog-infra-agent/php.sock"),
		udp:        os.Getenv("OPENLOG_PHP_UDP"),
		endpoint:   strings.TrimRight(env("OPENLOG_ENDPOINT", "http://openlog:4318"), "/") + "/v1/traces",
		licenseKey: os.Getenv("OPENLOG_LICENSE_KEY"),
	}
	if c.socket == "off" {
		c.socket = ""
	}
	mode, err := strconv.ParseUint(env("OPENLOG_PHP_SOCKET_MODE", "0666"), 8, 32)
	if err != nil {
		return c, fmt.Errorf("OPENLOG_PHP_SOCKET_MODE: %w", err)
	}
	c.socketMode = fs.FileMode(mode)
	if c.timeout, err = time.ParseDuration(env("OPENLOG_PHP_REASSEMBLY_TIMEOUT", "5s")); err != nil {
		return c, fmt.Errorf("OPENLOG_PHP_REASSEMBLY_TIMEOUT: %w", err)
	}
	if c.flushInterval, err = time.ParseDuration(env("OPENLOG_FLUSH_INTERVAL", "1s")); err != nil {
		return c, fmt.Errorf("OPENLOG_FLUSH_INTERVAL: %w", err)
	}
	if c.maxPending, err = strconv.Atoi(env("OPENLOG_PHP_MAX_PENDING_TRACES", "10000")); err != nil {
		return c, fmt.Errorf("OPENLOG_PHP_MAX_PENDING_TRACES: %w", err)
	}
	if c.maxBatchBytes, err = strconv.Atoi(env("OPENLOG_BATCH_BYTES", "1048576")); err != nil {
		return c, fmt.Errorf("OPENLOG_BATCH_BYTES: %w", err)
	}
	switch v := strings.ToLower(os.Getenv("OPENLOG_DEBUG_DUMP")); v {
	case "", "0", "false", "off":
	case "2", "raw":
		c.debug = 2
	default:
		c.debug = 1
	}
	return c, nil
}

// hostResourceAttrs are the attributes the forwarder adds to every PHP resource (the extension cannot set them).
func hostResourceAttrs() []*commonpb.KeyValue {
	name := os.Getenv("OPENLOG_HOST_NAME")
	if name == "" {
		name, _ = os.Hostname()
	}
	attrs := []*commonpb.KeyValue{strKV("host.name", name)}
	id := os.Getenv("OPENLOG_HOST_ID")
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/host/etc/machine-id"} {
		if id != "" {
			break
		}
		if b, err := os.ReadFile(p); err == nil {
			id = strings.TrimSpace(string(b))
		}
	}
	if id != "" {
		attrs = append(attrs, strKV("host.id", id))
	}
	return append(attrs, strKV("os.type", "linux"),
		strKV("openlog.agent.name", "openlog-php-forwarder"), strKV("openlog.agent.version", agentVersion))
}

// counters are the forwarder's self-telemetry (logged every 10 s).
type counters struct {
	accepted, malformed, unsupported, dropped atomic.Int64
	spans, timeouts, queueDropSpans           atomic.Int64
	exportedSpans, exportFailedSpans          atomic.Int64
	payloads, exportRetries                   atomic.Int64
	pending                                   atomic.Int64
}

type forwarder struct {
	cfg   config
	stats *counters
	now   func() time.Time
	emit  func(td *tracepb.TracesData, spans int)
	dump  io.Writer // nil unless debug

	mu    sync.Mutex
	asm   *assembler
	batch *batcher
}

func newForwarder(cfg config, host []*commonpb.KeyValue, st *counters, emit func(*tracepb.TracesData, int)) *forwarder {
	f := &forwarder{cfg: cfg, stats: st, now: time.Now, emit: emit,
		asm: newAssembler(cfg.timeout, cfg.maxPending), batch: newBatcher(host, cfg.maxBatchBytes)}
	if cfg.debug > 0 {
		f.dump = os.Stdout
	}
	return f
}

// handle processes one datagram.
func (f *forwarder) handle(b []byte) {
	m, err := decode(b)
	switch {
	case errors.Is(err, errUnsupportedVersion):
		f.stats.unsupported.Add(1)
		f.debugf("DROP unsupported_version bytes=%d\n", len(b))
		return
	case err != nil:
		f.stats.malformed.Add(1)
		f.debugf("DROP malformed bytes=%d error=%q\n", len(b), err.Error())
		if f.cfg.debug > 1 {
			f.debugf("  raw=%s\n", truncate(string(b), 4096))
		}
		return
	}
	if f.dump != nil {
		f.debugf("MSG pid=%d trace=%x seq=%d last=%t spans=%d bytes=%d service=%q sampling=%g function_trace=%t dropped_spans=%d\n",
			m.pid, m.traceID, m.seq, m.last, len(m.spans), m.size, resourceValue(m.resource, "service.name"),
			m.samplingRatio, m.functionTrace, m.droppedSpans)
		if f.cfg.debug > 1 {
			f.debugf("  raw=%s\n", string(b))
		}
	}
	f.mu.Lock()
	t := f.asm.add(m, f.now())
	f.stats.dropped.Add(int64(f.asm.takeDropped()))
	f.stats.pending.Store(int64(f.asm.len()))
	var out []*tracepb.TracesData
	if t != nil {
		out = f.convert([]*trace{t}, false)
	}
	f.mu.Unlock()
	f.send(out)
}

// sweep expires overdue split traces (as incomplete) and, when flush is set, emits the current batch.
func (f *forwarder) sweep(flush bool) {
	f.mu.Lock()
	expired := f.asm.expire(f.now())
	f.stats.timeouts.Add(int64(len(expired)))
	f.stats.pending.Store(int64(f.asm.len()))
	out := f.convert(expired, flush)
	f.mu.Unlock()
	f.send(out)
}

// shutdown emits every pending trace as incomplete plus the open batch.
func (f *forwarder) shutdown() {
	f.mu.Lock()
	out := f.convert(f.asm.flush(), true)
	f.stats.pending.Store(0)
	f.mu.Unlock()
	f.send(out)
}

// convert must be called with f.mu held.
func (f *forwarder) convert(traces []*trace, flush bool) []*tracepb.TracesData {
	var out []*tracepb.TracesData
	for _, t := range traces {
		f.stats.accepted.Add(int64(len(t.parts)))
		c := convertTrace(t)
		f.stats.spans.Add(int64(len(c.spans)))
		f.dumpTrace(t, c)
		out = append(out, f.batch.add(c)...)
	}
	if flush {
		if td := f.batch.take(); td != nil {
			out = append(out, td)
		}
	}
	return out
}

func (f *forwarder) send(out []*tracepb.TracesData) {
	for _, td := range out {
		f.emit(td, SpanCount(td))
	}
}

func (f *forwarder) debugf(format string, args ...any) {
	if f.dump != nil {
		fmt.Fprintf(f.dump, format, args...)
	}
}

func (f *forwarder) dumpTrace(t *trace, c converted) {
	if f.dump == nil {
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "TRACE %x pid=%d parts=%d complete=%t spans=%d service=%q\n", t.parts[0].traceID, t.parts[0].pid,
		len(t.parts), t.complete, len(c.spans), resourceValue(c.resource, "service.name"))
	for _, sp := range c.spans {
		parent := "-"
		if len(sp.ParentSpanId) > 0 {
			parent = fmt.Sprintf("%x", sp.ParentSpanId)
		}
		var events []string
		for _, e := range sp.Events {
			events = append(events, e.Name)
		}
		var marks []string
		for _, kv := range sp.Attributes {
			switch kv.Key {
			case AttrSamplingRatio, AttrDroppedSpans, AttrIncomplete, "http.route", "http.response.status_code",
				"db.system.name", "server.address", "url.full", "openlog.php.segment":
				marks = append(marks, kv.Key+"="+anyString(kv.Value))
			}
		}
		fmt.Fprintf(&sb, "  span %x parent=%s kind=%d status=%d dur=%.3fms attrs=%d events=%v name=%q %s\n",
			sp.SpanId, parent, sp.Kind, sp.Status.GetCode(), float64(sp.EndTimeUnixNano-sp.StartTimeUnixNano)/1e6,
			len(sp.Attributes), events, truncate(sp.Name, 120), strings.Join(marks, " "))
	}
	fmt.Fprint(f.dump, sb.String())
}

func resourceValue(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

func anyString(v *commonpb.AnyValue) string {
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return truncate(x.StringValue, 80)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	}
	return "[...]"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// listenUnix creates the socket directory (0755), replaces a stale socket, binds and applies the mode.
func listenUnix(path string, mode fs.FileMode) (*net.UnixConn, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("socket directory: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		_ = os.Remove(path) // stale socket of a previous forwarder; senders use unconnected sendto
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	_ = conn.SetReadBuffer(4 << 20)
	return conn, nil
}

func listenUDP(addr string) (*net.UDPConn, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(4 << 20)
	return conn, nil
}

func readLoop(ctx context.Context, conn net.Conn, f *forwarder) {
	buf := make([]byte, MaxDatagramBytes+1) // a longer datagram is truncated to len(buf) and rejected as malformed
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Debug("read failed", "error", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		f.handle(buf[:n])
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	st := &counters{}
	exp := newExporter(cfg.endpoint, cfg.licenseKey, st)
	f := newForwarder(cfg, hostResourceAttrs(), st, exp.enqueue)

	var conns []net.Conn
	if cfg.socket != "" {
		c, err := listenUnix(cfg.socket, cfg.socketMode)
		if err != nil {
			slog.Error("unix listener", "socket", cfg.socket, "error", err)
		} else {
			conns = append(conns, c)
		}
	}
	if cfg.udp != "" {
		c, err := listenUDP(cfg.udp)
		if err != nil {
			slog.Error("udp listener", "addr", cfg.udp, "error", err)
		} else {
			conns = append(conns, c)
		}
	}
	if len(conns) == 0 {
		slog.Error("no listener could be started")
		os.Exit(1)
	}

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() { readLoop(ctx, c, f) })
	}
	exportDone := make(chan struct{})
	go func() { exp.run(); close(exportDone) }()

	slog.Info("openlog PHP forwarder listening", "version", agentVersion, "socket", cfg.socket,
		"socket_mode", fmt.Sprintf("%#o", cfg.socketMode), "udp", cfg.udp, "endpoint", cfg.endpoint,
		"license_key_set", cfg.licenseKey != "", "debug_dump", cfg.debug)

	sweepEvery := min(cfg.flushInterval, max(cfg.timeout/5, 50*time.Millisecond))
	sweepT := time.NewTicker(sweepEvery)
	statsT := time.NewTicker(10 * time.Second)
	lastFlush := time.Now()
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case now := <-sweepT.C:
			flush := now.Sub(lastFlush) >= cfg.flushInterval
			if flush {
				lastFlush = now
			}
			f.sweep(flush)
		case <-statsT.C:
			logStats(st)
		}
	}

	for _, c := range conns {
		_ = c.Close()
	}
	wg.Wait()
	if cfg.socket != "" {
		_ = os.Remove(cfg.socket)
	}
	f.shutdown()
	exp.close()
	select {
	case <-exportDone:
	case <-time.After(10 * time.Second):
		slog.Warn("export queue not drained before shutdown")
	}
	logStats(st)
}

func logStats(st *counters) {
	slog.Info("stats",
		"messages_accepted", st.accepted.Load(), "messages_malformed", st.malformed.Load(),
		"messages_unsupported_version", st.unsupported.Load(), "messages_dropped", st.dropped.Load(),
		"spans", st.spans.Load(), "reassembly_timeouts", st.timeouts.Load(), "pending_traces", st.pending.Load(),
		"payloads", st.payloads.Load(), "spans_exported", st.exportedSpans.Load(),
		"spans_export_failed", st.exportFailedSpans.Load(), "export_retries", st.exportRetries.Load(),
		"spans_queue_dropped", st.queueDropSpans.Load())
}
