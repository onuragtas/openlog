package promscrape

import (
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestStaticTargets(t *testing.T) {
	cfg := &config.PrometheusConfig{Targets: []config.ScrapeTarget{
		{URL: "http://127.0.0.1:9100/metrics", Labels: map[string]string{"env": "prod", "a": "1"}},
		{URL: "https://exporter.internal:9443/m", Job: "custom"},
	}}
	ts := StaticTargets(cfg)
	if len(ts) != 2 {
		t.Fatalf("targets = %v", ts)
	}
	if ts[0].Job != "127.0.0.1" || ts[0].Instance != "127.0.0.1:9100" || ts[0].Source != SourceStatic {
		t.Fatalf("target 0 = %+v", ts[0])
	}
	if len(ts[0].Attrs) != 2 || ts[0].Attrs[0].Key != "a" {
		t.Fatalf("labels must become sorted attributes: %v", ts[0].Attrs)
	}
	if ts[1].Job != "custom" || ts[1].Static == nil {
		t.Fatalf("target 1 = %+v", ts[1])
	}
}

func TestContainerTargets(t *testing.T) {
	cs := []containers.Container{
		{ID: "c1", Name: "api", State: "running", IPs: []string{"172.17.0.5"}, Labels: map[string]string{
			KeyScrape: "true", KeyPort: "9090", KeyPath: "custom/metrics?x=1", "com.docker.compose.service": "api-svc"}},
		// One declared port: used without a port label; no own IP: the published port.
		{ID: "c2", Name: "worker", State: "running", Labels: map[string]string{KeyScrape: "TRUE"},
			Ports: []containers.Port{{PrivatePort: 8080, PublicPort: 18080, Protocol: "tcp"}}},
		{ID: "c3", Name: "noopt", State: "running", IPs: []string{"172.17.0.6"}, Labels: map[string]string{KeyPort: "1"}},
		{ID: "c4", Name: "stopped", State: "exited", Labels: map[string]string{KeyScrape: "true", KeyPort: "1"}},
		{ID: "c5", Name: "ambiguous", State: "running", IPs: []string{"172.17.0.7"}, Labels: map[string]string{KeyScrape: "true"},
			Ports: []containers.Port{{PrivatePort: 1, Protocol: "tcp"}, {PrivatePort: 2, Protocol: "tcp"}}},
		{ID: "c6", Name: "in-pod", State: "running", IPs: []string{"10.0.0.9"}, Labels: map[string]string{KeyScrape: "true", KeyPort: "9"},
			Extra: []*commonpb.KeyValue{otlputil.Str("k8s.pod.uid", "u1")}},
		{ID: "c7", Name: "badscheme", State: "running", IPs: []string{"172.17.0.8"}, Labels: map[string]string{KeyScrape: "true", KeyPort: "9", KeyScheme: "ftp"}},
	}
	ts, problems := ContainerTargets(cs, true)
	if len(ts) != 2 {
		t.Fatalf("targets = %+v", ts)
	}
	if ts[0].URL != "http://172.17.0.5:9090/custom/metrics?x=1" || ts[0].Job != "api-svc" || ts[0].Source != SourceContainer {
		t.Fatalf("api = %+v", ts[0])
	}
	if attrValue(ts[0].Attrs, "container.id") != "c1" || attrValue(ts[0].Attrs, "container.name") != "api" {
		t.Fatalf("api attrs = %v", ts[0].Attrs)
	}
	if ts[1].URL != "http://127.0.0.1:18080/metrics" || ts[1].Job != "worker" {
		t.Fatalf("worker = %+v", ts[1])
	}
	if len(problems) != 2 || !strings.Contains(problems[0], "ambiguous") || !strings.Contains(problems[1], "badscheme") {
		t.Fatalf("problems = %v", problems)
	}
	// Without pod scraping the container of a pod is scraped through its label.
	ts, _ = ContainerTargets(cs, false)
	if len(ts) != 3 {
		t.Fatalf("targets without pod mode = %d", len(ts))
	}
}

func TestPodTargets(t *testing.T) {
	pods := []PodEndpoint{
		{UID: "u1", Name: "api-7d9-x", Namespace: "shop", IP: "10.1.2.3", Annotations: map[string]string{KeyScrape: "true", KeyPort: "8080"},
			Attrs: []*commonpb.KeyValue{otlputil.Str("openlog.k8s.workload.name", "api")}},
		{UID: "u2", Name: "db-0", Namespace: "shop", IP: "10.1.2.4", Annotations: map[string]string{KeyScrape: "true", KeyJob: "postgres"},
			ContainerPorts: []int{9187}},
		{UID: "u3", Name: "pending", Namespace: "shop", Annotations: map[string]string{KeyScrape: "true", KeyPort: "1"}},
		{UID: "u4", Name: "bad", Namespace: "shop", IP: "10.1.2.5", Annotations: map[string]string{KeyScrape: "true", KeyPort: "x"}},
	}
	ts, problems := PodTargets(pods)
	if len(ts) != 2 || len(problems) != 1 {
		t.Fatalf("targets = %+v problems = %v", ts, problems)
	}
	if ts[0].URL != "http://10.1.2.3:8080/metrics" || ts[0].Job != "shop/api" || ts[0].Source != SourcePod {
		t.Fatalf("api = %+v", ts[0])
	}
	if ts[1].URL != "http://10.1.2.4:9187/metrics" || ts[1].Job != "postgres" {
		t.Fatalf("db = %+v", ts[1])
	}
}

func TestMetricFilter(t *testing.T) {
	f := config.MetricFilter{Include: []string{"http_*", "up"}, Exclude: []string{"*_bucket"}}
	for name, want := range map[string]bool{"http_requests_total": true, "up": true, "go_goroutines": false, "http_x_bucket": false} {
		if f.Keep(name) != want {
			t.Errorf("Keep(%q) = %v", name, !want)
		}
	}
}
