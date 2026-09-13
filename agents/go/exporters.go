package openlog

import (
	"context"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/encoding/gzip"
)

type exporters struct {
	trace  sdktrace.SpanExporter
	metric sdkmetric.Exporter
	log    sdklog.Exporter
}

// newExporters builds the three OTLP exporters. Retries (429/502/503/504 over HTTP,
// UNAVAILABLE/RESOURCE_EXHAUSTED over gRPC) back off exponentially and honor
// Retry-After / RetryInfo from ingest (D-014).
func newExporters(ctx context.Context, cfg *Config) (*exporters, error) {
	u, _ := url.Parse(cfg.Endpoint)
	insecure := u.Scheme == "http"
	base := strings.TrimRight(u.Path, "/")
	r := cfg.retry

	if cfg.Protocol == ProtocolGRPC {
		hostport := u.Host
		var comp string
		if cfg.Compression == "gzip" {
			comp = gzip.Name
		}
		te, err := otlptrace.New(ctx, otlptracegrpc.NewClient(grpcTraceOpts(cfg, hostport, insecure, comp)...))
		if err != nil {
			return nil, err
		}
		mopts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(hostport), otlpmetricgrpc.WithHeaders(cfg.Headers),
			otlpmetricgrpc.WithTimeout(cfg.ExportTimeout),
			otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
		lopts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(hostport), otlploggrpc.WithHeaders(cfg.Headers),
			otlploggrpc.WithTimeout(cfg.ExportTimeout),
			otlploggrpc.WithRetry(otlploggrpc.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
		if insecure {
			mopts = append(mopts, otlpmetricgrpc.WithInsecure())
			lopts = append(lopts, otlploggrpc.WithInsecure())
		}
		if comp != "" {
			mopts = append(mopts, otlpmetricgrpc.WithCompressor(comp))
			lopts = append(lopts, otlploggrpc.WithCompressor(comp))
		}
		me, err := otlpmetricgrpc.New(ctx, mopts...)
		if err != nil {
			return nil, err
		}
		le, err := otlploggrpc.New(ctx, lopts...)
		if err != nil {
			return nil, err
		}
		return &exporters{trace: te, metric: me, log: le}, nil
	}

	topts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(u.Host), otlptracehttp.WithURLPath(base + "/v1/traces"),
		otlptracehttp.WithHeaders(cfg.Headers), otlptracehttp.WithTimeout(cfg.ExportTimeout),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
	mopts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(u.Host), otlpmetrichttp.WithURLPath(base + "/v1/metrics"),
		otlpmetrichttp.WithHeaders(cfg.Headers), otlpmetrichttp.WithTimeout(cfg.ExportTimeout),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
	lopts := []otlploghttp.Option{otlploghttp.WithEndpoint(u.Host), otlploghttp.WithURLPath(base + "/v1/logs"),
		otlploghttp.WithHeaders(cfg.Headers), otlploghttp.WithTimeout(cfg.ExportTimeout),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
	if insecure {
		topts = append(topts, otlptracehttp.WithInsecure())
		mopts = append(mopts, otlpmetrichttp.WithInsecure())
		lopts = append(lopts, otlploghttp.WithInsecure())
	}
	if cfg.Compression == "gzip" {
		topts = append(topts, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
		mopts = append(mopts, otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression))
		lopts = append(lopts, otlploghttp.WithCompression(otlploghttp.GzipCompression))
	}
	te, err := otlptracehttp.New(ctx, topts...)
	if err != nil {
		return nil, err
	}
	me, err := otlpmetrichttp.New(ctx, mopts...)
	if err != nil {
		return nil, err
	}
	le, err := otlploghttp.New(ctx, lopts...)
	if err != nil {
		return nil, err
	}
	return &exporters{trace: te, metric: me, log: le}, nil
}

func grpcTraceOpts(cfg *Config, hostport string, insecure bool, comp string) []otlptracegrpc.Option {
	r := cfg.retry
	o := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(hostport), otlptracegrpc.WithHeaders(cfg.Headers),
		otlptracegrpc.WithTimeout(cfg.ExportTimeout), otlptracegrpc.WithReconnectionPeriod(5 * time.Second),
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{Enabled: true, InitialInterval: r.initial, MaxInterval: r.max, MaxElapsedTime: r.elapsed})}
	if insecure {
		o = append(o, otlptracegrpc.WithInsecure())
	}
	if comp != "" {
		o = append(o, otlptracegrpc.WithCompressor(comp))
	}
	return o
}
