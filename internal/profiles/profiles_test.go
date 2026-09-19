package profiles

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
)

// builder assembles an OTLP profiles payload the way an agent would: everything lives in the dictionary and
// the samples point at it by index. Writing it out longhand is the point — the indirection is exactly what
// FromOTLP has to get right.
type builder struct {
	pd      pprofile.ProfilesData
	strings map[string]int32
}

func newBuilder() *builder {
	b := &builder{pd: pprofile.NewProfilesData(), strings: map[string]int32{}}
	b.str("") // index 0 is the empty string by convention
	return b
}

func (b *builder) str(s string) int32 {
	if i, ok := b.strings[s]; ok {
		return i
	}
	table := b.pd.Dictionary().StringTable()
	table.Append(s)
	i := int32(table.Len() - 1)
	b.strings[s] = i
	return i
}

// fn adds a function and returns its index.
func (b *builder) fn(name string) int32 {
	table := b.pd.Dictionary().FunctionTable()
	f := table.AppendEmpty()
	f.SetNameStrindex(b.str(name))
	return int32(table.Len() - 1)
}

// location adds a location holding the given functions as (possibly inlined) lines.
func (b *builder) location(funcs ...int32) int32 {
	table := b.pd.Dictionary().LocationTable()
	loc := table.AppendEmpty()
	for _, fi := range funcs {
		loc.Lines().AppendEmpty().SetFunctionIndex(fi)
	}
	return int32(table.Len() - 1)
}

// stack adds a stack of locations, leaf first, as OTLP orders them.
func (b *builder) stack(locs ...int32) int32 {
	table := b.pd.Dictionary().StackTable()
	st := table.AppendEmpty()
	for _, l := range locs {
		st.LocationIndices().Append(l)
	}
	return int32(table.Len() - 1)
}

// profile adds a profile with a sample type and returns it for samples to be added.
func (b *builder) profile(service, sampleType, unit string) pprofile.Profile {
	rp := b.pd.ResourceProfiles().AppendEmpty()
	rp.Resource().Attributes().PutStr("service.name", service)
	rp.Resource().Attributes().PutStr("deployment.environment.name", "production")
	rp.Resource().Attributes().PutStr("host.id", "host-1")
	p := rp.ScopeProfiles().AppendEmpty().Profiles().AppendEmpty()
	p.SampleType().SetTypeStrindex(b.str(sampleType))
	p.SampleType().SetUnitStrindex(b.str(unit))
	return p
}

func sample(p pprofile.Profile, stackIdx int32, values ...int64) {
	s := p.Samples().AppendEmpty()
	s.SetStackIndex(stackIdx)
	for _, v := range values {
		s.Values().Append(v)
	}
}

func TestFromOTLPResolvesStacksRootFirst(t *testing.T) {
	b := newBuilder()
	main, handler, query := b.fn("main"), b.fn("handleRequest"), b.fn("db.Query")
	// OTLP orders a stack leaf first, so this is db.Query called by handleRequest called by main.
	st := b.stack(b.location(query), b.location(handler), b.location(main))
	p := b.profile("checkout", "cpu", "nanoseconds")
	p.SetDurationNano(10_000_000_000)
	when := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	p.SetTime(pcommon.NewTimestampFromTime(when))
	sample(p, st, 1_500_000)

	rows, err := FromOTLP(b.pd, "tenant-a", time.Now())
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.TenantID != "tenant-a" || r.ServiceName != "checkout" || r.Environment != "production" || r.HostID != "host-1" {
		t.Errorf("identity: %+v", r)
	}
	if r.ProfileType != "cpu" || r.Unit != "nanoseconds" || r.Value != 1_500_000 || r.DurationNs != 10_000_000_000 {
		t.Errorf("measurement: %+v", r)
	}
	if !r.Timestamp.Equal(when) {
		t.Errorf("timestamp %s, want %s", r.Timestamp, when)
	}
	// Root first is what a flame graph reads, and the leaf is what self time belongs to.
	want := []string{"main", "handleRequest", "db.Query"}
	if len(r.Stack) != len(want) {
		t.Fatalf("stack %v, want %v", r.Stack, want)
	}
	for i := range want {
		if r.Stack[i] != want[i] {
			t.Fatalf("stack %v, want %v", r.Stack, want)
		}
	}
	if r.Leaf() != "db.Query" {
		t.Errorf("leaf %q", r.Leaf())
	}
}

func TestInlinedLinesBecomeTheirOwnFrames(t *testing.T) {
	b := newBuilder()
	outer, inlined := b.fn("compute"), b.fn("helper")
	// One location, two lines: the optimiser inlined helper into compute. Both must appear, or the flame
	// graph hides exactly the function the compiler made invisible.
	st := b.stack(b.location(inlined, outer))
	p := b.profile("api", "cpu", "nanoseconds")
	sample(p, st, 42)

	rows, err := FromOTLP(b.pd, "t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Stack) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Stack[0] != "compute" || rows[0].Stack[1] != "helper" {
		t.Errorf("inlined frames %v, want [compute helper]", rows[0].Stack)
	}
}

func TestSamplesWithoutFramesAreDropped(t *testing.T) {
	b := newBuilder()
	empty := b.stack() // a stack with no locations
	p := b.profile("api", "cpu", "nanoseconds")
	sample(p, empty, 10)
	sample(p, 99, 10) // a stack index that is not in the table

	rows, err := FromOTLP(b.pd, "t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows from unattributable samples, want 0: %+v", len(rows), rows)
	}
}

func TestProfileWithoutASampleTypeIsRefused(t *testing.T) {
	b := newBuilder()
	st := b.stack(b.location(b.fn("main")))
	rp := b.pd.ResourceProfiles().AppendEmpty()
	rp.Resource().Attributes().PutStr("service.name", "api")
	p := rp.ScopeProfiles().AppendEmpty().Profiles().AppendEmpty()
	sample(p, st, 1)

	if _, err := FromOTLP(b.pd, "t", time.Now()); err == nil {
		t.Error("a profile whose numbers have no named type must be refused")
	}
}

func TestDeepStacksKeepTheirInnermostFrames(t *testing.T) {
	b := newBuilder()
	locs := make([]int32, 0, MaxFrames+10)
	// Built leaf first: leaf0 is the innermost frame.
	for i := 0; i < MaxFrames+10; i++ {
		locs = append(locs, b.location(b.fn(frameName(i))))
	}
	st := b.stack(locs...)
	p := b.profile("api", "cpu", "nanoseconds")
	sample(p, st, 1)

	rows, err := FromOTLP(b.pd, "t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stack := rows[0].Stack
	if len(stack) != MaxFrames {
		t.Fatalf("%d frames, want %d", len(stack), MaxFrames)
	}
	// The leaf survives: that is where the time is spent.
	if stack[len(stack)-1] != frameName(0) {
		t.Errorf("leaf %q, want %q", stack[len(stack)-1], frameName(0))
	}
}

func frameName(i int) string { return "f" + string(rune('a'+i%26)) + string(rune('0'+i/26)) }

func TestFlameSumsEveryPathOnce(t *testing.T) {
	rows := []Row{
		{Stack: []string{"main", "a"}, Value: 3},
		{Stack: []string{"main", "a", "leaf"}, Value: 2},
		{Stack: []string{"main", "b"}, Value: 5},
	}
	root := Flame(rows)
	if root.Value != 10 {
		t.Errorf("root %d, want 10", root.Value)
	}
	main := find(root, "main")
	if main == nil || main.Value != 10 {
		t.Fatalf("main %+v", main)
	}
	a := find(main, "a")
	if a == nil || a.Value != 5 {
		t.Errorf("a %+v, want 5", a)
	}
	if leaf := find(a, "leaf"); leaf == nil || leaf.Value != 2 {
		t.Errorf("leaf %+v, want 2", leaf)
	}
	if b := find(main, "b"); b == nil || b.Value != 5 {
		t.Errorf("b %+v, want 5", b)
	}
}

func find(n *Node, name string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestTooManySamplesAreRefused(t *testing.T) {
	b := newBuilder()
	st := b.stack(b.location(b.fn("main")))
	p := b.profile("api", "cpu", "nanoseconds")
	for i := 0; i < MaxRowsPerRequest+1; i++ {
		sample(p, st, 1)
	}
	_, err := FromOTLP(b.pd, "t", time.Now())
	if !errors.Is(err, ErrTooManyRows) {
		t.Errorf("err = %v, want ErrTooManyRows", err)
	}
}
