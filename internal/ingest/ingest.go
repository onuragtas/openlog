// Package ingest implements the OTLP receiver (HTTP and gRPC). It
// authenticates license keys, enforces the body limit, converts OTLP/JSON to
// protobuf and produces one Kafka record per export request.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // register gzip decompressor
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/internal/version"
)

// retryAfter is the retry hint returned when Kafka is unavailable (D-014):
// the HTTP Retry-After header and the gRPC google.rpc.RetryInfo delay.
const retryAfter = 5 * time.Second

// Producer is the subset of queue.Producer used by ingest.
type Producer interface {
	Produce(ctx context.Context, msgs ...queue.Message) error
}

// Service is the OTLP receiver.
type Service struct {
	cfg         config.Ingest
	topicPrefix string
	resolver    tenant.Resolver
	producer    Producer
	log         *slog.Logger
	m           *metrics
	now         func() time.Time

	httpSrv *http.Server
	grpcSrv *grpc.Server
	// extraRoutes registers non-OTLP routes on the HTTP listener (agent sync, release mirror).
	extraRoutes func(mux *http.ServeMux)
}

// SetHTTPRoutes adds routes to the OTLP/HTTP listener (e.g. /v1/openlog/agent/sync). Must be
// called before Run.
func (s *Service) SetHTTPRoutes(register func(mux *http.ServeMux)) {
	s.extraRoutes = register
	s.httpSrv.Handler = version.Middleware(s.HTTPHandler())
}

type metrics struct {
	requests *prometheus.CounterVec
	bytes    *prometheus.CounterVec
	rejected *prometheus.CounterVec
	produce  *prometheus.HistogramVec
}

// New creates the service. reg may be nil.
func New(cfg config.Ingest, topicPrefix string, resolver tenant.Resolver, producer Producer, log *slog.Logger, reg prometheus.Registerer) *Service {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_ingest_requests_total", Help: "OTLP export requests by signal, transport and result code.",
		}, []string{"signal", "transport", "code"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_ingest_received_bytes_total", Help: "Uncompressed protobuf bytes produced to Kafka.",
		}, []string{"signal"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_ingest_rejected_items_total", Help: "Items rejected by validation (reported as OTLP partial success).",
		}, []string{"signal"}),
		produce: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "openlog_ingest_produce_duration_seconds", Help: "Kafka produce latency until acknowledgement.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
		}, []string{"signal"}),
	}
	if reg != nil {
		reg.MustRegister(m.requests, m.bytes, m.rejected, m.produce)
	}
	s := &Service{cfg: cfg, topicPrefix: topicPrefix, resolver: resolver, producer: producer, log: log, m: m, now: time.Now}
	s.httpSrv = &http.Server{
		Handler:           version.Middleware(s.HTTPHandler()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.grpcSrv = s.newGRPCServer()
	return s
}

// errUnavailable marks produce failures.
var errUnavailable = errors.New("kafka unavailable")

// export produces a validated request. body, when non-nil, is the original
// protobuf encoding and is forwarded without re-marshalling.
func (s *Service) export(ctx context.Context, sig queue.Signal, tenantID string, p prepared, body []byte) error {
	if p.empty {
		return nil
	}
	if body == nil || p.changed {
		var err error
		body, err = proto.Marshal(p.msg)
		if err != nil {
			return err
		}
	}
	if p.rejected > 0 {
		s.m.rejected.WithLabelValues(string(sig)).Add(float64(p.rejected))
	}
	msg := queue.Message{
		Topic:      queue.Topic(s.topicPrefix, sig),
		Key:        p.key,
		Value:      body,
		TenantID:   tenantID,
		RequestID:  newRequestID(),
		ReceivedAt: s.now(),
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ProduceTimeout)
	defer cancel()
	start := time.Now()
	err := s.producer.Produce(ctx, msg)
	s.m.produce.WithLabelValues(string(sig)).Observe(time.Since(start).Seconds())
	if err != nil {
		s.log.Warn("produce failed", "signal", sig, "tenant_id", tenantID, "err", err)
		return errUnavailable
	}
	s.m.bytes.WithLabelValues(string(sig)).Add(float64(len(body)))
	return nil
}

func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// Run serves OTLP/HTTP and OTLP/gRPC until ctx is cancelled, then shuts down
// gracefully: listeners close, in-flight requests finish (their produce calls
// complete) bounded by shutdownTimeout.
func (s *Service) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	httpLn, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	grpcLn, err := net.Listen("tcp", s.cfg.GRPCAddr)
	if err != nil {
		httpLn.Close()
		return err
	}
	s.log.Info("ingest listening", "http", httpLn.Addr().String(), "grpc", grpcLn.Addr().String())
	errc := make(chan error, 2)
	go func() {
		if err := s.httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	go func() {
		if err := s.grpcSrv.Serve(grpcLn); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errc <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	s.log.Info("ingest shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = s.httpSrv.Shutdown(sctx)
	done := make(chan struct{})
	go func() { s.grpcSrv.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-sctx.Done():
		s.grpcSrv.Stop()
	}
	return err
}

// Ensure the gRPC services are implemented.
var (
	_ colmetrics.MetricsServiceServer = metricsServer{}
	_ collogs.LogsServiceServer       = logsServer{}
	_ coltrace.TraceServiceServer     = traceServer{}
)
