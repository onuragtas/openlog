// Package openloggrpc instruments gRPC servers and clients with OpenTelemetry stats
// handlers (otelgrpc), using the providers installed by openlog.Start.
//
//	srv := grpc.NewServer(openloggrpc.ServerOption())
//	conn, err := grpc.NewClient(addr, openloggrpc.DialOption(), ...)
package openloggrpc

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// Option configures the instrumentation (otelgrpc options: WithFilter, WithTracerProvider, …).
type Option = otelgrpc.Option

// Re-exported otelgrpc options.
var (
	WithFilter         = otelgrpc.WithFilter
	WithTracerProvider = otelgrpc.WithTracerProvider
	WithMeterProvider  = otelgrpc.WithMeterProvider
	WithPropagators    = otelgrpc.WithPropagators
	WithMessageEvents  = otelgrpc.WithMessageEvents
)

// ServerOption returns a grpc.ServerOption that creates a server span and RPC server metrics per call.
func ServerOption(opts ...Option) grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(opts...))
}

// DialOption returns a grpc.DialOption that creates client spans and propagates trace context.
func DialOption(opts ...Option) grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler(opts...))
}

// ServerHandler returns the server stats.Handler for custom wiring.
func ServerHandler(opts ...Option) stats.Handler { return otelgrpc.NewServerHandler(opts...) }

// ClientHandler returns the client stats.Handler for custom wiring.
func ClientHandler(opts ...Option) stats.Handler { return otelgrpc.NewClientHandler(opts...) }
