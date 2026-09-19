package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

func testConfig(t *testing.T, root, endpoint string) *config.Config {
	cfg := config.Default()
	cfg.Host.RootPath = root
	cfg.Endpoint = endpoint
	cfg.LicenseKey = "k"
	cfg.StateDir = t.TempDir()
	cfg.Buffer.Dir = filepath.Join(t.TempDir(), "buffer")
	cfg.Discovery.RulesDir = filepath.Join(t.TempDir(), "none")
	cfg.Export.MaxRequestBytes = 64 << 10
	// Pipeline tests count inventory snapshots; integration status changes
	// (fixture services are unreachable) would add snapshots.
	cfg.Integrations.Enabled = false
	return cfg
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOnceJSON(t *testing.T) {
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	a, err := New(testConfig(t, fs.Root(), ""), "test", quiet(), false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := a.Once(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Metrics struct {
			ResourceMetrics []json.RawMessage `json:"resourceMetrics"`
		} `json:"metrics"`
		Logs struct {
			ResourceLogs []json.RawMessage `json:"resourceLogs"`
		} `json:"logs"`
		DiscoveredServices []struct {
			RuleID string `json:"rule_id"`
		} `json:"discovered_services"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Metrics.ResourceMetrics) != 1 || len(out.Logs.ResourceLogs) != 1 || len(out.DiscoveredServices) != 5 {
		t.Errorf("once output: %d %d %v", len(out.Metrics.ResourceMetrics), len(out.Logs.ResourceLogs), out.DiscoveredServices)
	}
}

type ingest struct {
	mu       sync.Mutex
	down     atomic.Bool
	requests map[string]int
	logs     []*logspb.LogRecord
}

func (in *ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if in.down.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	zr, _ := gzip.NewReader(r.Body)
	b, _ := io.ReadAll(zr)
	in.mu.Lock()
	defer in.mu.Unlock()
	in.requests[r.URL.Path]++
	if r.URL.Path == "/v1/logs" {
		var ld logspb.LogsData
		if err := proto.Unmarshal(b, &ld); err == nil {
			for _, rl := range ld.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					in.logs = append(in.logs, sl.LogRecords...)
				}
			}
		}
	}
}

// The snapshot time must reflect when inventory was collected. Before the fix it was the tick
// start, which precedes a slow metrics delivery (retries against a 503ing ingest) by tens of seconds.
func TestTickSnapshotTimeAfterMetricsDelivery(t *testing.T) {
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	in := &ingest{requests: map[string]int{}}
	srv := httptest.NewServer(in)
	defer srv.Close()

	a, err := New(testConfig(t, fs.Root(), srv.URL), "test", quiet(), true)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1704153600, 0)
	calls := 0
	// The first reading is the tick start; every later one is 30s later (metrics delivery took 30s).
	a.Now = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(30 * time.Second)
	}
	a.Tick(context.Background())

	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.logs) == 0 {
		t.Fatal("no inventory records sent")
	}
	want := uint64(start.Add(30 * time.Second).UnixNano())
	for _, rec := range in.logs {
		if rec.TimeUnixNano != want {
			t.Fatalf("snapshot record time = %s, want %s (collection time, not tick start)",
				time.Unix(0, int64(rec.TimeUnixNano)).UTC(), start.Add(30*time.Second).UTC())
		}
	}
	if !a.lastInventory.Equal(start.Add(30 * time.Second)) {
		t.Errorf("lastInventory = %s", a.lastInventory)
	}
}

func TestTickBufferAndReplay(t *testing.T) {
	fixture := testfixtures.ServiceHost()
	fs := hostfstest.Build(t, fixture)
	in := &ingest{requests: map[string]int{}}
	srv := httptest.NewServer(in)
	defer srv.Close()

	cfg := testConfig(t, fs.Root(), srv.URL)
	cfg.Export.MaxRequestBytes = 8 << 10 // force the snapshot to span several requests
	a, err := New(cfg, "test", quiet(), true)
	if err != nil {
		t.Fatal(err)
	}
	a.exp.MaxAttempts = 1
	clock := time.Unix(1704153600, 0)
	a.Now = func() time.Time { return clock }

	// Endpoint down: metrics and the (split) snapshot get buffered.
	in.down.Store(true)
	a.Tick(context.Background())
	if a.buf.Len() < 2 {
		t.Fatalf("expected buffered payloads, got %d", a.buf.Len())
	}
	buffered := a.buf.Len()

	// Recovery: buffer replays in order, then the new metrics are sent.
	in.down.Store(false)
	clock = clock.Add(10 * time.Second)
	a.Tick(context.Background())
	if a.buf.Len() != 0 {
		t.Fatalf("buffer not drained: %d", a.buf.Len())
	}
	in.mu.Lock()
	if in.requests["/v1/logs"] < 2 || in.requests["/v1/metrics"] != 2 || buffered != in.requests["/v1/logs"]+1 {
		t.Errorf("requests = %v (buffered %d)", in.requests, buffered)
	}
	last := in.logs[len(in.logs)-1]
	if last.Attributes[0].Value.GetStringValue() != "openlog.inventory.snapshot" {
		t.Error("snapshot-complete record must arrive last")
	}
	in.mu.Unlock()

	snap := a.stats.Snapshot()
	if snap.ExportItems[selfmon.ExportKey{Signal: "logs", Outcome: "buffered"}] == 0 ||
		snap.ExportItems[selfmon.ExportKey{Signal: "logs", Outcome: "sent"}] == 0 ||
		snap.ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "sent"}] == 0 {
		t.Errorf("export counters = %v", snap.ExportItems)
	}

	// No change and not due: no new snapshot.
	clock = clock.Add(10 * time.Second)
	a.Tick(context.Background())
	in.mu.Lock()
	logsBefore := in.requests["/v1/logs"]
	in.mu.Unlock()

	// A newly enabled unit changes the fingerprint → immediate snapshot.
	wants := filepath.Join(fs.Root(), "etc/systemd/system/multi-user.target.wants")
	time.Sleep(10 * time.Millisecond)
	if err := os.Symlink("/lib/systemd/system/backup.service", filepath.Join(wants, "backup.service")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Second)
	a.Tick(context.Background())
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.requests["/v1/logs"] <= logsBefore {
		t.Errorf("change did not trigger a snapshot: %v", in.requests)
	}
}

// Log files are tailed while the agent runs, sent as plain OTLP logs (not
// inventory events) and their offsets are persisted on shutdown.
func TestRunTailsLogFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	fixture := testfixtures.ServiceHost()
	fixture["/var/log/app/app.log"] = "before start\n"
	fs := hostfstest.Build(t, fixture)
	in := &ingest{requests: map[string]int{}}
	srv := httptest.NewServer(in)
	defer srv.Close()

	cfg := testConfig(t, fs.Root(), srv.URL)
	cfg.Interval = config.Duration(time.Second)
	cfg.Logs.Files = []config.LogFile{{Path: "/var/log/app/*.log"}}
	cfg.Logs.PollInterval = config.Duration(100 * time.Millisecond)
	cfg.Logs.StartAt = "beginning"
	a, err := New(cfg, "test", quiet(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()

	logPath := filepath.Join(fs.Root(), "var/log/app/app.log")
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("level=warn msg=\"after start\"\n")
	f.Close()

	fileRecords := func() []string {
		in.mu.Lock()
		defer in.mu.Unlock()
		var out []string
		for _, r := range in.logs {
			for _, kv := range r.Attributes {
				if kv.Key == "event.name" {
					break
				}
				if kv.Key == "log.file.path" && kv.Value.GetStringValue() == "/var/log/app/app.log" {
					out = append(out, r.Body.GetStringValue())
				}
			}
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(fileRecords()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if got := fileRecords(); len(got) != 2 || got[0] != "before start" || got[1] != `level=warn msg="after start"` {
		t.Fatalf("file records = %q", got)
	}
	state, err := os.ReadFile(filepath.Join(cfg.StateDir, "logs-state.json"))
	fi, _ := os.Stat(logPath)
	if err != nil || !bytes.Contains(state, []byte(`"offset":`+strconv.FormatInt(fi.Size(), 10))) {
		t.Errorf("state = %s (size %d) %v", state, fi.Size(), err)
	}
}

func TestOnceScrapesPrometheusTargets(t *testing.T) {
	exporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# TYPE app_jobs_total counter\napp_jobs_total 7\n"))
	}))
	defer exporter.Close()
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	cfg := testConfig(t, fs.Root(), "")
	cfg.Prometheus.Targets = []config.ScrapeTarget{{URL: exporter.URL + "/metrics", Job: "app"}}
	a, err := New(cfg, "test", quiet(), false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := a.Once(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"app_jobs_total"`, `"stringValue": "app"`, `"stringValue": "prometheus"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("once output lacks %s", want)
		}
	}
}
