package promscrape

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// acceptHeader prefers OpenMetrics (exemplars, _created) and falls back to the text format, like Prometheus.
const acceptHeader = "application/openmetrics-text;version=1.0.0;q=0.75,text/plain;version=0.0.4;q=0.5,*/*;q=0.1"

// maxPendingScrapes bounds the scrapes of one target kept between two export rounds (a target scraped faster
// than the agent interval); older ones are dropped.
const maxPendingScrapes = 8

// maxBackoff caps the delay after failed scrapes.
const maxBackoff = 5 * time.Minute

// Health metrics of every target, the names Prometheus gives them.
const (
	MetricUp             = "up"
	MetricScrapeDuration = "scrape_duration_seconds"
	MetricScrapeSamples  = "scrape_samples_scraped"
)

// Options configures a Manager.
type Options struct {
	Config *config.PrometheusConfig
	// Resource is the host resource; its attributes start every target resource.
	Resource     *resourcepb.Resource
	AgentName    string
	AgentVersion string
	Log          *slog.Logger
	Stats        *selfmon.Stats
	// Discover returns the discovered (container and pod) targets; nil disables discovery.
	Discover func(ctx context.Context) []Target
}

// Manager runs one scrape loop per target.
type Manager struct {
	o   Options
	log *slog.Logger
	sem chan struct{}

	mu       sync.Mutex
	runners  map[string]*runner
	pending  map[string][]*metricspb.ResourceMetrics
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	slowdown float64
	problems []string

	// Now is the sample clock (tests).
	Now func() time.Time
}

// NewManager creates a manager; nothing runs until Start (or CollectOnce after Refresh).
func NewManager(o Options) *Manager {
	m := &Manager{o: o, log: o.Log, runners: map[string]*runner{}, pending: map[string][]*metricspb.ResourceMetrics{},
		slowdown: 1, Now: time.Now}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	m.sem = make(chan struct{}, max(1, o.Config.MaxConcurrent))
	return m
}

// SetDiscover sets the discovery of container and pod targets; it must be called before Start.
func (m *Manager) SetDiscover(fn func(ctx context.Context) []Target) { m.o.Discover = fn }

// Start runs the target refresh loop and the scrape loops until Stop.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	ctx = m.ctx
	m.mu.Unlock()
	m.Refresh(ctx)
	if m.o.Discover == nil {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(m.o.Config.Interval.D())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Refresh(ctx)
			}
		}
	}()
}

// Stop cancels every loop and waits for them.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// SetSlowdown multiplies all scrape intervals (resource budget back-off).
func (m *Manager) SetSlowdown(f float64) {
	m.mu.Lock()
	m.slowdown = max(f, 1)
	m.mu.Unlock()
}

// Refresh recomputes the target set: static targets first, then discovered ones, bounded by max_targets.
func (m *Manager) Refresh(ctx context.Context) {
	targets := StaticTargets(m.o.Config)
	if m.o.Discover != nil {
		targets = append(targets, m.o.Discover(ctx)...)
	}
	m.Reconcile(targets)
}

// Reconcile starts loops for new targets, restarts changed ones and stops vanished ones.
func (m *Manager) Reconcile(targets []Target) {
	want := map[string]Target{}
	var dropped []string
	for _, t := range targets {
		if _, dup := want[t.Key]; dup {
			continue
		}
		if len(want) >= m.o.Config.MaxTargets {
			dropped = append(dropped, t.URL)
			continue
		}
		want[t.Key] = t
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(dropped) > 0 {
		m.problems = []string{fmt.Sprintf("prometheus.max_targets (%d) reached; not scraped: %s", m.o.Config.MaxTargets, strings.Join(dropped, ", "))}
		m.log.Warn("prometheus.max_targets reached", "max_targets", m.o.Config.MaxTargets, "not_scraped", len(dropped))
	} else {
		m.problems = nil
	}
	for k, r := range m.runners {
		t, ok := want[k]
		if ok && sig(t) == r.sig {
			continue
		}
		r.stop()
		delete(m.runners, k)
		delete(m.pending, k)
		if !ok {
			m.log.Info("prometheus target removed", "url", r.t.URL, "source", r.t.Source)
		}
	}
	for _, k := range sortedTargetKeys(want) {
		if _, ok := m.runners[k]; ok {
			continue
		}
		r, err := m.newRunner(want[k])
		if err != nil {
			m.log.Warn("prometheus target not scraped", "url", want[k].URL, "error", err)
			continue
		}
		m.runners[k] = r
		m.log.Info("prometheus target added", "url", r.t.URL, "job", r.t.Job, "source", r.t.Source)
		m.startLocked(r)
	}
}

// Targets returns the current targets with their last scrape result (sorted by key).
func (m *Manager) Targets() []TargetStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TargetStatus, 0, len(m.runners))
	for _, k := range sortedRunnerKeys(m.runners) {
		out = append(out, m.runners[k].status())
	}
	return out
}

// Problems returns target-level configuration problems of the last refresh.
func (m *Manager) Problems() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.problems...)
}

// TargetStatus is the health of one target.
type TargetStatus struct {
	URL, Job, Source string
	Up               bool
	LastScrape       time.Time
	LastError        string
	Samples          int
}

// CollectOnce scrapes every target once and waits (used by -once and tests).
func (m *Manager) CollectOnce(ctx context.Context) {
	m.mu.Lock()
	rs := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		rs = append(rs, r)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Go(func() { r.scrape(ctx) })
	}
	wg.Wait()
}

// Drain returns and clears the metrics scraped since the last call.
func (m *Manager) Drain() []*metricspb.ResourceMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*metricspb.ResourceMetrics
	keys := make([]string, 0, len(m.pending))
	for k := range m.pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, m.pending[k]...)
	}
	clear(m.pending)
	return out
}

func (m *Manager) store(key string, rm *metricspb.ResourceMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[key]; !ok {
		return
	}
	p := append(m.pending[key], rm)
	if len(p) > maxPendingScrapes {
		p = p[len(p)-maxPendingScrapes:]
	}
	m.pending[key] = p
}

func (m *Manager) interval(t Target) time.Duration {
	m.mu.Lock()
	f := m.slowdown
	m.mu.Unlock()
	return time.Duration(float64(m.o.Config.EffectiveInterval(t.Static)) * f)
}

func (m *Manager) startLocked(r *runner) {
	if m.ctx == nil || r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		r.loop(ctx)
	}()
}

// sig changes when a target must be restarted.
func sig(t Target) string {
	var b strings.Builder
	b.WriteString(t.URL + "|" + t.Job + "|")
	for _, a := range t.Attrs {
		b.WriteString(a.Key + "=" + a.Value.GetStringValue() + ";")
	}
	if s := t.Static; s != nil {
		fmt.Fprintf(&b, "|%v|%v|%d|%s|%s|%s|%v|%v", s.Interval, s.Timeout, s.SampleLimit, s.Username,
			secretHash(s.Password), secretHash(s.BearerToken), s.TLS, s.Metrics)
	}
	return b.String()
}

func secretHash(s config.Secret) string {
	v, err := s.Resolve()
	if err != nil || v == "" {
		return ""
	}
	return strconv.FormatUint(fnv64(v), 16)
}

func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

type runner struct {
	m      *Manager
	t      Target
	sig    string
	client *http.Client
	filter config.MetricFilter
	limit  int
	conv   *Converter
	cancel context.CancelFunc

	scrapeMu sync.Mutex // one scrape of a target at a time (loop and CollectOnce)

	mu     sync.Mutex
	st     TargetStatus
	failed int
}

func (m *Manager) newRunner(t Target) (*runner, error) {
	r := &runner{m: m, t: t, sig: sig(t), filter: m.o.Config.Metrics, limit: m.o.Config.SampleLimit, conv: NewConverter()}
	r.st = TargetStatus{URL: t.URL, Job: t.Job, Source: t.Source}
	tr := &http.Transport{MaxIdleConns: 1, IdleConnTimeout: 2 * time.Minute, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		Proxy: nil}
	if s := t.Static; s != nil {
		if s.SampleLimit > 0 {
			r.limit = s.SampleLimit
		}
		if s.Metrics != nil {
			r.filter = *s.Metrics
		}
		if tc := s.TLS; tc != nil {
			tr.TLSClientConfig.InsecureSkipVerify = tc.InsecureSkipVerify
			tr.TLSClientConfig.ServerName = tc.ServerName
			if tc.CAFile != "" {
				pem, err := os.ReadFile(tc.CAFile)
				if err != nil {
					return nil, fmt.Errorf("tls.ca_file: %w", err)
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(pem) {
					return nil, fmt.Errorf("tls.ca_file %s: no PEM certificate", tc.CAFile)
				}
				tr.TLSClientConfig.RootCAs = pool
			}
		}
	}
	// Redirects are followed only to the same host (an exporter behind a /metrics → /metrics/ redirect).
	r.client = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	return r, nil
}

func (r *runner) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.client.CloseIdleConnections()
}

func (r *runner) status() TargetStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

func (r *runner) loop(ctx context.Context) {
	iv := r.m.interval(r.t)
	// Spread the first scrapes of many targets over the interval.
	t := time.NewTimer(time.Duration(rand.Int64N(int64(min(iv, 5*time.Second)) + 1)))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := r.scrape(ctx)
		next := r.m.interval(r.t)
		if err != nil && ctx.Err() == nil {
			r.failed++
			next = max(next, min(next<<min(r.failed-1, 10), maxBackoff))
		} else {
			r.failed = 0
		}
		t.Reset(next)
	}
}

func (r *runner) timeout() time.Duration {
	if s := r.t.Static; s != nil && s.Timeout > 0 {
		return s.Timeout.D()
	}
	return r.m.o.Config.Timeout.D()
}

// scrape fetches, parses and converts one scrape and stores its resource (health metrics even on failure).
func (r *runner) scrape(ctx context.Context) error {
	select {
	case r.m.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.m.sem }()
	r.scrapeMu.Lock()
	defer r.scrapeMu.Unlock()

	now := r.m.Now()
	start := time.Now()
	metrics, samples, err := r.fetch(ctx, now)
	took := time.Since(start)
	if r.m.o.Stats != nil {
		r.m.o.Stats.AddIntegrationCollection(IntegrationID, took, err != nil)
	}
	up := int64(1)
	if err != nil {
		up, metrics, samples = 0, nil, 0
	}
	ts := uint64(now.UnixNano())
	gauge := func(name, unit string, v float64) *metricspb.Metric {
		return &metricspb.Metric{Name: name, Unit: unit, Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
			TimeUnixNano: ts, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}}}}}
	}
	metrics = append(metrics,
		gauge(MetricUp, "1", float64(up)),
		gauge(MetricScrapeDuration, "s", took.Seconds()),
		gauge(MetricScrapeSamples, "1", float64(samples)),
	)
	r.m.store(r.t.Key, &metricspb.ResourceMetrics{
		Resource: &resourcepb.Resource{Attributes: r.resource()},
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope:   &commonpb.InstrumentationScope{Name: r.m.o.AgentName + "/integrations/" + IntegrationID, Version: r.m.o.AgentVersion},
			Metrics: metrics,
		}},
	})

	r.mu.Lock()
	prevErr := r.st.LastError
	r.st.Up, r.st.LastScrape, r.st.Samples, r.st.LastError = err == nil, now, samples, ""
	if err != nil {
		r.st.LastError = err.Error()
	}
	r.mu.Unlock()
	if err != nil && err.Error() != prevErr {
		r.m.log.Warn("prometheus scrape failed", "url", r.t.URL, "job", r.t.Job, "error", err)
	} else if err == nil && prevErr != "" {
		r.m.log.Info("prometheus scrape recovered", "url", r.t.URL, "job", r.t.Job)
	}
	return err
}

func (r *runner) fetch(ctx context.Context, now time.Time) ([]*metricspb.Metric, int, error) {
	timeout := r.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.t.URL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("User-Agent", r.m.o.AgentName+"/"+r.m.o.AgentVersion)
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64))
	if s := r.t.Static; s != nil {
		if s.BearerToken != "" {
			tok, err := s.BearerToken.Resolve()
			if err != nil {
				return nil, 0, fmt.Errorf("bearer_token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		} else if s.Username != "" || s.Password != "" {
			pw, err := s.Password.Resolve()
			if err != nil {
				return nil, 0, fmt.Errorf("password: %w", err)
			}
			req.SetBasicAuth(s.Username, pw)
		}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, scrubURLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, 0, fmt.Errorf("server returned HTTP status %s", resp.Status)
	}
	limit := int64(r.m.o.Config.BodyLimitBytes)
	body := &limitedReader{r: resp.Body, n: limit}
	fams, err := Parse(body, FormatOf(resp.Header.Get("Content-Type")), Limits{MaxSamples: r.limit})
	if body.exceeded {
		return nil, 0, fmt.Errorf("body larger than prometheus.body_limit_bytes (%d)", limit)
	}
	if errors.Is(err, ErrSampleLimit) {
		return nil, 0, fmt.Errorf("more than %d samples (sample_limit)", r.limit)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("parse: %w", err)
	}
	samples := 0
	kept := fams[:0]
	for _, f := range fams {
		if !r.filter.IsZero() && !r.filter.Keep(f.Name) {
			continue
		}
		samples += len(f.Samples)
		kept = append(kept, f)
	}
	return r.conv.Convert(kept, now), samples, nil
}

// resource: host resource, then the target's own attributes, then the scrape identity.
func (r *runner) resource() []*commonpb.KeyValue {
	var base []*commonpb.KeyValue
	if res := r.m.o.Resource; res != nil {
		base = res.Attributes
	}
	id := []*commonpb.KeyValue{
		otlputil.Str(AttrIntegrationID, IntegrationID),
		otlputil.Str(AttrScrapeSource, r.t.Source),
		otlputil.Str(AttrServiceName, r.t.Job),
		otlputil.Str(AttrServiceInstanceID, r.t.Instance),
	}
	if u := r.t.URL; u != "" {
		if host, port, ok := splitHostPort(r.t.Instance); ok {
			id = append(id, otlputil.Str(AttrServerAddress, host), otlputil.Int(AttrServerPort, int64(port)))
		}
		scheme, _, _ := strings.Cut(u, "://")
		id = append(id, otlputil.Str(AttrURLScheme, scheme))
	}
	return mergeAttrs(base, r.t.Attrs, id)
}

func splitHostPort(hp string) (string, int, bool) {
	i := strings.LastIndexByte(hp, ':')
	if i < 0 {
		return hp, 0, false
	}
	p, err := strconv.Atoi(hp[i+1:])
	if err != nil {
		return hp, 0, false
	}
	return strings.Trim(hp[:i], "[]"), p, true
}

// mergeAttrs concatenates attribute lists; later lists override earlier keys.
func mergeAttrs(lists ...[]*commonpb.KeyValue) []*commonpb.KeyValue {
	idx := map[string]int{}
	var out []*commonpb.KeyValue
	for _, l := range lists {
		for _, a := range l {
			if i, ok := idx[a.Key]; ok {
				out[i] = a
				continue
			}
			idx[a.Key] = len(out)
			out = append(out, a)
		}
	}
	return out
}

// limitedReader stops after n bytes and remembers that the body was longer.
type limitedReader struct {
	r        io.Reader
	n        int64
	exceeded bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// Probe one byte to tell an exactly-sized body from a longer one.
		var one [1]byte
		n, err := l.r.Read(one[:])
		if n > 0 {
			l.exceeded = true
			return 0, errors.New("body limit exceeded")
		}
		return 0, err
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// scrubURLError drops the URL from *url.Error (it is logged next to it anyway and may carry a query token).
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func sortedTargetKeys(m map[string]Target) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedRunnerKeys(m map[string]*runner) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
