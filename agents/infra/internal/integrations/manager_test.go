package integrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// fake is a scriptable integration: behaviour per endpoint address.
type fake struct {
	mu      sync.Mutex
	results map[string]error // missing = success
	creates atomic.Int32
	closes  atomic.Int32
	seen    []string
}

func (f *fake) ID() string { return "fake" }
func (f *fake) Spec() EndpointSpec {
	return EndpointSpec{DefaultPort: 7000, SkipPort: func(p int) bool { return p == 7001 }}
}
func (f *fake) Hint(inst *Instance) string { return "integrations:\n  fake: {}" }
func (f *fake) New(inst *Instance, ep Endpoint) (Collector, error) {
	f.creates.Add(1)
	return &fakeCollector{f: f, ep: ep, inst: inst}, nil
}
func (f *fake) set(addr string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.results, addr)
	} else {
		f.results[addr] = err
	}
}

type fakeCollector struct {
	f    *fake
	ep   Endpoint
	inst *Instance
}

func (c *fakeCollector) Close() { c.f.closes.Add(1) }
func (c *fakeCollector) Collect(ctx context.Context, b *Batch) error {
	c.f.mu.Lock()
	err := c.f.results[c.ep.Address]
	c.f.seen = append(c.f.seen, c.ep.Address)
	c.f.mu.Unlock()
	var pe *PartialError
	if err != nil && !errors.As(err, &pe) {
		return err
	}
	b.Resource().SumInt("fake.requests", "{requests}", true, 42)
	b.Resource(otlputil.Str("fake.db", "a")).GaugeInt("fake.size", "By", 7)
	return err
}

// Integrations only reach config ids known to IntegrationsConfig; "fake" reuses redis settings.
func testManager(t *testing.T, f Integration, mutate func(*config.IntegrationsConfig), rules ...*discovery.Rule) *Manager {
	t.Helper()
	cfg := config.Default().Integrations
	cfg.Timeout = config.Duration(time.Second)
	if mutate != nil {
		mutate(&cfg)
	}
	if len(rules) == 0 {
		rules = []*discovery.Rule{{ID: "svc", Integration: &discovery.Integration{ID: "fake", AutoEnable: true}}}
	}
	return NewManager(Options{
		Config: &cfg, Integrations: []Integration{&renamed{f, "redis"}}, Rules: rules,
		Stats: selfmon.New(time.Now()), HostName: "web-1", AgentName: "openlog-infra-agent", AgentVersion: "test",
		Resource: &resourcepb.Resource{Attributes: nil},
	})
}

// renamed maps the fake onto a configured integration id.
type renamed struct {
	Integration
	id string
}

func (r *renamed) ID() string { return r.id }

func svc(ports ...int) discovery.Service {
	s := discovery.Service{RuleID: "svc", Instance: "/usr/bin/svc", Integration: discovery.IntegrationStatus{ID: "redis", Status: "enabled"}}
	for _, p := range ports {
		s.Ports = append(s.Ports, discovery.PortRef{Protocol: "tcp", Address: "0.0.0.0", Port: p})
	}
	return s
}

func rulesFor(requires ...string) []*discovery.Rule {
	return []*discovery.Rule{{ID: "svc", Integration: &discovery.Integration{ID: "redis", AutoEnable: true, Requires: requires}}}
}

func status(m *Manager, s discovery.Service) discovery.IntegrationStatus {
	ss := []discovery.Service{s}
	m.Annotate(ss)
	return ss[0].Integration
}

func attr(attrs []*commonKV, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			if a.Value.GetStringValue() != "" {
				return a.Value.GetStringValue()
			}
			return fmt.Sprint(a.Value.GetIntValue())
		}
	}
	return ""
}

func TestCollectEnabledWithResourceAttributes(t *testing.T) {
	f := &fake{results: map[string]error{}}
	m := testManager(t, f, nil, rulesFor()...)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)
	m.CollectOnce(context.Background())

	st := status(m, s)
	if st.Status != discovery.StatusEnabled || st.Endpoint != "127.0.0.1:7000" || st.Error != "" {
		t.Fatalf("status = %+v", st)
	}
	rms := m.Drain()
	if len(rms) != 2 {
		t.Fatalf("resources = %d, want 2", len(rms))
	}
	r := rms[0].Resource.Attributes
	for k, want := range map[string]string{
		"openlog.discovery.id": "svc", "openlog.discovery.instance": "/usr/bin/svc", "openlog.integration.id": "redis",
		"service.instance.id": "web-1:7000", "server.address": "127.0.0.1", "server.port": "7000",
	} {
		if got := attr(r, k); got != want {
			t.Errorf("resource %s = %q, want %q", k, got, want)
		}
	}
	if attr(rms[1].Resource.Attributes, "fake.db") != "a" || attr(rms[1].Resource.Attributes, "openlog.integration.id") != "redis" {
		t.Errorf("entity resource = %v", rms[1].Resource.Attributes)
	}
	if rms[0].ScopeMetrics[0].Scope.Name != "openlog-infra-agent/integrations/redis" {
		t.Errorf("scope = %v", rms[0].ScopeMetrics[0].Scope)
	}
	if len(m.Drain()) != 0 {
		t.Error("drain must clear pending metrics")
	}
	snap := m.o.Stats.Snapshot()
	if snap.IntegrationCollections["redis"] != 1 || snap.IntegrationErrors["redis"] != 0 {
		t.Errorf("self telemetry = %v %v", snap.IntegrationCollections, snap.IntegrationErrors)
	}
}

func TestEndpointFallbackAndSkip(t *testing.T) {
	f := &fake{results: map[string]error{"127.0.0.1:7000": fmt.Errorf("dial: %w", ErrUnreachable)}}
	m := testManager(t, f, nil, rulesFor()...)
	s := svc(7001, 7002, 7000)
	m.Reconcile([]discovery.Service{s}, nil)
	m.CollectOnce(context.Background())
	if st := status(m, s); st.Status != discovery.StatusEnabled || st.Endpoint != "127.0.0.1:7002" {
		t.Fatalf("status = %+v", st)
	}
	for _, a := range f.seen {
		if strings.HasSuffix(a, ":7001") {
			t.Errorf("skipped port was tried: %v", f.seen)
		}
	}
	// All unreachable → error listing endpoints.
	f.set("127.0.0.1:7002", fmt.Errorf("dial: %w", ErrUnreachable))
	m.CollectOnce(context.Background()) // established collector fails → re-probe
	st := status(m, s)
	if st.Status != discovery.StatusError || !strings.Contains(st.Error, "no endpoint reachable (127.0.0.1:7000, 127.0.0.1:7002)") {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusTransitionsAndSanitizing(t *testing.T) {
	t.Setenv("FAKE_PW", "hunter2")
	f := &fake{results: map[string]error{}}
	m := testManager(t, f, func(c *config.IntegrationsConfig) {
		c.Redis.Username, c.Redis.Password = "openlog", "env:FAKE_PW"
	}, rulesFor("credentials")...)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)

	if st := status(m, s); st.Status != discovery.StatusEnabled {
		t.Fatalf("pending status = %+v", st)
	}
	if _, pending := m.StatusFingerprint(); !pending {
		t.Error("fingerprint must report pending before the first collection")
	}

	f.set("127.0.0.1:7000", errors.New("Error 1045: Access denied for openlog:hunter2@tcp(127.0.0.1:7000)/ password=hunter2"))
	m.CollectOnce(context.Background())
	st := status(m, s)
	if st.Status != discovery.StatusError || strings.Contains(st.Error, "hunter2") || !strings.Contains(st.Error, "Access denied") {
		t.Fatalf("error status = %+v", st)
	}
	fp1, pending := m.StatusFingerprint()
	if pending {
		t.Error("still pending after a collection")
	}

	f.set("127.0.0.1:7000", NeedsConfiguration("authentication required", false))
	m.CollectOnce(context.Background())
	if st := status(m, s); st.Status != discovery.StatusNeedsConfiguration || st.Hint == "" {
		t.Fatalf("needs_configuration status = %+v", st)
	}

	f.set("127.0.0.1:7000", Partial(errors.New("replica status: permission denied")))
	m.CollectOnce(context.Background())
	if st := status(m, s); st.Status != discovery.StatusEnabled || !strings.Contains(st.Error, "permission denied") {
		t.Fatalf("partial status = %+v", st)
	}
	if len(m.Drain()) == 0 {
		t.Error("partial collection must keep its data")
	}

	f.set("127.0.0.1:7000", nil)
	m.CollectOnce(context.Background())
	st = status(m, s)
	if st.Status != discovery.StatusEnabled || st.Error != "" {
		t.Fatalf("recovered status = %+v", st)
	}
	if fp2, _ := m.StatusFingerprint(); fp2 == fp1 {
		t.Error("fingerprint must change with the status")
	}
	if e := m.o.Stats.Snapshot().IntegrationErrors["redis"]; e != 2 {
		t.Errorf("errors counted = %d, want 2 (partial is not an error)", e)
	}
}

func TestStaticStatuses(t *testing.T) {
	f := &fake{results: map[string]error{}}
	s := svc(7000)

	m := testManager(t, f, nil, rulesFor("credentials")...)
	m.Reconcile([]discovery.Service{s}, nil)
	st := status(m, s)
	if st.Status != discovery.StatusNeedsConfiguration || !strings.Contains(st.Error, "credentials required") || st.Hint == "" {
		t.Errorf("missing credentials = %+v", st)
	}
	m.CollectOnce(context.Background())
	if f.creates.Load() != 0 {
		t.Error("no connection may be attempted without credentials")
	}

	m = testManager(t, f, func(c *config.IntegrationsConfig) { c.Redis.Enabled = false }, rulesFor()...)
	m.Reconcile([]discovery.Service{s}, nil)
	if st := status(m, s); st.Status != discovery.StatusNotAvailable || !strings.Contains(st.Error, "disabled") {
		t.Errorf("disabled = %+v", st)
	}

	m = testManager(t, f, func(c *config.IntegrationsConfig) {
		off := false
		c.Redis.Instances = []config.InstanceConfig{{Match: config.InstanceMatch{Port: 7000}, Enabled: &off}}
	}, rulesFor()...)
	m.Reconcile([]discovery.Service{s}, nil)
	if st := status(m, s); st.Status != discovery.StatusNotAvailable {
		t.Errorf("instance disabled = %+v", st)
	}

	// Nothing known about the sockets: the default port on loopback.
	m = testManager(t, f, nil, rulesFor()...)
	m.Reconcile([]discovery.Service{svc()}, nil)
	m.CollectOnce(context.Background())
	if st := status(m, svc()); st.Status != discovery.StatusEnabled || st.Endpoint != "127.0.0.1:7000" {
		t.Errorf("loopback default port = %+v", st)
	}
	// Without a default port no endpoint can be derived.
	m = testManager(t, &noDefaultPort{f}, nil, rulesFor()...)
	m.Reconcile([]discovery.Service{svc()}, nil)
	if st := status(m, svc()); st.Status != discovery.StatusNeedsConfiguration || !strings.Contains(st.Error, "no endpoint") {
		t.Errorf("no endpoint = %+v", st)
	}

	other := []discovery.Service{{RuleID: "mongodb", Instance: "/usr/bin/mongod", Integration: discovery.IntegrationStatus{ID: "mongodb", Status: "needs_configuration"}}}
	m.Annotate(other)
	if other[0].Integration.Status != discovery.StatusNotAvailable || other[0].Integration.ID != "mongodb" {
		t.Errorf("unimplemented integration = %+v", other[0].Integration)
	}
}

func TestInstanceOverride(t *testing.T) {
	f := &fake{results: map[string]error{}}
	var got config.InstanceSettings
	m := testManager(t, &captureIntegration{fake: f, got: &got}, func(c *config.IntegrationsConfig) {
		c.Redis.Password = "env:DEFAULT"
		c.Redis.Instances = []config.InstanceConfig{
			{Match: config.InstanceMatch{Port: 9999}, InstanceSettings: config.InstanceSettings{Username: "wrong"}},
			{Match: config.InstanceMatch{Endpoint: "127.0.0.1:7000"}, InstanceSettings: config.InstanceSettings{Username: "u1", Endpoint: "unix:/run/svc.sock"}},
		}
	}, rulesFor()...)
	m.Reconcile([]discovery.Service{svc(7000)}, nil)
	m.CollectOnce(context.Background())
	if got.Username != "u1" || got.Password != "env:DEFAULT" || got.Endpoint != "unix:/run/svc.sock" {
		t.Errorf("merged settings = %+v", got)
	}
	if st := status(m, svc(7000)); st.Endpoint != "unix:/run/svc.sock" {
		t.Errorf("explicit endpoint = %+v", st)
	}
}

type noDefaultPort struct{ *fake }

func (noDefaultPort) Spec() EndpointSpec { return EndpointSpec{} }

func TestRemoteConfigAppliedWithoutRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	f := &fake{results: map[string]error{}}
	var got config.InstanceSettings
	state := filepath.Join(t.TempDir(), "state", RemoteStateFile)
	newManager := func(mutate func(*config.IntegrationsConfig)) *Manager {
		m := testManager(t, &captureIntegration{fake: f, got: &got}, mutate, rulesFor("credentials")...)
		m.o.RemoteStatePath = state
		m.loadRemote()
		return m
	}
	m := newManager(nil)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)
	if st := status(m, s); st.Status != discovery.StatusNeedsConfiguration || m.RemoteRevision() != "" {
		t.Fatalf("before remote config = %+v rev %q", st, m.RemoteRevision())
	}

	rc := &config.RemoteIntegrations{Revision: "sha256:1", Items: []config.RemoteItem{
		{Integration: "redis", Enabled: true, Match: &config.RemoteMatch{Instance: "/usr/bin/svc"}, Username: "u", Password: "pw-1"},
	}}
	if !m.SetRemote(rc) || m.SetRemote(rc) || m.SetRemote(nil) {
		t.Fatal("SetRemote must report exactly one change")
	}
	m.CollectOnce(context.Background())
	if st := status(m, s); st.Status != discovery.StatusEnabled || got.Username != "u" {
		t.Fatalf("after remote config = %+v settings %+v", st, got)
	}
	if pw, _ := got.Password.Resolve(); pw != "pw-1" || m.RemoteRevision() != "sha256:1" {
		t.Errorf("password/revision = %q %q", pw, m.RemoteRevision())
	}
	fi, err := os.Stat(state)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("persisted state = %v %v", fi, err)
	}

	// A changed password restarts the instance.
	creates := f.creates.Load()
	rc2 := &config.RemoteIntegrations{Revision: "sha256:2", Items: []config.RemoteItem{
		{Integration: "redis", Enabled: true, Match: &config.RemoteMatch{Instance: "/usr/bin/svc"}, Username: "u", Password: "pw-2"},
	}}
	m.SetRemote(rc2)
	m.CollectOnce(context.Background())
	if pw, _ := got.Password.Resolve(); pw != "pw-2" || f.creates.Load() == creates {
		t.Errorf("password change not applied (%q, creates %d)", pw, f.creates.Load())
	}

	// Restart: the persisted config is effective before the backend answers.
	m2 := newManager(nil)
	m2.Reconcile([]discovery.Service{s}, nil)
	m2.CollectOnce(context.Background())
	if st := status(m2, s); st.Status != discovery.StatusEnabled || m2.RemoteRevision() != "sha256:2" {
		t.Errorf("after restart = %+v rev %q", st, m2.RemoteRevision())
	}

	// Disable on this host.
	m2.SetRemote(&config.RemoteIntegrations{Revision: "sha256:3", Items: []config.RemoteItem{{Integration: "redis", Enabled: false}}})
	if st := status(m2, s); st.Status != discovery.StatusNotAvailable {
		t.Errorf("disabled remotely = %+v", st)
	}

	// integrations.remote_config: false ignores remote and persisted config.
	m3 := newManager(func(c *config.IntegrationsConfig) { c.RemoteConfig = false })
	m3.Reconcile([]discovery.Service{s}, nil)
	if m3.SetRemote(rc) || m3.RemoteRevision() != config.RevisionDisabled {
		t.Error("remote config must be ignored")
	}
	if st := status(m3, s); st.Status != discovery.StatusNeedsConfiguration {
		t.Errorf("opted out = %+v", st)
	}
}

type captureIntegration struct {
	*fake
	got *config.InstanceSettings
}

func (c *captureIntegration) New(inst *Instance, ep Endpoint) (Collector, error) {
	*c.got = inst.Settings
	return c.fake.New(inst, ep)
}

func TestLifecycleStartStop(t *testing.T) {
	f := &fake{results: map[string]error{}}
	m := testManager(t, f, func(c *config.IntegrationsConfig) { c.Interval = config.Duration(50 * time.Millisecond) }, rulesFor()...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, pending := m.StatusFingerprint(); !pending && len(m.Drain()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("instance did not collect after the service appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Same service again: runner kept (no new collector).
	creates := f.creates.Load()
	m.Reconcile([]discovery.Service{s}, nil)
	if f.creates.Load() != creates {
		t.Error("unchanged service must keep its runner")
	}
	// Service disappears: runner stops and closes its connection.
	m.Reconcile(nil, nil)
	for f.closes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("collector not closed after the service disappeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fp, _ := m.StatusFingerprint(); fp == 0 {
		t.Log("empty fingerprint")
	}
	m.Stop()
}

func TestPanicIsContained(t *testing.T) {
	m := testManager(t, panicky{&fake{results: map[string]error{}}}, nil, rulesFor()...)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)
	m.CollectOnce(context.Background())
	if st := status(m, s); st.Status != discovery.StatusError || !strings.Contains(st.Error, "panic") {
		t.Fatalf("status = %+v", st)
	}
}

type panicky struct{ *fake }

func (panicky) New(*Instance, Endpoint) (Collector, error) { return panicCollector{}, nil }

type panicCollector struct{}

func (panicCollector) Close()                                {}
func (panicCollector) Collect(context.Context, *Batch) error { panic("boom") }

func TestMaxInstances(t *testing.T) {
	f := &fake{results: map[string]error{}}
	m := testManager(t, f, func(c *config.IntegrationsConfig) { c.MaxInstances = 1 }, rulesFor()...)
	a, b := svc(7000), svc(7000)
	b.Instance = "/usr/local/bin/svc"
	m.Reconcile([]discovery.Service{a, b}, nil)
	ss := []discovery.Service{a, b}
	m.Annotate(ss)
	if ss[0].Integration.Status != discovery.StatusEnabled || ss[1].Integration.Status != discovery.StatusNotAvailable {
		t.Errorf("statuses = %+v / %+v", ss[0].Integration, ss[1].Integration)
	}
}

func TestDeriveEndpoints(t *testing.T) {
	spec := EndpointSpec{DefaultPort: 6379, SkipPort: func(p int) bool { return p == 16379 }}
	tg := Target{
		Ports: []discovery.PortRef{
			{Protocol: "tcp", Address: "::", Port: 7000}, {Protocol: "tcp", Address: "0.0.0.0", Port: 6379},
			{Protocol: "tcp", Address: "10.0.0.5", Port: 16379}, {Protocol: "udp", Address: "0.0.0.0", Port: 6379},
		},
		Containers: []containers.Container{{
			ID: strings.Repeat("a", 64), Name: "cache", IPs: []string{"172.18.0.2"},
			Ports: []containers.Port{{IP: "0.0.0.0", PrivatePort: 6379, PublicPort: 16380, Protocol: "tcp"}},
		}},
	}
	got := displays(DeriveEndpoints(tg, spec, nil))
	want := []string{"127.0.0.1:6379", "[::1]:7000", "127.0.0.1:16380", "172.18.0.2:6379"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("endpoints = %v, want %v", got, want)
	}
	if i := MatchInstance([]config.InstanceConfig{{Match: config.InstanceMatch{Container: "cache"}}}, tg, nil); i != 0 {
		t.Error("container name match")
	}
	if i := MatchInstance([]config.InstanceConfig{{Match: config.InstanceMatch{Container: "aaaaaaaaaaaa", Port: 16380}}}, tg, nil); i != 0 {
		t.Error("container id prefix + public port match")
	}
	if i := MatchInstance([]config.InstanceConfig{{Match: config.InstanceMatch{Port: 1, Container: "cache"}}}, tg, nil); i != -1 {
		t.Error("all match fields must match")
	}
	if id := ServiceInstanceID(TCP("127.0.0.1", 5432), "db-1"); id != "db-1:5432" {
		t.Errorf("service.instance.id = %s", id)
	}
	if id := ServiceInstanceID(TCP("10.1.2.3", 5432), "db-1"); id != "10.1.2.3:5432" {
		t.Errorf("service.instance.id = %s", id)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"dial openlog:s3cret@tcp(10.0.0.1:3306)/": "dial openlog:***@tcp(10.0.0.1:3306)/",
		"postgres://u:pw@host/db failed":          "postgres://u:***@host/db failed",
		"password=abc host=x":                     "password=*** host=x",
		"line1\n\tline2":                          "line1 line2",
		"token hunter2 in message":                "token *** in message",
		`{"user":"u","password":"abc"}`:           `{"user":"u","password":"***"}`,
		// Server messages stay readable.
		"Access denied for user 'openlog'@'172.18.0.1' (using password: YES)": "Access denied for user 'openlog'@'172.18.0.1' (using password: YES)",
		"WRONGPASS invalid username-password pair or user is disabled.":       "WRONGPASS invalid username-password pair or user is disabled.",
	}
	for in, want := range cases {
		if got := SanitizeString(in, "hunter2"); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SanitizeString(strings.Repeat("x", 1000)); len(got) != MaxErrorBytes {
		t.Errorf("truncate: %d", len(got))
	}
}

func TestBatchCardinalityGuard(t *testing.T) {
	b := NewBatch(time.Unix(100, 0), 3)
	s := b.Resource()
	for i := range 5 {
		s.SumInt("m", "1", true, int64(i))
	}
	if b.Points() != 3 || b.Dropped() != 2 {
		t.Errorf("points=%d dropped=%d", b.Points(), b.Dropped())
	}
	b.SetStartTime(time.Unix(50, 0))
	s.GaugeInt("g", "1", 1) // dropped too
	rms := b.ResourceMetrics(nil, nil)
	if len(rms) != 1 || len(rms[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints) != 3 {
		t.Errorf("rms = %v", rms)
	}
}
