package otlpprofiles

import (
	"testing"

	"github.com/google/pprof/profile"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
)

// testPprof is the shape a Go CPU profile really has: two sample types over the same samples, stacks ordered
// leaf first, one function reached through two different stacks, and a sample that measured nothing.
func testPprof() *profile.Profile {
	main := &profile.Function{ID: 1, Name: "main.main"}
	handle := &profile.Function{ID: 2, Name: "main.handle"}
	query := &profile.Function{ID: 3, Name: "db.Query"}
	lm := &profile.Location{ID: 1, Line: []profile.Line{{Function: main}}}
	lh := &profile.Location{ID: 2, Line: []profile.Line{{Function: handle}}}
	lq := &profile.Location{ID: 3, Line: []profile.Line{{Function: query}}}
	return &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        10_000_000,
		TimeNanos:     1757757600000000000,
		DurationNanos: 60_000_000_000,
		Function:      []*profile.Function{main, handle, query},
		Location:      []*profile.Location{lm, lh, lq},
		Sample: []*profile.Sample{
			{Location: []*profile.Location{lq, lh, lm}, Value: []int64{3, 30_000_000}, Label: map[string][]string{"thread": {"worker-3"}}},
			{Location: []*profile.Location{lh, lm}, Value: []int64{1, 10_000_000}},
			{Location: []*profile.Location{lm}, Value: []int64{0, 0}},
		},
	}
}

func resourceAttr(k, v string) []*commonpb.KeyValue {
	return []*commonpb.KeyValue{{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}}
}

func convert(t *testing.T) *profilespb.ProfilesData {
	t.Helper()
	data := FromPprof(testPprof(), resourceAttr("service.name", "checkout"), "scope", "1.2.3")
	if data == nil {
		t.Fatal("conversion produced nothing")
	}
	return data
}

// frames resolves a sample's stack back to function names, in the order they are stored.
func frames(d *profilespb.ProfilesDictionary, stackIndex int32) []string {
	var out []string
	for _, li := range d.StackTable[stackIndex].LocationIndices {
		for _, line := range d.LocationTable[li].Lines {
			out = append(out, d.StringTable[d.FunctionTable[line.FunctionIndex].NameStrindex])
		}
	}
	return out
}

// A pprof CPU profile declares two sample types; an OTLP Profile declares one. Mixing them would mean summing
// counts with nanoseconds, so each type becomes a profile of its own with a single value per sample.
func TestFromPprofSplitsSampleTypes(t *testing.T) {
	data := convert(t)
	if len(data.ResourceProfiles) != 1 {
		t.Fatalf("%d resource profiles", len(data.ResourceProfiles))
	}
	rp := data.ResourceProfiles[0]
	if got := rp.Resource.Attributes[0].Key; got != "service.name" {
		t.Errorf("resource attribute %q", got)
	}
	if len(rp.ScopeProfiles) != 1 {
		t.Fatalf("%d scope profiles", len(rp.ScopeProfiles))
	}
	sp := rp.ScopeProfiles[0]
	if sp.Scope.Name != "scope" || sp.Scope.Version != "1.2.3" {
		t.Errorf("scope %+v", sp.Scope)
	}
	if len(sp.Profiles) != 2 {
		t.Fatalf("%d profiles, want one per sample type", len(sp.Profiles))
	}
	str := data.Dictionary.StringTable
	want := [][2]string{{"samples", "count"}, {"cpu", "nanoseconds"}}
	for i, p := range sp.Profiles {
		if got := [2]string{str[p.SampleType.TypeStrindex], str[p.SampleType.UnitStrindex]}; got != want[i] {
			t.Errorf("profile %d sample type %v, want %v", i, got, want[i])
		}
		for _, s := range p.Samples {
			if len(s.Values) != 1 {
				t.Errorf("profile %d sample carries %d values, want 1", i, len(s.Values))
			}
		}
		if p.DurationNano != 60_000_000_000 || p.TimeUnixNano != 1757757600000000000 || p.Period != 10_000_000 {
			t.Errorf("profile %d timing %d %d %d", i, p.TimeUnixNano, p.DurationNano, p.Period)
		}
		if str[p.PeriodType.TypeStrindex] != "cpu" {
			t.Errorf("profile %d period type", i)
		}
	}
	// The second profile carries the CPU nanoseconds of the first two samples.
	if v := sp.Profiles[1].Samples[0].Values[0]; v != 30_000_000 {
		t.Errorf("cpu value %d", v)
	}
}

// The dictionary is the point of the format: a frame in many stacks must cost one entry, not one per use.
func TestFromPprofInternsTheDictionary(t *testing.T) {
	d := convert(t).Dictionary
	if len(d.FunctionTable) != 3 {
		t.Errorf("%d functions, want 3", len(d.FunctionTable))
	}
	if len(d.LocationTable) != 3 {
		t.Errorf("%d locations, want 3", len(d.LocationTable))
	}
	// Two distinct stacks, shared by both profiles; the third sample measured nothing and makes none.
	if len(d.StackTable) != 2 {
		t.Errorf("%d stacks, want 2", len(d.StackTable))
	}
	if d.StringTable[0] != "" {
		t.Errorf("string table does not start with the empty string: %q", d.StringTable[0])
	}
}

// Stack order is carried over rather than reversed: pprof stores leaf first and so does OTLP, and the side
// that draws the flame graph is the one that decides which end is the root.
func TestFromPprofKeepsStackOrder(t *testing.T) {
	data := convert(t)
	got := frames(data.Dictionary, data.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0].StackIndex)
	want := []string{"db.Query", "main.handle", "main.main"}
	if len(got) != len(want) {
		t.Fatalf("stack %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stack %v, want %v", got, want)
		}
	}
}

// Labels are what a flame graph is filtered by, so they travel as sample attributes rather than being dropped.
func TestFromPprofCarriesLabels(t *testing.T) {
	data := convert(t)
	d := data.Dictionary
	s := data.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0]
	if len(s.AttributeIndices) != 1 {
		t.Fatalf("%d attributes", len(s.AttributeIndices))
	}
	kv := d.AttributeTable[s.AttributeIndices[0]]
	if d.StringTable[kv.KeyStrindex] != "thread" || kv.Value.GetStringValue() != "worker-3" {
		t.Errorf("attribute %v = %v", d.StringTable[kv.KeyStrindex], kv.Value)
	}
	// The sample that measured nothing carried no label either, and is gone entirely.
	if n := len(data.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples); n != 2 {
		t.Errorf("%d samples, want 2 (the zero-valued one dropped)", n)
	}
}

func TestFromPprofRefusesEmpty(t *testing.T) {
	if FromPprof(nil, nil, "s", "v") != nil {
		t.Error("nil profile produced a payload")
	}
	if FromPprof(&profile.Profile{}, nil, "s", "v") != nil {
		t.Error("profile without a sample type produced a payload")
	}
	// Every sample measured zero: there is nothing to draw, so nothing is sent.
	p := testPprof()
	for _, s := range p.Sample {
		s.Value = []int64{0, 0}
	}
	if FromPprof(p, nil, "s", "v") != nil {
		t.Error("all-zero profile produced a payload")
	}
}
