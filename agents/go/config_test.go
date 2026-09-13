package openlog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) lookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestConfigPrecedence(t *testing.T) {
	full := map[string]string{
		"OTEL_SERVICE_NAME":           "otel-svc",
		"OPENLOG_SERVICE_NAME":        "openlog-svc",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel:4318",
		"OPENLOG_ENDPOINT":            "http://openlog:4318",
		"OTEL_TRACES_SAMPLER":         "parentbased_traceidratio",
		"OTEL_TRACES_SAMPLER_ARG":     "0.5",
		"OPENLOG_SAMPLING_RATIO":      "0.25",
		"OTEL_LOG_LEVEL":              "error",
		"OPENLOG_LOG_LEVEL":           "debug",
		"OTEL_RESOURCE_ATTRIBUTES":    "team=otel,region=eu",
		"OPENLOG_RESOURCE_ATTRIBUTES": "team=openlog",
		"OTEL_EXPORTER_OTLP_HEADERS":  "x-a=1,openlog-license-key=from-otel-headers",
		"OPENLOG_LICENSE_KEY":         "lk",
		"OPENLOG_ENVIRONMENT":         "prod",
	}
	cases := []struct {
		name  string
		env   map[string]string
		opts  []Option
		check func(t *testing.T, c *Config)
	}{
		{"option beats env", full, []Option{WithServiceName("opt-svc"), WithEndpoint("http://opt:4318"), WithSamplingRatio(0.1)},
			func(t *testing.T, c *Config) {
				eq(t, c.ServiceName, "opt-svc")
				eq(t, c.Endpoint, "http://opt:4318")
				eq(t, c.SamplingRatio, 0.1)
			}},
		{"OPENLOG beats OTEL", full, nil, func(t *testing.T, c *Config) {
			eq(t, c.ServiceName, "openlog-svc")
			eq(t, c.Endpoint, "http://openlog:4318")
			eq(t, c.SamplingRatio, 0.25)
			eq(t, c.LogLevel, slog.LevelDebug)
			eq(t, c.ResourceAttributes["team"], "openlog")
			eq(t, c.ResourceAttributes["region"], "eu")
			eq(t, c.Headers["x-a"], "1")
			eq(t, c.Headers[LicenseKeyHeader], "lk")
			eq(t, c.Environment, "prod")
		}},
		{"OTEL only", map[string]string{"OTEL_SERVICE_NAME": "otel-svc", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel:4318",
			"OTEL_TRACES_SAMPLER_ARG": "0.5", "OTEL_LOG_LEVEL": "error", "OTEL_METRIC_EXPORT_INTERVAL": "15000"},
			nil, func(t *testing.T, c *Config) {
				eq(t, c.ServiceName, "otel-svc")
				eq(t, c.Endpoint, "http://otel:4318")
				eq(t, c.SamplingRatio, 0.5)
				eq(t, c.LogLevel, slog.LevelError)
				eq(t, c.MetricInterval, 15*time.Second)
			}},
		{"sampler arg ignored for non-ratio sampler", map[string]string{"OTEL_TRACES_SAMPLER": "always_on", "OTEL_TRACES_SAMPLER_ARG": "0.5"},
			nil, func(t *testing.T, c *Config) { eq(t, c.SamplingRatio, 1.0) }},
		{"defaults", map[string]string{}, nil, func(t *testing.T, c *Config) {
			eq(t, c.Endpoint, "http://localhost:4318")
			eq(t, c.Protocol, ProtocolHTTP)
			eq(t, c.Compression, "gzip")
			eq(t, c.SamplingRatio, 1.0)
			eq(t, c.MetricInterval, 60*time.Second)
			eq(t, c.RuntimeMetrics, true)
			eq(t, c.Enabled, true)
			eq(t, c.LogLevel, slog.LevelWarn)
			if !strings.HasPrefix(c.ServiceName, "unknown_service:") {
				t.Errorf("service name %q", c.ServiceName)
			}
		}},
		{"grpc default endpoint", map[string]string{"OPENLOG_PROTOCOL": "grpc"}, nil, func(t *testing.T, c *Config) {
			eq(t, c.Endpoint, "http://localhost:4317")
			eq(t, c.Protocol, ProtocolGRPC)
		}},
		{"missing scheme means https", map[string]string{"OPENLOG_ENDPOINT": "ingest.example.com:4318/"}, nil, func(t *testing.T, c *Config) {
			eq(t, c.Endpoint, "https://ingest.example.com:4318")
		}},
		{"service.name from resource attributes", map[string]string{"OTEL_RESOURCE_ATTRIBUTES": "service.name=from-attrs"}, nil,
			func(t *testing.T, c *Config) { eq(t, c.ServiceName, "from-attrs") }},
		{"OTEL_SDK_DISABLED", map[string]string{"OTEL_SDK_DISABLED": "true"}, nil, func(t *testing.T, c *Config) { eq(t, c.Enabled, false) }},
		{"OPENLOG_ENABLED beats OTEL_SDK_DISABLED", map[string]string{"OTEL_SDK_DISABLED": "true", "OPENLOG_ENABLED": "true"}, nil,
			func(t *testing.T, c *Config) { eq(t, c.Enabled, true) }},
		{"option disables", map[string]string{"OPENLOG_ENABLED": "true"}, []Option{WithEnabled(false)},
			func(t *testing.T, c *Config) { eq(t, c.Enabled, false) }},
		{"ratio clamped", map[string]string{"OPENLOG_SAMPLING_RATIO": "3"}, nil, func(t *testing.T, c *Config) { eq(t, c.SamplingRatio, 1.0) }},
		{"durations and switches", map[string]string{"OPENLOG_METRIC_EXPORT_INTERVAL": "10s", "OPENLOG_SHUTDOWN_TIMEOUT": "2s",
			"OPENLOG_RUNTIME_METRICS": "false", "OPENLOG_COMPRESSION": "none", "OPENLOG_HOST_ID": "fixed"}, nil,
			func(t *testing.T, c *Config) {
				eq(t, c.MetricInterval, 10*time.Second)
				eq(t, c.ShutdownTimeout, 2*time.Second)
				eq(t, c.RuntimeMetrics, false)
				eq(t, c.Compression, "none")
				eq(t, c.HostID, "fixed")
			}},
		{"percent-encoded attribute values", map[string]string{"OPENLOG_RESOURCE_ATTRIBUTES": "a=b%20c, d = e "}, nil,
			func(t *testing.T, c *Config) {
				eq(t, c.ResourceAttributes["a"], "b c")
				eq(t, c.ResourceAttributes["d"], "e")
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, err := loadConfig(env(tc.env), tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, c)
		})
	}
}

func TestConfigErrorsAndWarnings(t *testing.T) {
	if _, _, err := loadConfig(env(map[string]string{"OPENLOG_PROTOCOL": "thrift"}), nil); err == nil {
		t.Error("unsupported protocol accepted")
	}
	if _, _, err := loadConfig(env(map[string]string{"OPENLOG_ENDPOINT": "ftp://x"}), nil); err == nil {
		t.Error("ftp endpoint accepted")
	}
	if _, _, err := loadConfig(env(map[string]string{"OPENLOG_COMPRESSION": "zstd"}), nil); err == nil {
		t.Error("zstd accepted")
	}
	_, warns, err := loadConfig(env(map[string]string{"OPENLOG_SAMPLING_RATIO": "abc", "OPENLOG_METRIC_EXPORT_INTERVAL": "-1s"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"OPENLOG_SAMPLING_RATIO", "OPENLOG_METRIC_EXPORT_INTERVAL", "license key", "service name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
