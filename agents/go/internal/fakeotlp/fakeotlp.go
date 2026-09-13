// Package fakeotlp is an in-process OTLP receiver (HTTP and gRPC) for tests.
package fakeotlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // server-side gzip decompression
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// Request is one received OTLP/HTTP request.
type Request struct {
	Path            string
	Header          http.Header
	ContentEncoding string
	Body            []byte // decompressed
	At              time.Time
}

// Response is a scripted reply; the zero value is 200.
type Response struct {
	Status     int
	RetryAfter string
}

// HTTP is a fake OTLP/HTTP receiver.
type HTTP struct {
	*httptest.Server

	mu        sync.Mutex
	requests  []Request
	responses map[string][]Response
	notify    chan struct{}
}

// NewHTTP starts a receiver; Close it when done.
func NewHTTP() *HTTP {
	h := &HTTP{notify: make(chan struct{}, 1024), responses: map[string][]Response{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// Enqueue scripts the next responses for path, e.g. /v1/traces (consumed in order, then 200).
func (h *HTTP) Enqueue(path string, rs ...Response) {
	h.mu.Lock()
	h.responses[path] = append(h.responses[path], rs...)
	h.mu.Unlock()
}

func (h *HTTP) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := raw
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ = io.ReadAll(zr)
	}
	h.mu.Lock()
	h.requests = append(h.requests, Request{Path: r.URL.Path, Header: r.Header.Clone(),
		ContentEncoding: r.Header.Get("Content-Encoding"), Body: body, At: time.Now()})
	resp := Response{Status: http.StatusOK}
	if q := h.responses[r.URL.Path]; len(q) > 0 {
		resp = q[0]
		h.responses[r.URL.Path] = q[1:]
	}
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
	if resp.RetryAfter != "" {
		w.Header().Set("Retry-After", resp.RetryAfter)
	}
	if resp.Status != http.StatusOK && resp.Status != 0 {
		w.WriteHeader(resp.Status)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	var out proto.Message
	switch r.URL.Path {
	case "/v1/traces":
		out = &coltrace.ExportTraceServiceResponse{}
	case "/v1/metrics":
		out = &colmetrics.ExportMetricsServiceResponse{}
	default:
		out = &collogs.ExportLogsServiceResponse{}
	}
	b, _ := proto.Marshal(out)
	_, _ = w.Write(b)
}

// Requests returns received requests for path ("" = all).
func (h *HTTP) Requests(path string) []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Request
	for _, r := range h.requests {
		if path == "" || r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// WaitFor waits until n requests for path arrived or the timeout elapsed.
func (h *HTTP) WaitFor(path string, n int, timeout time.Duration) []Request {
	deadline := time.After(timeout)
	for {
		if rs := h.Requests(path); len(rs) >= n {
			return rs
		}
		select {
		case <-h.notify:
		case <-deadline:
			return h.Requests(path)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Spans decodes every successfully received trace request.
func (h *HTTP) Spans() []*tracepb.ResourceSpans {
	var out []*tracepb.ResourceSpans
	for _, r := range h.Requests("/v1/traces") {
		var m coltrace.ExportTraceServiceRequest
		if proto.Unmarshal(r.Body, &m) == nil {
			out = append(out, m.ResourceSpans...)
		}
	}
	return out
}

// Metrics decodes every received metrics request.
func (h *HTTP) Metrics() []*metricspb.ResourceMetrics {
	var out []*metricspb.ResourceMetrics
	for _, r := range h.Requests("/v1/metrics") {
		var m colmetrics.ExportMetricsServiceRequest
		if proto.Unmarshal(r.Body, &m) == nil {
			out = append(out, m.ResourceMetrics...)
		}
	}
	return out
}

// Logs decodes every received logs request.
func (h *HTTP) Logs() []*logspb.ResourceLogs {
	var out []*logspb.ResourceLogs
	for _, r := range h.Requests("/v1/logs") {
		var m collogs.ExportLogsServiceRequest
		if proto.Unmarshal(r.Body, &m) == nil {
			out = append(out, m.ResourceLogs...)
		}
	}
	return out
}

// GRPC is a fake OTLP/gRPC receiver.
type GRPC struct {
	Addr string
	srv  *grpc.Server

	mu     sync.Mutex
	md     []metadata.MD
	spans  []*tracepb.ResourceSpans
	nTrace int
}

// NewGRPC listens on 127.0.0.1:0.
func NewGRPC() (*GRPC, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &GRPC{Addr: lis.Addr().String(), srv: grpc.NewServer()}
	coltrace.RegisterTraceServiceServer(g.srv, &traceSvc{g: g})
	colmetrics.RegisterMetricsServiceServer(g.srv, &metricsSvc{g: g})
	collogs.RegisterLogsServiceServer(g.srv, &logsSvc{g: g})
	go func() { _ = g.srv.Serve(lis) }()
	return g, nil
}

// Close stops the server.
func (g *GRPC) Close() { g.srv.Stop() }

func (g *GRPC) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	g.mu.Lock()
	g.md = append(g.md, md)
	g.mu.Unlock()
}

// Metadata returns the metadata of every call.
func (g *GRPC) Metadata() []metadata.MD {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]metadata.MD(nil), g.md...)
}

// Spans returns received resource spans.
func (g *GRPC) Spans() []*tracepb.ResourceSpans {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*tracepb.ResourceSpans(nil), g.spans...)
}

type traceSvc struct {
	coltrace.UnimplementedTraceServiceServer
	g *GRPC
}

func (s *traceSvc) Export(ctx context.Context, r *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	s.g.record(ctx)
	s.g.mu.Lock()
	s.g.spans = append(s.g.spans, r.ResourceSpans...)
	s.g.mu.Unlock()
	return &coltrace.ExportTraceServiceResponse{}, nil
}

type metricsSvc struct {
	colmetrics.UnimplementedMetricsServiceServer
	g *GRPC
}

func (s *metricsSvc) Export(ctx context.Context, _ *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	s.g.record(ctx)
	return &colmetrics.ExportMetricsServiceResponse{}, nil
}

type logsSvc struct {
	collogs.UnimplementedLogsServiceServer
	g *GRPC
}

func (s *logsSvc) Export(ctx context.Context, _ *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	s.g.record(ctx)
	return &collogs.ExportLogsServiceResponse{}, nil
}
