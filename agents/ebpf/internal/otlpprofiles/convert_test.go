package otlpprofiles

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
)

func frames(names ...string) []Frame {
	out := make([]Frame, 0, len(names))
	for i, n := range names {
		out = append(out, Frame{Function: n, Address: uint64(0x1000 + i)})
	}
	return out
}

func convert(t *testing.T, samples []Sample) *profilespb.ProfilesData {
	t.Helper()
	res := []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{
		Value: &commonpb.AnyValue_StringValue{StringValue: "postgres"}}}}
	return FromSamples(samples, res, "openlog-ebpf/profiler", "1.2.3", 1_700_000_000_000_000_000, 60_000_000_000, 10_101_010)
}

func onlyProfile(t *testing.T, d *profilespb.ProfilesData) *profilespb.Profile {
	t.Helper()
	if d == nil {
		t.Fatal("no payload")
	}
	rp := d.GetResourceProfiles()
	if len(rp) != 1 || len(rp[0].GetScopeProfiles()) != 1 || len(rp[0].GetScopeProfiles()[0].GetProfiles()) != 1 {
		t.Fatalf("expected exactly one profile, got %d resource profiles", len(rp))
	}
	return rp[0].GetScopeProfiles()[0].GetProfiles()[0]
}

func TestSampleTypeSaysWhatTheNumbersMean(t *testing.T) {
	d := convert(t, []Sample{{Frames: frames("main", "work"), Value: 5000}})
	p := onlyProfile(t, d)
	st := d.GetDictionary().GetStringTable()
	if got := st[p.GetSampleType().GetTypeStrindex()]; got != "cpu" {
		t.Errorf("sample type = %q, want cpu", got)
	}
	if got := st[p.GetSampleType().GetUnitStrindex()]; got != "nanoseconds" {
		t.Errorf("unit = %q, want nanoseconds", got)
	}
	// A chart that cannot say its unit is a number with no meaning, so the period travels too.
	if p.GetPeriod() != 10_101_010 {
		t.Errorf("period = %d", p.GetPeriod())
	}
	if p.GetDurationNano() != 60_000_000_000 || p.GetTimeUnixNano() != 1_700_000_000_000_000_000 {
		t.Errorf("window = %d for %d", p.GetTimeUnixNano(), p.GetDurationNano())
	}
}

// Index 0 of the string table is the empty string by convention; a reader that assumes it would otherwise
// resolve every unset index to a real name.
func TestStringTableStartsEmpty(t *testing.T) {
	d := convert(t, []Sample{{Frames: frames("main"), Value: 1}})
	if st := d.GetDictionary().GetStringTable(); len(st) == 0 || st[0] != "" {
		t.Fatalf("string table does not start with the empty string: %q", st)
	}
}

// The order is the contract: OTLP carries stacks leaf first and the server is what reverses them. Getting
// this backwards produces a flame graph that is upside down and still looks plausible.
func TestFramesStayLeafFirst(t *testing.T) {
	d := convert(t, []Sample{{Frames: frames("db.Query", "handle", "main"), Value: 900}})
	p := onlyProfile(t, d)
	dict := d.GetDictionary()
	idx := dict.GetStackTable()[p.GetSamples()[0].GetStackIndex()].GetLocationIndices()
	var got []string
	for _, i := range idx {
		loc := dict.GetLocationTable()[i]
		fn := dict.GetFunctionTable()[loc.GetLines()[0].GetFunctionIndex()]
		got = append(got, dict.GetStringTable()[fn.GetNameStrindex()])
	}
	want := []string{"db.Query", "handle", "main"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("stack = %v, want %v", got, want)
		}
	}
}

// A frame in two stacks must cost one dictionary entry: that interning is the whole reason the payload of a
// busy minute stays small.
func TestRepeatedFrameIsInternedOnce(t *testing.T) {
	shared := Frame{Function: "runtime.mallocgc", Address: 0x4242}
	d := convert(t, []Sample{
		{Frames: []Frame{shared, {Function: "a", Address: 1}}, Value: 10},
		{Frames: []Frame{shared, {Function: "b", Address: 2}}, Value: 20},
	})
	n := 0
	for _, f := range d.GetDictionary().GetFunctionTable() {
		if d.GetDictionary().GetStringTable()[f.GetNameStrindex()] == "runtime.mallocgc" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("runtime.mallocgc interned %d times, want 1", n)
	}
	if got := len(d.GetDictionary().GetStackTable()); got != 2 {
		t.Errorf("stack table has %d entries, want 2", got)
	}
}

// Two different stacks that happen to share a leaf must not collapse into one.
func TestDistinctStacksStayDistinct(t *testing.T) {
	d := convert(t, []Sample{
		{Frames: frames("leaf", "one"), Value: 1},
		{Frames: frames("leaf", "two"), Value: 1},
	})
	p := onlyProfile(t, d)
	if a, b := p.GetSamples()[0].GetStackIndex(), p.GetSamples()[1].GetStackIndex(); a == b {
		t.Errorf("two different stacks share index %d", a)
	}
}

func TestValuelessSamplesAreDropped(t *testing.T) {
	d := convert(t, []Sample{
		{Frames: frames("real"), Value: 7},
		{Frames: frames("zero"), Value: 0},
		{Frames: frames("negative"), Value: -5},
		{Frames: nil, Value: 100},
	})
	if got := len(onlyProfile(t, d).GetSamples()); got != 1 {
		t.Errorf("kept %d samples, want 1", got)
	}
}

// An interval in which nothing ran produces no request at all: an empty profile costs both sides a round
// trip and draws an empty flame graph.
func TestIdleIntervalProducesNothing(t *testing.T) {
	if d := convert(t, nil); d != nil {
		t.Error("an empty interval produced a payload")
	}
	if d := convert(t, []Sample{{Frames: frames("idle"), Value: 0}}); d != nil {
		t.Error("an interval with only zero-valued samples produced a payload")
	}
}

func TestSampleCapIsEnforced(t *testing.T) {
	many := make([]Sample, MaxSamples+50)
	for i := range many {
		many[i] = Sample{Frames: []Frame{{Function: "f", Address: uint64(i)}}, Value: 1}
	}
	if got := len(onlyProfile(t, convert(t, many)).GetSamples()); got != MaxSamples {
		t.Errorf("emitted %d samples, want the cap %d", got, MaxSamples)
	}
}
