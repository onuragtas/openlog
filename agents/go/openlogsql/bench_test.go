package openlogsql

import (
	"context"
	"database/sql"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }

const benchQuery = "SELECT id, total FROM orders WHERE customer = 'c-123' AND status IN (1, 2, 3)"

func runSQLBench(b *testing.B, db *sql.DB, ctx context.Context) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rows, err := db.QueryContext(ctx, benchQuery)
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
		}
		_ = rows.Close()
	}
}

// BenchmarkQuery compares database/sql on the fake driver with and without instrumentation
// (sampled parent span, batch processor with a discarding exporter).
func BenchmarkQuery(b *testing.B) {
	b.Run("plain", func(b *testing.B) {
		db, _ := sql.Open("fakedb", "")
		defer db.Close()
		runSQLBench(b, db, context.Background())
	})
	for _, mode := range []struct {
		name string
		q    QueryTextMode
	}{{"instrumented-sanitized", QuerySanitized}, {"instrumented-raw", QueryRaw}} {
		b.Run(mode.name, func(b *testing.B) {
			tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(discardExporter{}, sdktrace.WithMaxQueueSize(1<<16)))
			defer func() { _ = tp.Shutdown(context.Background()) }()
			db := sql.OpenDB(mustConnector(b, WrapDriver(&fakeDriver{}, "postgres", WithTracerProvider(tp), WithQueryText(mode.q)), "postgres://db:5432/shop"))
			defer db.Close()
			ctx, span := tp.Tracer("bench").Start(context.Background(), "request")
			defer span.End()
			runSQLBench(b, db, ctx)
		})
	}
	b.Run("sanitize-only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = Sanitize(benchQuery, "postgresql")
		}
	})
}
