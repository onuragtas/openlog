package openloghttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onuragtas/openlog/agents/go/openloghttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }

func benchMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"`+r.PathValue("id")+`"}`)
	})
	return mux
}

func runBench(b *testing.B, h http.Handler) {
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
		}
	})
}

// BenchmarkHandler compares a plain ServeMux with the instrumented one (real SDK: batch span
// processor with a discarding exporter, periodic metric reader).
func BenchmarkHandler(b *testing.B) {
	b.Run("plain", func(b *testing.B) { runBench(b, benchMux()) })
	for _, tc := range []struct {
		name    string
		sampler sdktrace.Sampler
	}{{"instrumented-sampled", sdktrace.AlwaysSample()}, {"instrumented-unsampled", sdktrace.NeverSample()}} {
		b.Run(tc.name, func(b *testing.B) {
			tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(tc.sampler), sdktrace.WithBatcher(discardExporter{}, sdktrace.WithMaxQueueSize(1<<16)))
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
			defer func() { _ = tp.Shutdown(context.Background()) }()
			runBench(b, openloghttp.Middleware(benchMux(), openloghttp.WithTracerProvider(tp), openloghttp.WithMeterProvider(mp)))
		})
	}
}
