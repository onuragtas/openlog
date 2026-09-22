// Package otlpprofiles turns aggregated CPU samples into an OTLP profiles payload
// (docs/contracts/ebpf-profiler.md §5, docs/contracts/profiles.md).
//
// It is a second implementation of the shaping the Go agent does in its own internal/otlpprofiles, and that
// duplication is deliberate (D-148): agents/go is a published library, so it cannot depend on a shared
// package wired with a `replace` directive — the directive does not reach consumers, and `go get` would
// break for anyone importing the agent. The input differs too. The Go agent converts a pprof profile, which
// arrives as a dictionary of its own; here the input is already aggregated (stack, value) pairs from a BPF
// map, so there is no pprof to parse and no second sample type to split out.
//
// **Use the plain `go.opentelemetry.io/proto/otlp/...` modules, never the `slim` ones.** The slim modules
// replace the full ones rather than accompanying them: both register the same proto file, so importing one
// of each panics at init.
package otlpprofiles

import (
	"strconv"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// MaxSamples bounds the samples of one profile. The receiving side caps what it will expand, so the agent
// must not send more than it would accept back (ebpf-profiler.md §6).
const MaxSamples = 100_000

// Frame is one call frame. Function is a resolved name where the binary carries symbols, and
// `<binary>+0x<offset>` where it does not — knowing which binary burned the CPU is still an answer, so an
// unresolved frame is reported rather than dropped (ebpf-profiler.md §7).
type Frame struct {
	Function string
	Address  uint64
}

// Sample is one aggregated call stack and the CPU nanoseconds measured on it.
//
// Frames are **leaf first**, the order OTLP carries. The receiving side decides which end a flame graph is
// drawn from, so nothing is reversed here.
type Sample struct {
	Frames []Frame
	Value  int64
}

// dict builds the payload's shared dictionary. Every table is interned by value, so a frame that appears in
// a thousand stacks costs one entry and a thousand int32s.
type dict struct {
	pb *profilespb.ProfilesDictionary

	strings   map[string]int32
	functions map[string]int32
	locations map[string]int32
	stacks    map[string]int32
}

func newDict() *dict {
	d := &dict{
		pb:        &profilespb.ProfilesDictionary{},
		strings:   map[string]int32{},
		functions: map[string]int32{},
		locations: map[string]int32{},
		stacks:    map[string]int32{},
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

func (d *dict) function(name string) int32 {
	if i, ok := d.functions[name]; ok {
		return i
	}
	d.pb.FunctionTable = append(d.pb.FunctionTable, &profilespb.Function{NameStrindex: d.str(name)})
	i := int32(len(d.pb.FunctionTable) - 1)
	d.functions[name] = i
	return i
}

// location interns one frame. Two frames with the same name at different addresses are different locations:
// the address is what tells two static functions of the same name apart.
func (d *dict) location(f Frame) int32 {
	key := f.Function + "\x00" + strconv.FormatUint(f.Address, 16)
	if i, ok := d.locations[key]; ok {
		return i
	}
	d.pb.LocationTable = append(d.pb.LocationTable, &profilespb.Location{
		Address: f.Address,
		Lines:   []*profilespb.Line{{FunctionIndex: d.function(f.Function)}},
	})
	i := int32(len(d.pb.LocationTable) - 1)
	d.locations[key] = i
	return i
}

func (d *dict) stack(frames []Frame) int32 {
	idx := make([]int32, 0, len(frames))
	var key strings.Builder
	for _, f := range frames {
		i := d.location(f)
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

// FromSamples converts one interval's samples into an OTLP profiles payload, or nil when there is nothing
// worth sending. An interval in which nothing ran produces no request at all (ebpf-profiler.md §5): an empty
// profile costs both sides a round trip and draws an empty flame graph.
//
// One profile, one unit. Only CPU nanoseconds are produced here, so unlike a pprof conversion there is no
// second sample type to split into a second profile — but the invariant it protects is the same, and the
// sample type is stated rather than assumed so a chart can say what its numbers mean.
func FromSamples(
	samples []Sample,
	resource []*commonpb.KeyValue,
	scopeName, scopeVersion string,
	startUnixNano uint64,
	durationNano uint64,
	periodNano int64,
) *profilespb.ProfilesData {
	d := newDict()
	out := make([]*profilespb.Sample, 0, min(len(samples), MaxSamples))
	for _, s := range samples {
		// A sample worth no time contributes nothing to any aggregate and would still cost a row
		// downstream. Negative is impossible from the kernel and is refused rather than trusted.
		if s.Value <= 0 || len(s.Frames) == 0 {
			continue
		}
		if len(out) >= MaxSamples {
			break
		}
		out = append(out, &profilespb.Sample{StackIndex: d.stack(s.Frames), Values: []int64{s.Value}})
	}
	if len(out) == 0 {
		return nil
	}

	cpu, nanos := d.str("cpu"), d.str("nanoseconds")
	return &profilespb.ProfilesData{
		Dictionary: d.pb,
		ResourceProfiles: []*profilespb.ResourceProfiles{{
			Resource: &resourcepb.Resource{Attributes: resource},
			ScopeProfiles: []*profilespb.ScopeProfiles{{
				Scope: &commonpb.InstrumentationScope{Name: scopeName, Version: scopeVersion},
				Profiles: []*profilespb.Profile{{
					SampleType:   &profilespb.ValueType{TypeStrindex: cpu, UnitStrindex: nanos},
					PeriodType:   &profilespb.ValueType{TypeStrindex: cpu, UnitStrindex: nanos},
					Period:       periodNano,
					Samples:      out,
					TimeUnixNano: startUnixNano,
					DurationNano: durationNano,
				}},
			}},
		}},
	}
}
