package openlogslog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/onuragtas/openlog/agents/go/openlogslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memExporter) Export(_ context.Context, rs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rs {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

func TestWrapAddsTraceIDsAndExports(t *testing.T) {
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	var buf bytes.Buffer
	logger := slog.New(openlogslog.Wrap(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		openlogslog.WithLoggerProvider(lp))).With("component", "checkout")

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "request")
	logger.InfoContext(ctx, "order placed", "order_id", 42)
	logger.DebugContext(ctx, "debug detail") // below the export level: stdout only
	logger.WithGroup("g").ErrorContext(context.Background(), "no span", "k", "v")
	span.End()

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("stdout lines = %d: %s", len(lines), buf.String())
	}
	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	sc := span.SpanContext()
	if first["trace_id"] != sc.TraceID().String() || first["span_id"] != sc.SpanID().String() || first["component"] != "checkout" {
		t.Errorf("stdout record = %v", first)
	}
	if bytes.Contains(lines[2], []byte("trace_id")) {
		t.Error("trace_id added without a span")
	}

	if len(exp.records) != 2 {
		t.Fatalf("exported = %d, want 2 (info + error)", len(exp.records))
	}
	r := exp.records[0]
	if r.TraceID() != sc.TraceID() || r.SpanID() != sc.SpanID() || r.Body().AsString() != "order placed" || r.Severity() != log.SeverityInfo {
		t.Errorf("exported record: trace %v span %v body %q severity %v", r.TraceID(), r.SpanID(), r.Body().AsString(), r.Severity())
	}
	attrs := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool { attrs[string(kv.Key)] = kv.Value.Emit(); return true })
	if attrs["component"] != "checkout" || attrs["order_id"] != "42" {
		t.Errorf("exported attributes = %v", attrs)
	}
}

func TestNewHandlerLevel(t *testing.T) {
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	logger := slog.New(openlogslog.NewHandler(openlogslog.WithLoggerProvider(lp), openlogslog.WithLevel(slog.LevelWarn)))
	logger.Info("dropped")
	logger.Warn("kept")
	if len(exp.records) != 1 || exp.records[0].Body().AsString() != "kept" {
		t.Errorf("records = %d", len(exp.records))
	}
}
