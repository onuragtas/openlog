package openlog

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/go/internal/fakeotlp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func baseOpts(endpoint string) []Option {
	return []Option{
		WithEndpoint(endpoint), WithLicenseKey("test-license"), WithServiceName("svc"), WithHostID("host-1"),
		WithMetricInterval(time.Hour), withRetry(10*time.Millisecond, 50*time.Millisecond, 5*time.Second),
	}
}

func attrMap(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		if sv, ok := kv.Value.Value.(*commonpb.AnyValue_StringValue); ok {
			m[kv.Key] = sv.StringValue
		} else {
			m[kv.Key] = kv.Value.String()
		}
	}
	return m
}

func TestHTTPExportHeadersGzipResourceAndShutdownFlush(t *testing.T) {
	srv := fakeotlp.NewHTTP()
	defer srv.Close()
	shutdown, err := start(context.Background(), env(nil), io.Discard, baseOpts(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := otel.Tracer("test").Start(context.Background(), "work")
	var rec otellog.Record
	rec.SetBody(attribute.StringValue("hello"))
	rec.SetSeverity(otellog.SeverityInfo)
	global.GetLoggerProvider().Logger("test").Emit(ctx, rec)
	span.End()
	traceID := span.SpanContext().TraceID()

	// Nothing is exported before the batch timeout; shutdown must flush everything.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		reqs := srv.Requests(path)
		if len(reqs) == 0 {
			t.Fatalf("no %s request", path)
		}
		r := reqs[0]
		eq(t, r.Header.Get(LicenseKeyHeader), "test-license")
		eq(t, r.ContentEncoding, "gzip")
		eq(t, r.Header.Get("Content-Type"), "application/x-protobuf")
	}

	spans := srv.Spans()
	if len(spans) != 1 || len(spans[0].ScopeSpans) == 0 || spans[0].ScopeSpans[0].Spans[0].Name != "work" {
		t.Fatalf("spans = %v", spans)
	}
	res := attrMap(spans[0].Resource.Attributes)
	for k, v := range map[string]string{"service.name": "svc", "host.id": "host-1", "telemetry.distro.name": "openlog",
		"telemetry.distro.version": Version, "telemetry.sdk.language": "go"} {
		eq(t, res[k], v)
	}

	logs := srv.Logs()
	if len(logs) == 0 || len(logs[0].ScopeLogs) == 0 {
		t.Fatal("no logs")
	}
	lr := logs[0].ScopeLogs[0].LogRecords[0]
	eq(t, trace.TraceID(lr.TraceId), traceID)
	eq(t, lr.Body.GetStringValue(), "hello")

	names := map[string]bool{}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				names[m.Name] = true
			}
		}
	}
	for _, n := range []string{"go.memory.used", "go.goroutine.count", "go.schedule.duration", "openlog.go.gc.cycles"} {
		if !names[n] {
			t.Errorf("runtime metric %s not exported (got %v)", n, names)
		}
	}
}

func TestHTTPRetryHonorsRetryAfter(t *testing.T) {
	srv := fakeotlp.NewHTTP()
	defer srv.Close()
	srv.Enqueue("/v1/traces", fakeotlp.Response{Status: http.StatusServiceUnavailable, RetryAfter: "1"})
	shutdown, err := start(context.Background(), env(nil), io.Discard, append(baseOpts(srv.URL), WithRuntimeMetrics(false)))
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "retry-me")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	reqs := srv.Requests("/v1/traces")
	if len(reqs) != 2 {
		t.Fatalf("trace requests = %d, want 2 (503 then retry)", len(reqs))
	}
	if gap := reqs[1].At.Sub(reqs[0].At); gap < 900*time.Millisecond {
		t.Errorf("retry after %v, want >= 1s (Retry-After)", gap)
	}
	if len(srv.Spans()) != 2 { // both bodies decoded: the rejected one and the retried one
		t.Errorf("decoded spans = %d", len(srv.Spans()))
	}
}

func TestExportFailureIsNotFatalAndShutdownIsBounded(t *testing.T) {
	srv := fakeotlp.NewHTTP()
	url := srv.URL
	srv.Close() // nothing listens any more
	var diag strings.Builder
	shutdown, err := start(context.Background(), env(nil), &syncWriter{b: &diag},
		append(baseOpts(url), WithShutdownTimeout(700*time.Millisecond), withRetry(50*time.Millisecond, 100*time.Millisecond, 30*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		_, span := otel.Tracer("test").Start(context.Background(), "lost")
		span.End()
	}
	began := time.Now()
	_ = shutdown(context.Background()) // an error is expected; a hang or panic is not
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("shutdown took %v with a 700ms timeout", d)
	}
}

func TestGRPCExport(t *testing.T) {
	g, err := fakeotlp.NewGRPC()
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	shutdown, err := start(context.Background(), env(nil), io.Discard,
		append(baseOpts("http://"+g.Addr), WithProtocol(ProtocolGRPC), WithRuntimeMetrics(false)))
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "grpc-work")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	spans := g.Spans()
	if len(spans) != 1 || spans[0].ScopeSpans[0].Spans[0].Name != "grpc-work" {
		t.Fatalf("spans = %v", spans)
	}
	md := g.Metadata()
	if len(md) == 0 || strings.Join(md[0].Get(LicenseKeyHeader), "") != "test-license" {
		t.Errorf("metadata = %v", md)
	}
}

func TestStartDisabledAndTwice(t *testing.T) {
	shutdown, err := start(context.Background(), env(map[string]string{"OPENLOG_ENABLED": "false"}), io.Discard, nil)
	if err != nil || shutdown(context.Background()) != nil {
		t.Fatal("disabled start failed")
	}
	srv := fakeotlp.NewHTTP()
	defer srv.Close()
	shutdown, err = start(context.Background(), env(nil), io.Discard, baseOpts(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := start(context.Background(), env(nil), io.Discard, baseOpts(srv.URL)); err != ErrAlreadyStarted {
		t.Errorf("second start: %v", err)
	}
	_ = shutdown(context.Background())
	shutdown, err = start(context.Background(), env(nil), io.Discard, baseOpts(srv.URL))
	if err != nil {
		t.Errorf("start after shutdown: %v", err)
	}
	_ = shutdown(context.Background())
}
