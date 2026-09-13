package openloghttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onuragtas/openlog/agents/go/openloghttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func setup() (*tracetest.SpanRecorder, *sdkmetric.ManualReader, []openloghttp.Option) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return rec, reader, []openloghttp.Option{openloghttp.WithTracerProvider(tp), openloghttp.WithMeterProvider(mp),
		openloghttp.WithPropagators(propagation.TraceContext{})}
}

func attrs(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestMiddlewareServeMuxPattern(t *testing.T) {
	rec, reader, opts := setup()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := openloghttp.Middleware(mux, opts...)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "GET /users/{id}" || s.SpanKind() != trace.SpanKindServer {
		t.Errorf("name %q kind %v", s.Name(), s.SpanKind())
	}
	a := attrs(s.Attributes())
	if a["http.route"].AsString() != "/users/{id}" || a["http.request.method"].AsString() != "GET" ||
		a["http.response.status_code"].AsInt64() != 418 || a["url.path"].AsString() != "/users/42" {
		t.Errorf("attributes = %v", a)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				if v, ok := dp.Attributes.Value("http.route"); ok && v.AsString() == "/users/{id}" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("http.server.request.duration lacks http.route")
	}
}

func TestHandlerStaticRouteAndUnmatched(t *testing.T) {
	rec, _, opts := setup()
	h := openloghttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}, "/health", opts...)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/health?x=1", nil))
	s := rec.Ended()[0]
	if s.Name() != "POST /health" || attrs(s.Attributes())["http.route"].AsString() != "/health" {
		t.Errorf("name %q attrs %v", s.Name(), s.Attributes())
	}

	rec, _, opts = setup()
	openloghttp.Middleware(http.NewServeMux(), opts...).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	s = rec.Ended()[0]
	if s.Name() != "GET" {
		t.Errorf("unmatched request span name %q", s.Name())
	}
	if _, ok := attrs(s.Attributes())["http.route"]; ok {
		t.Error("unmatched request has http.route")
	}
}

func TestTransportPropagatesContext(t *testing.T) {
	rec, _, opts := setup()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	srv := httptest.NewServer(openloghttp.Middleware(mux, opts...))
	defer srv.Close()

	client := &http.Client{Transport: openloghttp.Transport(nil, opts...)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/items/7", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	var client0, server0 sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.SpanKind() {
		case trace.SpanKindClient:
			client0 = s
		case trace.SpanKindServer:
			server0 = s
		}
	}
	if client0 == nil || server0 == nil {
		t.Fatalf("spans = %v", rec.Ended())
	}
	if server0.Parent().SpanID() != client0.SpanContext().SpanID() || server0.SpanContext().TraceID() != client0.SpanContext().TraceID() {
		t.Error("server span is not a child of the client span")
	}
	if a := attrs(client0.Attributes()); a["http.response.status_code"].AsInt64() != 200 || a["server.address"].AsString() == "" {
		t.Errorf("client attributes = %v", a)
	}
}
