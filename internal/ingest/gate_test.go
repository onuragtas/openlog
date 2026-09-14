package ingest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type fakeGate struct {
	suspended map[string]bool
	reject    map[string]bool
	limit     int64
	sources   []string
}

func (g *fakeGate) Suspended(tenant string) (string, bool) { return "abuse", g.suspended[tenant] }

func (g *fakeGate) RejectHosts(_ string, hosts []string) (map[string]bool, int64) {
	out := map[string]bool{}
	for _, h := range hosts {
		if g.reject[h] {
			out[h] = true
		}
	}
	return out, g.limit
}

func (g *fakeGate) ObserveSource(_ string, addr string) { g.sources = append(g.sources, addr) }

func hostLogs(hosts ...string) *collogs.ExportLogsServiceRequest {
	req := &collogs.ExportLogsServiceRequest{}
	for _, h := range hosts {
		req.ResourceLogs = append(req.ResourceLogs, &logspb.ResourceLogs{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "host.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: h}}}}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "a"}}},
				{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "b"}}}}}},
		})
	}
	return req
}

func postLogs(t *testing.T, s *Service, req proto.Message) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := proto.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctProtobuf)
	r.Header.Set("openlog-license-key", "goodkey")
	rec := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(rec, r)
	return rec
}

func errorReason(t *testing.T, st *rpcstatus.Status) string {
	t.Helper()
	for _, d := range st.GetDetails() {
		var info errdetails.ErrorInfo
		if d.MessageIs(&info) {
			if err := d.UnmarshalTo(&info); err != nil {
				t.Fatal(err)
			}
			return info.Reason
		}
	}
	return ""
}

func TestGateSuspendedOrganizationHTTP403(t *testing.T) {
	prod := &fakeProducer{}
	s := newTestService(t, prod, 1<<20)
	g := &fakeGate{suspended: map[string]bool{}}
	s.SetGate(g)
	rec := postLogs(t, s, hostLogs("h1"))
	if rec.Code != http.StatusOK || len(g.sources) != 1 {
		t.Fatalf("active organization: status %d sources %v", rec.Code, g.sources)
	}
	g.suspended = map[string]bool{"tenant-a": true} // the tenant of goodkey (newTestService)
	n := len(prod.msgs)
	rec = postLogs(t, s, hostLogs("h1"))
	var st rpcstatus.Status
	if err := proto.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden || codes.Code(st.Code) != codes.PermissionDenied || errorReason(t, &st) != ReasonOrgSuspended {
		t.Fatalf("suspended: status %d body %+v (tenant of goodkey must be in the suspended set)", rec.Code, &st)
	}
	if len(prod.msgs) != n {
		t.Error("suspended request was produced")
	}
}

func TestGateHostLimitHTTP(t *testing.T) {
	prod := &fakeProducer{}
	s := newTestService(t, prod, 1<<20)
	s.SetGate(&fakeGate{reject: map[string]bool{"new-host": true}, limit: 5})

	rec := postLogs(t, s, hostLogs("new-host"))
	var st rpcstatus.Status
	if err := proto.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || codes.Code(st.Code) != codes.ResourceExhausted ||
		errorReason(t, &st) != ReasonQuotaExceeded || !bytes.Contains([]byte(st.Message), []byte("host limit of 5 hosts")) {
		t.Fatalf("all hosts over the limit: status %d body %+v", rec.Code, &st)
	}
	if len(prod.msgs) != 0 {
		t.Fatal("rejected host was produced")
	}

	rec = postLogs(t, s, hostLogs("old-host", "new-host"))
	if rec.Code != http.StatusOK || len(prod.msgs) != 1 {
		t.Fatalf("mixed request: status %d produced %d", rec.Code, len(prod.msgs))
	}
	var resp collogs.ExportLogsServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetPartialSuccess().GetRejectedLogRecords() != 2 {
		t.Errorf("partial success %+v", resp.GetPartialSuccess())
	}
	var produced collogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(prod.msgs[0].Value, &produced); err != nil || len(produced.ResourceLogs) != 1 {
		t.Fatalf("produced %d resources, %v", len(produced.ResourceLogs), err)
	}
}

func TestGateGRPC(t *testing.T) {
	prod := &fakeProducer{}
	s := newTestService(t, prod, 1<<20)
	g := &fakeGate{reject: map[string]bool{"new-host": true}, limit: 1, suspended: map[string]bool{}}
	s.SetGate(g)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("openlog-license-key", "goodkey"))
	_, err := logsServer{s: s}.Export(ctx, hostLogs("new-host"))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("host limit: %v", err)
	}
	g.suspended["tenant-a"] = true
	_, err = logsServer{s: s}.Export(ctx, hostLogs("old-host"))
	st, _ := status.FromError(err)
	reason := ""
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			reason = info.Reason
		}
	}
	if st.Code() != codes.PermissionDenied || reason != ReasonOrgSuspended {
		t.Fatalf("suspended: %v", err)
	}
}
