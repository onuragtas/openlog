//go:build tailsampling

// Integration test of tail-based sampling (D-075) against real Kafka + ClickHouse (docker compose project
// openlog-m4-tail) with ingest, processor and two sampler instances built from the tree and run in-process:
//
//	go test -tags tailsampling -count=1 -v -timeout 20m ./test/tailsampling
//
// Traces (errors, slow, normal) are head-sampled at 0.5 (tracestate ot=th:8) by the generator and sent through
// OTLP/HTTP ingest with the root and child spans of each trace in different requests. The policy keeps errors and
// slow traces and 25 % of the rest. Phase 2 starts a second sampler (rebalance), phase 3 stops the first one
// (revocation decides its buffered traces). Checked: no error/slow trace lost, normal keep ratio, span weights,
// APM requests/errors (apm_transactions_1m) against the true (pre head-sampling) counts, duplicates and split traces.
//
// TAILTEST_KEEP=1 keeps the compose project; TAILTEST_NO_UP=1 uses a running one.
package tailsampling

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/tenant"
)

const (
	project    = "openlog-m4-tail"
	tenantID   = "tailtest"
	licenseKey = "tail-test-key"
	ingestHTTP = "127.0.0.1:14318"
	chAddr     = "127.0.0.1:19300"
	perPhase   = 8000 // logical traces per phase (before head sampling)
	batch      = 100
	headTh     = uint64(1) << 55 // ot=th:8 → p = 0.5
	policy     = `{"enabled":true,"baseline_ratio":0.25,"max_spans_per_second":0,"rules":[{"name":"errors","type":"error"},{"name":"slow","type":"latency","threshold_ms":1000}]}`
)

func compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", "docker-compose.yml"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func TestMain(m *testing.M) {
	up := os.Getenv("TAILTEST_NO_UP") == ""
	if up {
		if err := compose("up", "-d", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, "compose up:", err)
			_ = compose("down", "-v")
			os.Exit(1)
		}
	}
	code := m.Run()
	if up && os.Getenv("TAILTEST_KEEP") == "" {
		_ = compose("down", "-v")
	}
	os.Exit(code)
}

type sent struct {
	class string // error, slow, normal
}

func TestTailSamplingEndToEnd(t *testing.T) {
	env := map[string]string{
		"OPENLOG_LOG_LEVEL":                   "warn",
		"OPENLOG_KAFKA_BROKERS":               "127.0.0.1:19392",
		"OPENLOG_CLICKHOUSE_ADDR":             chAddr,
		"OPENLOG_CLICKHOUSE_USER":             "openlog",
		"OPENLOG_CLICKHOUSE_PASSWORD":         "openlog",
		"OPENLOG_CLICKHOUSE_CLUSTER":          "openlog",
		"OPENLOG_AUTH_MODE":                   "static",
		"OPENLOG_LICENSE_KEYS":                licenseKey + "=" + tenantID,
		"OPENLOG_INGEST_HTTP_ADDR":            ingestHTTP,
		"OPENLOG_INGEST_GRPC_ADDR":            "127.0.0.1:14317",
		"OPENLOG_KAFKA_PARTITIONS":            "6",
		"OPENLOG_UPDATE_CHECK":                "disabled",
		"OPENLOG_PROCESSOR_FLUSH_INTERVAL":    "1s",
		"OPENLOG_PROCESSOR_INSERT_MODE":       "distributed",
		"OPENLOG_TAILSAMPLING_ENABLED":        "true",
		"OPENLOG_TAILSAMPLING_DECISION_WAIT":  "4s",
		"OPENLOG_TAILSAMPLING_DEFAULT_POLICY": policy,
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := app.RunMigrate(ctx, cfg, log); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)

	var wg sync.WaitGroup
	run := func(name string, fn func(context.Context, config.Config, *admin.Server, *slog.Logger) error) (context.CancelFunc, *admin.Server) {
		cctx, ccancel := context.WithCancel(ctx)
		adm := admin.New("127.0.0.1:0", log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(cctx, cfg, adm, log.With("instance", name)); err != nil && cctx.Err() == nil {
				t.Errorf("%s: %v", name, err)
			}
		}()
		return ccancel, adm
	}
	stopIngest, _ := run("ingest", app.RunIngest)
	stopProcessor, _ := run("processor", app.RunProcessor)
	stopA, admA := run("sampler-a", app.RunSampler)
	defer func() {
		stopIngest()
		stopProcessor()
		cancel()
		wg.Wait()
	}()
	waitHTTP(t, "http://"+ingestHTTP+"/v1/traces")

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 42))
	truth := map[string]int{}
	headSent := map[string]sent{} // hex trace id → class
	var mu sync.Mutex
	sendPhase := func(phase int) {
		for i := 0; i < perPhase; i += batch {
			var roots, children []*tracepb.Span
			now := time.Now()
			for j := 0; j < batch; j++ {
				id := make([]byte, 16)
				binary.BigEndian.PutUint64(id[:8], rng.Uint64())
				binary.BigEndian.PutUint64(id[8:], rng.Uint64())
				class, dur, code := "normal", 20*time.Millisecond, tracepb.Status_STATUS_CODE_UNSET
				switch x := rng.Float64(); {
				case x < 0.05:
					class, code = "error", tracepb.Status_STATUS_CODE_ERROR
				case x < 0.10:
					class, dur = "slow", 1500*time.Millisecond
				}
				truth[class]++
				if binary.BigEndian.Uint64(id[8:])&(1<<56-1) < headTh { // head sampler drops it
					continue
				}
				mu.Lock()
				headSent[hex.EncodeToString(id)] = sent{class: class}
				mu.Unlock()
				rootID, childID := randID(rng), randID(rng)
				roots = append(roots, &tracepb.Span{
					TraceId: id, SpanId: rootID, TraceState: "ot=th:8", Name: "GET /orders", Kind: tracepb.Span_SPAN_KIND_SERVER,
					StartTimeUnixNano: uint64(now.Add(-dur).UnixNano()), EndTimeUnixNano: uint64(now.UnixNano()),
					Status:     &tracepb.Status{Code: code},
					Attributes: []*commonpb.KeyValue{str("http.request.method", "GET"), str("http.route", "/orders")},
				})
				children = append(children, &tracepb.Span{
					TraceId: id, SpanId: childID, ParentSpanId: rootID, TraceState: "ot=th:8", Name: "load order", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
					StartTimeUnixNano: uint64(now.Add(-dur).UnixNano()), EndTimeUnixNano: uint64(now.Add(-dur + 5*time.Millisecond).UnixNano()),
				})
			}
			post(t, roots)
			go func(c []*tracepb.Span) { time.Sleep(700 * time.Millisecond); post(t, c) }(children)
			time.Sleep(40 * time.Millisecond)
		}
		t.Logf("phase %d sent", phase)
	}

	sendPhase(1)
	stopB, admB := run("sampler-b", app.RunSampler) // rebalance: A gives up partitions while traces are buffered
	defer stopB()
	sendPhase(2)
	stopA() // revocation: A decides everything it still buffers
	sendPhase(3)
	time.Sleep(time.Second)

	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{chAddr}, Database: "openlog", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Wait for the decision wait plus processing until the span count is stable.
	var last uint64
	stable := 0
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline) && stable < 4; time.Sleep(2 * time.Second) {
		var n uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM openlog.spans WHERE tenant_id = ?", tenantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == last && n > 0 {
			stable++
		} else {
			stable = 0
		}
		last = n
	}

	type stored struct {
		weight float64
		status string
		count  int
	}
	roots := map[string]*stored{}
	rows, err := conn.Query(ctx, "SELECT trace_id, sample_weight, toString(status_code) FROM openlog.spans WHERE tenant_id = ? AND parent_span_id = ''", tenantID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, status string
		var w float64
		if err := rows.Scan(&id, &w, &status); err != nil {
			t.Fatal(err)
		}
		if s := roots[id]; s != nil {
			s.count++
			continue
		}
		roots[id] = &stored{weight: w, status: status, count: 1}
	}
	rows.Close()
	children := map[string]int{}
	rows, err = conn.Query(ctx, "SELECT trace_id FROM openlog.spans WHERE tenant_id = ? AND parent_span_id != ''", tenantID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		children[id]++
	}
	rows.Close()

	var total, unique uint64
	if err := conn.QueryRow(ctx, "SELECT count(), uniqExact(span_id) FROM openlog.spans WHERE tenant_id = ?", tenantID).Scan(&total, &unique); err != nil {
		t.Fatal(err)
	}
	var requests, errorsW float64
	if err := conn.QueryRow(ctx, "SELECT sum(requests), sum(errors) FROM openlog.apm_transactions_1m WHERE tenant_id = ? AND service_name = 'shop' AND timestamp >= ?",
		tenantID, start.Truncate(time.Minute)).Scan(&requests, &errorsW); err != nil {
		t.Fatal(err)
	}

	sentBy, keptBy := map[string]int{}, map[string]int{}
	var badWeight, lost, splitRoot, splitChild int
	for id, s := range headSent {
		sentBy[s.class]++
		r := roots[id]
		if r == nil {
			if s.class != "normal" {
				lost++
			}
			if children[id] > 0 {
				splitChild++
			}
			continue
		}
		keptBy[s.class]++
		want := 2.0
		if s.class == "normal" {
			want = 8
		}
		if math.Abs(r.weight-want) > 1e-6 {
			badWeight++
		}
		if children[id] == 0 {
			splitRoot++
		}
	}
	trueTotal := float64(truth["error"] + truth["slow"] + truth["normal"])
	normalRatio := float64(keptBy["normal"]) / float64(sentBy["normal"])
	t.Logf("logical traces %d (errors %d, slow %d); head-sampled sent %d (errors %d, slow %d, normal %d)",
		int(trueTotal), truth["error"], truth["slow"], len(headSent), sentBy["error"], sentBy["slow"], sentBy["normal"])
	t.Logf("kept roots: errors %d/%d, slow %d/%d, normal %d/%d (%.3f, want 0.25)", keptBy["error"], sentBy["error"], keptBy["slow"], sentBy["slow"],
		keptBy["normal"], sentBy["normal"], normalRatio)
	t.Logf("spans stored %d, unique span ids %d (duplicates %d = %.3f%%)", total, unique, total-unique, 100*float64(total-unique)/math.Max(1, float64(total)))
	t.Logf("split traces: kept root without child %d, child without root %d; wrong weights %d; lost error/slow traces %d", splitRoot, splitChild, badWeight, lost)
	t.Logf("APM requests %.0f vs true %d (%.2f%%), errors %.0f vs true %d (%.2f%%)", requests, int(trueTotal), 100*(requests-trueTotal)/trueTotal,
		errorsW, truth["error"], 100*(errorsW-float64(truth["error"]))/float64(truth["error"]))
	revokedA := counter(t, admA, "openlog_tailsampling_evictions_total", "reason", "revoked")
	shutdownA := counter(t, admA, "openlog_tailsampling_evictions_total", "reason", "shutdown")
	decisionsB := counter(t, admB, "openlog_tailsampling_decisions_total", "", "")
	lateB := counter(t, admB, "openlog_tailsampling_late_spans_total", "", "")
	t.Logf("sampler A: revoked-decided traces %.0f, shutdown-decided %.0f; sampler B decisions %.0f, late spans %.0f", revokedA, shutdownA, decisionsB, lateB)

	if lost > 0 {
		t.Errorf("%d error/slow traces lost", lost)
	}
	if badWeight > 0 {
		t.Errorf("%d kept root spans with a wrong sample_weight", badWeight)
	}
	if normalRatio < 0.21 || normalRatio > 0.29 {
		t.Errorf("normal keep ratio %.3f outside [0.21, 0.29]", normalRatio)
	}
	if rel := math.Abs(requests-trueTotal) / trueTotal; rel > 0.08 {
		t.Errorf("APM requests %.0f deviate %.1f%% from the true %d", requests, rel*100, int(trueTotal))
	}
	if rel := math.Abs(errorsW-float64(truth["error"])) / float64(truth["error"]); rel > 0.12 {
		t.Errorf("APM errors %.0f deviate %.1f%% from the true %d", errorsW, rel*100, truth["error"])
	}
	if dup := float64(total-unique) / float64(total); dup > 0.01 {
		t.Errorf("duplicate spans %.2f%% > 1%%", dup*100)
	}
	if revokedA+shutdownA == 0 || decisionsB == 0 {
		t.Errorf("rebalance not exercised: sampler A revoked/shutdown decisions %.0f, sampler B decisions %.0f", revokedA+shutdownA, decisionsB)
	}
	if split := float64(splitRoot+splitChild) / float64(len(roots)); split > 0.05 {
		t.Errorf("split traces %.2f%% > 5%%", split*100)
	}
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func randID(rng *rand.Rand) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, rng.Uint64()|1)
	return b
}

var client = &http.Client{Timeout: 30 * time.Second}

func post(t *testing.T, spans []*tracepb.Span) {
	if len(spans) == 0 {
		return
	}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", "shop")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Error(err)
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		r, _ := http.NewRequest(http.MethodPost, "http://"+ingestHTTP+"/v1/traces", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set(tenant.HeaderLicenseKey, licenseKey)
		resp, err := client.Do(r)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		if attempt == 4 {
			t.Errorf("export: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func waitHTTP(t *testing.T, url string) {
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		resp, err := http.Post(url, "application/x-protobuf", strings.NewReader(""))
		if err == nil {
			resp.Body.Close()
			return
		}
	}
	t.Fatal("ingest did not start")
}

// counter sums a counter family on the admin registry, optionally filtered by one label.
func counter(t *testing.T, adm *admin.Server, name, label, value string) float64 {
	mfs, err := adm.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label != "" && !hasLabel(m, label, value) {
				continue
			}
			sum += m.GetCounter().GetValue()
		}
	}
	return sum
}

func hasLabel(m *dto.Metric, k, v string) bool {
	for _, l := range m.GetLabel() {
		if l.GetName() == k && l.GetValue() == v {
			return true
		}
	}
	return false
}
