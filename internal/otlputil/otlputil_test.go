package otlputil

import (
	"testing"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func strKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func res(kvs ...*commonpb.KeyValue) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: kvs}
}

func TestMetricsPartitionKey(t *testing.T) {
	cases := []struct {
		name string
		rs   []*resourcepb.Resource
		want string
	}{
		{"host on second resource wins over service on first", []*resourcepb.Resource{res(strKV("service.name", "svc")), res(strKV("host.id", "h2"))}, "t1/h2"},
		{"first host", []*resourcepb.Resource{res(strKV("host.id", "h1")), res(strKV("host.id", "h2"))}, "t1/h1"},
		{"service fallback", []*resourcepb.Resource{res(strKV("service.name", "svc"))}, "t1/svc"},
		{"empty", []*resourcepb.Resource{res()}, "t1/"},
		{"no resources", nil, "t1/"},
	}
	for _, c := range cases {
		req := &colmetrics.ExportMetricsServiceRequest{}
		lreq := &collogs.ExportLogsServiceRequest{}
		for _, r := range c.rs {
			req.ResourceMetrics = append(req.ResourceMetrics, &metricspb.ResourceMetrics{Resource: r})
			lreq.ResourceLogs = append(lreq.ResourceLogs, &logspb.ResourceLogs{Resource: r})
		}
		if got := MetricsPartitionKey("t1", req); got != c.want {
			t.Errorf("%s: metrics key = %q, want %q", c.name, got, c.want)
		}
		if got := LogsPartitionKey("t1", lreq); got != c.want {
			t.Errorf("%s: logs key = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTracesPartitionKey(t *testing.T) {
	tid := []byte{0x0a, 0xf7, 0x65, 0x19, 0x16, 0xcd, 0x43, 0xdd, 0x84, 0x48, 0xeb, 0x21, 0x1c, 0x80, 0x31, 0x9c}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
		{ScopeSpans: []*tracepb.ScopeSpans{{}}},
		{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{TraceId: tid}, {TraceId: make([]byte, 16)}}}}},
	}}
	if got := TracesPartitionKey("t", req); got != "t/0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("got %q", got)
	}
	if got := TracesPartitionKey("t", &coltrace.ExportTraceServiceRequest{}); got != "t/" {
		t.Errorf("empty got %q", got)
	}
}

func TestAnyValueString(t *testing.T) {
	arr := &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{
		{Value: &commonpb.AnyValue_IntValue{IntValue: 1}},
		{Value: &commonpb.AnyValue_StringValue{StringValue: "x"}},
	}}}}
	cases := map[string]*commonpb.AnyValue{
		"":        nil,
		"true":    {Value: &commonpb.AnyValue_BoolValue{BoolValue: true}},
		"-5":      {Value: &commonpb.AnyValue_IntValue{IntValue: -5}},
		"0.25":    {Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 0.25}},
		"AQI=":    {Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{1, 2}}},
		`[1,"x"]`: arr,
	}
	for want, v := range cases {
		if got := AnyValueString(v); got != want {
			t.Errorf("AnyValueString = %q, want %q", got, want)
		}
	}
	if HexID(make([]byte, 8)) != "" || HexID([]byte{0, 1}) != "0001" {
		t.Error("HexID")
	}
}
