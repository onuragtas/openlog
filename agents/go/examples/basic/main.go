// Command basic shows the smallest openlog setup: a span, a log line linked to it and a
// custom metric.
//
//	OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 go run ./basic
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	openlog "github.com/onuragtas/openlog/agents/go"
	"github.com/onuragtas/openlog/agents/go/openlogslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func main() {
	ctx := context.Background()
	shutdown, err := openlog.Start(ctx, openlog.WithServiceName("example-basic"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	logger := slog.New(openlogslog.Wrap(slog.NewTextHandler(os.Stdout, nil)))
	tracer := otel.Tracer("example/basic")
	jobs, _ := otel.Meter("example/basic").Int64Counter("example.jobs", metric.WithUnit("{job}"))

	for i := range 3 {
		ctx, span := tracer.Start(ctx, "process-job")
		span.SetAttributes(attribute.Int("job.index", i))
		time.Sleep(20 * time.Millisecond)
		jobs.Add(ctx, 1)
		logger.InfoContext(ctx, "job done", "index", i) // trace_id/span_id are added by the bridge
		span.End()
	}
}
