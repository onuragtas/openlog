// Command openlog-loadgen sends realistic OTLP/HTTP load to openlog-ingest: host
// metrics per the semantic conventions, inventory snapshots, logs and traces.
// It prints the achieved throughput periodically and on exit.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/version"
)

type options struct {
	endpoint          string
	licenseKey        string
	hosts             int
	hostPrefix        string
	interval          time.Duration
	logsPerSec        int
	spansPerSec       int
	inventoryInterval time.Duration
	duration          time.Duration
	concurrency       int
	gzip              bool
}

func main() {
	var o options
	flag.StringVar(&o.endpoint, "endpoint", "http://localhost:4318", "OTLP/HTTP base URL of openlog-ingest")
	flag.StringVar(&o.licenseKey, "license-key", os.Getenv("OPENLOG_LICENSE_KEY"), "license key (default $OPENLOG_LICENSE_KEY)")
	flag.IntVar(&o.hosts, "hosts", 10, "number of simulated hosts")
	flag.StringVar(&o.hostPrefix, "host-prefix", "loadgen", "host name prefix")
	flag.DurationVar(&o.interval, "interval", 10*time.Second, "host metrics interval")
	flag.IntVar(&o.logsPerSec, "logs-per-sec", 100, "log records per second (all hosts)")
	flag.IntVar(&o.spansPerSec, "spans-per-sec", 100, "spans per second (all hosts)")
	flag.DurationVar(&o.inventoryInterval, "inventory-interval", 10*time.Minute, "inventory snapshot interval per host")
	flag.DurationVar(&o.duration, "duration", 0, "stop after this long (0 = until interrupted)")
	flag.IntVar(&o.concurrency, "concurrency", 8, "concurrent HTTP requests")
	flag.BoolVar(&o.gzip, "gzip", true, "gzip request bodies")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("openlog-loadgen %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	if o.licenseKey == "" {
		fmt.Fprintln(os.Stderr, "openlog-loadgen: -license-key is required")
		os.Exit(2)
	}
	if o.hosts <= 0 || o.concurrency <= 0 || o.interval <= 0 {
		fmt.Fprintln(os.Stderr, "openlog-loadgen: -hosts, -concurrency and -interval must be > 0")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.duration)
		defer cancel()
	}
	g := newGenerator(o)
	g.run(ctx)
}

// ---- sending ----

type job struct {
	path  string
	body  []byte
	kind  string // metrics, logs, spans, inventory
	items int
}

type stats struct {
	requests, errors                   atomic.Int64
	dataPoints, logs, spans, inventory atomic.Int64
	bytes                              atomic.Int64
	lastStatusMu                       sync.Mutex
	lastError                          string
}

type generator struct {
	o      options
	hosts  []*host
	client *http.Client
	jobs   chan job
	st     stats
	start  time.Time
}

type host struct {
	id, name   string
	resource   *resourcepb.Resource
	bootTime   time.Time
	cpuSeconds map[string]float64
	diskBytes  [2]float64
	netBytes   [2]float64
}

func newGenerator(o options) *generator {
	g := &generator{o: o, client: &http.Client{Timeout: 30 * time.Second}, jobs: make(chan job, o.concurrency*4)}
	for i := 0; i < o.hosts; i++ {
		name := fmt.Sprintf("%s-%04d", o.hostPrefix, i)
		id := fmt.Sprintf("%x", sha(name))
		h := &host{
			id: id, name: name, bootTime: time.Now().Add(-time.Duration(mrand.IntN(30*24)) * time.Hour),
			cpuSeconds: map[string]float64{},
			resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				str("host.id", id), str("host.name", name), str("host.arch", "amd64"), str("os.type", "linux"),
				str("os.name", "ubuntu"), str("os.version", "24.04"), str("os.description", "Ubuntu 24.04 LTS"),
				str("openlog.os.kernel_release", "6.8.0-45-generic"), str("openlog.entity.type", "host"),
				str("openlog.agent.name", "openlog-infra-agent"), str("openlog.agent.version", "0.1.0-loadgen"),
				str("env", []string{"prod", "staging"}[i%2]),
			}},
		}
		g.hosts = append(g.hosts, h)
	}
	return g
}

func (g *generator) run(ctx context.Context) {
	g.start = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < g.o.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range g.jobs {
				g.send(j)
			}
		}()
	}
	fmt.Printf("openlog-loadgen: endpoint=%s hosts=%d interval=%s logs/s=%d spans/s=%d\n",
		g.o.endpoint, g.o.hosts, g.o.interval, g.o.logsPerSec, g.o.spansPerSec)

	var prodWG sync.WaitGroup
	prodWG.Add(4)
	go func() { defer prodWG.Done(); g.metricsLoop(ctx) }()
	go func() { defer prodWG.Done(); g.inventoryLoop(ctx) }()
	go func() { defer prodWG.Done(); g.perSecondLoop(ctx, g.logsTick) }()
	go func() { defer prodWG.Done(); g.perSecondLoop(ctx, g.tracesTick) }()

	report := time.NewTicker(10 * time.Second)
	defer report.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-report.C:
			g.report("progress")
		}
	}
	prodWG.Wait()
	close(g.jobs)
	wg.Wait()
	g.report("total")
}

func (g *generator) enqueue(ctx context.Context, j job) {
	select {
	case g.jobs <- j:
	case <-ctx.Done():
	}
}

func (g *generator) send(j job) {
	body := j.body
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(g.o.endpoint, "/")+j.path, nil)
	if err != nil {
		g.fail(err.Error())
		return
	}
	if g.o.gzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(body)
		zw.Close()
		body = buf.Bytes()
		req.Header.Set("Content-Encoding", "gzip")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("openlog-license-key", g.o.licenseKey)
	resp, err := g.client.Do(req)
	g.st.requests.Add(1)
	if err != nil {
		g.fail(err.Error())
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g.fail(fmt.Sprintf("%s: HTTP %d", j.path, resp.StatusCode))
		return
	}
	g.st.bytes.Add(int64(len(j.body)))
	switch j.kind {
	case "metrics":
		g.st.dataPoints.Add(int64(j.items))
	case "logs":
		g.st.logs.Add(int64(j.items))
	case "spans":
		g.st.spans.Add(int64(j.items))
	case "inventory":
		g.st.inventory.Add(int64(j.items))
	}
}

func (g *generator) fail(msg string) {
	g.st.errors.Add(1)
	g.st.lastStatusMu.Lock()
	g.st.lastError = msg
	g.st.lastStatusMu.Unlock()
}

func (g *generator) report(label string) {
	secs := time.Since(g.start).Seconds()
	g.st.lastStatusMu.Lock()
	lastErr := g.st.lastError
	g.st.lastStatusMu.Unlock()
	fmt.Printf("[%s %6.0fs] requests=%d errors=%d | datapoints=%d (%.0f/s) logs=%d (%.0f/s) spans=%d (%.0f/s) inventory_records=%d | %.2f MiB/s uncompressed",
		label, secs, g.st.requests.Load(), g.st.errors.Load(),
		g.st.dataPoints.Load(), float64(g.st.dataPoints.Load())/secs,
		g.st.logs.Load(), float64(g.st.logs.Load())/secs,
		g.st.spans.Load(), float64(g.st.spans.Load())/secs,
		g.st.inventory.Load(), float64(g.st.bytes.Load())/secs/(1<<20))
	if lastErr != "" {
		fmt.Printf(" | last error: %s", lastErr)
	}
	fmt.Println()
}

func marshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// ---- metrics ----

func (g *generator) metricsLoop(ctx context.Context) {
	t := time.NewTicker(g.o.interval)
	defer t.Stop()
	for {
		now := time.Now()
		for _, h := range g.hosts {
			req, n := h.metrics(now, g.o.interval)
			g.enqueue(ctx, job{path: "/v1/metrics", body: marshal(req), kind: "metrics", items: n})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

var cpuModes = []string{"user", "nice", "system", "idle", "iowait", "interrupt", "softirq", "steal"}

func (h *host) metrics(now time.Time, interval time.Duration) (*colmetrics.ExportMetricsServiceRequest, int) {
	ts := uint64(now.UnixNano())
	start := uint64(h.bootTime.UnixNano())
	var ms []*metricspb.Metric
	n := 0
	gauge := func(name, unit string, dps ...*metricspb.NumberDataPoint) {
		ms = append(ms, &metricspb.Metric{Name: name, Unit: unit, Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: dps}}})
		n += len(dps)
	}
	sum := func(name, unit string, monotonic bool, dps ...*metricspb.NumberDataPoint) {
		ms = append(ms, &metricspb.Metric{Name: name, Unit: unit, Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: monotonic, DataPoints: dps}}})
		n += len(dps)
	}
	dp := func(v float64, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
		return &metricspb.NumberDataPoint{StartTimeUnixNano: start, TimeUnixNano: ts, Attributes: attrs, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}
	}
	idp := func(v int64, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
		return &metricspb.NumberDataPoint{StartTimeUnixNano: start, TimeUnixNano: ts, Attributes: attrs, Value: &metricspb.NumberDataPoint_AsInt{AsInt: v}}
	}

	// CPU: random utilization split, cumulative time grows accordingly (4 CPUs).
	busy := 0.05 + 0.5*mrand.Float64()
	shares := map[string]float64{"user": busy * 0.6, "nice": 0.001, "system": busy * 0.3, "iowait": busy * 0.05, "interrupt": 0.001, "softirq": busy * 0.04, "steal": 0.002}
	total := 0.0
	for _, v := range shares {
		total += v
	}
	shares["idle"] = math.Max(0, 1-total)
	var util, times []*metricspb.NumberDataPoint
	for _, m := range cpuModes {
		h.cpuSeconds[m] += shares[m] * interval.Seconds() * 4
		util = append(util, dp(shares[m], str("cpu.mode", m)))
		times = append(times, dp(h.cpuSeconds[m], str("cpu.mode", m)))
	}
	sum("system.cpu.time", "s", true, times...)
	gauge("system.cpu.utilization", "1", util...)
	sum("system.cpu.logical.count", "{cpu}", false, idp(4))
	gauge("system.cpu.load_average.1m", "{thread}", dp(busy*4))
	gauge("system.cpu.load_average.5m", "{thread}", dp(busy*3.5))
	gauge("system.cpu.load_average.15m", "{thread}", dp(busy*3))

	const memTotal = 16 << 30
	used := memTotal * (0.3 + 0.4*mrand.Float64())
	cached, buffers := memTotal*0.2, memTotal*0.02
	free := memTotal - used - cached - buffers
	sum("system.memory.usage", "By", false,
		idp(int64(used), str("system.memory.state", "used")), idp(int64(free), str("system.memory.state", "free")),
		idp(int64(cached), str("system.memory.state", "cached")), idp(int64(buffers), str("system.memory.state", "buffers")))
	sum("system.memory.limit", "By", false, idp(memTotal))
	gauge("system.memory.utilization", "1",
		dp(used/memTotal, str("system.memory.state", "used")), dp(free/memTotal, str("system.memory.state", "free")),
		dp(cached/memTotal, str("system.memory.state", "cached")), dp(buffers/memTotal, str("system.memory.state", "buffers")))

	fsAttrs := []*commonpb.KeyValue{str("system.device", "/dev/sda1"), str("system.filesystem.mountpoint", "/"), str("system.filesystem.type", "ext4")}
	const fsTotal = 100 << 30
	fsUsed := fsTotal * 0.42
	sum("system.filesystem.usage", "By", false,
		idp(int64(fsUsed), append(fsAttrs, str("system.filesystem.state", "used"))...),
		idp(int64(fsTotal-fsUsed), append(fsAttrs, str("system.filesystem.state", "free"))...))
	gauge("system.filesystem.utilization", "1", dp(fsUsed/fsTotal, fsAttrs...))

	h.diskBytes[0] += 1e6 * interval.Seconds() * mrand.Float64()
	h.diskBytes[1] += 3e6 * interval.Seconds() * mrand.Float64()
	sum("system.disk.io", "By", true,
		dp(h.diskBytes[0], str("system.device", "sda"), str("disk.io.direction", "read")),
		dp(h.diskBytes[1], str("system.device", "sda"), str("disk.io.direction", "write")))
	h.netBytes[0] += 5e5 * interval.Seconds() * mrand.Float64()
	h.netBytes[1] += 2e5 * interval.Seconds() * mrand.Float64()
	sum("system.network.io", "By", true,
		dp(h.netBytes[0], str("network.interface.name", "eth0"), str("network.io.direction", "receive")),
		dp(h.netBytes[1], str("network.interface.name", "eth0"), str("network.io.direction", "transmit")))
	gauge("system.uptime", "s", dp(now.Sub(h.bootTime).Seconds()))
	sum("system.process.count", "{process}", false,
		idp(int64(2+mrand.IntN(4)), str("process.status", "running")), idp(int64(180+mrand.IntN(40)), str("process.status", "sleeping")))

	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: h.resource,
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope:   &commonpb.InstrumentationScope{Name: "openlog-infra-agent/hostmetrics", Version: "0.1.0"},
			Metrics: ms,
		}},
	}}}

	// An application histogram on the same host, to exercise histogram storage.
	bounds := []float64{5, 10, 25, 50, 100, 250, 500, 1000}
	counts := make([]uint64, len(bounds)+1)
	var cnt uint64
	var total2 float64
	for i := 0; i < 200; i++ {
		v := mrand.ExpFloat64() * 40
		total2 += v
		cnt++
		j := 0
		for j < len(bounds) && v > bounds[j] {
			j++
		}
		counts[j]++
	}
	req.ResourceMetrics = append(req.ResourceMetrics, &metricspb.ResourceMetrics{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", "checkout"), str("host.id", h.id), str("host.name", h.name)}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "http.server.request.duration", Unit: "ms",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.HistogramDataPoint{{
					StartTimeUnixNano: uint64(now.Add(-interval).UnixNano()), TimeUnixNano: ts, Count: cnt, Sum: &total2,
					BucketCounts: counts, ExplicitBounds: bounds, Attributes: []*commonpb.KeyValue{str("http.route", "/cart")},
				}},
			}},
		}}}},
	})
	return req, n + 1
}

// ---- inventory ----

func (g *generator) inventoryLoop(ctx context.Context) {
	if g.o.inventoryInterval <= 0 {
		return
	}
	t := time.NewTicker(g.o.inventoryInterval)
	defer t.Stop()
	for {
		for _, h := range g.hosts {
			items, snapshot, n := h.inventory(time.Now())
			// Items first, snapshot-complete record after them (same partition key).
			g.enqueue(ctx, job{path: "/v1/logs", body: marshal(items), kind: "inventory", items: n})
			g.enqueue(ctx, job{path: "/v1/logs", body: marshal(snapshot), kind: "inventory", items: 1})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *host) inventory(now time.Time) (*collogs.ExportLogsServiceRequest, *collogs.ExportLogsServiceRequest, int) {
	snap := uuidv4()
	ts := uint64(now.UnixNano())
	var recs []*logspb.LogRecord
	item := func(category, key string, body any) {
		b, _ := json.Marshal(body)
		recs = append(recs, &logspb.LogRecord{
			TimeUnixNano: ts, ObservedTimeUnixNano: ts,
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: string(b)}},
			Attributes: []*commonpb.KeyValue{
				str("event.name", "openlog.inventory.item"), str("openlog.inventory.snapshot_id", snap),
				str("openlog.inventory.category", category), str("openlog.inventory.key", key),
			},
		})
	}
	item("os", "os", map[string]any{"id": "ubuntu", "name": "Ubuntu", "version_id": "24.04", "pretty_name": "Ubuntu 24.04 LTS",
		"kernel_release": "6.8.0-45-generic", "arch": "amd64", "hostname": h.name, "boot_time": h.bootTime.UTC().Format(time.RFC3339)})
	item("hardware", "cpu", map[string]any{"vendor": "GenuineIntel", "model": "Intel(R) Xeon(R) Platinum 8375C", "logical_cores": 4, "physical_cores": 2, "sockets": 1, "mhz": 2900})
	item("hardware", "memory", map[string]any{"total_bytes": 16 << 30, "swap_total_bytes": 0})
	for _, p := range [][2]string{{"openssl", "3.0.13-0ubuntu3.4"}, {"libssl3t64", "3.0.13-0ubuntu3.4"}, {"nginx", "1.24.0-2ubuntu7"}, {"redis-server", "5:7.0.15-1build2"}, {"curl", "8.5.0-2ubuntu10.4"}} {
		item("package", "dpkg:"+p[0], map[string]any{"manager": "dpkg", "name": p[0], "version": p[1], "arch": "amd64"})
	}
	item("systemd_unit", "redis-server.service", map[string]any{"name": "redis-server.service", "type": "service", "enabled_state": "enabled", "description": "Advanced key-value store", "exec_start": "/usr/bin/redis-server /etc/redis/redis.conf"})
	item("listening_port", "tcp:127.0.0.1:6379", map[string]any{"protocol": "tcp", "family": 4, "address": "127.0.0.1", "port": 6379, "pid": 812, "process_name": "redis-server", "process_exe": "/usr/bin/redis-server"})
	item("listening_port", "tcp:0.0.0.0:443", map[string]any{"protocol": "tcp", "family": 4, "address": "0.0.0.0", "port": 443, "pid": 901, "process_name": "nginx", "process_exe": "/usr/sbin/nginx"})
	// IPv6 addresses are bracketed in keys (D-016).
	item("listening_port", "tcp:[::]:22", map[string]any{"protocol": "tcp", "family": 6, "address": "::", "port": 22, "pid": 640, "process_name": "sshd", "process_exe": "/usr/sbin/sshd"})
	item("discovered_service", "redis:/usr/bin/redis-server", map[string]any{
		"rule_id": "redis", "name": "Redis", "category": "database", "instance": "/usr/bin/redis-server", "version": "7.0.15",
		"matched_by": []string{"process", "systemd_unit", "listening_port"}, "pids": []int{812},
		"ports":         []map[string]any{{"protocol": "tcp", "address": "127.0.0.1", "port": 6379}},
		"systemd_units": []string{"redis-server.service"}, "packages": []string{"dpkg:redis-server"}, "container_ids": []string{},
		"integration": map[string]any{"id": "redis", "status": "needs_configuration"}, "apm_hint": nil,
	})
	item("discovered_service", "nginx:/usr/sbin/nginx", map[string]any{
		"rule_id": "nginx", "name": "NGINX", "category": "web_server", "instance": "/usr/sbin/nginx", "version": "1.24.0",
		"matched_by": []string{"process", "listening_port"}, "pids": []int{901},
		"ports":       []map[string]any{{"protocol": "tcp", "address": "0.0.0.0", "port": 443}},
		"integration": map[string]any{"id": "nginx", "status": "enabled"}, "apm_hint": nil,
	})
	wrap := func(recs []*logspb.LogRecord) *collogs.ExportLogsServiceRequest {
		return &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  h.resource,
			ScopeLogs: []*logspb.ScopeLogs{{Scope: &commonpb.InstrumentationScope{Name: "openlog-infra-agent/inventory"}, LogRecords: recs}},
		}}}
	}
	snapRec := &logspb.LogRecord{TimeUnixNano: ts, ObservedTimeUnixNano: ts, Attributes: []*commonpb.KeyValue{
		str("event.name", "openlog.inventory.snapshot"), str("openlog.inventory.snapshot_id", snap),
		{Key: "openlog.inventory.item_count", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(len(recs))}}},
	}}
	return wrap(recs), wrap([]*logspb.LogRecord{snapRec}), len(recs)
}

// ---- logs & traces ----

func (g *generator) perSecondLoop(ctx context.Context, tick func(ctx context.Context, now time.Time)) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			tick(ctx, now)
		}
	}
}

var logMessages = []struct {
	sev  logspb.SeverityNumber
	text string
	msg  string
}{
	{logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO", "GET /api/cart 200 12ms"},
	{logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO", "user session refreshed"},
	{logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG", "cache hit for key product:1234"},
	{logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN", "slow query detected: 1.2s SELECT * FROM orders"},
	{logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR", "payment gateway timeout after 30s"},
}

func (g *generator) logsTick(ctx context.Context, now time.Time) {
	if g.o.logsPerSec <= 0 {
		return
	}
	per := g.o.logsPerSec / len(g.hosts)
	rem := g.o.logsPerSec % len(g.hosts)
	for i, h := range g.hosts {
		n := per
		if i < rem {
			n++
		}
		if n == 0 {
			continue
		}
		recs := make([]*logspb.LogRecord, n)
		for j := range recs {
			m := logMessages[mrand.IntN(len(logMessages))]
			ts := uint64(now.Add(-time.Duration(mrand.IntN(1000)) * time.Millisecond).UnixNano())
			recs[j] = &logspb.LogRecord{
				TimeUnixNano: ts, ObservedTimeUnixNano: uint64(now.UnixNano()), SeverityNumber: m.sev, SeverityText: m.text,
				Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: m.msg}},
				Attributes: []*commonpb.KeyValue{str("log.file.path", "/var/log/app/checkout.log")},
			}
			if m.sev >= logspb.SeverityNumber_SEVERITY_NUMBER_WARN {
				recs[j].TraceId, recs[j].SpanId = randBytes(16), randBytes(8)
			}
		}
		res := &resourcepb.Resource{Attributes: append(append([]*commonpb.KeyValue{}, h.resource.Attributes...), str("service.name", "checkout"))}
		req := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{Resource: res,
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}}}}}
		g.enqueue(ctx, job{path: "/v1/logs", body: marshal(req), kind: "logs", items: n})
	}
}

func (g *generator) tracesTick(ctx context.Context, now time.Time) {
	if g.o.spansPerSec <= 0 {
		return
	}
	const spansPerTrace = 5
	remaining := g.o.spansPerSec
	byService := map[string][]*tracepb.Span{}
	h := g.hosts[mrand.IntN(len(g.hosts))]
	for remaining > 0 {
		n := min(spansPerTrace, remaining)
		remaining -= n
		traceID := randBytes(16)
		rootID := randBytes(8)
		start := now.Add(-time.Duration(mrand.IntN(900)) * time.Millisecond)
		dur := time.Duration(20+mrand.IntN(300)) * time.Millisecond
		status := &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}
		if mrand.IntN(50) == 0 {
			status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "upstream returned 503"}
		}
		root := &tracepb.Span{
			TraceId: traceID, SpanId: rootID, Name: "GET /api/cart", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(dur).UnixNano()), Status: status,
			Attributes: []*commonpb.KeyValue{str("http.request.method", "GET"), str("http.route", "/api/cart"),
				{Key: "http.response.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 200}}}},
		}
		byService["frontend"] = append(byService["frontend"], root)
		for i := 1; i < n; i++ {
			cs := start.Add(time.Duration(i) * dur / time.Duration(n+1))
			child := &tracepb.Span{
				TraceId: traceID, SpanId: randBytes(8), ParentSpanId: rootID, Kind: tracepb.Span_SPAN_KIND_CLIENT,
				Name:              []string{"SELECT cart_items", "GET redis cart:*", "POST /pricing", "publish cart.viewed"}[(i-1)%4],
				StartTimeUnixNano: uint64(cs.UnixNano()), EndTimeUnixNano: uint64(cs.Add(dur / time.Duration(n+2)).UnixNano()),
				Status: &tracepb.Status{},
			}
			if i == 1 {
				child.Events = []*tracepb.Span_Event{{TimeUnixNano: uint64(cs.UnixNano()), Name: "query.start",
					Attributes: []*commonpb.KeyValue{str("db.system", "postgresql")}}}
			}
			byService["checkout"] = append(byService["checkout"], child)
		}
	}
	req := &coltrace.ExportTraceServiceRequest{}
	count := 0
	for svc, spans := range byService {
		res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", svc), str("host.id", h.id), str("host.name", h.name)}}
		req.ResourceSpans = append(req.ResourceSpans, &tracepb.ResourceSpans{Resource: res, ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}})
		count += len(spans)
	}
	g.enqueue(ctx, job{path: "/v1/traces", body: marshal(req), kind: "spans", items: count})
}

// ---- helpers ----

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	b[0] |= 1 // never all-zero
	return b
}

func uuidv4() string {
	b := randBytes(16)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// sha is a tiny stable 128-bit id derived from the host name (FNV-1a based),
// so repeated runs reuse the same host ids.
func sha(s string) []byte {
	var h1, h2 uint64 = 14695981039346656037, 1099511628211
	for i := 0; i < len(s); i++ {
		h1 ^= uint64(s[i])
		h1 *= 1099511628211
		h2 ^= uint64(s[len(s)-1-i])
		h2 *= 14695981039346656037 | 1
	}
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i] = byte(h1 >> (8 * i))
		out[8+i] = byte(h2 >> (8 * i))
	}
	return out
}
