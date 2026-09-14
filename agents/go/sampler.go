package openlog

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// SamplingRatioKey is the span attribute carrying the head sampling probability of a
// trace root sampled by this agent, or of a local entry span whose remote parent was
// sampled with a probability below 1 (read from tracestate ot=th). It is only set when
// the probability is below 1. The APM backend weights RED metrics by 1/p (apm.md §4).
const SamplingRatioKey = attribute.Key("sampling.ratio")

// OpenTelemetry tracestate entry for consistent probability sampling
// (https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/).
const (
	otKey          = "ot"
	maxOTValueLen  = 256
	thresholdBits  = 56
	maxThreshold   = uint64(1) << thresholdBits // exclusive; T = 2^56 would mean p = 0
	randomnessMask = maxThreshold - 1
)

// newSampler returns a parent-based sampler:
//   - new traces (no parent) are sampled with probability ratio by comparing the 56-bit
//     randomness (tracestate ot=rv, else the lower 56 bits of the trace id) against the
//     rejection threshold T = (1-ratio)·2^56. Sampled roots carry sampling.ratio and write
//     ot=th:<T> into tracestate so downstream services of any language can weight by 1/p;
//   - children of a sampled remote parent are sampled and, when the incoming tracestate
//     carries ot=th (or legacy ot=p) with p < 1, get sampling.ratio = p;
//   - other children follow their parent's decision.
//
// With writeRV, roots without an incoming ot=rv generate explicit randomness and write
// ot=rv:<14 hex> (kept for the whole trace); the decision then uses it instead of the trace id.
func newSampler(ratio float64, writeRV bool) sdktrace.Sampler {
	var root sdktrace.Sampler
	switch {
	case ratio >= 1 || math.IsNaN(ratio):
		root = sdktrace.AlwaysSample()
	case ratio <= 0:
		root = sdktrace.NeverSample()
	default:
		root = newRatioRoot(ratio)
	}
	if writeRV {
		if rr, ok := root.(ratioRoot); ok {
			rr.writeRV = true
			root = rr
		} else {
			root = rvRoot{root}
		}
	}
	return sdktrace.ParentBased(root, sdktrace.WithRemoteParentSampled(remoteSampled{}))
}

type ratioRoot struct {
	threshold uint64
	th        string
	attr      attribute.KeyValue
	desc      string
	writeRV   bool
}

// newRandomness returns 56 random bits and their ot=rv encoding (always 14 hex digits).
// A variable so tests can fix it.
var newRandomness = func() (uint64, string) {
	r := rand.Uint64() & randomnessMask
	return r, fmt.Sprintf("%014x", r)
}

// rvRoot adds ot=rv to the tracestate of roots decided by another sampler (ratio 0 or 1).
type rvRoot struct{ sdktrace.Sampler }

func (s rvRoot) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	res := s.Sampler.ShouldSample(p)
	if ot := parseOT(res.Tracestate.Get(otKey)); !ot.hasRandomness() {
		_, rv := newRandomness()
		res.Tracestate = withOT(res.Tracestate, append(ot.without("rv"), otField{"rv", rv}))
	}
	return res
}

func newRatioRoot(ratio float64) sdktrace.Sampler {
	// ratio·2^56 is exact in float64 (power-of-two scaling); 1-ratio would not be.
	keep := uint64(math.Round(ratio * float64(maxThreshold)))
	if keep >= maxThreshold { // ratio indistinguishable from 1 at 56 bits
		return sdktrace.AlwaysSample()
	}
	if keep == 0 {
		keep = 1
	}
	t := maxThreshold - keep
	return ratioRoot{
		threshold: t,
		th:        encodeThreshold(t),
		attr:      SamplingRatioKey.Float64(ratio),
		desc:      fmt.Sprintf("OpenlogConsistentRatio{%g}", ratio),
	}
}

func (s ratioRoot) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	ts := trace.SpanContextFromContext(p.ParentContext).TraceState()
	ot := parseOT(ts.Get(otKey))
	r, ok := ot.randomness()
	switch {
	case ok:
	case s.writeRV:
		var rv string
		r, rv = newRandomness()
		ot = append(ot.without("rv"), otField{"rv", rv})
	default:
		r = binary.BigEndian.Uint64(p.TraceID[8:16]) & randomnessMask
	}
	if r < s.threshold {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: withOT(ts, ot.without("th"))}
	}
	return sdktrace.SamplingResult{
		Decision:   sdktrace.RecordAndSample,
		Attributes: []attribute.KeyValue{s.attr},
		Tracestate: withOT(ts, ot.with("th", s.th)),
	}
}

func (s ratioRoot) Description() string { return s.desc }

// remoteSampled samples every span whose remote parent is sampled (the ParentBased
// default) and records the upstream sampling probability on it.
type remoteSampled struct{}

func (remoteSampled) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	ts := trace.SpanContextFromContext(p.ParentContext).TraceState()
	res := sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample, Tracestate: ts}
	if prob, ok := parseOT(ts.Get(otKey)).probability(); ok && prob > 0 && prob < 1 {
		res.Attributes = []attribute.KeyValue{SamplingRatioKey.Float64(prob)}
	}
	return res
}

func (remoteSampled) Description() string { return "OpenlogRemoteParentSampled" }

// encodeThreshold renders T as up to 14 hex digits without trailing zeros ("0" for T=0).
func encodeThreshold(t uint64) string {
	s := strings.TrimRight(fmt.Sprintf("%014x", t), "0")
	if s == "" {
		return "0"
	}
	return s
}

// parseThreshold decodes th:<hex> (1..14 hex digits, right-padded with zeros).
func parseThreshold(s string) (uint64, bool) {
	if len(s) == 0 || len(s) > 14 {
		return 0, false
	}
	t, err := strconv.ParseUint(s+strings.Repeat("0", 14-len(s)), 16, 64)
	return t, err == nil
}

// otValue is the ordered list of sub-keys of the tracestate `ot` entry ("k1:v1;k2:v2").
type otValue []otField

type otField struct{ k, v string }

func parseOT(s string) otValue {
	if s == "" {
		return nil
	}
	var out otValue
	for part := range strings.SplitSeq(s, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok && k != "" {
			out = append(out, otField{k, v})
		}
	}
	return out
}

func (o otValue) get(k string) (string, bool) {
	for _, f := range o {
		if f.k == k {
			return f.v, true
		}
	}
	return "", false
}

// randomness returns the explicit 56-bit randomness value ot=rv:<14 hex digits>.
func (o otValue) randomness() (uint64, bool) {
	v, ok := o.get("rv")
	if !ok || len(v) != 14 {
		return 0, false
	}
	r, err := strconv.ParseUint(v, 16, 64)
	return r, err == nil
}

// hasRandomness reports whether a valid ot=rv is present.
func (o otValue) hasRandomness() bool {
	_, ok := o.randomness()
	return ok
}

// probability returns p from th:<hex> (p = 1 - T/2^56) or legacy p:<n> (p = 2^-n).
func (o otValue) probability() (float64, bool) {
	if v, ok := o.get("th"); ok {
		if t, ok := parseThreshold(v); ok {
			return 1 - float64(t)/float64(maxThreshold), true
		}
	}
	if v, ok := o.get("p"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 63 {
			if n == 63 {
				return 0, true
			}
			return math.Ldexp(1, -n), true
		}
	}
	return 0, false
}

// with returns o with sub-key k set to v (moved to the front, other sub-keys kept in order).
func (o otValue) with(k, v string) otValue {
	return append(otValue{{k, v}}, o.without(k)...)
}

func (o otValue) without(k string) otValue {
	out := make(otValue, 0, len(o))
	for _, f := range o {
		if f.k != k {
			out = append(out, f)
		}
	}
	return out
}

func (o otValue) String() string {
	var b strings.Builder
	for i, f := range o {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(f.k)
		b.WriteByte(':')
		b.WriteString(f.v)
	}
	return b.String()
}

// withOT stores o as the `ot` member of ts, keeping W3C limits: the value is at most 256
// characters (sub-keys other than th and rv are dropped, last first, until it fits) and the
// list keeps at most 32 members (TraceState.Insert drops the right-most member). An empty o
// removes the member. On an invalid value ts is returned unchanged.
func withOT(ts trace.TraceState, o otValue) trace.TraceState {
	for len(o.String()) > maxOTValueLen {
		i := len(o) - 1
		for i >= 0 && (o[i].k == "th" || o[i].k == "rv") {
			i--
		}
		if i < 0 {
			return ts
		}
		o = append(o[:i:i], o[i+1:]...)
	}
	if len(o) == 0 {
		return ts.Delete(otKey)
	}
	out, err := ts.Insert(otKey, o.String())
	if err != nil {
		return ts
	}
	return out
}

// randomTracerProvider sets the W3C Trace Context Level 2 random flag (traceparent flags
// 0x02) on root spans: the SDK's default ID generator makes the lower 56 bits of every new
// trace id random, so downstream consistent-probability samplers may use them as randomness.
// The SDK copies the parent's flags into a new span; for a root the parent is the (invalid)
// span context of ctx, so the flag is added there. Children inherit the flag from their
// parent, so a remote parent without it (W3C Level 1) is propagated unchanged. Spans started
// with trace.WithNewRoot are not marked.
type randomTracerProvider struct {
	embedded.TracerProvider
	tp trace.TracerProvider
}

func (p randomTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return randomTracer{t: p.tp.Tracer(name, opts...)}
}

type randomTracer struct {
	embedded.Tracer
	t trace.Tracer
}

func (t randomTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() && !sc.IsRandom() {
		ctx = trace.ContextWithSpanContext(ctx, sc.WithTraceFlags(sc.TraceFlags().WithRandom(true)))
	}
	return t.t.Start(ctx, name, opts...)
}
