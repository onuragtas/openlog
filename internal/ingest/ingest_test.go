package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tenant"
)

type fakeProducer struct {
	mu   sync.Mutex
	msgs []queue.Message
	err  error
}

func (f *fakeProducer) Produce(ctx context.Context, msgs ...queue.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, msgs...)
	return nil
}

func newTestService(t *testing.T, p Producer, maxBody int64) *Service {
	t.Helper()
	res, err := tenant.ParseStatic("goodkey=tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Ingest{MaxBodyBytes: maxBody, ProduceTimeout: time.Second, HTTPAddr: "127.0.0.1:0", GRPCAddr: "127.0.0.1:0"}
	return New(cfg, "openlog", res, p, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
}

func metricsReq() *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			{Key: "host.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "host-1"}}},
		}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "system.uptime", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: 1, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 3}}}}}},
			{Name: "", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{}, {}}}}},
		}}},
	}}}
}

func post(h http.Handler, path, ct, key string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPProtobufGzipAndPartialSuccess(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	raw, _ := proto.Marshal(metricsReq())
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(raw)
	w.Close()

	rec := post(s.HTTPHandler(), "/v1/metrics", ctProtobuf, "goodkey", gz.Bytes(), map[string]string{"Content-Encoding": "gzip"})
	if rec.Code != 200 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	var resp colmetrics.ExportMetricsServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetPartialSuccess().GetRejectedDataPoints() != 2 {
		t.Errorf("rejected = %d", resp.GetPartialSuccess().GetRejectedDataPoints())
	}
	if len(fp.msgs) != 1 {
		t.Fatalf("produced %d", len(fp.msgs))
	}
	m := fp.msgs[0]
	if m.Topic != "openlog.otlp.metrics.v1" || m.Key != "tenant-a/host-1" || m.TenantID != "tenant-a" || m.RequestID == "" {
		t.Errorf("bad message %+v", m)
	}
	var produced colmetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(m.Value, &produced); err != nil {
		t.Fatal(err)
	}
	if n := len(produced.ResourceMetrics[0].ScopeMetrics[0].Metrics); n != 1 {
		t.Errorf("produced metrics = %d, want invalid one filtered", n)
	}
	rh := m.Record().Headers
	if len(rh) != 4 || rh[0].Key != queue.HeaderSchemaVersion || string(rh[0].Value) != "1" {
		t.Errorf("headers %v", rh)
	}
}

func TestHTTPJSONHexIDs(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
	 "scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"GET /","kind":2,
	 "startTimeUnixNano":"1544712660000000000","endTimeUnixNano":1544712661000000000}]}]}]}`
	rec := post(s.HTTPHandler(), "/v1/traces", "application/json; charset=utf-8", "goodkey", []byte(body), nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ctJSON {
		t.Errorf("content-type %q", ct)
	}
	if len(fp.msgs) != 1 || fp.msgs[0].Key != "tenant-a/5b8efff798038103d269b633813fc60c" {
		t.Fatalf("msgs %+v", fp.msgs)
	}
	var req coltrace.ExportTraceServiceRequest
	proto.Unmarshal(fp.msgs[0].Value, &req)
	sp := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.StartTimeUnixNano != 1544712660000000000 || sp.EndTimeUnixNano != 1544712661000000000 || sp.Kind != tracepb.Span_SPAN_KIND_SERVER || len(sp.SpanId) != 8 {
		t.Errorf("span %+v", sp)
	}
}

func TestHTTPErrors(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 64)
	h := s.HTTPHandler()
	small, _ := proto.Marshal(&collogs.ExportLogsServiceRequest{})

	if rec := post(h, "/v1/logs", ctProtobuf, "", small, nil); rec.Code != 401 {
		t.Errorf("missing key: %d", rec.Code)
	}
	if rec := post(h, "/v1/logs", ctProtobuf, "wrong", small, nil); rec.Code != 401 {
		t.Errorf("wrong key: %d", rec.Code)
	}
	if rec := post(h, "/v1/logs", ctProtobuf, "goodkey", bytes.Repeat([]byte{0}, 65), nil); rec.Code != 413 {
		t.Errorf("too large: %d", rec.Code)
	}
	// Small on the wire, large after decompression.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(bytes.Repeat([]byte("a"), 10000))
	w.Close()
	if rec := post(h, "/v1/logs", ctProtobuf, "goodkey", gz.Bytes(), map[string]string{"Content-Encoding": "gzip"}); rec.Code != 413 {
		t.Errorf("gzip bomb: %d", rec.Code)
	}
	if rec := post(h, "/v1/logs", "text/plain", "goodkey", small, nil); rec.Code != 415 {
		t.Errorf("content type: %d", rec.Code)
	}
	if rec := post(h, "/v1/logs", ctJSON, "goodkey", []byte("{not json"), nil); rec.Code != 400 {
		t.Errorf("bad json: %d", rec.Code)
	}

	fp.err = errors.New("broker down")
	req, _ := proto.Marshal(metricsReq())
	s2 := newTestService(t, fp, 1<<20)
	rec := post(s2.HTTPHandler(), "/v1/metrics", ctProtobuf, "goodkey", req, nil)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("produce failure: code %d, Retry-After %q (want 503, \"5\")", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestGRPC(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	ln := bufconn.Listen(1 << 20)
	go s.GRPCServer().Serve(ln)
	defer s.GRPCServer().Stop()
	conn, err := grpc.NewClient("passthrough:///buf",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(4<<20)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := colmetrics.NewMetricsServiceClient(conn)

	_, err = client.Export(context.Background(), metricsReq())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("no key: %v", err)
	}

	ctx := metadata.AppendToOutgoingContext(context.Background(), "openlog-license-key", "goodkey")
	resp, err := client.Export(ctx, metricsReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPartialSuccess().GetRejectedDataPoints() != 2 || len(fp.msgs) != 1 {
		t.Errorf("resp %v msgs %d", resp, len(fp.msgs))
	}

	big := metricsReq()
	big.ResourceMetrics[0].Resource.Attributes[0].Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: strings.Repeat("x", 2<<20)}}
	if _, err = client.Export(ctx, big); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("big: %v", err)
	}

	fp.err = errors.New("down")
	_, err = client.Export(ctx, metricsReq())
	var retry *errdetails.RetryInfo
	for _, d := range status.Convert(err).Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			retry = ri
		}
	}
	if retry == nil || retry.GetRetryDelay().AsDuration() != retryAfter {
		t.Errorf("UNAVAILABLE without RetryInfo(%s): %v", retryAfter, status.Convert(err).Details())
	}
	if status.Code(err) != codes.Unavailable {
		t.Errorf("unavailable: %v", err)
	}
}
