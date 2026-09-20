// Package otlpprofiles converts a Go runtime pprof profile into an OTLP profiles payload.
//
// It is internal on purpose. OTLP profiles are still `v1development` upstream: the message types will change
// before they stabilise, and a package that people import into their own applications must not put that
// churn in their build. Everything that knows the wire format lives here, behind one function.
//
// The conversion itself is a re-shaping, not a reinterpretation: pprof is a dictionary of functions and
// locations with samples pointing into it, and OTLP profiles are the same idea with the dictionary hoisted
// one level up and stacks interned as well. Nothing is recomputed, nothing is invented.
//
// **Use the plain `go.opentelemetry.io/proto/otlp/...` modules here, never the `slim` ones.** The slim
// modules are a replacement for the full ones, not a lighter companion: both register the same proto file
// paths (`opentelemetry/proto/common/v1/common.proto`) in the global protobuf registry, so linking both
// panics at init. This agent always links the plain ones through the OTel SDK's OTLP exporters, so a slim
// import anywhere in the module kills every binary that imports the agent.
package otlpprofiles

import (
	"strconv"
	"strings"

	"github.com/google/pprof/profile"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Limits bound one payload. A minute of CPU samples from a busy process is tens of thousands of stacks, and
// the receiving side caps what it will expand (internal/profiles), so the agent must not send more than it
// would accept back.
const (
	// MaxSamples bounds the samples of one profile, per sample type.
	MaxSamples = 100_000
	// MaxLabelBytes bounds a sample label key or value.
	MaxLabelBytes = 512
)

// dict builds the payload's shared dictionary. Every table is interned by identity or by value, so a frame
// that appears in a thousand stacks costs one entry and a thousand int32s.
type dict struct {
	pb *profilespb.ProfilesDictionary

	strings   map[string]int32
	functions map[*profile.Function]int32
	locations map[*profile.Location]int32
	stacks    map[string]int32
	attrs     map[string]int32
}

func newDict() *dict {
	d := &dict{
		pb:        &profilespb.ProfilesDictionary{},
		strings:   map[string]int32{},
		functions: map[*profile.Function]int32{},
		locations: map[*profile.Location]int32{},
		stacks:    map[string]int32{},
		attrs:     map[string]int32{},
	}
	d.str("") // index 0 is the empty string by convention
	return d
}

func (d *dict) str(s string) int32 {
	if i, ok := d.strings[s]; ok {
		return i
	}
	d.pb.StringTable = append(d.pb.StringTable, s)
	i := int32(len(d.pb.StringTable) - 1)
	d.strings[s] = i
	return i
}

func (d *dict) function(f *profile.Function) int32 {
	if f == nil {
		return 0
	}
	if i, ok := d.functions[f]; ok {
		return i
	}
	d.pb.FunctionTable = append(d.pb.FunctionTable, &profilespb.Function{
		NameStrindex:       d.str(f.Name),
		SystemNameStrindex: d.str(f.SystemName),
		FilenameStrindex:   d.str(f.Filename),
		StartLine:          f.StartLine,
	})
	i := int32(len(d.pb.FunctionTable) - 1)
	d.functions[f] = i
	return i
}

// location keeps every line of a pprof location. Inlined calls are separate lines of one location, and
// dropping them would make an inlined function disappear from every flame graph it is really in.
func (d *dict) location(l *profile.Location) int32 {
	if i, ok := d.locations[l]; ok {
		return i
	}
	lines := make([]*profilespb.Line, 0, len(l.Line))
	for _, ln := range l.Line {
		lines = append(lines, &profilespb.Line{
			FunctionIndex: d.function(ln.Function),
			Line:          ln.Line,
			Column:        ln.Column,
		})
	}
	d.pb.LocationTable = append(d.pb.LocationTable, &profilespb.Location{Address: l.Address, Lines: lines})
	i := int32(len(d.pb.LocationTable) - 1)
	d.locations[l] = i
	return i
}

// stack interns a call stack. pprof orders locations leaf first and so does OTLP, so the order is carried
// over rather than reversed — the receiving side is what decides which end a flame graph draws from.
func (d *dict) stack(locs []*profile.Location) int32 {
	idx := make([]int32, 0, len(locs))
	var key strings.Builder
	for _, l := range locs {
		i := d.location(l)
		idx = append(idx, i)
		key.WriteString(strconv.Itoa(int(i)))
		key.WriteByte(',')
	}
	if i, ok := d.stacks[key.String()]; ok {
		return i
	}
	d.pb.StackTable = append(d.pb.StackTable, &profilespb.Stack{LocationIndices: idx})
	i := int32(len(d.pb.StackTable) - 1)
	d.stacks[key.String()] = i
	return i
}

// attr interns one sample label. Labels are what make a profile filterable — a thread name, a request kind
// set with pprof.Do — so they are carried rather than dropped.
func (d *dict) attr(key, value string) int32 {
	key, value = truncate(key, MaxLabelBytes), truncate(value, MaxLabelBytes)
	k := key + "\x00" + value
	if i, ok := d.attrs[k]; ok {
		return i
	}
	d.pb.AttributeTable = append(d.pb.AttributeTable, &profilespb.KeyValueAndUnit{
		KeyStrindex: d.str(key),
		Value:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}},
	})
	i := int32(len(d.pb.AttributeTable) - 1)
	d.attrs[k] = i
	return i
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// FromPprof converts one pprof profile into an OTLP profiles payload.
//
// A pprof CPU profile declares two sample types — `samples/count` and `cpu/nanoseconds` — while an OTLP
// Profile declares exactly one. So one Profile is emitted per sample type, each sample carrying the single
// value of that type. That is what keeps "one profile, one unit" true, which is the invariant every chart
// drawn from this data depends on: nanoseconds and counts must never be summed together.
func FromPprof(p *profile.Profile, resource []*commonpb.KeyValue, scopeName, scopeVersion string) *profilespb.ProfilesData {
	if p == nil || len(p.SampleType) == 0 {
		return nil
	}
	d := newDict()

	profiles := make([]*profilespb.Profile, 0, len(p.SampleType))
	for i, st := range p.SampleType {
		samples := make([]*profilespb.Sample, 0, min(len(p.Sample), MaxSamples))
		for _, s := range p.Sample {
			if i >= len(s.Value) || s.Value[i] == 0 {
				// A zero contributes nothing to any aggregate and would still cost a row downstream.
				continue
			}
			if len(samples) >= MaxSamples {
				break
			}
			samples = append(samples, &profilespb.Sample{
				StackIndex:       d.stack(s.Location),
				Values:           []int64{s.Value[i]},
				AttributeIndices: d.labels(s),
			})
		}
		if len(samples) == 0 {
			continue
		}
		pp := &profilespb.Profile{
			SampleType:   &profilespb.ValueType{TypeStrindex: d.str(st.Type), UnitStrindex: d.str(st.Unit)},
			Samples:      samples,
			TimeUnixNano: uint64(max(p.TimeNanos, 0)),
			DurationNano: uint64(max(p.DurationNanos, 0)),
			Period:       p.Period,
		}
		if p.PeriodType != nil {
			pp.PeriodType = &profilespb.ValueType{TypeStrindex: d.str(p.PeriodType.Type), UnitStrindex: d.str(p.PeriodType.Unit)}
		}
		profiles = append(profiles, pp)
	}
	if len(profiles) == 0 {
		return nil
	}

	return &profilespb.ProfilesData{
		Dictionary: d.pb,
		ResourceProfiles: []*profilespb.ResourceProfiles{{
			Resource: &resourcepb.Resource{Attributes: resource},
			ScopeProfiles: []*profilespb.ScopeProfiles{{
				Scope:    &commonpb.InstrumentationScope{Name: scopeName, Version: scopeVersion},
				Profiles: profiles,
			}},
		}},
	}
}

// labels interns a sample's string labels. Numeric labels are left out: they carry a unit of their own
// (pprof's NumUnit) and squeezing them into a string attribute would lose it.
func (d *dict) labels(s *profile.Sample) []int32 {
	if len(s.Label) == 0 {
		return nil
	}
	var out []int32
	for k, vs := range s.Label {
		for _, v := range vs {
			out = append(out, d.attr(k, v))
		}
	}
	return out
}
