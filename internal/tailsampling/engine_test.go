package tailsampling

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/apm"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

type spanSpec struct {
	traceID    []byte
	spanID     uint64
	service    string
	err        bool
	durMs      int64
	traceState string
}

func traceID(n uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[:8], n)
	binary.BigEndian.PutUint64(b[8:], n*0x9e3779b97f4a7c15)
	return b
}

func request(specs ...spanSpec) *coltrace.ExportTraceServiceRequest {
	req := &coltrace.ExportTraceServiceRequest{}
	for _, s := range specs {
		sid := make([]byte, 8)
		binary.BigEndian.PutUint64(sid, s.spanID+1)
		sp := &tracepb.Span{TraceId: s.traceID, SpanId: sid, Name: "GET /x", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: 1_000_000_000, EndTimeUnixNano: uint64(1_000_000_000 + s.durMs*1e6), TraceState: s.traceState}
		if s.err {
			sp.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
		}
		req.ResourceSpans = append(req.ResourceSpans, &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s.service}}}}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{sp}}},
		})
	}
	return req
}

func outputSpans(outs []Output) []*tracepb.Span {
	var out []*tracepb.Span
	for _, o := range outs {
		for _, rs := range o.Request.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				out = append(out, ss.GetSpans()...)
			}
		}
	}
	return out
}

func newTestEngine(opts Options, p Policy) (*Engine, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	e := NewEngine(opts, StaticPolicies{Default: p}, nil)
	e.SetClock(clk.now)
	return e, clk
}

func src(partition int32, offset int64) Source {
	return Source{TenantID: "t1", RequestID: "r", Topic: "raw", Partition: partition, Offset: offset}
}

func TestEngineWaitsThenDecidesAndLateSpansFollow(t *testing.T) {
	p := Policy{Enabled: true, BaselineRatio: 0, Rules: []Rule{{Name: "errors", Type: RuleError}}}
	e, clk := newTestEngine(Options{DecisionWait: 10 * time.Second}, p)
	errTrace, okTrace := traceID(1), traceID(2)
	e.Add(src(0, 0), request(spanSpec{traceID: errTrace, spanID: 1, service: "a"}, spanSpec{traceID: okTrace, spanID: 2, service: "a"}))
	clk.advance(5 * time.Second)
	e.Add(src(0, 1), request(spanSpec{traceID: errTrace, spanID: 3, service: "b", err: true}))
	e.Tick()
	if outs := e.TakeOutputs(); len(outs) != 0 {
		t.Fatalf("decided before the wait: %d outputs", len(outs))
	}
	if off := e.CommitOffsets(nil); len(off) != 0 {
		t.Fatalf("nothing is committable while traces are buffered, got %v", off)
	}
	clk.advance(5 * time.Second)
	e.Tick()
	outs := e.TakeOutputs()
	if len(outs) != 1 || len(outputSpans(outs)) != 2 {
		t.Fatalf("want the error trace with 2 spans, got %d outputs / %d spans", len(outs), len(outputSpans(outs)))
	}
	if off := e.CommitOffsets(nil); len(off) != 0 {
		t.Fatalf("offsets must wait until the output is produced, got %v", off)
	}
	e.Release(outs)
	off := e.CommitOffsets(nil)
	if got := off["raw"][0].Offset; got != 2 {
		t.Fatalf("commit offset = %d, want 2", got)
	}
	// Late spans follow the cached decision.
	e.Add(src(0, 2), request(spanSpec{traceID: errTrace, spanID: 4, service: "c"}, spanSpec{traceID: okTrace, spanID: 5, service: "c", err: true}))
	late := e.TakeOutputs()
	if len(outputSpans(late)) != 1 || len(e.traces) != 0 {
		t.Fatalf("late spans: %d kept, %d buffered traces; want 1, 0", len(outputSpans(late)), len(e.traces))
	}
}

func TestEngineMemoryBoundsDecideEarly(t *testing.T) {
	e, _ := newTestEngine(Options{DecisionWait: time.Hour, MaxTraces: 3, MaxSpansPerTrace: 2}, KeepAll())
	for i := range 5 {
		e.Add(src(0, int64(i)), request(spanSpec{traceID: traceID(uint64(i)), spanID: uint64(i), service: "a"}))
	}
	if n, _ := e.Buffered(); n != 3 {
		t.Fatalf("buffered traces = %d, want 3", n)
	}
	if got := len(e.TakeOutputs()); got != 2 {
		t.Fatalf("evicted outputs = %d, want 2", got)
	}
	e.Add(src(0, 5), request(spanSpec{traceID: traceID(4), spanID: 10, service: "a"}))
	if n, _ := e.Buffered(); n != 2 {
		t.Fatalf("a trace reaching max spans must be decided, buffered = %d", n)
	}
	outs := e.TakeOutputs()
	if len(outs) != 1 || len(outputSpans(outs)) != 2 {
		t.Fatalf("max_spans output: %d outputs", len(outs))
	}
}

func TestEngineRevocationDecidesOnlyAffectedTraces(t *testing.T) {
	e, _ := newTestEngine(Options{DecisionWait: time.Hour}, KeepAll())
	e.Add(src(0, 0), request(spanSpec{traceID: traceID(1), spanID: 1, service: "a"}))
	e.Add(src(1, 0), request(spanSpec{traceID: traceID(2), spanID: 2, service: "a"}))
	revoked := map[string][]int32{"raw": {1}}
	e.DecidePartitions(revoked)
	outs := e.TakeOutputs()
	if len(outs) != 1 || string(outs[0].TraceID) != string(traceID(2)) {
		t.Fatalf("revocation outputs: %+v", outs)
	}
	e.Release(outs)
	off := e.CommitOffsets(revoked)
	if len(off) != 1 || off["raw"][1].Offset != 1 {
		t.Fatalf("revoked partition commit = %v", off)
	}
	e.DropPartitions(revoked)
	if n, _ := e.Buffered(); n != 1 {
		t.Fatalf("trace of the kept partition must stay buffered, buffered = %d", n)
	}
}

func TestEngineCommitWaitsForOldestRecord(t *testing.T) {
	e, clk := newTestEngine(Options{DecisionWait: 10 * time.Second}, Policy{Enabled: true, BaselineRatio: 0})
	e.Add(src(0, 10), request(spanSpec{traceID: traceID(1), spanID: 1, service: "a"}))
	clk.advance(6 * time.Second)
	e.Add(src(0, 11), request(spanSpec{traceID: traceID(2), spanID: 2, service: "a"}))
	clk.advance(5 * time.Second)
	e.Tick() // trace 1 dropped, trace 2 still buffered
	if off := e.CommitOffsets(nil); off["raw"][0].Offset != 11 {
		t.Fatalf("commit = %v, want 11 (record 11 still buffered)", off)
	}
}

// TestAdjustedCountsUnbiased is the property test of D-075: with consistent head sampling and tail
// policies (error keep, latency keep, probabilistic baseline, rate limit), the sum of the weights the
// processor derives from the rewritten tracestate estimates the true number of traces and errors.
func TestAdjustedCountsUnbiased(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	policy := Policy{Enabled: true, BaselineRatio: 0.1, MaxSpansPerSecond: 0, Rules: []Rule{
		{Name: "errors", Type: RuleError},
		{Name: "slow", Type: RuleLatency, ThresholdMs: 1000},
		{Name: "half", Type: RuleService, Services: []string{"half"}, Ratio: ratio(0.5)},
	}}
	for _, tc := range []struct {
		name      string
		headP     float64
		rateLimit float64
	}{
		{"no head sampling", 1, 0},
		{"head 0.25 (th)", 0.25, 0},
		{"head 0.5 + rate limit", 0.5, 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol := policy
			pol.MaxSpansPerSecond = tc.rateLimit
			e, clk := newTestEngine(Options{DecisionWait: time.Second, MaxTraces: 1 << 20}, pol)
			const n = 200_000
			var trueTotal, trueErrors, trueHalf float64
			var estTotal, estErrors, estHalf float64
			headTh := Threshold(tc.headP)
			ts := ""
			if tc.headP < 1 {
				ts = "ot=th:" + EncodeThreshold(headTh)
			}
			offset := int64(0)
			for i := range n {
				id := make([]byte, 16)
				binary.BigEndian.PutUint64(id[:8], rng.Uint64())
				binary.BigEndian.PutUint64(id[8:], rng.Uint64())
				isErr := rng.Float64() < 0.03
				dur := int64(50)
				if rng.Float64() < 0.02 {
					dur = 2000
				}
				svc := "shop"
				if rng.Float64() < 0.2 {
					svc = "half"
				}
				trueTotal++
				if isErr {
					trueErrors++
				}
				if svc == "half" && !isErr && dur < 1000 {
					trueHalf++
				}
				if traceRandomness(id, "") < headTh { // head sampler drops it
					continue
				}
				e.Add(src(0, offset), request(spanSpec{traceID: id, spanID: 1, service: svc, err: isErr, durMs: dur, traceState: ts},
					spanSpec{traceID: id, spanID: 2, service: svc, durMs: 10, traceState: ts}))
				offset++
				if i%500 == 0 {
					clk.advance(100 * time.Millisecond)
					e.Tick()
				}
				for _, o := range e.TakeOutputs() {
					for _, rs := range o.Request.GetResourceSpans() {
						for _, ss := range rs.GetScopeSpans() {
							for _, sp := range ss.GetSpans() {
								if sp.GetSpanId()[7] != 2 { // count the root span only
									continue
								}
								w := apm.SampleWeight(sp.GetTraceState(), nil, nil)
								estTotal += w
								if sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
									estErrors += w
								}
								if svc := rs.GetResource().GetAttributes()[0].GetValue().GetStringValue(); svc == "half" && sp.GetStatus() == nil && sp.GetEndTimeUnixNano()-sp.GetStartTimeUnixNano() < 1e9 {
									estHalf += w
								}
							}
						}
					}
				}
			}
			clk.advance(time.Hour)
			e.DecideAll(ReasonShutdown)
			for _, o := range e.TakeOutputs() {
				for _, rs := range o.Request.GetResourceSpans() {
					for _, sp := range rs.GetScopeSpans()[0].GetSpans() {
						if sp.GetSpanId()[7] == 2 {
							w := apm.SampleWeight(sp.GetTraceState(), nil, nil)
							estTotal += w
							if sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
								estErrors += w
							}
							if rs.GetResource().GetAttributes()[0].GetValue().GetStringValue() == "half" && sp.GetStatus() == nil && sp.GetEndTimeUnixNano()-sp.GetStartTimeUnixNano() < 1e9 {
								estHalf += w
							}
						}
					}
				}
			}
			check := func(what string, est, truth float64) {
				rel := math.Abs(est-truth) / truth
				t.Logf("%s: estimate %.0f, true %.0f (%.2f%%)", what, est, truth, rel*100)
				if rel > 0.05 {
					t.Errorf("%s: estimate %.0f deviates %.1f%% from %.0f", what, est, rel*100, truth)
				}
			}
			check("traces", estTotal, trueTotal)
			check("errors", estErrors, trueErrors)
			check("half-service", estHalf, trueHalf)
		})
	}
}
