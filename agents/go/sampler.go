package openlog

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SamplingRatioKey is the span attribute carrying the head sampling probability of a
// trace root sampled by this agent (only set when the ratio is below 1). The APM backend
// scales RED metrics computed from sampled spans by 1/sampling.ratio (10-m2.md §3).
const SamplingRatioKey = attribute.Key("sampling.ratio")

// newSampler returns ParentBased(root) where root samples new traces by trace id ratio and
// records the ratio on the root span. Children follow their parent's decision.
func newSampler(ratio float64) sdktrace.Sampler {
	switch {
	case ratio >= 1:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case ratio <= 0:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	}
	return sdktrace.ParentBased(ratioRoot{
		inner: sdktrace.TraceIDRatioBased(ratio),
		attr:  SamplingRatioKey.Float64(ratio),
		desc:  fmt.Sprintf("OpenlogRatio{%g}", ratio),
	})
}

type ratioRoot struct {
	inner sdktrace.Sampler
	attr  attribute.KeyValue
	desc  string
}

func (s ratioRoot) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	res := s.inner.ShouldSample(p)
	if res.Decision == sdktrace.RecordAndSample {
		res.Attributes = append(res.Attributes, s.attr)
	}
	return res
}

func (s ratioRoot) Description() string { return s.desc }
