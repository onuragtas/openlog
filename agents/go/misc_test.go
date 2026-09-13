package openlog

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type syncWriter struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestSampler(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(0.5)), sdktrace.WithSpanProcessor(rec))
	tr := tp.Tracer("t")
	sampled := 0
	for range 2000 {
		ctx, root := tr.Start(context.Background(), "root")
		_, child := tr.Start(ctx, "child")
		child.End()
		root.End()
		if root.SpanContext().IsSampled() {
			sampled++
			if !child.SpanContext().IsSampled() {
				t.Fatal("child of a sampled root not sampled")
			}
		}
	}
	if sampled < 850 || sampled > 1150 {
		t.Errorf("sampled %d of 2000 at ratio 0.5", sampled)
	}
	for _, s := range rec.Ended() {
		has := false
		for _, a := range s.Attributes() {
			if a.Key == SamplingRatioKey && a.Value.AsFloat64() == 0.5 {
				has = true
			}
		}
		if isRoot := !s.Parent().IsValid(); has != isRoot {
			t.Fatalf("span %s root=%v sampling.ratio=%v", s.Name(), isRoot, has)
		}
	}

	// A sampled remote parent is followed regardless of the ratio.
	rec2 := tracetest.NewSpanRecorder()
	tp2 := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(0)), sdktrace.WithSpanProcessor(rec2))
	remote := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true})
	_, s := tp2.Tracer("t").Start(trace.ContextWithRemoteSpanContext(context.Background(), remote), "server")
	s.End()
	if len(rec2.Ended()) != 1 {
		t.Error("sampled remote parent not followed")
	}
	_, s = tp2.Tracer("t").Start(context.Background(), "root")
	s.End()
	if len(rec2.Ended()) != 1 {
		t.Error("ratio 0 sampled a root")
	}
	// Ratio 1 adds no attribute.
	rec3 := tracetest.NewSpanRecorder()
	tp3 := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(1)), sdktrace.WithSpanProcessor(rec3))
	_, s = tp3.Tracer("t").Start(context.Background(), "root")
	s.End()
	if len(rec3.Ended()[0].Attributes()) != 0 {
		t.Error("ratio 1 added attributes")
	}
}

func TestRuntimeProducer(t *testing.T) {
	p := newRuntimeProducer()
	runtime.GC()
	sms, err := p.Produce(context.Background())
	if err != nil || len(sms) != 1 {
		t.Fatal(sms, err)
	}
	eq(t, sms[0].Scope.Name, RuntimeScope)
	got := map[string]metricdata.Metrics{}
	for _, m := range sms[0].Metrics {
		got[m.Name] = m
	}
	for _, n := range []string{"go.memory.used", "go.memory.allocated", "go.memory.allocations", "go.memory.gc.goal",
		"go.goroutine.count", "go.processor.limit", "go.config.gogc", "go.schedule.duration",
		"openlog.go.gc.cycles", "openlog.go.gc.pause.duration"} {
		if _, ok := got[n]; !ok {
			t.Errorf("missing %s", n)
		}
	}
	used := got["go.memory.used"].Data.(metricdata.Sum[int64])
	if len(used.DataPoints) != 2 || used.IsMonotonic || used.DataPoints[1].Value <= 0 {
		t.Errorf("go.memory.used = %+v", used)
	}
	if v := got["openlog.go.gc.cycles"].Data.(metricdata.Sum[int64]); !v.IsMonotonic || v.DataPoints[0].Value < 1 {
		t.Errorf("gc cycles = %+v", v)
	}
	for _, n := range []string{"go.schedule.duration", "openlog.go.gc.pause.duration"} {
		h := got[n].Data.(metricdata.Histogram[float64]).DataPoints[0]
		var sum uint64
		for _, c := range h.BucketCounts {
			sum += c
		}
		if sum != h.Count || len(h.BucketCounts) != len(h.Bounds)+1 {
			t.Errorf("%s: count %d, bucket sum %d, buckets %d, bounds %d", n, h.Count, sum, len(h.BucketCounts), len(h.Bounds))
		}
	}
	if n := got["openlog.go.gc.pause.duration"].Data.(metricdata.Histogram[float64]).DataPoints[0].Count; n == 0 {
		t.Error("no GC pauses after runtime.GC()")
	}
}

func TestErrorHandlerRateLimits(t *testing.T) {
	var buf strings.Builder
	h := newErrorHandler(slog.New(slog.NewTextHandler(&buf, nil)))
	now := time.Unix(0, 0)
	h.now = func() time.Time { return now }
	for range 10 {
		h.Handle(errors.New("connection refused"))
	}
	if c := strings.Count(buf.String(), "connection refused"); c != 1 {
		t.Errorf("logged %d times", c)
	}
	now = now.Add(2 * time.Minute)
	h.Handle(errors.New("connection refused"))
	if !strings.Contains(buf.String(), "suppressed_repeats=9") {
		t.Errorf("log = %s", buf.String())
	}
}
