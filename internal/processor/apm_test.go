package processor

import (
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

	"github.com/onuragtas/openlog/internal/apm"
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func resourceSpans(service string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
	return &tracepb.ResourceSpans{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", service),
			strAttr("service.namespace", "shop"), strAttr("deployment.environment", "demo"), strAttr("host.id", "h1")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}
}

// Application resources (OTel SDKs) carry host.id without the infra agent's attributes; they must
// neither create nor overwrite host rows (ReplacingMergeTree keeps the latest row). Only
// openlog.entity.type=host resources upsert hosts.
func TestAppResourcesDoNotUpsertHosts(t *testing.T) {
	app := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "orders"), strAttr("host.id", "h-1"), strAttr("host.name", "web-1")}}
	later := recv.Add(time.Minute)
	r := NewRows()
	r.AddMetrics("t1", recv, appMetrics(app))
	r.AddLogs("t1", recv, appLogs(app))
	r.AddTraces("t1", later, &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: app,
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{TraceId: mustHex("5b8efff798038103d269b633813fc60c"), SpanId: mustHex("eee19b7ec3c1b174"),
			Name: "GET /", Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: ts1, EndTimeUnixNano: ts1 + 1}}}}}}})
	if len(r.Metrics) != 1 || len(r.Logs) != 1 || len(r.Spans) != 1 || r.Spans[0].HostID != "h-1" {
		t.Fatalf("telemetry rows: %d metrics, %d logs, %d spans", len(r.Metrics), len(r.Logs), len(r.Spans))
	}
	if n := len(r.Hosts()); n != 0 {
		t.Fatalf("application resources produced %d host rows: %+v", n, r.Hosts())
	}
	// The agent's resource in the same batch still upserts the host, and a later app span does not replace it.
	r.AddMetrics("t1", recv, appMetrics(hostResource()))
	r.AddTraces("t1", later.Add(time.Hour), &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: app,
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{TraceId: mustHex("5b8efff798038103d269b633813fc60d"), SpanId: mustHex("eee19b7ec3c1b175"),
			Name: "GET /", Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: ts1, EndTimeUnixNano: ts1 + 1}}}}}}})
	hosts := r.Hosts()
	if len(hosts) != 1 || hosts[0].AgentVersion != "0.1.0" || hosts[0].OSDescription != "Ubuntu 24.04 LTS" || !hosts[0].LastSeen.Equal(recv) {
		t.Errorf("hosts %+v", hosts)
	}
}

func appMetrics(res *resourcepb.Resource) *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{Resource: res,
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "http.server.request.count",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ts1,
				Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1}}}}}}}}}}}}
}

func appLogs(res *resourcepb.Resource) *collogs.ExportLogsServiceRequest {
	return &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{Resource: res,
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{TimeUnixNano: ts1,
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "hello"}}}}}}}}}
}

func TestAddTracesAPMColumns(t *testing.T) {
	trace := []byte("0123456789abcdef")
	id := func(b byte) []byte { return []byte{b, 0, 0, 0, 0, 0, 0, 1} }
	start := uint64(time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC).UnixNano())
	span := func(sid, parent []byte, name string, kind tracepb.Span_SpanKind, attrs ...*commonpb.KeyValue) *tracepb.Span {
		return &tracepb.Span{TraceId: trace, SpanId: sid, ParentSpanId: parent, Name: name, Kind: kind,
			StartTimeUnixNano: start, EndTimeUnixNano: start + 5e6, Attributes: attrs}
	}
	frontendRoot := span(id(1), nil, "GET /api/orders/:id", tracepb.Span_SPAN_KIND_SERVER,
		strAttr("http.request.method", "GET"), strAttr("http.route", "/api/orders/:id"), intAttr("http.response.status_code", 502))
	frontendClient := span(id(2), id(1), "GET", tracepb.Span_SPAN_KIND_CLIENT,
		strAttr("http.request.method", "GET"), strAttr("server.address", "orders"), intAttr("server.port", 8080))
	frontendClient.TraceState = "ot=th:8"
	ordersServer := span(id(3), id(2), "GET /orders/{id}", tracepb.Span_SPAN_KIND_SERVER,
		strAttr("http.request.method", "GET"), strAttr("url.path", "/orders/34"), intAttr("http.response.status_code", 500))
	ordersServer.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "order 34 failed"}
	ordersServer.Events = []*tracepb.Span_Event{{Name: "exception", TimeUnixNano: start + 1e6, Attributes: []*commonpb.KeyValue{
		strAttr("exception.type", "*errors.errorString"), strAttr("exception.message", "order 34: inventory shard 2 unavailable"),
		strAttr("exception.stacktrace", "main.loadInventory()\n\t/app/main.go:88 +0x1\n")}}}
	// Nested server span of the same service in the same request: not an entry span.
	ordersNested := span(id(4), id(3), "internal server", tracepb.Span_SPAN_KIND_SERVER, strAttr("http.request.method", "GET"))
	// Parent in another batch but flagged remote: entry.
	remoteFlagged := span(id(5), id(9), "consume", tracepb.Span_SPAN_KIND_CONSUMER,
		strAttr("messaging.system", "kafka"), strAttr("messaging.destination.name", "orders"))
	remoteFlagged.Flags = 0x300
	db := span(id(6), id(3), "SELECT orders", tracepb.Span_SPAN_KIND_CLIENT,
		strAttr("db.system", "postgresql"), strAttr("db.name", "orders"), strAttr("db.statement", "SELECT * FROM orders WHERE id = $1 AND x = 'y'"))

	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
		resourceSpans("frontend", frontendRoot, frontendClient),
		resourceSpans("orders", ordersServer, ordersNested, remoteFlagged, db),
	}}
	rows := NewRows()
	rows.AddTraces("t1", time.Unix(0, int64(start)), req)
	if len(rows.Spans) != 6 {
		t.Fatalf("spans: %d", len(rows.Spans))
	}
	got := map[string]apm.Derived{}
	for _, s := range rows.Spans {
		got[s.Name] = s.APM
		if n := len(s.Values()); n != len(Columns[TableSpans]) {
			t.Fatalf("values %d, columns %d", n, len(Columns[TableSpans]))
		}
	}
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("%s: %+v", name, got[name])
		}
	}
	a := got["GET /api/orders/:id"]
	check("frontend root", a.IsEntry && a.TransactionName == "GET /api/orders/:id" && a.TransactionType == "web" &&
		a.IsError && a.HTTPStatusCode == 502 && a.ServiceNamespace == "shop" && a.Environment == "demo" && a.ErrorType == "HTTP 502")
	c := got["GET"]
	check("frontend client", !c.IsEntry && c.PeerType == "external" && c.PeerName == "orders:8080" && c.SampleWeight == 2 && !c.IsError)
	o := got["GET /orders/{id}"]
	check("orders server", o.IsEntry && o.TransactionName == "GET /orders/{id}" && o.IsError && o.ErrorGroupID != 0 &&
		o.ErrorMessage == "order <n>: inventory shard <n> unavailable" && o.ErrorType == "*errors.errorString")
	check("nested server", !got["internal server"].IsEntry)
	r := got["consume"]
	check("remote flagged consumer", r.IsEntry && r.TransactionType == "messaging" && r.TransactionName == "process orders")
	d := got["SELECT orders"]
	check("db", d.DBSystem == "postgresql" && d.DBStatementNormalized == "SELECT * FROM orders WHERE id = ? AND x = ?" &&
		d.PeerType == "db" && d.PeerName == "postgresql/orders" && d.DBOperation == "SELECT")

	// Application resources carry host.id but must not create or overwrite host rows.
	if n := len(rows.Hosts()); n != 0 {
		t.Errorf("app spans with host.id produced %d host rows: %+v", n, rows.Hosts())
	}

	// Conversion is deterministic (dedup tokens): the same request yields identical values.
	again := NewRows()
	again.AddTraces("t1", time.Unix(0, int64(start)), req)
	for i := range rows.Spans {
		if rows.Spans[i].APM != again.Spans[i].APM {
			t.Errorf("span %d not deterministic", i)
		}
	}
}
