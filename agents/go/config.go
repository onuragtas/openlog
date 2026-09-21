package openlog

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Protocol values for OPENLOG_PROTOCOL / WithProtocol.
const (
	ProtocolHTTP = "http/protobuf"
	ProtocolGRPC = "grpc"
)

// LicenseKeyHeader is the ingest authentication header (docs/contracts/config.md).
const LicenseKeyHeader = "openlog-license-key"

const (
	defaultHTTPEndpoint    = "http://localhost:4318"
	defaultGRPCEndpoint    = "http://localhost:4317"
	defaultMetricInterval  = 60 * time.Second
	defaultProfileInterval = 60 * time.Second
	minProfileInterval     = 10 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	defaultExportTimeout   = 10 * time.Second
	defaultInfraStateDir   = "/var/lib/openlog-infra-agent"
	defaultInfraRuntimeDir = "/run/openlog-infra-agent"
)

// Config is the resolved agent configuration. Precedence per field:
// Option > OPENLOG_* environment > OTEL_* environment > default.
type Config struct {
	Enabled          bool
	LicenseKey       string
	Endpoint         string // base URL; http/protobuf appends /v1/{traces,metrics,logs}
	Protocol         string // ProtocolHTTP or ProtocolGRPC
	Compression      string // "gzip" or "none"
	Headers          map[string]string
	ServiceName      string
	ServiceVersion   string
	ServiceNamespace string
	Environment      string // deployment.environment.name
	SamplingRatio    float64
	// SamplingRV makes new traces carry explicit randomness (tracestate ot=rv) instead of relying
	// on the random trace id alone.
	SamplingRV bool
	LogLevel   slog.Level
	// ResourceAttributes are merged over detected attributes; explicit service/environment
	// settings and host.id override (HostID) still win.
	ResourceAttributes map[string]string
	HostID             string // explicit host.id; empty = detect
	RuntimeMetrics     bool
	MetricInterval     time.Duration
	// Profiling exports a CPU profile of this process on an interval (profiler.go). On by default: a service
	// nobody profiled is a service whose slow span has no answer, and the sampling cost is a few percent of
	// one core. OPENLOG_PROFILING=false turns it off — which matters because Go allows one CPU profile per
	// process, so while this runs, net/http/pprof's own /debug/pprof/profile cannot.
	Profiling       bool
	ProfileInterval time.Duration
	ShutdownTimeout time.Duration
	ExportTimeout   time.Duration
	HostRoot        string // prefix for host files (/etc/machine-id, …); "/" by default
	InfraStateDir   string // infra agent state dir holding a generated host-id
	InfraRuntimeDir string // infra agent runtime dir where the running agent publishes its host-id
	StateDir        string // where the Go agent persists a generated host id; "" = user cache dir

	retry retrySettings
}

type retrySettings struct {
	initial, max, elapsed time.Duration
}

// Option overrides one configuration value; options beat environment variables.
type Option func(*Config)

// WithLicenseKey sets the ingest license key (header openlog-license-key).
func WithLicenseKey(key string) Option { return func(c *Config) { c.LicenseKey = key } }

// WithEndpoint sets the OTLP base URL, e.g. https://ingest.example.com:4318 or
// http://localhost:4317 with ProtocolGRPC. A missing scheme means https.
func WithEndpoint(u string) Option { return func(c *Config) { c.Endpoint = u } }

// WithProtocol selects ProtocolHTTP (default) or ProtocolGRPC.
func WithProtocol(p string) Option { return func(c *Config) { c.Protocol = p } }

// WithCompression selects "gzip" (default) or "none".
func WithCompression(v string) Option { return func(c *Config) { c.Compression = v } }

// WithHeaders adds export headers (merged; the license key header always wins).
func WithHeaders(h map[string]string) Option {
	return func(c *Config) {
		for k, v := range h {
			c.Headers[k] = v
		}
	}
}

// WithServiceName sets service.name.
func WithServiceName(name string) Option { return func(c *Config) { c.ServiceName = name } }

// WithServiceVersion sets service.version.
func WithServiceVersion(v string) Option { return func(c *Config) { c.ServiceVersion = v } }

// WithServiceNamespace sets service.namespace.
func WithServiceNamespace(ns string) Option { return func(c *Config) { c.ServiceNamespace = ns } }

// WithEnvironment sets deployment.environment.name (e.g. production).
func WithEnvironment(env string) Option { return func(c *Config) { c.Environment = env } }

// WithSamplingRatio sets the parent-based head sampling ratio for new traces (0..1).
func WithSamplingRatio(r float64) Option { return func(c *Config) { c.SamplingRatio = r } }

// WithSamplingRV makes new traces write explicit randomness (tracestate ot=rv:<14 hex>),
// used for the sampling decision and kept by every downstream service. Off by default: the
// trace id is random and root spans carry the W3C random flag.
func WithSamplingRV(enabled bool) Option { return func(c *Config) { c.SamplingRV = enabled } }

// WithLogLevel sets the level of the agent's own diagnostics (stderr).
func WithLogLevel(l slog.Level) Option { return func(c *Config) { c.LogLevel = l } }

// WithResourceAttributes adds resource attributes.
func WithResourceAttributes(attrs map[string]string) Option {
	return func(c *Config) {
		for k, v := range attrs {
			c.ResourceAttributes[k] = v
		}
	}
}

// WithHostID sets host.id explicitly instead of detecting it.
func WithHostID(id string) Option { return func(c *Config) { c.HostID = id } }

// WithRuntimeMetrics enables or disables Go runtime metrics (default enabled).
func WithRuntimeMetrics(enabled bool) Option { return func(c *Config) { c.RuntimeMetrics = enabled } }

// WithMetricInterval sets the metric export interval (default 60s).
func WithMetricInterval(d time.Duration) Option { return func(c *Config) { c.MetricInterval = d } }

// WithProfiling enables or disables continuous CPU profiling (default enabled). Requires the HTTP protocol.
// Turn it off when the process needs net/http/pprof: Go allows one CPU profile at a time, so the two cannot
// both run.
func WithProfiling(enabled bool) Option { return func(c *Config) { c.Profiling = enabled } }

// WithProfileInterval sets how long each CPU profile covers and how often one is exported (default 60s,
// minimum 10s).
func WithProfileInterval(d time.Duration) Option { return func(c *Config) { c.ProfileInterval = d } }

// WithShutdownTimeout bounds the final flush when the shutdown context has no deadline (default 5s).
func WithShutdownTimeout(d time.Duration) Option { return func(c *Config) { c.ShutdownTimeout = d } }

// WithEnabled turns the agent on or off (off: Start installs nothing and returns a no-op shutdown).
func WithEnabled(enabled bool) Option { return func(c *Config) { c.Enabled = enabled } }

// withRetry shortens exporter retries (tests).
func withRetry(initial, max, elapsed time.Duration) Option {
	return func(c *Config) { c.retry = retrySettings{initial, max, elapsed} }
}

type lookupFunc func(string) (string, bool)

// loadConfig resolves defaults, OTEL_*, OPENLOG_* and options, in that order.
func loadConfig(lookup lookupFunc, opts []Option) (*Config, []string, error) {
	c := &Config{
		Enabled:            true,
		Protocol:           ProtocolHTTP,
		Compression:        "gzip",
		Headers:            map[string]string{},
		SamplingRatio:      1,
		LogLevel:           slog.LevelWarn,
		ResourceAttributes: map[string]string{},
		RuntimeMetrics:     true,
		MetricInterval:     defaultMetricInterval,
		Profiling:          true,
		ProfileInterval:    defaultProfileInterval,
		ShutdownTimeout:    defaultShutdownTimeout,
		ExportTimeout:      defaultExportTimeout,
		HostRoot:           "/",
		InfraStateDir:      defaultInfraStateDir,
		InfraRuntimeDir:    defaultInfraRuntimeDir,
		retry:              retrySettings{initial: time.Second, max: 30 * time.Second, elapsed: time.Minute},
	}
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }
	get := func(names ...string) (string, string, bool) {
		for _, n := range names {
			if v, ok := lookup(n); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), n, true
			}
		}
		return "", "", false
	}
	boolVar := func(dst *bool, names ...string) {
		if v, n, ok := get(names...); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				warn("%s=%q is not a boolean; ignored", n, v)
				return
			}
			*dst = b
		}
	}
	durVar := func(dst *time.Duration, name string) {
		if v, ok := lookup(name); ok && v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				warn("%s=%q is not a positive duration; ignored", name, v)
				return
			}
			*dst = d
		}
	}

	// ---- OTEL_* (lower precedence) ----
	if v, _, ok := get("OTEL_SDK_DISABLED"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Enabled = !b
		}
	}
	if v, _, ok := get("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		c.Endpoint = v
	}
	if v, _, ok := get("OTEL_EXPORTER_OTLP_PROTOCOL"); ok {
		c.Protocol = v
	}
	if v, _, ok := get("OTEL_EXPORTER_OTLP_COMPRESSION"); ok {
		c.Compression = v
	}
	if v, _, ok := get("OTEL_EXPORTER_OTLP_HEADERS"); ok {
		for k, val := range parseKV(v) {
			c.Headers[k] = val
		}
	}
	if v, _, ok := get("OTEL_SERVICE_NAME"); ok {
		c.ServiceName = v
	}
	if v, _, ok := get("OTEL_RESOURCE_ATTRIBUTES"); ok {
		for k, val := range parseKV(v) {
			c.ResourceAttributes[k] = val
		}
	}
	if v, _, ok := get("OTEL_TRACES_SAMPLER_ARG"); ok {
		sampler, _, _ := get("OTEL_TRACES_SAMPLER")
		if sampler == "" || strings.HasSuffix(sampler, "traceidratio") {
			if r, err := strconv.ParseFloat(v, 64); err == nil {
				c.SamplingRatio = r
			}
		}
	}
	if v, _, ok := get("OTEL_LOG_LEVEL"); ok {
		if l, err := parseLevel(v); err == nil {
			c.LogLevel = l
		}
	}
	if v, _, ok := get("OTEL_METRIC_EXPORT_INTERVAL"); ok {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			c.MetricInterval = time.Duration(ms) * time.Millisecond
		}
	}

	// ---- OPENLOG_* ----
	boolVar(&c.Enabled, "OPENLOG_ENABLED")
	if v, _, ok := get("OPENLOG_LICENSE_KEY"); ok {
		c.LicenseKey = v
	}
	if v, _, ok := get("OPENLOG_ENDPOINT"); ok {
		c.Endpoint = v
	}
	if v, _, ok := get("OPENLOG_PROTOCOL"); ok {
		c.Protocol = v
	}
	if v, _, ok := get("OPENLOG_COMPRESSION"); ok {
		c.Compression = v
	}
	if v, _, ok := get("OPENLOG_SERVICE_NAME"); ok {
		c.ServiceName = v
	}
	if v, _, ok := get("OPENLOG_SERVICE_VERSION"); ok {
		c.ServiceVersion = v
	}
	if v, _, ok := get("OPENLOG_SERVICE_NAMESPACE"); ok {
		c.ServiceNamespace = v
	}
	if v, _, ok := get("OPENLOG_ENVIRONMENT"); ok {
		c.Environment = v
	}
	if v, n, ok := get("OPENLOG_SAMPLING_RATIO"); ok {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil {
			warn("%s=%q is not a number; ignored", n, v)
		} else {
			c.SamplingRatio = r
		}
	}
	if v, n, ok := get("OPENLOG_LOG_LEVEL"); ok {
		l, err := parseLevel(v)
		if err != nil {
			warn("%s: %v", n, err)
		} else {
			c.LogLevel = l
		}
	}
	if v, _, ok := get("OPENLOG_RESOURCE_ATTRIBUTES"); ok {
		for k, val := range parseKV(v) {
			c.ResourceAttributes[k] = val
		}
	}
	if v, _, ok := get("OPENLOG_HOST_ID"); ok {
		c.HostID = v
	}
	boolVar(&c.RuntimeMetrics, "OPENLOG_RUNTIME_METRICS")
	durVar(&c.MetricInterval, "OPENLOG_METRIC_EXPORT_INTERVAL")
	boolVar(&c.Profiling, "OPENLOG_PROFILING")
	durVar(&c.ProfileInterval, "OPENLOG_PROFILE_INTERVAL")
	durVar(&c.ShutdownTimeout, "OPENLOG_SHUTDOWN_TIMEOUT")
	if v, _, ok := get("OPENLOG_HOST_ROOT"); ok {
		c.HostRoot = v
	}
	if v, _, ok := get("OPENLOG_INFRA_STATE_DIR"); ok {
		c.InfraStateDir = v
	}
	if v, _, ok := get("OPENLOG_INFRA_RUNTIME_DIR"); ok {
		c.InfraRuntimeDir = v
	}
	boolVar(&c.SamplingRV, "OPENLOG_SAMPLING_RV")
	if v, _, ok := get("OPENLOG_STATE_DIR"); ok {
		c.StateDir = v
	}

	// ---- options ----
	for _, o := range opts {
		o(c)
	}

	// ---- normalize and validate ----
	switch strings.ToLower(c.Protocol) {
	case "http/protobuf", "http", "":
		c.Protocol = ProtocolHTTP
	case "grpc":
		c.Protocol = ProtocolGRPC
	default:
		return nil, warnings, fmt.Errorf("openlog: unsupported protocol %q (use %s or %s)", c.Protocol, ProtocolHTTP, ProtocolGRPC)
	}
	switch strings.ToLower(c.Compression) {
	case "gzip":
		c.Compression = "gzip"
	case "none", "":
		c.Compression = "none"
	default:
		return nil, warnings, fmt.Errorf("openlog: unsupported compression %q (use gzip or none)", c.Compression)
	}
	if c.Endpoint == "" {
		c.Endpoint = defaultHTTPEndpoint
		if c.Protocol == ProtocolGRPC {
			c.Endpoint = defaultGRPCEndpoint
		}
	}
	if !strings.Contains(c.Endpoint, "://") {
		c.Endpoint = "https://" + c.Endpoint
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, warnings, fmt.Errorf("openlog: invalid endpoint %q", c.Endpoint)
	}
	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	if c.SamplingRatio < 0 || c.SamplingRatio > 1 {
		warn("sampling ratio %v outside 0..1; clamped", c.SamplingRatio)
		c.SamplingRatio = min(max(c.SamplingRatio, 0), 1)
	}
	if c.ServiceName == "" {
		if v := c.ResourceAttributes["service.name"]; v != "" {
			c.ServiceName = v
		} else {
			exe, _ := os.Executable()
			c.ServiceName = "unknown_service:go"
			if exe != "" {
				c.ServiceName = "unknown_service:" + filepath.Base(exe)
			}
			warn("no service name configured (OPENLOG_SERVICE_NAME); using %q", c.ServiceName)
		}
	}
	if c.LicenseKey != "" {
		c.Headers[LicenseKeyHeader] = c.LicenseKey
	} else if _, ok := c.Headers[LicenseKeyHeader]; !ok && c.Headers["Authorization"] == "" && c.Headers["authorization"] == "" {
		warn("no license key configured (OPENLOG_LICENSE_KEY); openlog ingest will reject the data")
	}
	if c.MetricInterval <= 0 {
		c.MetricInterval = defaultMetricInterval
	}
	if c.ProfileInterval < minProfileInterval {
		// The interval is also how long each profile covers: below a few seconds the samples are too few to
		// mean anything and the export overhead starts to dominate what is being measured.
		if c.ProfileInterval > 0 {
			warn("OPENLOG_PROFILE_INTERVAL is below the %s minimum; using %s", minProfileInterval, minProfileInterval)
		}
		c.ProfileInterval = max(c.ProfileInterval, minProfileInterval)
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
	if c.HostRoot == "" {
		c.HostRoot = "/"
	}
	return c, warnings, nil
}

// parseKV parses the W3C-baggage-like "k1=v1,k2=v2" format of OTEL_RESOURCE_ATTRIBUTES
// and OTEL_EXPORTER_OTLP_HEADERS (values may be percent-encoded).
func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	case "off", "none":
		return slog.LevelError + 100, nil
	}
	return 0, fmt.Errorf("unknown log level %q (debug, info, warn, error, off)", s)
}
