package agent

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/infra/internal/buffer"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// fakeSender records delivered payloads and can simulate an outage (503 with
// Retry-After), a hanging ingest, or an ingest that blocks until cancelled.
type fakeSender struct {
	down         atomic.Bool
	block        atomic.Int64 // per-send delay in nanoseconds
	blockForever atomic.Bool

	mu          sync.Mutex
	metricTimes []uint64 // sample timestamp of each delivered metrics payload
	logs        []*logspb.LogRecord
	traces      []*tracepb.TracesData
}

func (f *fakeSender) Send(ctx context.Context, signal exporter.Signal, data []byte) error {
	if f.blockForever.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	if d := time.Duration(f.block.Load()); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	if f.down.Load() {
		return &exporter.Error{StatusCode: 503, Retryable: true, RetryAfter: 20 * time.Millisecond, Err: errors.New("kafka unavailable")}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch signal {
	case exporter.SignalMetrics:
		var md metricspb.MetricsData
		if err := proto.Unmarshal(data, &md); err != nil {
			return err
		}
		f.metricTimes = append(f.metricTimes, sampleTime(&md))
	case exporter.SignalLogs:
		var ld logspb.LogsData
		if err := proto.Unmarshal(data, &ld); err != nil {
			return err
		}
		for _, rl := range ld.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				f.logs = append(f.logs, sl.LogRecords...)
			}
		}
	case exporter.SignalTraces:
		var td tracepb.TracesData
		if err := proto.Unmarshal(data, &td); err != nil {
			return err
		}
		f.traces = append(f.traces, &td)
	}
	return nil
}

func (f *fakeSender) delivered() ([]uint64, []*logspb.LogRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.metricTimes), slices.Clone(f.logs)
}

func sampleTime(md *metricspb.MetricsData) uint64 {
	m := md.ResourceMetrics[0].ScopeMetrics[0].Metrics[0]
	if g := m.GetGauge(); g != nil {
		return g.DataPoints[0].TimeUnixNano
	}
	return m.GetSum().DataPoints[0].TimeUnixNano
}

func newPipelineAgent(t *testing.T, interval time.Duration, f *fakeSender) *Agent {
	t.Helper()
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	cfg := testConfig(t, fs.Root(), "http://ingest.invalid:4318")
	cfg.Interval = config.Duration(interval)
	cfg.Export.MaxRequestBytes = 8 << 10 // snapshots span several payloads
	a, err := New(cfg, "test", quiet(), true)
	if err != nil {
		t.Fatal(err)
	}
	a.pipe.sender = f
	a.pipe.maxItems = 3 // force spills to disk during an outage
	a.pipe.retryBase, a.pipe.retryMax = 10*time.Millisecond, 40*time.Millisecond
	return a
}

// checkSnapshots asserts that every snapshot-complete record follows all item
// records of its snapshot and reports the right item count.
func checkSnapshots(t *testing.T, recs []*logspb.LogRecord) int {
	t.Helper()
	items := map[string]int{}
	complete := map[string]bool{}
	for _, r := range recs {
		attrs := map[string]string{}
		var count int64
		for _, kv := range r.Attributes {
			attrs[kv.Key] = kv.Value.GetStringValue()
			if kv.Key == inventory.AttrItemCount {
				count = kv.Value.GetIntValue()
			}
		}
		id := attrs[inventory.AttrSnapshotID]
		switch attrs[inventory.AttrEventName] {
		case inventory.EventItem:
			if complete[id] {
				t.Fatalf("item record of snapshot %s arrived after its snapshot-complete record", id)
			}
			items[id]++
		case inventory.EventSnapshot:
			if int64(items[id]) != count {
				t.Fatalf("snapshot %s complete with %d items delivered, item_count %d", id, items[id], count)
			}
			complete[id] = true
		}
	}
	return len(complete)
}

// Collection must stay on schedule (±25%, at most one dropped tick) while the exporter is stalled, and
// every sample must be delivered, in order, once ingest recovers.
func TestCollectionStaysOnScheduleWhileExportStalls(t *testing.T) {
	if runtime.GOOS != "linux" {
		// macOS and Windows CI runners drop whole timer ticks under load (deviations of exact interval multiples);
		// the collection loop's independence from a stalled exporter is asserted on Linux.
		t.Skip("schedule precision is asserted on Linux only")
	}
	const interval = 200 * time.Millisecond
	const outage = 8 // intervals

	for _, mode := range []string{"503-retry-after", "hanging-ingest"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeSender{}
			if mode == "503-retry-after" {
				f.down.Store(true)
			} else {
				f.block.Store(int64(3 * interval))
			}
			a := newPipelineAgent(t, interval, f)
			var mu sync.Mutex
			var ticks []time.Time
			a.onCollect = func(ts time.Time) {
				mu.Lock()
				ticks = append(ticks, ts)
				mu.Unlock()
			}
			samples := func() int {
				mu.Lock()
				defer mu.Unlock()
				return len(ticks)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = a.Run(ctx)
			}()

			time.Sleep(outage * interval)
			if n := samples(); n < outage-1 {
				t.Errorf("only %d samples during a %d-interval outage", n, outage)
			}
			// Recovery: keep collecting while the backlog drains.
			f.down.Store(false)
			f.block.Store(0)
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(interval)
				got, _ := f.delivered()
				if n := samples(); n >= outage+6 && len(got) >= n {
					break
				}
			}
			cancel()
			<-done

			mu.Lock()
			defer mu.Unlock()
			// Every sample must sit on the schedule grid (±25%: timer jitter on a loaded host reaches ~25ms; a blocked loop is
			// off by at least a whole interval). A shared CI runner can stall the process past one tick
			// (time.Ticker then drops it); that single miss is tolerated. A collection loop blocked by the stalled
			// exporter shows up as off-grid samples or as several missed slots.
			missed, prevSlot := 0, int64(-1)
			for i, ts := range ticks {
				off := ts.Sub(ticks[0])
				slot := (off + interval/2) / interval
				if dev := (off - slot*interval).Abs(); dev > interval/4 {
					t.Errorf("sample %d is %s off schedule (tolerance %s)", i, dev, interval/4)
				}
				if int64(slot) <= prevSlot {
					t.Errorf("sample %d repeats schedule slot %d", i, slot)
				}
				if gap := int(int64(slot) - prevSlot - 1); gap > 0 {
					missed += gap
				}
				prevSlot = int64(slot)
			}
			if missed > 1 {
				t.Errorf("%d schedule slots missed (at most 1 tolerated)", missed)
			}
			times, logs := f.delivered()
			if len(times) != len(ticks) {
				t.Fatalf("delivered %d metric samples, collected %d", len(times), len(ticks))
			}
			for i := 1; i < len(times); i++ {
				if times[i] <= times[i-1] {
					t.Fatalf("metric samples out of order at %d: %v", i, times)
				}
			}
			if n := checkSnapshots(t, logs); n != 1 {
				t.Errorf("complete snapshots delivered = %d, want 1", n)
			}
			snap := a.stats.Snapshot()
			if d := snap.ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "dropped"}] +
				snap.ExportItems[selfmon.ExportKey{Signal: "logs", Outcome: "dropped"}]; d != 0 {
				t.Errorf("dropped %d items", d)
			}
			if mode == "503-retry-after" && snap.ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "buffered"}] == 0 {
				t.Error("outage should have spilled payloads to the disk buffer")
			}
			if a.buf.Len() != 0 {
				t.Errorf("disk buffer not drained: %d", a.buf.Len())
			}
			t.Logf("%s: %d samples, schedule deviation within %s, %d metric payloads and %d log records delivered in order",
				mode, len(ticks), interval/4, len(times), len(logs))
		})
	}
}

// When the disk buffer's max_bytes is exceeded, the oldest payloads are
// dropped and counted; the newest contiguous tail is delivered in order.
func TestDiskBufferOverflowDropsOldest(t *testing.T) {
	f := &fakeSender{}
	f.down.Store(true)
	a := newPipelineAgent(t, time.Second, f)
	a.cfg.Inventory.Enabled = false
	a.pipe.maxItems = 1
	clock := time.Unix(1704153600, 0)
	a.Now = func() time.Time { return clock }

	size := proto.Size(a.CollectMetrics(clock)) // also primes utilization/duration series
	b, err := buffer.Open(t.TempDir(), int64(size*7/2))
	if err != nil {
		t.Fatal(err)
	}
	a.buf, a.pipe.buf = b, b
	total := 0
	a.onEnqueue = func(_ exporter.Signal, items int) { total += items }

	const rounds = 8
	var want []uint64
	for i := 0; i < rounds; i++ {
		clock = clock.Add(10 * time.Second)
		want = append(want, uint64(clock.UnixNano()))
		a.Tick(context.Background())
	}
	f.down.Store(false)
	if err := a.pipe.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := f.delivered()
	if len(got) == 0 || len(got) >= rounds {
		t.Fatalf("delivered %d of %d samples", len(got), rounds)
	}
	if tail := want[rounds-len(got):]; !slices.Equal(got, tail) {
		t.Errorf("delivered %v, want newest tail %v", got, tail)
	}
	snap := a.stats.Snapshot()
	sent := snap.ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "sent"}]
	dropped := snap.ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "dropped"}]
	if dropped == 0 || int(sent+dropped) != total {
		t.Errorf("sent %d + dropped %d != collected %d points", sent, dropped, total)
	}
	t.Logf("delivered newest %d/%d samples; %d points dropped and counted", len(got), rounds, dropped)
}

// On shutdown collection stops, the queue is flushed for at most the deadline
// and everything still pending (including a send in flight) is persisted in order.
func TestShutdownPersistsPendingPayloads(t *testing.T) {
	f := &fakeSender{}
	f.blockForever.Store(true)
	a := newPipelineAgent(t, 100*time.Millisecond, f)
	a.pipe.maxItems = 100
	a.shutdownTimeout = 300 * time.Millisecond
	var payloads atomic.Int32
	a.onEnqueue = func(exporter.Signal, int) { payloads.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	time.Sleep(550 * time.Millisecond)
	stopAt := time.Now()
	cancel()
	<-done
	slack := 400 * time.Millisecond
	if runtime.GOOS != "linux" {
		slack = 1500 * time.Millisecond // slower shared macOS/Windows runners; shutdown must still be bounded
	}
	if took := time.Since(stopAt); took > a.shutdownTimeout+slack {
		t.Errorf("shutdown took %s (deadline %s)", took, a.shutdownTimeout)
	}

	b, err := buffer.Open(a.cfg.Buffer.Dir, a.cfg.Buffer.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() != int(payloads.Load()) || b.Len() < 5 {
		t.Fatalf("persisted %d payloads, enqueued %d", b.Len(), payloads.Load())
	}
	var times []uint64
	var logs []*logspb.LogRecord
	for {
		e, data, ok, _ := b.Peek()
		if !ok {
			break
		}
		if e.Signal == string(exporter.SignalMetrics) {
			var md metricspb.MetricsData
			if err := proto.Unmarshal(data, &md); err != nil {
				t.Fatal(err)
			}
			times = append(times, sampleTime(&md))
		} else {
			var ld logspb.LogsData
			if err := proto.Unmarshal(data, &ld); err != nil {
				t.Fatal(err)
			}
			logs = append(logs, ld.ResourceLogs[0].ScopeLogs[0].LogRecords...)
		}
		_ = b.Remove(e)
	}
	if !slices.IsSorted(times) || len(times) < 5 {
		t.Errorf("persisted metric samples not in order: %v", times)
	}
	if checkSnapshots(t, logs) != 1 {
		t.Error("persisted snapshot incomplete")
	}
	if s := a.stats.Snapshot().ExportItems[selfmon.ExportKey{Signal: "metrics", Outcome: "sent"}]; s != 0 {
		t.Errorf("sent = %d", s)
	}
}

// The CPU budget check measures collection cost only; export latency is excluded.
func TestBudgetMeasuresCollectionNotExport(t *testing.T) {
	f := &fakeSender{}
	a := newPipelineAgent(t, 10*time.Second, f)

	a.budgetStart, a.collectCost = time.Now().Add(-2*time.Minute), 100*time.Millisecond
	a.checkBudget(time.Now())
	if a.interval != 10*time.Second {
		t.Fatalf("interval raised for 0.08%% collection cost: %s", a.interval)
	}
	a.budgetStart, a.collectCost = time.Now().Add(-2*time.Minute), 5*time.Second
	a.checkBudget(time.Now())
	if a.interval != 20*time.Second || a.stats.Snapshot().Interval != 20*time.Second {
		t.Fatalf("interval = %s, gauge = %s; want 20s", a.interval, a.stats.Snapshot().Interval)
	}

	// A hanging ingest must not count as collection cost: rounds only enqueue.
	f.block.Store(int64(time.Second))
	a.budgetStart, a.collectCost = time.Now(), 0
	start := time.Now()
	a.collectRound()
	if took := time.Since(start); took >= time.Second || a.collectCost >= time.Second {
		t.Errorf("collection round took %s (cost %s) with a hanging exporter", took, a.collectCost)
	}
}

func TestPipelineOnSentHook(t *testing.T) {
	f := &fakeSender{}
	a := newPipelineAgent(t, time.Second, f)
	var sent atomic.Int32
	a.OnExportSuccess(func() { sent.Add(1) })
	f.down.Store(true)
	a.Tick(context.Background())
	if sent.Load() != 0 {
		t.Fatalf("hook ran for failed sends: %d", sent.Load())
	}
	f.down.Store(false)
	a.Tick(context.Background())
	if sent.Load() == 0 {
		t.Fatal("hook not called after a successful export")
	}
}
