// Command grpc runs an instrumented gRPC server (the standard health service) and a client
// that calls it, producing linked client/server spans.
//
//	OPENLOG_LICENSE_KEY=dev-license-key go run ./grpc -n 50
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"time"

	openlog "github.com/onuragtas/openlog/agents/go"
	openloggrpc "github.com/onuragtas/openlog/agents/go/instrumentation/grpc"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	n := flag.Int("n", 20, "number of calls")
	flag.Parse()
	ctx := context.Background()

	shutdown, err := openlog.Start(ctx, openlog.WithServiceName("example-grpc"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	srv := grpc.NewServer(openloggrpc.ServerOption())
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	defer srv.GracefulStop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), openloggrpc.DialOption())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := healthpb.NewHealthClient(conn)
	tracer := otel.Tracer("example/grpc")
	for i := range *n {
		cctx, span := tracer.Start(ctx, "health-probe")
		resp, err := client.Check(cctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			slog.ErrorContext(cctx, "check failed", "error", err)
		} else if i == 0 {
			slog.InfoContext(cctx, "health", "status", resp.GetStatus().String(), "trace_id", span.SpanContext().TraceID().String())
		}
		span.End()
		time.Sleep(50 * time.Millisecond)
	}
}
