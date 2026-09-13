package processor

import (
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func s(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func i64(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func hostResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		s("host.id", "h-1"), s("host.name", "web-1"), s("host.arch", "amd64"), s("os.type", "linux"),
		s("os.description", "Ubuntu 24.04 LTS"), s("openlog.agent.name", "openlog-infra-agent"), s("openlog.agent.version", "0.1.0"),
		s("env", "prod"),
	}}
}

var recv = time.Unix(1757757600, 0).UTC()

const ts1 = uint64(1757757600000000000)

func TestAddMetrics(t *testing.T) {
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: hostResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: "hostmetrics"},
			Metrics: []*metricspb.Metric{
				{Name: "system.cpu.utilization", Unit: "1", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
					{Attributes: []*commonpb.KeyValue{s("cpu.mode", "user")}, TimeUnixNano: ts1, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.12}},
					{Attributes: []*commonpb.KeyValue{s("cpu.mode", "idle")}, TimeUnixNano: ts1, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.8}},
				}}}},
				{Name: "system.network.io", Unit: "By", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
					AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: true,
					DataPoints: []*metricspb.NumberDataPoint{{StartTimeUnixNano: ts1 - 1e9, TimeUnixNano: ts1, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 12345}}},
				}}},
				{Name: "http.server.duration", Unit: "ms", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
					AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
					DataPoints:             []*metricspb.HistogramDataPoint{{TimeUnixNano: 0, Count: 4, Sum: ptr(10.0), BucketCounts: []uint64{1, 2, 1}, ExplicitBounds: []float64{1, 5}}},
				}}},
				{Name: "", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{}}},
			},
		}},
	}}}
	r := NewRows()
	r.AddMetrics("t1", recv, req)
	if len(r.Metrics) != 4 {
		t.Fatalf("rows = %d", len(r.Metrics))
	}
	g := r.Metrics[0]
	if g.TenantID != "t1" || g.MetricType != "gauge" || g.Temporality != "unspecified" || g.Value != 0.12 || g.HostID != "h-1" ||
		g.HostName != "web-1" || g.ScopeName != "hostmetrics" || g.Attributes["cpu.mode"] != "user" || !g.Timestamp.Equal(time.Unix(0, int64(ts1))) {
		t.Errorf("gauge row %+v", g)
	}
	if g.SeriesID == r.Metrics[1].SeriesID {
		t.Error("different attributes must give different series ids")
	}
	if g.SeriesID != SeriesID("t1", "system.cpu.utilization", g.ResourceAttributes, map[string]string{"cpu.mode": "user"}) {
		t.Error("series id not stable")
	}
	if SeriesID("t2", "system.cpu.utilization", g.ResourceAttributes, g.Attributes) == g.SeriesID {
		t.Error("series id must include tenant")
	}
	sum := r.Metrics[2]
	if sum.MetricType != "sum" || sum.Temporality != "cumulative" || !sum.IsMonotonic || sum.Value != 12345 || sum.StartTimestamp.UnixNano() != int64(ts1-1e9) {
		t.Errorf("sum row %+v", sum)
	}
	h := r.Metrics[3]
	if h.MetricType != "histogram" || h.Temporality != "delta" || h.Count != 4 || h.Sum != 10 || h.Value != 2.5 ||
		!reflect.DeepEqual(h.BucketCounts, []uint64{1, 2, 1}) || !reflect.DeepEqual(h.ExplicitBounds, []float64{1, 5}) || !h.Timestamp.Equal(recv) {
		t.Errorf("histogram row %+v", h)
	}
	if r.Dropped["empty_metric_name"] != 1 {
		t.Errorf("dropped %v", r.Dropped)
	}
	if vals := r.Values(TableMetrics); len(vals[0]) != len(Columns[TableMetrics]) {
		t.Errorf("values/columns mismatch %d vs %d", len(vals[0]), len(Columns[TableMetrics]))
	}
	hosts := r.Hosts()
	if len(hosts) != 1 || hosts[0].HostName != "web-1" || hosts[0].OSDescription != "Ubuntu 24.04 LTS" || hosts[0].Arch != "amd64" ||
		hosts[0].AgentVersion != "0.1.0" || hosts[0].ResourceAttributes["env"] != "prod" {
		t.Errorf("hosts %+v", hosts)
	}
}

func ptr[T any](v T) *T { return &v }

func TestAddLogsInventoryRouting(t *testing.T) {
	req := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: hostResource(),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			{TimeUnixNano: ts1, SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, SeverityText: "ERROR",
				Body:    &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "disk full"}},
				TraceId: mustHex("5b8efff798038103d269b633813fc60c"), SpanId: mustHex("eee19b7ec3c1b174"), Flags: 0x101,
				Attributes: []*commonpb.KeyValue{s("code", "E1")}},
			{TimeUnixNano: ts1, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: `{"name":"openssl","version":"3.0.13"}`}},
				Attributes: []*commonpb.KeyValue{s("event.name", "openlog.inventory.item"), s("openlog.inventory.snapshot_id", "snap-1"),
					s("openlog.inventory.category", "package"), s("openlog.inventory.key", "dpkg:openssl")}},
			{TimeUnixNano: ts1, Attributes: []*commonpb.KeyValue{s("event.name", "openlog.inventory.item"), s("openlog.inventory.snapshot_id", "snap-1")}},
			{TimeUnixNano: ts1, Attributes: []*commonpb.KeyValue{s("event.name", "openlog.inventory.snapshot"), s("openlog.inventory.snapshot_id", "snap-1"), i64("openlog.inventory.item_count", 1)}},
			{TimeUnixNano: ts1, EventName: "openlog.inventory.snapshot", Attributes: []*commonpb.KeyValue{s("openlog.inventory.snapshot_id", "snap-2"), i64("openlog.inventory.item_count", 0)}},
		}}},
	}}}
	r := NewRows()
	r.AddLogs("t1", recv, req)
	if len(r.Logs) != 1 {
		t.Fatalf("logs = %d (inventory must not reach logs)", len(r.Logs))
	}
	l := r.Logs[0]
	if l.Body != "disk full" || l.SeverityNumber != 17 || l.SeverityText != "ERROR" || l.TraceID != "5b8efff798038103d269b633813fc60c" ||
		l.SpanID != "eee19b7ec3c1b174" || l.TraceFlags != 1 || l.Attributes["code"] != "E1" || l.HostID != "h-1" || !l.ObservedTimestamp.Equal(recv) {
		t.Errorf("log row %+v", l)
	}
	if len(r.InventoryItems) != 1 {
		t.Fatalf("items = %d", len(r.InventoryItems))
	}
	it := r.InventoryItems[0]
	if it.HostID != "h-1" || it.SnapshotID != "snap-1" || it.Category != "package" || it.ItemKey != "dpkg:openssl" || it.Data != `{"name":"openssl","version":"3.0.13"}` {
		t.Errorf("item %+v", it)
	}
	if len(r.InventorySnapshots) != 2 || r.InventorySnapshots[0].ItemCount != 1 || r.InventorySnapshots[1].SnapshotID != "snap-2" {
		t.Errorf("snapshots %+v", r.InventorySnapshots)
	}
	if r.Dropped["invalid_inventory_item"] != 1 {
		t.Errorf("dropped %v", r.Dropped)
	}
	if len(r.Hosts()) != 1 {
		t.Error("host upsert expected")
	}
}

func TestInventoryWithoutHostDropped(t *testing.T) {
	req := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{s("service.name", "api")}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			{Attributes: []*commonpb.KeyValue{s("event.name", "openlog.inventory.snapshot"), s("openlog.inventory.snapshot_id", "x")}},
		}}},
	}}}
	r := NewRows()
	r.AddLogs("t", recv, req)
	if len(r.InventorySnapshots) != 0 || len(r.Logs) != 0 || r.Dropped["invalid_inventory_snapshot"] != 1 || len(r.Hosts()) != 0 {
		t.Errorf("unexpected %+v", r)
	}
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestAddTraces(t *testing.T) {
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{s("service.name", "checkout")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: mustHex("5b8efff798038103d269b633813fc60c"), SpanId: mustHex("eee19b7ec3c1b174"), ParentSpanId: mustHex("eee19b7ec3c1b173"),
				Name: "GET /users", Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: ts1, EndTimeUnixNano: ts1 + 1234567,
				Status:     &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom"},
				Attributes: []*commonpb.KeyValue{i64("http.status_code", 500)},
				Events:     []*tracepb.Span_Event{{TimeUnixNano: ts1 + 10, Name: "exception", Attributes: []*commonpb.KeyValue{s("exception.type", "E")}}},
				Links:      []*tracepb.Span_Link{{TraceId: mustHex("00000000000000000000000000000001"), SpanId: mustHex("0000000000000002")}},
			},
			{TraceId: make([]byte, 16), SpanId: mustHex("eee19b7ec3c1b174")},
		}}},
	}}}
	r := NewRows()
	r.AddTraces("t1", recv, req)
	if len(r.Spans) != 1 || r.Dropped["invalid_span_id"] != 1 {
		t.Fatalf("spans %d dropped %v", len(r.Spans), r.Dropped)
	}
	sp := r.Spans[0]
	if sp.TraceID != "5b8efff798038103d269b633813fc60c" || sp.ParentSpanID != "eee19b7ec3c1b173" || sp.Kind != "server" ||
		sp.StatusCode != "error" || sp.StatusMessage != "boom" || sp.DurationNs != 1234567 || sp.ServiceName != "checkout" ||
		sp.Attributes["http.status_code"] != "500" || sp.EventsName[0] != "exception" || sp.EventsAttributes[0]["exception.type"] != "E" ||
		sp.LinksSpanID[0] != "0000000000000002" {
		t.Errorf("span %+v", sp)
	}
	if len(r.Hosts()) != 0 {
		t.Error("no host.id => no host row")
	}
	if vals := r.Values(TableSpans); len(vals[0]) != len(Columns[TableSpans]) {
		t.Error("values/columns mismatch")
	}
}
