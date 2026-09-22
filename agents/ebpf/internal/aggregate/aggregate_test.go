package aggregate

import (
	"fmt"
	"testing"

	"github.com/onuragtas/openlog/agents/ebpf/internal/otlpprofiles"
)

// fakeSym names an address after itself, so a test can read a stack back as text.
type fakeSym struct{}

func (fakeSym) Resolve(_ int, a uint64) otlpprofiles.Frame {
	return otlpprofiles.Frame{Function: fmt.Sprintf("f%d", a), Address: a}
}

type fixedNamer map[int]string

func (n fixedNamer) Name(pid int) string { return n[pid] }

func names(s otlpprofiles.Sample) []string {
	out := make([]string, 0, len(s.Frames))
	for _, f := range s.Frames {
		out = append(out, f.Function)
	}
	return out
}

// A sample stands for one period of CPU time; the count alone would be a number with no unit.
func TestCountsBecomeNanosecondsThroughThePeriod(t *testing.T) {
	got := Run([]RawSample{{PID: 1, Addrs: []uint64{1, 2}, Count: 3}}, fakeSym{}, fixedNamer{1: "redis"}, 10_101_010)
	if len(got["redis"]) != 1 {
		t.Fatalf("got %+v", got)
	}
	if v := got["redis"][0].Value; v != 3*10_101_010 {
		t.Errorf("value = %d, want %d", v, 3*10_101_010)
	}
}

func TestSameStackMergesAndDifferentServicesStaySeparate(t *testing.T) {
	got := Run([]RawSample{
		{PID: 1, Addrs: []uint64{1, 2}, Count: 2},
		{PID: 1, Addrs: []uint64{1, 2}, Count: 5},
		{PID: 2, Addrs: []uint64{1, 2}, Count: 1},
	}, fakeSym{}, fixedNamer{1: "redis", 2: "nginx"}, 1)

	if len(got["redis"]) != 1 || got["redis"][0].Value != 7 {
		t.Errorf("redis = %+v, want one stack worth 7", got["redis"])
	}
	if len(got["nginx"]) != 1 || got["nginx"][0].Value != 1 {
		t.Errorf("nginx = %+v, want one stack worth 1", got["nginx"])
	}
}

// A sample nobody can name cannot be opened on any screen; filing it under a guess makes a wrong flame graph.
func TestUnnamedProcessesAreDropped(t *testing.T) {
	got := Run([]RawSample{
		{PID: 1, Addrs: []uint64{1}, Count: 1},
		{PID: 99, Addrs: []uint64{1}, Count: 1000},
	}, fakeSym{}, fixedNamer{1: "redis"}, 1)
	if len(got) != 1 {
		t.Errorf("got %d services, want only the named one: %+v", len(got), got)
	}
}

func TestValuelessSamplesAreIgnored(t *testing.T) {
	got := Run([]RawSample{
		{PID: 1, Addrs: []uint64{1}, Count: 0},
		{PID: 1, Addrs: []uint64{1}, Count: -3},
		{PID: 1, Addrs: nil, Count: 5},
	}, fakeSym{}, fixedNamer{1: "redis"}, 1)
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

// Leaf first is the order OTLP carries; the frames kept on truncation must be the leaf-most ones, and the
// cut has to be visible rather than implying the root was reached.
func TestDeepStackIsTruncatedAtTheRootAndMarked(t *testing.T) {
	addrs := make([]uint64, 300)
	for i := range addrs {
		addrs[i] = uint64(i)
	}
	got := Run([]RawSample{{PID: 1, Addrs: addrs, Count: 1}}, fakeSym{}, fixedNamer{1: "deep"}, 1)
	s := got["deep"][0]
	if len(s.Frames) != MaxFrames {
		t.Fatalf("kept %d frames, want %d", len(s.Frames), MaxFrames)
	}
	n := names(s)
	if n[0] != "f0" {
		t.Errorf("first frame = %s, want the leaf f0", n[0])
	}
	if n[len(n)-1] != TruncatedFrame {
		t.Errorf("last frame = %s, want %s", n[len(n)-1], TruncatedFrame)
	}
}

func TestShortStacksAreNotMarked(t *testing.T) {
	got := Run([]RawSample{{PID: 1, Addrs: []uint64{1, 2, 3}, Count: 1}}, fakeSym{}, fixedNamer{1: "s"}, 1)
	for _, f := range names(got["s"][0]) {
		if f == TruncatedFrame {
			t.Error("a stack that fit was marked truncated")
		}
	}
}

// What falls off the end must be the narrowest slivers, never the widest.
func TestStackCapKeepsTheLargest(t *testing.T) {
	raw := make([]RawSample, 0, MaxStacks+100)
	for i := 0; i < MaxStacks+100; i++ {
		raw = append(raw, RawSample{PID: 1, Addrs: []uint64{uint64(i)}, Count: int64(i + 1)})
	}
	got := Run(raw, fakeSym{}, fixedNamer{1: "busy"}, 1)
	if len(got["busy"]) != MaxStacks {
		t.Fatalf("kept %d stacks, want %d", len(got["busy"]), MaxStacks)
	}
	var smallest int64 = 1 << 62
	for _, s := range got["busy"] {
		if s.Value < smallest {
			smallest = s.Value
		}
	}
	if smallest <= 100 {
		t.Errorf("smallest kept value is %d: the cap dropped the wrong end", smallest)
	}
}

// Dropping the busiest name would hide exactly the process worth looking at.
func TestServiceCapKeepsTheBusiest(t *testing.T) {
	raw := make([]RawSample, 0, MaxServices+50)
	namer := fixedNamer{}
	for i := 0; i < MaxServices+50; i++ {
		namer[i] = fmt.Sprintf("svc%03d", i)
		raw = append(raw, RawSample{PID: i, Addrs: []uint64{1}, Count: int64(i + 1)})
	}
	got := Run(raw, fakeSym{}, namer, 1)
	if len(got) != MaxServices {
		t.Fatalf("kept %d services, want %d", len(got), MaxServices)
	}
	// The busiest is the highest index; the quietest must be gone.
	if _, ok := got[fmt.Sprintf("svc%03d", MaxServices+49)]; !ok {
		t.Error("the busiest service was dropped")
	}
	if _, ok := got["svc000"]; ok {
		t.Error("the quietest service survived the cap")
	}
}
