package promscrape

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Target sources.
const (
	SourceStatic    = "static"
	SourceContainer = "container"
	SourcePod       = "pod"
)

// Opt-in keys on containers (labels) and pods (annotations), the convention of Prometheus' kubernetes_sd
// example configuration that exporters and Helm charts already carry.
const (
	KeyScrape = "prometheus.io/scrape"
	KeyPort   = "prometheus.io/port"
	KeyPath   = "prometheus.io/path"
	KeyScheme = "prometheus.io/scheme"
	KeyJob    = "prometheus.io/job"
)

// Resource attribute keys of scrape targets (semantic-conventions §6.9).
const (
	AttrServiceName       = "service.name"
	AttrServiceInstanceID = "service.instance.id"
	AttrServerAddress     = "server.address"
	AttrServerPort        = "server.port"
	AttrURLScheme         = "url.scheme"
	AttrIntegrationID     = "openlog.integration.id"
	AttrScrapeSource      = "openlog.scrape.source"
	// IntegrationID is the openlog.integration.id of scraped metrics.
	IntegrationID = "prometheus"
)

// Target is one endpoint to scrape.
type Target struct {
	// Key identifies the target across refreshes: the URL plus the owning container or pod.
	Key    string
	Source string
	URL    string
	Job    string
	// Instance is host:port of the URL (Prometheus' instance label).
	Instance string
	// Attrs are resource attributes added after the host resource (labels, container or pod identity).
	Attrs  []*commonpb.KeyValue
	Static *config.ScrapeTarget // nil for discovered targets
}

// StaticTargets builds the configured targets.
func StaticTargets(cfg *config.PrometheusConfig) []Target {
	out := make([]Target, 0, len(cfg.Targets))
	for i := range cfg.Targets {
		st := &cfg.Targets[i]
		u, err := url.Parse(st.URL)
		if err != nil {
			continue // rejected by validation
		}
		t := Target{Key: "static:" + st.URL, Source: SourceStatic, URL: st.URL, Job: st.Job, Instance: u.Host, Static: st}
		if t.Job == "" {
			t.Job = u.Hostname()
		}
		for _, k := range sortedLabelKeys(st.Labels) {
			t.Attrs = append(t.Attrs, otlputil.Str(k, st.Labels[k]))
		}
		out = append(out, t)
	}
	return out
}

// PodEndpoint is a pod that asked to be scraped; the caller fills it from the pod cache.
type PodEndpoint struct {
	UID, Name, Namespace, IP string
	Annotations              map[string]string
	// ContainerPorts are the declared container ports, the fallback when no port annotation is set.
	ContainerPorts []int
	Attrs          []*commonpb.KeyValue
}

// PodTargets builds the targets of annotated pods.
func PodTargets(pods []PodEndpoint) ([]Target, []string) {
	var out []Target
	var problems []string
	for _, p := range pods {
		if !optedIn(p.Annotations) || p.IP == "" {
			continue
		}
		port, err := optPort(p.Annotations, p.ContainerPorts)
		if err != nil {
			problems = append(problems, fmt.Sprintf("pod %s/%s: %v", p.Namespace, p.Name, err))
			continue
		}
		u, err := optURL(p.Annotations, p.IP, port)
		if err != nil {
			problems = append(problems, fmt.Sprintf("pod %s/%s: %v", p.Namespace, p.Name, err))
			continue
		}
		job := p.Annotations[KeyJob]
		if job == "" {
			job = p.Namespace + "/" + p.Name
			if wl := attrValue(p.Attrs, "openlog.k8s.workload.name"); wl != "" {
				job = p.Namespace + "/" + wl
			}
		}
		out = append(out, Target{Key: "pod:" + p.UID + ":" + u.String(), Source: SourcePod, URL: u.String(), Job: job,
			Instance: u.Host, Attrs: p.Attrs})
	}
	return out, problems
}

// ContainerTargets builds the targets of labelled containers. Containers of a Kubernetes pod are skipped when
// skipPods is set: the pod annotation is the opt-in there, and scraping both would duplicate every series.
func ContainerTargets(cs []containers.Container, skipPods bool) ([]Target, []string) {
	var out []Target
	var problems []string
	for _, c := range cs {
		if c.State != "" && c.State != "running" {
			continue
		}
		if !optedIn(c.Labels) {
			continue
		}
		if skipPods && attrValue(c.Extra, "k8s.pod.uid") != "" {
			continue
		}
		var private []int
		for _, p := range c.Ports {
			if p.Protocol == "" || p.Protocol == "tcp" {
				private = append(private, p.PrivatePort)
			}
		}
		port, err := optPort(c.Labels, private)
		if err != nil {
			problems = append(problems, fmt.Sprintf("container %s: %v", c.Name, err))
			continue
		}
		host := ""
		if len(c.IPs) > 0 {
			host = c.IPs[0]
		} else {
			// No network address of its own (host network or unknown): the port published on the host.
			for _, p := range c.Ports {
				if p.PrivatePort == port && p.PublicPort > 0 {
					host, port = "127.0.0.1", p.PublicPort
					break
				}
			}
			if host == "" {
				host = "127.0.0.1"
			}
		}
		u, err := optURL(c.Labels, host, port)
		if err != nil {
			problems = append(problems, fmt.Sprintf("container %s: %v", c.Name, err))
			continue
		}
		job := c.Labels[KeyJob]
		if job == "" {
			job = c.Labels["com.docker.compose.service"]
		}
		if job == "" {
			job = c.Name
		}
		attrs := []*commonpb.KeyValue{otlputil.Str("container.id", c.ID), otlputil.Str("container.name", c.Name)}
		if c.Image != "" {
			attrs = append(attrs, otlputil.Str("container.image.name", c.Image))
		}
		attrs = append(attrs, c.Extra...)
		out = append(out, Target{Key: "container:" + c.ID + ":" + u.String(), Source: SourceContainer, URL: u.String(),
			Job: job, Instance: u.Host, Attrs: attrs})
	}
	return out, problems
}

func optedIn(m map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(m[KeyScrape]), "true")
}

func optPort(m map[string]string, declared []int) (int, error) {
	if v := strings.TrimSpace(m[KeyPort]); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("%s %q is not a port", KeyPort, v)
		}
		return p, nil
	}
	switch len(declared) {
	case 1:
		return declared[0], nil
	case 0:
		return 0, fmt.Errorf("%s is not set and no port is declared", KeyPort)
	}
	return 0, fmt.Errorf("%s is not set and %d ports are declared", KeyPort, len(declared))
}

func optURL(m map[string]string, host string, port int) (*url.URL, error) {
	scheme := strings.ToLower(strings.TrimSpace(m[KeyScheme]))
	switch scheme {
	case "":
		scheme = "http"
	case "http", "https":
	default:
		return nil, fmt.Errorf("%s %q must be http or https", KeyScheme, scheme)
	}
	p := strings.TrimSpace(m[KeyPath])
	if p == "" {
		p = "/metrics"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	pathPart, query, _ := strings.Cut(p, "?")
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: pathPart, RawQuery: query}
	return u, nil
}

func attrValue(kvs []*commonpb.KeyValue, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

func sortedLabelKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
