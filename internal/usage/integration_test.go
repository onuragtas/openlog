//go:build integration

// Integration tests of usage metering (D-079) against a real single-node ClickHouse cluster with the full schema:
// usage MVs and processor ingest accounting compared with ground-truth counts after duplicate insert retries, the
// query_log collector, the reader and per-tenant retention mutations (D-081). See testdata/docker-compose.yml:
//
//	OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19010 go test -tags integration -count=1 ./internal/usage
package usage_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/processor"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/usage"
	"github.com/onuragtas/openlog/schema"
)

var (
	schemaOnce sync.Once
	schemaErr  error
)

func chConn(t *testing.T) clickhouse.Conn {
	t.Helper()
	addr := os.Getenv("OPENLOG_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("OPENLOG_TEST_CLICKHOUSE_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	schemaOnce.Do(func() {
		ms, err := migrate.Load(schema.ClickHouseFS(), "clickhouse", "openlog")
		if err != nil {
			schemaErr = err
			return
		}
		schemaErr = migrate.Run(ctx, conn, ms, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	return conn
}

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func resource(service, host string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", service), kv("host.id", host), kv("host.name", host)}}
}

// chunk is one processor chunk: OTLP requests and the tokens of its tables.
type chunk struct {
	rows  *processor.Rows
	token string
}

func buildChunk(tenant string, ts time.Time, n int, seed uint64, token string) (*chunk, map[string]int) {
	rng := rand.New(rand.NewPCG(seed, seed))
	rows := processor.NewRows()
	truth := map[string]int{}
	for i := 0; i < n; i++ {
		host := fmt.Sprintf("host-%d", i%3)
		svc := fmt.Sprintf("svc-%d", i%2)
		tnano := uint64(ts.Add(time.Duration(i) * time.Millisecond).UnixNano())

		tid := make([]byte, 16)
		sid := make([]byte, 8)
		for j := range tid {
			tid[j] = byte(rng.IntN(255) + 1)
		}
		for j := range sid {
			sid[j] = byte(rng.IntN(255) + 1)
		}
		tr := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: resource(svc, host),
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{TraceId: tid, SpanId: sid, Name: "GET /x", Kind: tracepb.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: tnano, EndTimeUnixNano: tnano + 1e6}}}}}}}
		lg := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{Resource: resource(svc, host),
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
				{TimeUnixNano: tnano, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("log line %d", i)}}},
				{TimeUnixNano: tnano + 1, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "second"}}},
			}}}}}}
		mt := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{Resource: resource(svc, host),
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "system.cpu.utilization",
				Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: tnano,
					Attributes: []*commonpb.KeyValue{kv("container.id", fmt.Sprintf("C%d", i%4))},
					Value:      &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(i)}}}}}}}}}}}}

		rows.AddTraces(tenant, ts, tr)
		rows.AddLogs(tenant, ts, lg)
		rows.AddMetrics(tenant, ts, mt)
		for sig, m := range map[string]proto.Message{"traces": tr, "logs": lg, "metrics": mt} {
			b, _ := proto.Marshal(m)
			rows.AddIngestUsage(tenant, sig, ts, len(b))
			truth["ingest_"+sig] += len(b)
		}
	}
	return &chunk{rows: rows, token: token}, truth
}

func (c *chunk) write(t *testing.T, w processor.Writer) {
	t.Helper()
	for _, table := range processor.Tables {
		if c.rows.Len(table) == 0 {
			continue
		}
		if err := w.Write(context.Background(), table, c.token+":"+table, processor.Columns[table], c.rows.Values(table)); err != nil {
			t.Fatalf("write %s: %v", table, err)
		}
	}
}

func scalar[T any](t *testing.T, conn clickhouse.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestUsageMatchesStoredRowsAfterRetries(t *testing.T) {
	conn := chConn(t)
	ctx := context.Background()
	tenant := fmt.Sprintf("usage-%d", time.Now().UnixNano())
	ts := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	w := processor.ClickHouseWriter{Conn: conn, Database: "openlog"}

	// Tokens are unique per run: ClickHouse keeps deduplication state (Keeper) across test runs.
	c1, truth1 := buildChunk(tenant, ts, 20, 1, tenant+":0:0-19")
	c2, truth2 := buildChunk(tenant, ts.Add(10*time.Minute), 7, 2, tenant+":0:20-26")
	c1.write(t, w)
	c1.write(t, w) // re-delivered chunk: same tokens
	c2.write(t, w)
	c1.write(t, w)
	c2.write(t, w)

	reader := &usage.Reader{Conn: conn, Database: "openlog"}
	from, to := ts.Truncate(time.Hour).Add(-time.Hour), ts.Add(2*time.Hour)
	totals, err := reader.Totals(ctx, tenant, from, to)
	if err != nil {
		t.Fatal(err)
	}
	sig := map[string]usage.SignalUsage{}
	for _, s := range totals.Signals {
		sig[s.Signal] = s
	}
	// Ground truth: the stored raw rows (after deduplication) and the request sizes.
	p := ch.Context(ctx, ch.WithParameters(ch.Parameters{"t": tenant}))
	for signal, table := range map[string]string{"traces": "spans", "logs": "logs", "metrics": "metrics"} {
		var n uint64
		if err := conn.QueryRow(p, "SELECT count() FROM openlog."+table+" WHERE tenant_id = {t:String}").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 || sig[signal].Items != n {
			t.Errorf("%s: usage items %d, stored rows %d", signal, sig[signal].Items, n)
		}
		want := uint64(truth1["ingest_"+signal] + truth2["ingest_"+signal])
		if sig[signal].IngestBytes != want {
			t.Errorf("%s: ingest bytes %d, want %d", signal, sig[signal].IngestBytes, want)
		}
		if sig[signal].Bytes == 0 {
			t.Errorf("%s: no stored bytes estimate", signal)
		}
	}
	if sig["traces"].Items != 27 || sig["logs"].Items != 54 || sig["metrics"].Items != 27 {
		t.Errorf("items %+v", sig)
	}
	if sig["traces"].IngestRequests != 27 {
		t.Errorf("ingest requests %d", sig["traces"].IngestRequests)
	}
	if totals.Hosts != 3 || totals.Services != 2 || totals.Containers != 4 {
		t.Errorf("entities hosts=%d services=%d containers=%d", totals.Hosts, totals.Services, totals.Containers)
	}
	wantHosts := scalar[uint64](t, conn, "SELECT uniqExact(host_id) FROM openlog.metrics WHERE tenant_id = ?", tenant)
	if totals.Hosts != wantHosts {
		t.Errorf("hosts %d, raw %d", totals.Hosts, wantHosts)
	}
	// After merges (FINAL-free reads re-aggregate) the numbers do not change.
	if err := conn.Exec(ctx, "OPTIMIZE TABLE openlog.usage_signals_1h_local FINAL"); err != nil {
		t.Fatal(err)
	}
	again, _ := reader.Totals(ctx, tenant, from, to)
	if again.Signals[0] != totals.Signals[0] || again.IngestBytes != totals.IngestBytes {
		t.Errorf("after OPTIMIZE: %+v vs %+v", again, totals)
	}

	days, err := reader.Daily(ctx, tenant, from, to)
	if err != nil || len(days) == 0 {
		t.Fatalf("daily %v %v", days, err)
	}
	var dayIngest uint64
	for _, d := range days {
		dayIngest += d.IngestBytes
	}
	if dayIngest != totals.IngestBytes {
		t.Errorf("daily ingest %d, total %d", dayIngest, totals.IngestBytes)
	}
	top, err := reader.Top(ctx, tenant, "service", from, to, 5)
	if err != nil || len(top) != 2 || top[0].Bytes < top[1].Bytes {
		t.Errorf("top %+v %v", top, err)
	}
	all, err := reader.AllTenants(ctx, from, to)
	if err != nil || all[tenant].IngestBytes != totals.IngestBytes {
		t.Errorf("all tenants %+v %v", all[tenant], err)
	}
	if day, err := reader.DayAllTenants(ctx, ts); err != nil || day[tenant].IngestBytes == 0 {
		t.Errorf("day totals %+v %v", day[tenant], err)
	}
	if ratios, err := reader.CompressionRatios(ctx); err != nil || ratios["logs"] <= 0 {
		t.Errorf("ratios %v %v", ratios, err)
	}
}

func TestQueryCollector(t *testing.T) {
	conn := chConn(t)
	ctx := context.Background()
	tenant := fmt.Sprintf("q-%d", time.Now().UnixNano())
	start := time.Now().UTC().Add(-time.Minute)
	qctx := ch.Context(ctx, ch.WithSettings(ch.Settings{"log_comment": `{"component":"api","tenant_id":"` + tenant + `"}`}))
	for i := 0; i < 3; i++ {
		var n uint64
		if err := conn.QueryRow(qctx, "SELECT count() FROM numbers(100000)").Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	c := &usage.QueryCollector{Conn: conn, Database: "openlog", Cluster: "openlog"}
	end := time.Now().UTC().Add(time.Minute)
	for i := 0; i < 2; i++ { // re-collection replaces
		if _, err := c.Collect(ctx, start.Truncate(time.Hour), end); err != nil {
			t.Fatal(err)
		}
	}
	reader := &usage.Reader{Conn: conn, Database: "openlog"}
	totals, err := reader.Totals(ctx, tenant, start.Truncate(time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if totals.Query.Queries != 3 || totals.Query.ReadRows < 300000 {
		t.Errorf("query usage %+v", totals.Query)
	}
}

type memRetentionStore struct {
	orgs []quota.OrgPlan
	done map[string]bool
}

func (m *memRetentionStore) ListOrgPlans(context.Context) ([]quota.OrgPlan, error) {
	return m.orgs, nil
}
func (m *memRetentionStore) RecentMutation(_ context.Context, table, p, h string, _ time.Time) (bool, error) {
	return m.done[table+p+h], nil
}
func (m *memRetentionStore) RecordMutation(_ context.Context, t quota.RetentionTask) error {
	m.done[t.Table+t.PartitionID+t.Hash] = true
	return nil
}

func TestTenantRetentionDeletesOnlyShortRetentionTenants(t *testing.T) {
	conn := chConn(t)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	short, long := fmt.Sprintf("short-%d", sfx), fmt.Sprintf("long-%d", sfx)
	old := time.Now().UTC().AddDate(0, 0, -6).Truncate(time.Hour)
	recent := time.Now().UTC().Add(-time.Hour)
	w := processor.ClickHouseWriter{Conn: conn, Database: "openlog"}
	for i, tn := range []string{short, long} {
		for j, ts := range []time.Time{old, recent} {
			c, _ := buildChunk(tn, ts, 5, uint64(10*i+j), fmt.Sprintf("ret-%d-%d-%d", sfx, i, j))
			c.write(t, w)
		}
	}
	catalog, err := quota.ParseCatalog(`{"plans":[{"id":"free","limits":{"retention_days":{"logs":3}}},{"id":"pro"}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	store := &memRetentionStore{done: map[string]bool{}, orgs: []quota.OrgPlan{
		{OrgID: "o1", TenantID: short, PlanID: "free", Assigned: true}, {OrgID: "o2", TenantID: long, PlanID: "pro", Assigned: true}}}
	job := &quota.RetentionJob{Conn: conn, Database: "openlog", Cluster: "openlog", Catalog: catalog, Store: store, MaxMutations: 10}
	n, err := job.RunOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("submitted %d: %v", n, err)
	}
	if n, err := job.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second run submitted %d: %v", n, err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		pending := scalar[uint64](t, conn, "SELECT count() FROM system.mutations WHERE database = 'openlog' AND table = 'logs_local' AND NOT is_done")
		if pending == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	count := func(tenant string, before bool) uint64 {
		op := ">="
		if before {
			op = "<"
		}
		return scalar[uint64](t, conn, "SELECT count() FROM openlog.logs WHERE tenant_id = ? AND timestamp "+op+" ?", tenant, recent.Add(-24*time.Hour))
	}
	if n := count(short, true); n != 0 {
		t.Errorf("short-retention tenant still has %d old logs", n)
	}
	if n := count(short, false); n != 10 {
		t.Errorf("short-retention tenant recent logs %d, want 10", n)
	}
	if n := count(long, true); n != 10 {
		t.Errorf("long-retention tenant old logs %d, want 10", n)
	}
	// Spans and metrics have no plan retention here: untouched.
	if n := scalar[uint64](t, conn, "SELECT count() FROM openlog.spans WHERE tenant_id = ?", short); n != 10 {
		t.Errorf("spans %d", n)
	}
	_ = bytes.MinRead
}
