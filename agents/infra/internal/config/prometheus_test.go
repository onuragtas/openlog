package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPrometheusConfig(t *testing.T) {
	yml := `
prometheus:
  interval: 15s
  metrics: { exclude: ["go_*"] }
  targets:
    - url: http://127.0.0.1:9100/metrics
      job: node
      labels: { env: prod }
    - url: https://exporter.internal:9443/metrics
      interval: 60s
      timeout: 5s
      bearer_token: env:EXPORTER_TOKEN
      tls: { ca_file: /etc/ssl/exporter.pem, server_name: exporter }
      sample_limit: 500
      metrics: { include: ["app_*"] }
`
	cfg := Default()
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
	p := cfg.Prometheus
	if !p.Enabled || !p.Containers || !p.KubernetesPods || p.SampleLimit != 20000 || p.Timeout.D() != 10*time.Second {
		t.Fatalf("defaults lost: %+v", p)
	}
	if p.EffectiveInterval(&p.Targets[0]) != 15*time.Second || p.EffectiveInterval(&p.Targets[1]) != time.Minute {
		t.Fatal("intervals")
	}
	if p.Metrics.Keep("go_gc") || !p.Targets[1].Metrics.Keep("app_x") {
		t.Fatal("filters")
	}
	if len(cfg.Warnings()) != 0 {
		t.Fatalf("warnings = %v", cfg.Warnings())
	}
}

func TestPrometheusValidation(t *testing.T) {
	yml := `
prometheus:
  interval: 10s
  timeout: 20s
  sample_limit: 0
  max_targets: 0
  metrics: { include: ["["] }
  targets:
    - url: ftp://x/metrics
    - url: http://user:pw@x:1/metrics
    - url: http://a:1/m
      bearer_token: literal-token
      username: u
      tls: { ca_file: relative.pem }
      timeout: 1m
    - url: http://a:1/m
`
	cfg := Default()
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	err := cfg.Validate(false)
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, want := range []string{
		"prometheus.timeout", "prometheus.sample_limit", "prometheus.max_targets", `prometheus.metrics: invalid pattern "["`,
		"targets[0].url must be an http(s) URL", "targets[1].url must not contain credentials", "targets[2].bearer_token and username",
		"targets[2].tls.ca_file", "targets[2].timeout", `targets[3].url "http://a:1/m" is listed twice`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, "prometheus.targets[2].bearer_token is a literal") {
		t.Errorf("warnings = %s", w)
	}
}

// The shipped example must stay loadable as it is documented.
func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../packaging/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	if err := Parse(data, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
	if !cfg.Prometheus.Enabled || cfg.Prometheus.BodyLimitBytes != 32<<20 {
		t.Fatalf("prometheus = %+v", cfg.Prometheus)
	}
}
