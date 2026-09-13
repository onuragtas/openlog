package openloggrpc_test

import (
	"context"
	"net"
	"testing"

	openloggrpc "github.com/onuragtas/openlog/agents/go/instrumentation/grpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestServerAndClientSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	opts := []openloggrpc.Option{openloggrpc.WithTracerProvider(tp), openloggrpc.WithPropagators(propagation.TraceContext{})}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(openloggrpc.ServerOption(opts...))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), openloggrpc.DialOption(opts...))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, parent := tp.Tracer("t").Start(context.Background(), "job")
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	parent.End()
	srv.GracefulStop()

	var client, server sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.SpanKind() {
		case trace.SpanKindClient:
			client = s
		case trace.SpanKindServer:
			server = s
		}
	}
	if client == nil || server == nil {
		t.Fatalf("spans = %v", rec.Ended())
	}
	const name = "grpc.health.v1.Health/Check"
	if client.Name() != name || server.Name() != name {
		t.Errorf("names %q %q", client.Name(), server.Name())
	}
	if client.Parent().SpanID() != parent.SpanContext().SpanID() || server.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Error("parent/child linkage broken")
	}
	found := false
	for _, a := range server.Attributes() {
		if a.Key == "rpc.method" || a.Key == "rpc.system" || a.Key == "rpc.system.name" {
			found = true
		}
	}
	if !found {
		t.Errorf("no rpc.* attributes: %v", server.Attributes())
	}
}
