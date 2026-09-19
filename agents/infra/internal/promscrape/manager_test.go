package promscrape

import (
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func testConfig(targets ...config.ScrapeTarget) *config.PrometheusConfig {
	return &config.PrometheusConfig{Enabled: true, Interval: config.Duration(time.Hour), Timeout: config.Duration(2 * time.Second),
		SampleLimit: 1000, BodyLimitBytes: 1 << 20, MaxTargets: 10, MaxConcurrent: 2, Targets: targets}
}

func newTestManager(cfg *config.PrometheusConfig) *Manager {
	return NewManager(Options{Config: cfg, AgentName: "openlog-infra-agent", AgentVersion: "1.2.3",
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{otlputil.Str("host.id", "h1"), otlputil.Str("service.name", "ignored")}}})
}

func resAttr(rm *metricspb.ResourceMetrics, key string) *commonpb.AnyValue {
	for _, kv := range rm.Resource.Attributes {
		if kv.Key == key {
			return kv.Value
		}
	}
	return nil
}

func scrapeOnce(t *testing.T, m *Manager) []*metricspb.ResourceMetrics {
	t.Helper()
	m.Refresh(context.Background())
	m.CollectOnce(context.Background())
	return m.Drain()
}

func TestScrapeTextTarget(t *testing.T) {
	var gotAccept, gotAuth, gotTimeout atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept.Store(r.Header.Get("Accept"))
		gotAuth.Store(r.Header.Get("Authorization"))
		gotTimeout.Store(r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"))
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# TYPE go_goroutines gauge\ngo_goroutines 12\n# TYPE reqs_total counter\nreqs_total{code=\"200\"} 5\n"))
	}))
	defer srv.Close()
	t.Setenv("SCRAPE_TOKEN", "s3cret")
	m := newTestManager(testConfig(config.ScrapeTarget{URL: srv.URL + "/metrics", Job: "app", BearerToken: "env:SCRAPE_TOKEN",
		Labels: map[string]string{"env": "prod"}}))
	rms := scrapeOnce(t, m)
	if len(rms) != 1 {
		t.Fatalf("resources = %d", len(rms))
	}
	rm := rms[0]
	if resAttr(rm, "service.name").GetStringValue() != "app" || resAttr(rm, "host.id").GetStringValue() != "h1" ||
		resAttr(rm, "env").GetStringValue() != "prod" || resAttr(rm, "openlog.integration.id").GetStringValue() != "prometheus" ||
		resAttr(rm, "server.port").GetIntValue() == 0 || resAttr(rm, "url.scheme").GetStringValue() != "http" {
		t.Fatalf("resource = %v", rm.Resource.Attributes)
	}
	ms := metricsByName(rm.ScopeMetrics[0].Metrics)
	if ms["go_goroutines"].GetGauge().DataPoints[0].GetAsDouble() != 12 || ms["reqs_total"].GetSum() == nil {
		t.Fatalf("metrics = %v", ms)
	}
	if ms["up"].GetGauge().DataPoints[0].GetAsDouble() != 1 || ms["scrape_samples_scraped"].GetGauge().DataPoints[0].GetAsDouble() != 2 {
		t.Fatalf("health = %v %v", ms["up"], ms["scrape_samples_scraped"])
	}
	if rm.ScopeMetrics[0].Scope.Name != "openlog-infra-agent/integrations/prometheus" {
		t.Fatalf("scope = %v", rm.ScopeMetrics[0].Scope)
	}
	if !strings.HasPrefix(gotAccept.Load().(string), "application/openmetrics-text") || gotAuth.Load() != "Bearer s3cret" || gotTimeout.Load() != "2" {
		t.Fatalf("headers: accept=%v auth=%v timeout=%v", gotAccept.Load(), gotAuth.Load(), gotTimeout.Load())
	}
	st := m.Targets()
	if len(st) != 1 || !st[0].Up || st[0].Samples != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestScrapeOpenMetricsGzipAndFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte("# TYPE a counter\na_total 1\n# TYPE go_x gauge\ngo_x 1\n# EOF\n"))
		_ = gz.Close()
	}))
	defer srv.Close()
	m := newTestManager(testConfig(config.ScrapeTarget{URL: srv.URL, Metrics: &config.MetricFilter{Exclude: []string{"go_*"}}}))
	rms := scrapeOnce(t, m)
	ms := metricsByName(rms[0].ScopeMetrics[0].Metrics)
	if ms["a_total"] == nil || ms["go_x"] != nil {
		t.Fatalf("metrics = %v", ms)
	}
}

func TestScrapeFailuresReportDown(t *testing.T) {
	big := strings.Repeat("m 1\n", 1<<18) // 1 MiB
	for name, h := range map[string]http.HandlerFunc{
		"status":  func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusServiceUnavailable) },
		"syntax":  func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not a metric line at all{\n")) },
		"samples": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("m 1\n", 1001))) },
		"body":    func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(big + "x 1\n")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			cfg := testConfig(config.ScrapeTarget{URL: srv.URL})
			cfg.SampleLimit = 1_000_000
			if name == "samples" {
				cfg.SampleLimit = 1000
			}
			m := newTestManager(cfg)
			rms := scrapeOnce(t, m)
			ms := metricsByName(rms[0].ScopeMetrics[0].Metrics)
			if len(ms) != 3 || ms["up"].GetGauge().DataPoints[0].GetAsDouble() != 0 {
				t.Fatalf("metrics = %v", ms)
			}
			if st := m.Targets(); st[0].Up || st[0].LastError == "" {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestScrapeUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	m := newTestManager(testConfig(config.ScrapeTarget{URL: u + "/metrics?token=abc"}))
	scrapeOnce(t, m)
	st := m.Targets()[0]
	if st.Up || strings.Contains(st.LastError, "token=abc") {
		t.Fatalf("status = %+v (the URL, with its query, must not be in the error)", st)
	}
}

func TestReconcileLimitsAndRestarts(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTargets = 2
	m := newTestManager(cfg)
	m.Reconcile([]Target{{Key: "a", URL: "http://a:1/"}, {Key: "b", URL: "http://b:1/"}, {Key: "c", URL: "http://c:1/"}, {Key: "a", URL: "http://a:1/"}})
	if n := len(m.Targets()); n != 2 {
		t.Fatalf("targets = %d", n)
	}
	if p := m.Problems(); len(p) != 1 || !strings.Contains(p[0], "http://c:1/") {
		t.Fatalf("problems = %v", p)
	}
	before := m.runners["a"]
	m.Reconcile([]Target{{Key: "a", URL: "http://a:1/", Job: "changed"}})
	if len(m.runners) != 1 || m.runners["a"] == before || len(m.Problems()) != 0 {
		t.Fatal("changed target not restarted or vanished target kept")
	}
}

func TestPendingIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("a 1\n")) }))
	defer srv.Close()
	m := newTestManager(testConfig(config.ScrapeTarget{URL: srv.URL}))
	m.Refresh(context.Background())
	for range maxPendingScrapes + 3 {
		m.CollectOnce(context.Background())
	}
	if n := len(m.Drain()); n != maxPendingScrapes {
		t.Fatalf("pending = %d", n)
	}
	if n := len(m.Drain()); n != 0 {
		t.Fatalf("drain did not clear: %d", n)
	}
}

func TestStartScrapesInBackground(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("a 1\n"))
	}))
	defer srv.Close()
	cfg := testConfig(config.ScrapeTarget{URL: srv.URL})
	cfg.Interval = config.Duration(50 * time.Millisecond)
	m := newTestManager(cfg)
	discovered := Target{Key: "container:x", Source: SourceContainer, URL: srv.URL + "/d", Job: "d", Instance: "x"}
	m.SetDiscover(func(context.Context) []Target { return []Target{discovered} })
	m.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop()
	if hits.Load() < 4 || len(m.Targets()) != 2 {
		t.Fatalf("hits = %d targets = %d", hits.Load(), len(m.Targets()))
	}
}
