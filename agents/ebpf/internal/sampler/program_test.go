package sampler

import (
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

// The encoding is a contract with the kernel: the BPF program writes three 32-bit fields and userspace
// reads them back. A mismatch here would not fail, it would attribute samples to the wrong process.
func TestKeyRoundTrips(t *testing.T) {
	want := Key{PID: 4242, UserStack: 17, KernelStack: -1}
	b, err := want.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != KeySize {
		t.Fatalf("encoded %d bytes, want %d", len(b), KeySize)
	}
	var got Key
	if err := got.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// A negative stack id means the kernel could not walk that side. It must survive the round trip rather
// than becoming a huge positive number: a sample with only a kernel stack is still a sample.
func TestNegativeStackIDSurvives(t *testing.T) {
	b, _ := Key{PID: 1, UserStack: -14, KernelStack: -14}.MarshalBinary()
	var got Key
	if err := got.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if got.UserStack != -14 || got.KernelStack != -14 {
		t.Errorf("stack ids = %d/%d, want -14/-14", got.UserStack, got.KernelStack)
	}
}

func TestShortKeyIsRefused(t *testing.T) {
	var k Key
	if err := k.UnmarshalBinary(make([]byte, KeySize-1)); err == nil {
		t.Error("a short key was accepted")
	}
}

// The map specs and the encoding have to agree, or the kernel rejects every update at run time — on a
// machine this test cannot reach.
func TestMapSpecsMatchTheEncoding(t *testing.T) {
	c := CountsSpec(1 << 15)
	if c.KeySize != KeySize || c.ValueSize != ValueSize {
		t.Errorf("counts spec = %d/%d, want %d/%d", c.KeySize, c.ValueSize, KeySize, ValueSize)
	}
	// Per CPU by construction: a shared counter would lose updates on exactly the hottest stacks.
	if c.Type != ebpf.PerCPUHash {
		t.Errorf("counts map is %v, want PerCPUHash", c.Type)
	}
	s := StacksSpec(127, 10000)
	if s.Type != ebpf.StackTrace {
		t.Errorf("stacks map is %v, want StackTrace", s.Type)
	}
	if s.ValueSize != 127*8 {
		t.Errorf("stack value size = %d, want %d", s.ValueSize, 127*8)
	}
}

func TestProgramShape(t *testing.T) {
	p := Program()
	if p.Type != ebpf.PerfEvent {
		t.Errorf("program type = %v, want PerfEvent", p.Type)
	}
	// A BPF program that calls kernel helpers has to be GPL, or the verifier refuses the helper.
	if p.License != "GPL" {
		t.Errorf("license = %q, want GPL", p.License)
	}
	if len(p.Instructions) == 0 {
		t.Fatal("no instructions")
	}
	if last := p.Instructions[len(p.Instructions)-1]; last.OpCode != asm.Return().OpCode {
		t.Errorf("last instruction is %v, want a return", last.OpCode)
	}
}

// Both stacks are what makes a flame graph readable across the user/kernel boundary; dropping either
// would still produce a valid profile and a misleading one.
func TestProgramInternsBothStacks(t *testing.T) {
	n := 0
	for _, ins := range Program().Instructions {
		if ins.IsBuiltinCall() && ins.Constant == int64(asm.FnGetStackid) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("bpf_get_stackid called %d times, want 2 (user and kernel)", n)
	}
}

// The program refers to its maps by name; that is what lets it be assembled without a live fd, and a
// typo would only surface when a kernel rejected the load.
func TestProgramReferencesOnlyKnownMaps(t *testing.T) {
	known := map[string]bool{MapCounts: true, MapStacks: true}
	seen := map[string]int{}
	for _, ins := range Program().Instructions {
		if ref := ins.Reference(); ref != "" && ins.IsLoadFromMap() {
			if !known[ref] {
				t.Errorf("program references unknown map %q", ref)
			}
			seen[ref]++
		}
	}
	if seen[MapStacks] < 2 || seen[MapCounts] < 2 {
		t.Errorf("map references = %v, want both maps loaded at least twice", seen)
	}
}
