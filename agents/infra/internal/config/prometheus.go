package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

// PrometheusConfig configures scraping of Prometheus and OpenMetrics endpoints (semantic-conventions §6.9).
// Targets are the static list below plus, when enabled, containers and pods that opt in with the
// prometheus.io/scrape label or annotation.
type PrometheusConfig struct {
	Enabled bool `yaml:"enabled"`
	// Interval is the default scrape interval of every target.
	Interval Duration `yaml:"interval"`
	// Timeout bounds one scrape (connect + body); at most the interval.
	Timeout Duration `yaml:"timeout"`
	// SampleLimit rejects a whole scrape that exposes more samples (cardinality guard).
	SampleLimit int `yaml:"sample_limit"`
	// BodyLimitBytes rejects a larger (uncompressed) response body.
	BodyLimitBytes int `yaml:"body_limit_bytes"`
	// MaxTargets bounds the number of scraped targets (static targets first).
	MaxTargets int `yaml:"max_targets"`
	// MaxConcurrent limits scrapes running at the same time.
	MaxConcurrent int `yaml:"max_concurrent"`
	// Containers scrapes containers labelled prometheus.io/scrape=true (Docker, containerd, CRI-O).
	Containers bool `yaml:"containers"`
	// KubernetesPods scrapes the pods of this node annotated prometheus.io/scrape=true (node mode).
	KubernetesPods bool `yaml:"kubernetes_pods"`
	// Metrics filters the metric names of every target (a target's own filter replaces it).
	Metrics MetricFilter   `yaml:"metrics"`
	Targets []ScrapeTarget `yaml:"targets"`
}

// ScrapeTarget is one statically configured endpoint.
type ScrapeTarget struct {
	// URL is the full metrics URL, e.g. http://127.0.0.1:9100/metrics.
	URL string `yaml:"url"`
	// Job becomes service.name of the target's metrics (default: the URL host).
	Job      string            `yaml:"job"`
	Interval Duration          `yaml:"interval"`
	Timeout  Duration          `yaml:"timeout"`
	Labels   map[string]string `yaml:"labels"`
	// BearerToken: env:NAME, file:/path or (discouraged) a literal.
	BearerToken Secret     `yaml:"bearer_token"`
	Username    string     `yaml:"username"`
	Password    Secret     `yaml:"password"`
	TLS         *TLSConfig `yaml:"tls"`
	// SampleLimit overrides prometheus.sample_limit for this target (0: inherit).
	SampleLimit int           `yaml:"sample_limit"`
	Metrics     *MetricFilter `yaml:"metrics"`
}

// MetricFilter keeps metric names that match an include pattern (all when empty) and no exclude pattern.
// Patterns are shell globs (path.Match), e.g. go_* or *_bucket.
type MetricFilter struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Keep reports whether a metric name passes the filter.
func (f MetricFilter) Keep(name string) bool {
	if len(f.Include) > 0 {
		ok := false
		for _, p := range f.Include {
			if m, _ := path.Match(p, name); m {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, p := range f.Exclude {
		if m, _ := path.Match(p, name); m {
			return false
		}
	}
	return true
}

// IsZero reports an empty filter.
func (f MetricFilter) IsZero() bool { return len(f.Include) == 0 && len(f.Exclude) == 0 }

func defaultPrometheus() PrometheusConfig {
	return PrometheusConfig{
		Enabled: true, Interval: Duration(30 * time.Second), Timeout: Duration(10 * time.Second),
		SampleLimit: 20000, BodyLimitBytes: 32 << 20, MaxTargets: 64, MaxConcurrent: 4,
		Containers: true, KubernetesPods: true,
	}
}

// EffectiveInterval returns the interval of a target.
func (p *PrometheusConfig) EffectiveInterval(t *ScrapeTarget) time.Duration {
	if t != nil && t.Interval > 0 {
		return t.Interval.D()
	}
	return p.Interval.D()
}

func (f MetricFilter) validate(prefix string) []error {
	var errs []error
	for _, list := range [][]string{f.Include, f.Exclude} {
		for _, p := range list {
			if _, err := path.Match(p, ""); err != nil || p == "" {
				errs = append(errs, fmt.Errorf("%sinvalid pattern %q", prefix, p))
			}
		}
	}
	return errs
}

func (p *PrometheusConfig) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if p.Interval.D() < time.Second {
		add("prometheus.interval must be at least 1s")
	}
	if p.Timeout.D() <= 0 || p.Timeout.D() > p.Interval.D() {
		add("prometheus.timeout must be positive and not longer than prometheus.interval")
	}
	if p.SampleLimit < 1 || p.SampleLimit > 1_000_000 {
		add("prometheus.sample_limit must be between 1 and 1000000")
	}
	if p.BodyLimitBytes < 64<<10 || p.BodyLimitBytes > 512<<20 {
		add("prometheus.body_limit_bytes must be between 65536 and 536870912")
	}
	if p.MaxTargets < 1 || p.MaxTargets > 1000 {
		add("prometheus.max_targets must be between 1 and 1000")
	}
	if p.MaxConcurrent < 1 {
		add("prometheus.max_concurrent must be >= 1")
	}
	errs = append(errs, p.Metrics.validate("prometheus.metrics: ")...)
	seen := map[string]bool{}
	for i, t := range p.Targets {
		pre := fmt.Sprintf("prometheus.targets[%d].", i)
		u, err := url.Parse(t.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("%surl must be an http(s) URL (got %q)", pre, t.URL)
		} else if u.User != nil {
			add("%surl must not contain credentials; use username/password or bearer_token", pre)
		}
		if seen[t.URL] {
			add("%surl %q is listed twice", pre, t.URL)
		}
		seen[t.URL] = true
		iv := p.EffectiveInterval(&p.Targets[i])
		if t.Interval != 0 && iv < time.Second {
			add("%sinterval must be at least 1s", pre)
		}
		if t.Timeout != 0 && (t.Timeout.D() <= 0 || t.Timeout.D() > iv) {
			add("%stimeout must be positive and not longer than the interval", pre)
		}
		if t.SampleLimit < 0 || t.SampleLimit > 1_000_000 {
			add("%ssample_limit must be between 0 and 1000000", pre)
		}
		if t.BearerToken != "" && (t.Username != "" || t.Password != "") {
			add("%sbearer_token and username/password are mutually exclusive", pre)
		}
		for _, s := range []struct {
			name string
			v    Secret
		}{{"bearer_token", t.BearerToken}, {"password", t.Password}} {
			if err := s.v.validate(); err != nil {
				add("%s%s: %v", pre, s.name, err)
			}
		}
		if t.TLS != nil && t.TLS.CAFile != "" && !isAbsPath(t.TLS.CAFile) {
			add("%stls.ca_file must be an absolute path", pre)
		}
		for k := range t.Labels {
			if strings.TrimSpace(k) == "" {
				add("%slabels contains an empty key", pre)
			}
		}
		if t.Metrics != nil {
			errs = append(errs, t.Metrics.validate(pre+"metrics: ")...)
		}
	}
	return errs
}

func (p *PrometheusConfig) warnings() []string {
	var out []string
	for i, t := range p.Targets {
		if t.BearerToken.IsLiteral() {
			out = append(out, fmt.Sprintf("prometheus.targets[%d].bearer_token is a literal value; prefer env:NAME or file:/path", i))
		}
		if t.Password.IsLiteral() {
			out = append(out, fmt.Sprintf("prometheus.targets[%d].password is a literal value; prefer env:NAME or file:/path", i))
		}
	}
	return out
}
