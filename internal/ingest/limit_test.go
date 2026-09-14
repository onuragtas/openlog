package ingest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/queue"
)

type fakeLimiter struct {
	decision LimitDecision
	calls    []int
	tenants  []string
}

func (f *fakeLimiter) Allow(tenantID string, _ queue.Signal, bytes int) LimitDecision {
	f.calls = append(f.calls, bytes)
	f.tenants = append(f.tenants, tenantID)
	return f.decision
}

func traceRequest() *coltrace.ExportTraceServiceRequest {
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
		TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "op",
	}}}}}}}
}

func TestQuotaRejectsWith429(t *testing.T) {
	prod := &fakeProducer{}
	s := newTestService(t, prod, 1<<20)
	lim := &fakeLimiter{decision: LimitDecision{RetryAfter: 1500 * time.Millisecond, Message: "monthly ingest quota of plan \"free\" is used up"}}
	s.SetLimiter(lim)
	body, _ := proto.Marshal(traceRequest())
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", ctProtobuf)
	req.Header.Set("openlog-license-key", "goodkey")
	rec := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	var st rpcstatus.Status
	if err := proto.Unmarshal(rec.Body.Bytes(), &st); err != nil || codes.Code(st.Code) != codes.ResourceExhausted || st.Message != lim.decision.Message {
		t.Errorf("body %+v %v", &st, err)
	}
	if len(prod.msgs) != 0 {
		t.Error("rejected request was produced")
	}
	if len(lim.calls) != 1 || lim.calls[0] != len(body) || lim.tenants[0] != "tenant-a" {
		t.Errorf("limiter calls %v %v", lim.calls, lim.tenants)
	}

	// Allowed: produced normally.
	lim.decision = LimitDecision{Allowed: true}
	req = httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", ctProtobuf)
	req.Header.Set("openlog-license-key", "goodkey")
	rec = httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(prod.msgs) == 0 {
		t.Errorf("allowed: status %d produced %d", rec.Code, len(prod.msgs))
	}

	// Unauthenticated requests never reach the limiter.
	req = httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", ctProtobuf)
	rec = httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(lim.calls) != 2 {
		t.Errorf("unauthenticated: %d calls %d", rec.Code, len(lim.calls))
	}
}

func TestQuotaRejectsGRPCWithRetryInfo(t *testing.T) {
	prod := &fakeProducer{}
	s := newTestService(t, prod, 1<<20)
	s.SetLimiter(&fakeLimiter{decision: LimitDecision{RetryAfter: 3 * time.Second, Message: "ingest rate limit exceeded"}})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("openlog-license-key", "goodkey"))
	_, err := traceServer{s: s}.Export(ctx, traceRequest())
	st := status.Convert(err)
	if st.Code() != codes.ResourceExhausted || st.Message() != "ingest rate limit exceeded" {
		t.Fatalf("err %v", err)
	}
	var retry *errdetails.RetryInfo
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			retry = ri
		}
	}
	if retry == nil || retry.RetryDelay.AsDuration() != 3*time.Second {
		t.Errorf("RetryInfo %v", st.Details())
	}
	if len(prod.msgs) != 0 {
		t.Error("rejected request was produced")
	}
}
