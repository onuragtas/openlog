package integrations

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// eventful is an integration whose collector records an event per collection and samples every 20ms.
type eventful struct {
	fake
	samples atomic.Int32
}

func (e *eventful) New(inst *Instance, ep Endpoint) (Collector, error) {
	return &eventfulCollector{fakeCollector: fakeCollector{f: &e.fake, ep: ep, inst: inst}, e: e}, nil
}

type eventfulCollector struct {
	fakeCollector
	e *eventful
}

func (c *eventfulCollector) Collect(ctx context.Context, b *Batch) error {
	b.SetResourceAttr(otlputil.Str("service.instance.id", "hashed-id"))
	b.Event("openlog.db.query_stats", "", otlputil.Str("db.system.name", "postgresql"))
	return c.fakeCollector.Collect(ctx, b)
}

func (c *eventfulCollector) SampleInterval() time.Duration { return 20 * time.Millisecond }

func (c *eventfulCollector) Sample(ctx context.Context, b *Batch) error {
	c.e.samples.Add(1)
	b.Event("openlog.db.session_sample", "", otlputil.Str("openlog.db.session.id", "7"))
	return nil
}

func TestEventsAndSamples(t *testing.T) {
	e := &eventful{fake: fake{results: map[string]error{}}}
	m := testManager(t, e, nil, rulesFor()...)
	s := svc(7000)
	m.Reconcile([]discovery.Service{s}, nil)
	m.CollectOnce(context.Background())
	rls := m.DrainLogs()
	if len(rls) != 1 || len(rls[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("logs after one collection = %v", rls)
	}
	res := rls[0].Resource.Attributes
	if attr(res, AttrServiceInstanceID) != "hashed-id" || attr(res, AttrIntegrationID) != "redis" {
		t.Errorf("event resource = %v", res)
	}
	if rec := rls[0].ScopeLogs[0].LogRecords[0]; attr(rec.Attributes, AttrEventName) != "openlog.db.query_stats" {
		t.Errorf("record = %v", rec)
	}
	if len(m.DrainLogs()) != 0 {
		t.Fatal("DrainLogs did not clear")
	}

	// Background: the sampler runs between collections and its batches accumulate.
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for e.samples.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	m.Stop()
	samples := 0
	for _, rl := range m.DrainLogs() {
		if attr(rl.Resource.Attributes, AttrServiceInstanceID) != "hashed-id" {
			t.Errorf("sample resource lacks the collection's instance id: %v", rl.Resource.Attributes)
		}
		for _, r := range rl.ScopeLogs[0].LogRecords {
			if attr(r.Attributes, AttrEventName) == "openlog.db.session_sample" {
				samples++
			}
		}
	}
	if samples < 3 {
		t.Fatalf("session samples drained = %d (sampled %d)", samples, e.samples.Load())
	}
}

func TestPendingLogsBounded(t *testing.T) {
	e := &eventful{fake: fake{results: map[string]error{}}}
	m := testManager(t, e, nil, rulesFor()...)
	m.Reconcile([]discovery.Service{svc(7000)}, nil)
	for range maxPendingLogBatches + 5 {
		m.CollectOnce(context.Background())
	}
	if n := len(m.DrainLogs()); n != maxPendingLogBatches {
		t.Fatalf("pending batches = %d", n)
	}
}
