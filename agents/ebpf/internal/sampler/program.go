// The BPF program and its maps (docs/contracts/ebpf-profiler.md §3).
//
// This file has no build tag on purpose. ebpf.MapSpec, ebpf.ProgramSpec and asm.Instructions are plain
// data that build on every platform, so the program can be assembled — and its shape asserted by tests —
// on a machine that could never load it. What genuinely needs a kernel is kept in sampler_linux.go.
//
// **Assembled by hand rather than compiled from C.** bpf2go would put clang into the build of a repository
// that ships six os/arch targets with CGO_ENABLED=0, to produce the few dozen instructions below (D-148).
// The price is that this has to stay small enough to read, which is also the reason it is honest to keep it
// small.
//
// Not verified against a kernel verifier anywhere in CI. Nothing here claims otherwise.
package sampler

import (
	"encoding/binary"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

// Map names. The program references them by name, which is what lets it be built without a live fd.
const (
	MapCounts = "openlog_counts"
	MapStacks = "openlog_stacks"
)

const (
	// flagUserStack is BPF_F_USER_STACK. The library does not export it, so it is named here rather than
	// left as a bare 256 in the instruction stream.
	flagUserStack = 1 << 8

	// KeySize is pid(u32) + user_stack(s32) + kernel_stack(s32).
	KeySize = 12
	// ValueSize is one u64 counter.
	ValueSize = 8
)

// Key identifies one sampled stack: the process, and the two stack ids the kernel interned for it.
// A negative stack id is the kernel's way of saying it could not walk that side, and is kept rather than
// dropped — a sample with only a kernel stack is still a sample.
type Key struct {
	PID         uint32
	UserStack   int32
	KernelStack int32
}

// MarshalBinary encodes a key the way the BPF program writes it: three 32-bit fields, native order.
func (k Key) MarshalBinary() ([]byte, error) {
	b := make([]byte, KeySize)
	binary.LittleEndian.PutUint32(b[0:], k.PID)
	binary.LittleEndian.PutUint32(b[4:], uint32(k.UserStack))
	binary.LittleEndian.PutUint32(b[8:], uint32(k.KernelStack))
	return b, nil
}

// UnmarshalBinary decodes what the BPF program wrote.
func (k *Key) UnmarshalBinary(b []byte) error {
	if len(b) < KeySize {
		return errShortKey
	}
	k.PID = binary.LittleEndian.Uint32(b[0:])
	k.UserStack = int32(binary.LittleEndian.Uint32(b[4:]))
	k.KernelStack = int32(binary.LittleEndian.Uint32(b[8:]))
	return nil
}

// CountsSpec is the sample counter, per CPU.
//
// PerCPUHash rather than Hash, and that is not a detail: two CPUs sampling the same stack at the same
// instant would race on a shared counter, and a non-atomic read-add-write would lose one of them. The
// stacks that collide are the ones sampled most often — the hottest ones — so a shared map would
// undercount exactly what a profile exists to show. Per CPU there is no race, and userspace sums.
func CountsSpec(maxEntries uint32) *ebpf.MapSpec {
	return &ebpf.MapSpec{
		Name:       MapCounts,
		Type:       ebpf.PerCPUHash,
		KeySize:    KeySize,
		ValueSize:  ValueSize,
		MaxEntries: maxEntries,
	}
}

// StacksSpec is the kernel's stack-trace table: an id in, a stack of addresses out.
func StacksSpec(depth, maxEntries uint32) *ebpf.MapSpec {
	return &ebpf.MapSpec{
		Name:       MapStacks,
		Type:       ebpf.StackTrace,
		KeySize:    4,
		ValueSize:  depth * 8,
		MaxEntries: maxEntries,
	}
}

// Program assembles the sampler: on every perf event, intern both stacks, then increment this CPU's
// counter for (pid, user stack, kernel stack).
func Program() *ebpf.ProgramSpec {
	// The key is built on the stack below the frame pointer, then handed to the map helpers by address.
	const (
		offPID    = -16
		offUser   = -12
		offKernel = -8
		offValue  = -24
	)
	return &ebpf.ProgramSpec{
		Name:    "openlog_cpu_sample",
		Type:    ebpf.PerfEvent,
		License: "GPL",
		Instructions: asm.Instructions{
			asm.Mov.Reg(asm.R6, asm.R1), // keep the context: the helpers below clobber R1

			// key.user_stack = bpf_get_stackid(ctx, &stacks, BPF_F_USER_STACK)
			asm.Mov.Reg(asm.R1, asm.R6),
			asm.LoadMapPtr(asm.R2, 0).WithReference(MapStacks),
			asm.Mov.Imm(asm.R3, flagUserStack),
			asm.FnGetStackid.Call(),
			asm.StoreMem(asm.RFP, offUser, asm.R0, asm.Word),

			// key.kernel_stack = bpf_get_stackid(ctx, &stacks, 0)
			asm.Mov.Reg(asm.R1, asm.R6),
			asm.LoadMapPtr(asm.R2, 0).WithReference(MapStacks),
			asm.Mov.Imm(asm.R3, 0),
			asm.FnGetStackid.Call(),
			asm.StoreMem(asm.RFP, offKernel, asm.R0, asm.Word),

			// key.pid = bpf_get_current_pid_tgid() >> 32, the tgid — what a person means by "the process".
			asm.FnGetCurrentPidTgid.Call(),
			asm.RSh.Imm(asm.R0, 32),
			asm.StoreMem(asm.RFP, offPID, asm.R0, asm.Word),

			// r2 = &key
			asm.Mov.Reg(asm.R2, asm.RFP),
			asm.Add.Imm(asm.R2, offPID),

			// counts[key]
			asm.LoadMapPtr(asm.R1, 0).WithReference(MapCounts),
			asm.FnMapLookupElem.Call(),
			asm.JEq.Imm(asm.R0, 0, "init"),

			// present: this CPU's counter, so a plain add is safe.
			asm.LoadMem(asm.R1, asm.R0, 0, asm.DWord),
			asm.Add.Imm(asm.R1, 1),
			asm.StoreMem(asm.R0, 0, asm.R1, asm.DWord),
			asm.Ja.Label("out"),

			// absent: counts[key] = 1
			asm.Mov.Imm(asm.R1, 1).WithSymbol("init"),
			asm.StoreMem(asm.RFP, offValue, asm.R1, asm.DWord),
			asm.LoadMapPtr(asm.R1, 0).WithReference(MapCounts),
			asm.Mov.Reg(asm.R2, asm.RFP),
			asm.Add.Imm(asm.R2, offPID),
			asm.Mov.Reg(asm.R3, asm.RFP),
			asm.Add.Imm(asm.R3, offValue),
			asm.Mov.Imm(asm.R4, 0), // BPF_ANY
			asm.FnMapUpdateElem.Call(),

			asm.Mov.Imm(asm.R0, 0).WithSymbol("out"),
			asm.Return(),
		},
	}
}
