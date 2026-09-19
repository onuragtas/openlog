package integrations

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Resource attribute keys added to integration resources (semantic-conventions §6.1).
const (
	AttrDiscoveryID       = "openlog.discovery.id"
	AttrDiscoveryInstance = "openlog.discovery.instance"
	AttrIntegrationID     = "openlog.integration.id"
	AttrServiceInstanceID = "service.instance.id"
	AttrServerAddress     = "server.address"
	AttrServerPort        = "server.port"
)

// MaxBackoff caps the retry delay after failed collections.
const MaxBackoff = 5 * time.Minute

// maxPendingLogBatches bounds the event batches of one instance kept between two export rounds: unlike metrics
// (only the latest sample matters), every batch of statement deltas or session samples is distinct data, so they
// accumulate — up to this many, then the oldest are dropped.
const maxPendingLogBatches = 64

// Options configures a Manager.
type Options struct {
	Config       *config.IntegrationsConfig
	Integrations []Integration
	Rules        []*discovery.Rule
	FS           *hostfs.FS
	Log          *slog.Logger
	Stats        *selfmon.Stats
	// Resource is the host resource; its attributes start every integration resource.
	Resource     *resourcepb.Resource
	AgentName    string
	AgentVersion string
	HostName     string
	// RemoteStatePath persists the last remote integration config (0600);
	// empty disables persistence.
	RemoteStatePath string
}

// Manager binds integrations to discovered services and runs their collections.
type Manager struct {
	o        Options
	base     *config.IntegrationsConfig // config.yaml
	registry map[string]Integration
	rules    map[string]*discovery.Rule
	log      *slog.Logger
	sem      chan struct{}

	// reconcileMu serializes Reconcile (inventory rounds and remote config updates).
	reconcileMu sync.Mutex

	mu       sync.Mutex
	cfg      *config.IntegrationsConfig // effective: config.yaml with remote config applied
	remote   *config.RemoteIntegrations
	runners  map[string]*runner
	pending  map[string][]*metricspb.ResourceMetrics
	logs     map[string][]*logspb.ResourceLogs
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	slowdown float64

	// Last Reconcile input, replayed when the remote config changes.
	lastServices []discovery.Service
	lastCtrs     []containers.Container
	reconciled   bool

	// Now is the sample clock (tests).
	Now func() time.Time
}

// NewManager creates a manager; nothing runs until Reconcile (and Start for background collection).
func NewManager(o Options) *Manager {
	m := &Manager{
		o: o, base: o.Config, cfg: o.Config, registry: map[string]Integration{}, rules: map[string]*discovery.Rule{},
		log: o.Log, runners: map[string]*runner{}, pending: map[string][]*metricspb.ResourceMetrics{}, logs: map[string][]*logspb.ResourceLogs{},
		slowdown: 1, Now: time.Now,
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	for _, in := range o.Integrations {
		m.registry[in.ID()] = in
	}
	for _, r := range o.Rules {
		m.rules[r.ID] = r
	}
	n := o.Config.MaxConcurrent
	if n < 1 {
		n = 1
	}
	m.sem = make(chan struct{}, n)
	m.loadRemote()
	return m
}

// Start enables background collection; runners start on the next Reconcile
// (and existing ones immediately).
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	for _, r := range m.runners {
		m.startLocked(r)
	}
}

// Stop cancels all runners, waits for them and closes their connections.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runners {
		r.closeCollector()
	}
}

// SetSlowdown multiplies all collection intervals (resource budget back-off).
func (m *Manager) SetSlowdown(f float64) {
	m.mu.Lock()
	m.slowdown = max(f, 1)
	m.mu.Unlock()
}

func (m *Manager) interval(id string) time.Duration {
	m.mu.Lock()
	f, cfg := m.slowdown, m.cfg
	m.mu.Unlock()
	return time.Duration(float64(cfg.EffectiveInterval(id)) * f)
}

// Reconcile starts runners for new discovered services, restarts runners whose
// endpoints or settings changed and stops runners of vanished services.
func (m *Manager) Reconcile(services []discovery.Service, ctrs []containers.Container) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	m.mu.Lock()
	cfg := m.cfg
	m.lastServices, m.lastCtrs, m.reconciled = slices.Clone(services), slices.Clone(ctrs), true
	m.mu.Unlock()

	byID := map[string]containers.Container{}
	for _, c := range ctrs {
		byID[c.ID] = c
	}
	want := map[string]*runner{}
	sorted := slices.Clone(services)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })
	count := 0
	for _, s := range sorted {
		id := s.Integration.ID
		integ, ok := m.registry[id]
		if id == "" || !ok {
			continue
		}
		t := Target{Key: s.Key(), RuleID: s.RuleID, Instance: s.Instance, IntegrationID: id,
			Ports: s.Ports, Units: s.SystemdUnits}
		if r := m.rules[s.RuleID]; r != nil && r.Integration != nil {
			t.AutoEnable, t.Requires = r.Integration.AutoEnable, r.Integration.Requires
		}
		for _, cid := range s.ContainerIDs {
			if c, ok := byID[cid]; ok {
				t.Containers = append(t.Containers, c)
			}
		}
		r := m.build(cfg, integ, t)
		if r.static == nil {
			count++
			if count > cfg.MaxInstances {
				r.static = NotAvailable(fmt.Sprintf("integrations.max_instances (%d) reached", cfg.MaxInstances)).(*StatusError)
				r.setStatus(r.static, "")
			}
		}
		want[t.Key] = r
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for k, old := range m.runners {
		if nr, ok := want[k]; ok && nr.sig == old.sig {
			want[k] = old
			continue
		}
		old.stop()
		delete(m.pending, k)
		delete(m.logs, k)
		delete(m.runners, k)
		if _, ok := want[k]; !ok {
			m.log.Info("integration instance stopped", "integration", old.integ.ID(), "service", k)
		}
	}
	for k, r := range want {
		if _, ok := m.runners[k]; ok {
			continue
		}
		m.runners[k] = r
		if r.static == nil {
			m.log.Info("integration instance started", "integration", r.integ.ID(), "service", k,
				"endpoints", displays(r.inst.Endpoints))
		}
		m.startLocked(r)
	}
}

func displays(es []Endpoint) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Display)
	}
	return out
}

// build derives the instance and decides configuration-level statuses.
func (m *Manager) build(cfg *config.IntegrationsConfig, integ Integration, t Target) *runner {
	id := integ.ID()
	ic := cfg.Integration(id)
	spec := integ.Spec()
	inst := &Instance{Target: t, Timeout: cfg.Timeout.D(), FS: m.o.FS, Log: m.log.With("integration", id, "service", t.Key),
		HostName: m.o.HostName, Memo: &Memo{}}
	var derived []Endpoint
	if !spec.NoEndpoint {
		derived = DeriveEndpoints(t, spec, m.o.FS)
	}
	settings := ic.InstanceSettings
	instanceEnabled := true
	if i := MatchInstance(ic.Instances, t, derived); i >= 0 {
		settings = settings.Merge(ic.Instances[i].InstanceSettings)
		if e := ic.Instances[i].Enabled; e != nil {
			instanceEnabled = *e
		}
	}
	inst.Settings = settings
	inst.Endpoints = derived
	if settings.Endpoint != "" && !spec.NoEndpoint {
		inst.Explicit = true
		if id == config.IntegrationNginx {
			inst.Endpoints = []Endpoint{{Network: "url", Address: settings.Endpoint, Display: settings.Endpoint}}
		} else {
			inst.Endpoints = []Endpoint{ExplicitEndpoint(settings.Endpoint, m.o.FS)}
		}
	}
	r := &runner{m: m, integ: integ, inst: inst}
	// The password enters the signature only as a hash so that changed
	// credentials (e.g. from remote config) restart the instance.
	r.sig = fmt.Sprintf("%s|%v|%v|%s|%s|%s|%t|%t|%t", id, displays(inst.Endpoints), settings.Username, secretHash(settings.Password),
		settings.Database, strings.Join(settings.Databases, ","), instanceEnabled, ic.Enabled, cfg.Enabled)
	r.st = discovery.IntegrationStatus{ID: id, Status: discovery.StatusEnabled}

	hint := func() string { return integ.Hint(inst) }
	switch {
	case !cfg.Enabled:
		r.static = &StatusError{Status: discovery.StatusNotAvailable, Msg: "integrations are disabled (integrations.enabled: false)", Static: true}
	case !ic.Enabled:
		r.static = &StatusError{Status: discovery.StatusNotAvailable, Msg: "disabled in configuration (integrations." + id + ".enabled: false or disabled in openlog for this host)", Static: true}
	case !instanceEnabled:
		r.static = &StatusError{Status: discovery.StatusNotAvailable, Msg: "disabled for this instance in configuration", Static: true}
	case t.RequiresAny("credentials") && settings.Username == "" && settings.Password == "":
		r.static = &StatusError{Status: discovery.StatusNeedsConfiguration, Msg: "credentials required: set the username and password in openlog (host → Integrations) or integrations." + id + ".username and password", Static: true}
	case !spec.NoEndpoint && len(inst.Endpoints) == 0:
		r.static = &StatusError{Status: discovery.StatusNeedsConfiguration, Msg: "no endpoint could be derived from discovery: set the endpoint in openlog (host → Integrations) or integrations." + id + ".endpoint", Static: true}
	}
	if r.static != nil {
		h := ""
		if r.static.Status == discovery.StatusNeedsConfiguration {
			h = hint()
		}
		r.setStatus(r.static, h)
	}
	return r
}

func (m *Manager) startLocked(r *runner) {
	if m.ctx == nil || r.static != nil || r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	m.wg.Add(2)
	sampled := make(chan struct{})
	go func() {
		defer m.wg.Done()
		defer close(sampled)
		r.sampleLoop(ctx)
	}()
	go func() {
		defer m.wg.Done()
		defer close(r.done)
		defer func() { <-sampled }()
		r.loop(ctx)
	}()
}

// CollectOnce runs one collection of every active instance and waits for them
// (used by -once and tests).
func (m *Manager) CollectOnce(ctx context.Context) {
	m.mu.Lock()
	rs := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		if r.static == nil {
			rs = append(rs, r)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Go(func() { r.collect(ctx) })
	}
	wg.Wait()
}

// Drain returns and clears the metrics collected since the last call. Only the
// latest sample of each instance is kept.
func (m *Manager) Drain() []*metricspb.ResourceMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*metricspb.ResourceMetrics
	for _, k := range SortedKeys(m.pending) {
		out = append(out, m.pending[k]...)
	}
	clear(m.pending)
	return out
}

// DrainLogs returns and clears the events (database statistics, session samples, plans) recorded since the last call.
func (m *Manager) DrainLogs() []*logspb.ResourceLogs {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*logspb.ResourceLogs
	for _, k := range SortedKeys(m.logs) {
		out = append(out, m.logs[k]...)
	}
	clear(m.logs)
	return out
}

func (m *Manager) storeLogs(key string, rl *logspb.ResourceLogs) {
	if rl == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[key]; !ok {
		return
	}
	l := append(m.logs[key], rl)
	if len(l) > maxPendingLogBatches {
		l = l[len(l)-maxPendingLogBatches:]
	}
	m.logs[key] = l
}

func (m *Manager) store(key string, rms []*metricspb.ResourceMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[key]; ok {
		m.pending[key] = rms
	}
}

// Annotate sets integration status, error, hint and endpoint of discovered
// services from the running instances (semantic-conventions §3.4).
func (m *Manager) Annotate(services []discovery.Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range services {
		s := &services[i]
		id := s.Integration.ID
		if id == "" {
			continue
		}
		if _, ok := m.registry[id]; !ok {
			s.Integration = discovery.IntegrationStatus{ID: id, Status: discovery.StatusNotAvailable,
				Hint: "this agent version has no " + id + " integration"}
			continue
		}
		if r, ok := m.runners[s.Key()]; ok {
			s.Integration = r.status()
		}
	}
}

// StatusFingerprint hashes all instance statuses; pending is true while an
// active instance has not completed its first collection.
func (m *Manager) StatusFingerprint() (fp uint64, pending bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := fnv.New64a()
	for _, k := range SortedKeys(m.runners) {
		r := m.runners[k]
		st := r.status()
		r.mu.Lock()
		if r.static == nil && !r.verified {
			pending = true
		}
		r.mu.Unlock()
		fmt.Fprintf(h, "%s|%s|%s|%s;", k, st.Status, st.Error, st.Endpoint)
	}
	return h.Sum64(), pending
}

type runner struct {
	m     *Manager
	integ Integration
	inst  *Instance
	sig   string

	static *StatusError
	cancel context.CancelFunc
	done   chan struct{}

	collectMu sync.Mutex
	collector Collector
	// common are the batch-wide resource attributes of the last successful collection (e.g. the MySQL
	// service.instance.id): samples carry them too, so their events land on the same instance.
	common   []*commonpb.KeyValue
	current  int // index of the endpoint the collector uses
	failures int

	mu       sync.Mutex
	st       discovery.IntegrationStatus
	verified bool
}

func (r *runner) status() discovery.IntegrationStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

func (r *runner) setStatus(err error, hint string) {
	id := r.integ.ID()
	var st discovery.IntegrationStatus
	var se *StatusError
	var pe *PartialError
	switch {
	case err == nil:
		st = discovery.IntegrationStatus{ID: id, Status: discovery.StatusEnabled}
	case errors.As(err, &pe):
		st = discovery.IntegrationStatus{ID: id, Status: discovery.StatusEnabled, Error: r.sanitize(pe.Err)}
	case errors.As(err, &se):
		st = discovery.IntegrationStatus{ID: id, Status: se.Status, Error: r.sanitize(se), Hint: hint}
	default:
		st = discovery.IntegrationStatus{ID: id, Status: discovery.StatusError, Error: r.sanitize(err)}
	}
	r.mu.Lock()
	prev := r.st
	r.st = st
	r.mu.Unlock()
	if prev.Status != st.Status || prev.Error != st.Error {
		lvl := slog.LevelInfo
		if st.Status == discovery.StatusError {
			lvl = slog.LevelWarn
		}
		r.m.log.Log(context.Background(), lvl, "integration status", "integration", id, "service", r.inst.Target.Key,
			"status", st.Status, "error", st.Error)
	}
}

func (r *runner) sanitize(err error) string {
	var secrets []string
	if pw, e := r.inst.Settings.Password.Resolve(); e == nil && pw != "" {
		secrets = append(secrets, pw)
	}
	return SanitizeString(err.Error(), secrets...)
}

func (r *runner) stop() {
	if r.cancel != nil {
		r.cancel()
		// Do not wait while holding the manager lock for long: the runner exits
		// after its current collection (bounded by the timeout).
		go func(done chan struct{}) {
			<-done
			r.closeCollector()
		}(r.done)
		return
	}
	r.closeCollector()
}

func (r *runner) closeCollector() {
	r.collectMu.Lock()
	defer r.collectMu.Unlock()
	if r.collector != nil {
		r.collector.Close()
		r.collector = nil
	}
}

func (r *runner) loop(ctx context.Context) {
	iv := r.m.interval(r.integ.ID())
	first := time.Duration(rand.Int64N(int64(min(2*time.Second, iv)) + 1))
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := r.collect(ctx)
		next := r.m.interval(r.integ.ID())
		var se *StatusError
		if errors.As(err, &se) && se.Static {
			return
		}
		if err != nil {
			var pe *PartialError
			if !errors.As(err, &pe) {
				r.failures++
				next = max(next, min(next<<min(r.failures-1, 10), MaxBackoff))
			}
		} else {
			r.failures = 0
		}
		t.Reset(next)
	}
}

// sampleLoop runs the collector's Sampler, if it has one, between collections. It samples only once a
// collection succeeded (the collector exists and its endpoint is known) and never opens connections itself.
func (r *runner) sampleLoop(ctx context.Context) {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(r.sample(ctx))
	}
}

// sample takes one sample and returns the delay until the next attempt.
func (r *runner) sample(ctx context.Context) time.Duration {
	const idle = 5 * time.Second // how often to look again while there is no sampler (yet)
	r.collectMu.Lock()
	sp, ok := r.collector.(Sampler)
	current := r.current
	r.collectMu.Unlock()
	if !ok || sp.SampleInterval() <= 0 {
		return idle
	}
	iv := sp.SampleInterval()
	select {
	case r.m.sem <- struct{}{}:
	case <-ctx.Done():
		return iv
	}
	defer func() { <-r.m.sem }()
	r.collectMu.Lock()
	defer r.collectMu.Unlock()
	if r.collector == nil {
		return idle
	}
	sctx, cancel := context.WithTimeout(ctx, min(r.inst.Timeout, iv))
	defer cancel()
	batch := NewBatch(r.m.Now(), DefaultMaxPoints)
	for _, kv := range r.common {
		batch.SetResourceAttr(kv)
	}
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("sampler panic: %v", p)
			}
		}()
		return sp.Sample(sctx, batch)
	}()
	if err != nil {
		r.inst.Log.Debug("sample failed", "error", r.sanitize(err))
	}
	var ep Endpoint
	if current >= 0 && current < len(r.inst.Endpoints) {
		ep = r.inst.Endpoints[current]
	}
	r.m.storeLogs(r.inst.Target.Key, batch.ResourceLogs(r.baseAttrs(ep), r.scope()))
	return iv
}

// collect runs one collection: the current collector, or the endpoint
// candidates in order until one answers.
func (r *runner) collect(ctx context.Context) error {
	select {
	case r.m.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.m.sem }()
	r.collectMu.Lock()
	defer r.collectMu.Unlock()

	id := r.integ.ID()
	ctx, cancel := context.WithTimeout(ctx, r.inst.Timeout)
	defer cancel()
	start := time.Now()
	batch := NewBatch(r.m.Now(), DefaultMaxPoints)

	var err error
	used := -1
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("integration panic: %v", p)
			if r.collector != nil {
				r.collector.Close()
				r.collector = nil
			}
			r.finish(id, start, err, batch, used)
		}
	}()

	if r.collector != nil {
		used = r.current
		err = r.collector.Collect(ctx, batch)
		if err != nil && !isPartial(err) {
			r.collector.Close()
			r.collector = nil
			if IsUnreachable(err) || errors.Is(err, ErrTryNext) {
				batch = NewBatch(r.m.Now(), DefaultMaxPoints)
				err, used = r.probe(ctx, batch)
			}
		}
	} else {
		err, used = r.probe(ctx, batch)
	}
	r.finish(id, start, err, batch, used)
	return err
}

func isPartial(err error) bool {
	var pe *PartialError
	return errors.As(err, &pe)
}

// probe tries endpoint candidates, preferring the last one that worked. The
// reported error is the most specific one: server answers beat unreachable ones.
func (r *runner) probe(ctx context.Context, batch *Batch) (error, int) {
	eps := r.inst.Endpoints
	if r.integ.Spec().NoEndpoint {
		eps = []Endpoint{{}}
	}
	order := make([]int, 0, len(eps))
	if r.current > 0 && r.current < len(eps) {
		order = append(order, r.current)
	}
	for i := range eps {
		if i != r.current || r.current == 0 {
			order = append(order, i)
		}
	}
	var best error
	bestRank := -1
	usedIdx := -1
	for _, i := range order {
		if ctx.Err() != nil {
			break
		}
		c, err := r.integ.New(r.inst, eps[i])
		if err == nil {
			err = c.Collect(ctx, batch)
			if err == nil || isPartial(err) {
				r.collector, r.current = c, i
				return err, i
			}
			c.Close()
		}
		rank := 2
		switch {
		case IsUnreachable(err):
			rank = 0
		case errors.Is(err, ErrTryNext):
			rank = 1
		}
		if rank > bestRank {
			best, bestRank, usedIdx = err, rank, i
		}
		if rank == 2 {
			break // the server answered: the endpoint is right, the problem is elsewhere
		}
	}
	if best == nil {
		best = ctx.Err()
	}
	var se *StatusError
	if bestRank < 2 && !errors.As(best, &se) {
		if r.inst.Explicit {
			best = fmt.Errorf("%s: %w", r.inst.Endpoints[0].Display, best)
		} else if bestRank == 1 {
			best = NeedsConfiguration(strings.TrimSuffix(best.Error(), ": "+ErrTryNext.Error()), false)
		} else {
			best = fmt.Errorf("no endpoint reachable (%s): %w", strings.Join(displays(eps), ", "), best)
		}
	}
	return best, usedIdx
}

func (r *runner) finish(id string, start time.Time, err error, batch *Batch, used int) {
	failed := err != nil && !isPartial(err)
	r.m.o.Stats.AddIntegrationCollection(id, time.Since(start), failed)
	var ep Endpoint
	if used >= 0 && used < len(r.inst.Endpoints) {
		ep = r.inst.Endpoints[used]
	}
	if batch.Points() > 0 && (!failed || used >= 0) {
		if batch.Dropped() > 0 && err == nil {
			err = Partial(fmt.Errorf("cardinality guard: %d data points dropped", batch.Dropped()))
		}
		r.m.store(r.inst.Target.Key, batch.ResourceMetrics(r.baseAttrs(ep), r.scope()))
	}
	if !failed {
		r.common = batch.ResourceAttrs()
	}
	if len(batch.Events()) > 0 && (!failed || used >= 0) {
		r.m.storeLogs(r.inst.Target.Key, batch.ResourceLogs(r.baseAttrs(ep), r.scope()))
	}
	hint := ""
	var se *StatusError
	if errors.As(err, &se) && se.Status == discovery.StatusNeedsConfiguration {
		hint = r.integ.Hint(r.inst)
	}
	r.setStatus(err, hint)
	r.mu.Lock()
	r.verified = true
	if ep.Display != "" && !failed {
		r.st.Endpoint = ep.Display
	}
	r.mu.Unlock()
}

func (r *runner) scope() *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{Name: r.m.o.AgentName + "/integrations/" + r.integ.ID(), Version: r.m.o.AgentVersion}
}

func (r *runner) baseAttrs(ep Endpoint) []*commonpb.KeyValue {
	var attrs []*commonpb.KeyValue
	if r.m.o.Resource != nil {
		attrs = append(attrs, r.m.o.Resource.Attributes...)
	}
	t := r.inst.Target
	attrs = append(attrs,
		otlputil.Str(AttrDiscoveryID, t.RuleID),
		otlputil.Str(AttrDiscoveryInstance, t.Instance),
		otlputil.Str(AttrIntegrationID, r.integ.ID()),
	)
	switch ep.Network {
	case "tcp":
		attrs = append(attrs,
			otlputil.Str(AttrServiceInstanceID, ServiceInstanceID(ep, r.m.o.HostName)),
			otlputil.Str(AttrServerAddress, ep.Host()),
			otlputil.Int(AttrServerPort, int64(ep.Port())))
	case "unix":
		attrs = append(attrs,
			otlputil.Str(AttrServiceInstanceID, ServiceInstanceID(ep, r.m.o.HostName)),
			otlputil.Str(AttrServerAddress, strings.TrimPrefix(ep.Display, "unix:")))
	case "url":
		if e, ok := URLEndpoint(ep.Address); ok {
			attrs = append(attrs,
				otlputil.Str(AttrServiceInstanceID, ServiceInstanceID(e, r.m.o.HostName)),
				otlputil.Str(AttrServerAddress, e.Host()),
				otlputil.Int(AttrServerPort, int64(e.Port())))
		}
	}
	return attrs
}
