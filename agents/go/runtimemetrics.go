package openlog

import (
	"context"
	"math"
	"runtime/metrics"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// RuntimeScope is the instrumentation scope of the Go runtime metrics.
const RuntimeScope = "github.com/onuragtas/openlog/agents/go/runtime"

// runtimeProducer turns runtime/metrics samples into OTLP metrics on every collection of the
// periodic reader (no background goroutine, no MemStats stop-the-world). Names follow the
// OTel Go runtime semantic conventions; the two GC metrics that have no semconv name use the
// openlog. prefix (semantic-conventions.md).
type runtimeProducer struct {
	start time.Time

	mu      sync.Mutex
	samples []metrics.Sample
	index   map[string]int
}

const (
	rmHeapStacks     = "/memory/classes/heap/stacks:bytes"
	rmOSStacks       = "/memory/classes/os-stacks:bytes"
	rmTotal          = "/memory/classes/total:bytes"
	rmHeapReleased   = "/memory/classes/heap/released:bytes"
	rmMemLimit       = "/gc/gomemlimit:bytes"
	rmAllocBytes     = "/gc/heap/allocs:bytes"
	rmAllocObjects   = "/gc/heap/allocs:objects"
	rmGCGoal         = "/gc/heap/goal:bytes"
	rmGoroutines     = "/sched/goroutines:goroutines"
	rmGOMAXPROCS     = "/sched/gomaxprocs:threads"
	rmGOGC           = "/gc/gogc:percent"
	rmSchedLatencies = "/sched/latencies:seconds"
	rmGCCycles       = "/gc/cycles/total:gc-cycles"
	rmGCPauses       = "/sched/pauses/total/gc:seconds"
)

// Explicit bucket boundaries (seconds) the runtime's fine-grained histograms are folded into.
var runtimeLatencyBounds = []float64{0.00001, 0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

func newRuntimeProducer() *runtimeProducer {
	names := []string{rmHeapStacks, rmOSStacks, rmTotal, rmHeapReleased, rmMemLimit, rmAllocBytes, rmAllocObjects,
		rmGCGoal, rmGoroutines, rmGOMAXPROCS, rmGOGC, rmSchedLatencies, rmGCCycles, rmGCPauses}
	supported := map[string]bool{}
	for _, d := range metrics.All() {
		supported[d.Name] = true
	}
	p := &runtimeProducer{start: time.Now(), index: map[string]int{}}
	for _, n := range names {
		if supported[n] {
			p.index[n] = len(p.samples)
			p.samples = append(p.samples, metrics.Sample{Name: n})
		}
	}
	return p
}

func (p *runtimeProducer) Produce(context.Context) ([]metricdata.ScopeMetrics, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metrics.Read(p.samples)
	now := time.Now()

	u := func(name string) (int64, bool) {
		i, ok := p.index[name]
		if !ok || p.samples[i].Value.Kind() != metrics.KindUint64 {
			return 0, false
		}
		v := p.samples[i].Value.Uint64()
		if v > math.MaxInt64 {
			v = math.MaxInt64
		}
		return int64(v), true
	}
	var out []metricdata.Metrics
	point := func(value int64, attrs ...attribute.KeyValue) metricdata.DataPoint[int64] {
		return metricdata.DataPoint[int64]{Attributes: attribute.NewSet(attrs...), StartTime: p.start, Time: now, Value: value}
	}
	sum := func(name, desc, unit string, monotonic bool, dps ...metricdata.DataPoint[int64]) {
		out = append(out, metricdata.Metrics{Name: name, Description: desc, Unit: unit,
			Data: metricdata.Sum[int64]{DataPoints: dps, Temporality: metricdata.CumulativeTemporality, IsMonotonic: monotonic}})
	}

	if total, ok := u(rmTotal); ok {
		hs, _ := u(rmHeapStacks)
		oss, _ := u(rmOSStacks)
		rel, _ := u(rmHeapReleased)
		stack := hs + oss
		sum("go.memory.used", "Memory used by the Go runtime.", "By", false,
			point(stack, attribute.String("go.memory.type", "stack")),
			point(total-rel-stack, attribute.String("go.memory.type", "other")))
	}
	if v, ok := u(rmMemLimit); ok && v != math.MaxInt64 {
		sum("go.memory.limit", "Go runtime memory limit configured by the user, if a limit exists.", "By", false, point(v))
	}
	if v, ok := u(rmAllocBytes); ok {
		sum("go.memory.allocated", "Memory allocated to the heap by the application.", "By", true, point(v))
	}
	if v, ok := u(rmAllocObjects); ok {
		sum("go.memory.allocations", "Count of allocations to the heap by the application.", "{allocation}", true, point(v))
	}
	if v, ok := u(rmGCGoal); ok {
		sum("go.memory.gc.goal", "Heap size target for the end of the GC cycle.", "By", false, point(v))
	}
	if v, ok := u(rmGoroutines); ok {
		sum("go.goroutine.count", "Count of live goroutines.", "{goroutine}", false, point(v))
	}
	if v, ok := u(rmGOMAXPROCS); ok {
		sum("go.processor.limit", "The number of OS threads that can execute user-level Go code simultaneously.", "{thread}", false, point(v))
	}
	if i, ok := p.index[rmGOGC]; ok && p.samples[i].Value.Kind() == metrics.KindUint64 {
		// GOGC=off reports 0 in some versions and a huge value in others; only sane values.
		if v := p.samples[i].Value.Uint64(); v < math.MaxInt32 {
			sum("go.config.gogc", "Heap size target percentage configured by the user, otherwise 100.", "%", false, point(int64(v)))
		}
	}
	if v, ok := u(rmGCCycles); ok {
		sum("openlog.go.gc.cycles", "Completed GC cycles.", "{gc_cycle}", true, point(v))
	}
	if h := p.histogram(rmSchedLatencies, now); h != nil {
		out = append(out, metricdata.Metrics{Name: "go.schedule.duration",
			Description: "The time goroutines have spent in the scheduler in a runnable state before actually running.",
			Unit:        "s", Data: *h})
	}
	if h := p.histogram(rmGCPauses, now); h != nil {
		out = append(out, metricdata.Metrics{Name: "openlog.go.gc.pause.duration",
			Description: "Stop-the-world pause latencies caused by the garbage collector.", Unit: "s", Data: *h})
	}
	return []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: RuntimeScope, Version: Version}, Metrics: out}}, nil
}

// histogram folds a runtime Float64Histogram into runtimeLatencyBounds. A runtime bucket
// [lo, hi) is assigned by its upper bound; Sum is estimated from bucket midpoints because the
// runtime does not report exact sums.
func (p *runtimeProducer) histogram(name string, now time.Time) *metricdata.Histogram[float64] {
	i, ok := p.index[name]
	if !ok || p.samples[i].Value.Kind() != metrics.KindFloat64Histogram {
		return nil
	}
	rh := p.samples[i].Value.Float64Histogram()
	counts := make([]uint64, len(runtimeLatencyBounds)+1)
	var total uint64
	var est float64
	for b, c := range rh.Counts {
		if c == 0 {
			continue
		}
		lo, hi := rh.Buckets[b], rh.Buckets[b+1]
		idx := sort.SearchFloat64s(runtimeLatencyBounds, hi)
		if math.IsInf(hi, 1) {
			idx = len(runtimeLatencyBounds)
		}
		counts[idx] += c
		total += c
		mid := lo
		if !math.IsInf(hi, 1) && !math.IsInf(lo, -1) {
			mid = (lo + hi) / 2
		} else if math.IsInf(lo, -1) {
			mid = 0
		}
		est += mid * float64(c)
	}
	return &metricdata.Histogram[float64]{
		Temporality: metricdata.CumulativeTemporality,
		DataPoints: []metricdata.HistogramDataPoint[float64]{{
			Attributes: attribute.NewSet(), StartTime: p.start, Time: now,
			Count: total, Bounds: runtimeLatencyBounds, BucketCounts: counts, Sum: est,
		}},
	}
}
