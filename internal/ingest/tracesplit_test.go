package ingest

import (
	"encoding/hex"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestSplitByTrace(t *testing.T) {
	a, b := make([]byte, 16), make([]byte, 16)
	a[15], b[15] = 1, 2
	res1 := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name"}}}
	res2 := &resourcepb.Resource{}
	scope := &commonpb.InstrumentationScope{Name: "s"}
	sp := func(id []byte, n byte) *tracepb.Span {
		return &tracepb.Span{TraceId: id, SpanId: []byte{0, 0, 0, 0, 0, 0, 0, n}}
	}
	single := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
		{Resource: res1, ScopeSpans: []*tracepb.ScopeSpans{{Scope: scope, Spans: []*tracepb.Span{sp(a, 1), sp(a, 2)}}}},
	}}
	if got := splitByTrace("t", single); got != nil {
		t.Fatalf("single trace must not be split, got %d parts", len(got))
	}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
		{Resource: res1, ScopeSpans: []*tracepb.ScopeSpans{{Scope: scope, Spans: []*tracepb.Span{sp(a, 1), sp(b, 2), sp(a, 3)}}}},
		{Resource: res2, ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{sp(b, 4)}}}},
	}}
	parts := splitByTrace("t", req)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].key != "t/"+hex.EncodeToString(a) || parts[1].key != "t/"+hex.EncodeToString(b) {
		t.Fatalf("keys %q %q", parts[0].key, parts[1].key)
	}
	pa := parts[0].req
	if len(pa.ResourceSpans) != 1 || pa.ResourceSpans[0].Resource != res1 || len(pa.ResourceSpans[0].ScopeSpans[0].Spans) != 2 ||
		pa.ResourceSpans[0].ScopeSpans[0].Scope != scope {
		t.Fatalf("trace a grouping wrong: %v", pa)
	}
	pb := parts[1].req
	if len(pb.ResourceSpans) != 2 || pb.ResourceSpans[1].Resource != res2 {
		t.Fatalf("trace b must keep both resources: %v", pb)
	}
}
