package ingest

import (
	"context"
	"errors"
	"math"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/internal/version"
)

func (s *Service) newGRPCServer() *grpc.Server {
	maxMsg := s.cfg.MaxBodyBytes
	if maxMsg > math.MaxInt32 {
		maxMsg = math.MaxInt32
	}
	srv := grpc.NewServer(
		// grpc-go enforces this on the decompressed message and answers RESOURCE_EXHAUSTED.
		grpc.MaxRecvMsgSize(int(maxMsg)),
		grpc.ChainUnaryInterceptor(versionHeaderInterceptor, s.grpcMetricsInterceptor),
	)
	colmetrics.RegisterMetricsServiceServer(srv, metricsServer{s: s})
	collogs.RegisterLogsServiceServer(srv, logsServer{s: s})
	coltrace.RegisterTraceServiceServer(srv, traceServer{s: s})
	return srv
}

// versionHeaderInterceptor sends x-openlog-version as response header metadata
// (docs/contracts/releases-updates.md §5).
func versionHeaderInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	_ = grpc.SetHeader(ctx, metadata.Pairs(version.GRPCHeader, version.String()))
	return h(ctx, req)
}

// GRPCServer exposes the gRPC server (used by tests).
func (s *Service) GRPCServer() *grpc.Server { return s.grpcSrv }

func (s *Service) grpcMetricsInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	resp, err := h(ctx, req)
	sig := "unknown"
	switch req.(type) {
	case *colmetrics.ExportMetricsServiceRequest:
		sig = string(queue.SignalMetrics)
	case *collogs.ExportLogsServiceRequest:
		sig = string(queue.SignalLogs)
	case *coltrace.ExportTraceServiceRequest:
		sig = string(queue.SignalTraces)
	}
	s.m.requests.WithLabelValues(sig, "grpc", status.Code(err).String()).Inc()
	return resp, err
}

func (s *Service) grpcExport(ctx context.Context, sig queue.Signal, msg proto.Message) (prepared, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	key := tenant.KeyFromValues(func(name string) string {
		if v := md.Get(name); len(v) > 0 {
			return v[0]
		}
		return ""
	})
	tenantID, err := s.resolver.Resolve(ctx, key)
	if errors.Is(err, tenant.ErrUnavailable) {
		return prepared{}, unavailableError()
	}
	if err != nil {
		return prepared{}, status.Error(codes.Unauthenticated, "invalid or missing license key")
	}
	p := prepare(sig, tenantID, msg)
	if err := s.export(ctx, sig, tenantID, p, nil); err != nil {
		return prepared{}, unavailableError()
	}
	return p, nil
}

// unavailableError is UNAVAILABLE with a google.rpc.RetryInfo detail, the
// gRPC counterpart of the HTTP Retry-After header (D-014). OTLP exporters
// honour RetryInfo as the minimum retry delay.
func unavailableError() error {
	st := status.New(codes.Unavailable, "backend temporarily unavailable, retry later")
	if withInfo, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)}); err == nil {
		st = withInfo
	}
	return st.Err()
}

type metricsServer struct {
	colmetrics.UnimplementedMetricsServiceServer
	s *Service
}

func (m metricsServer) Export(ctx context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	p, err := m.s.grpcExport(ctx, queue.SignalMetrics, req)
	if err != nil {
		return nil, err
	}
	return partialSuccess(queue.SignalMetrics, p.rejected, p.errMsg).(*colmetrics.ExportMetricsServiceResponse), nil
}

type logsServer struct {
	collogs.UnimplementedLogsServiceServer
	s *Service
}

func (l logsServer) Export(ctx context.Context, req *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	p, err := l.s.grpcExport(ctx, queue.SignalLogs, req)
	if err != nil {
		return nil, err
	}
	return partialSuccess(queue.SignalLogs, p.rejected, p.errMsg).(*collogs.ExportLogsServiceResponse), nil
}

type traceServer struct {
	coltrace.UnimplementedTraceServiceServer
	s *Service
}

func (t traceServer) Export(ctx context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	p, err := t.s.grpcExport(ctx, queue.SignalTraces, req)
	if err != nil {
		return nil, err
	}
	return partialSuccess(queue.SignalTraces, p.rejected, p.errMsg).(*coltrace.ExportTraceServiceResponse), nil
}
