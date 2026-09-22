//go:build linux

package sampler

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"

	"github.com/onuragtas/openlog/agents/ebpf/internal/aggregate"
	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
)

// stackDepth is the BPF stack map's own limit (docs/contracts/ebpf-profiler.md §6).
const stackDepth = 127

// maxStackEntries and maxCountEntries bound the kernel-side tables. They are larger than the per-interval
// limits the aggregator applies, because the kernel fills them between drains and dropping there would
// lose samples silently.
const (
	maxStackEntries = 16384
	maxCountEntries = 1 << 16
)

type linuxSampler struct {
	counts *ebpf.Map
	stacks *ebpf.Map
	prog   *ebpf.Program
	events []int // one perf event fd per CPU
	cpus   int
}

// New loads the program, creates its maps and opens one perf event per CPU.
//
// It returns an error rather than a sampler that produces nothing: a profiler that looks healthy and
// collects nothing is the worst way for this to fail (contract §3). Everything opened before a failure is
// closed on the way out.
func New(cfg config.Config) (Sampler, error) {
	cpus, err := ebpf.PossibleCPU()
	if err != nil {
		return nil, fmt.Errorf("sampler: possible CPUs: %w", err)
	}

	counts, err := ebpf.NewMap(CountsSpec(maxCountEntries))
	if err != nil {
		return nil, fmt.Errorf("sampler: counts map: %w", err)
	}
	stacks, err := ebpf.NewMap(StacksSpec(stackDepth, maxStackEntries))
	if err != nil {
		counts.Close()
		return nil, fmt.Errorf("sampler: stack map: %w", err)
	}

	// The program refers to its maps by name; this is where those names become file descriptors.
	spec := Program()
	if err := spec.Instructions.AssociateMap(MapCounts, counts); err != nil {
		counts.Close()
		stacks.Close()
		return nil, fmt.Errorf("sampler: bind %s: %w", MapCounts, err)
	}
	if err := spec.Instructions.AssociateMap(MapStacks, stacks); err != nil {
		counts.Close()
		stacks.Close()
		return nil, fmt.Errorf("sampler: bind %s: %w", MapStacks, err)
	}

	prog, err := ebpf.NewProgram(spec)
	if err != nil {
		counts.Close()
		stacks.Close()
		// The verifier's own message is the only useful thing here, so it is not wrapped away.
		return nil, fmt.Errorf("sampler: load program (needs CAP_BPF and CAP_PERFMON, kernel >= 5.8): %w", err)
	}

	s := &linuxSampler{counts: counts, stacks: stacks, prog: prog, cpus: cpus}
	for cpu := 0; cpu < cpus; cpu++ {
		fd, err := openPerfEvent(cpu, cfg.Frequency)
		if err != nil {
			s.Close()
			paranoid, readErr := readParanoid(cfg.HostRoot)
			return nil, fmt.Errorf("sampler: perf event on cpu %d: %w%s", cpu, err, PerfHint(err, paranoid, readErr))
		}
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_SET_BPF, prog.FD()); err != nil {
			unix.Close(fd)
			s.Close()
			return nil, fmt.Errorf("sampler: attach to cpu %d: %w", cpu, err)
		}
		s.events = append(s.events, fd)
	}
	return s, nil
}

// openPerfEvent asks for a software CPU clock sampled at a frequency rather than a period, so the rate is
// the same on a busy core and an idle one.
func openPerfEvent(cpu, freq int) (int, error) {
	attr := &unix.PerfEventAttr{
		Type:   unix.PERF_TYPE_SOFTWARE,
		Config: unix.PERF_COUNT_SW_CPU_CLOCK,
		Size:   uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
		Sample: uint64(freq),
		Bits:   unix.PerfBitFreq,
	}
	// pid -1 with a cpu means "everything running on this CPU", which is what whole-host means.
	return unix.PerfEventOpen(attr, -1, cpu, -1, unix.PERF_FLAG_FD_CLOEXEC)
}

// Sample waits out the window, then drains what the kernel wrote.
func (s *linuxSampler) Sample(ctx context.Context, d time.Duration) ([]aggregate.RawSample, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
	}
	return s.drain()
}

// drain reads every counted stack and deletes it, so the next window starts empty and a key that stopped
// being sampled stops being reported.
func (s *linuxSampler) drain() ([]aggregate.RawSample, error) {
	var (
		key    Key
		values = make([]uint64, s.cpus) // per-CPU maps want a slice as long as PossibleCPU
		out    []aggregate.RawSample
		keys   []Key
	)
	it := s.counts.Iterate()
	for it.Next(&key, &values) {
		count := SumPerCPU(values)
		if count <= 0 {
			continue
		}
		addrs := s.resolve(key)
		if len(addrs) > 0 {
			out = append(out, aggregate.RawSample{PID: int(key.PID), Addrs: addrs, Count: count})
		}
		keys = append(keys, key)
	}
	if err := it.Err(); err != nil {
		return out, fmt.Errorf("sampler: read counts: %w", err)
	}
	for _, k := range keys {
		// Deleting during iteration is not safe, so it happens here.
		_ = s.counts.Delete(k)
	}
	return out, nil
}

// resolve turns the two interned stack ids into one stack, leaf first: kernel frames sit above user frames
// at the moment of the sample, so the kernel side comes first.
func (s *linuxSampler) resolve(key Key) []uint64 {
	var addrs []uint64
	if key.KernelStack >= 0 {
		addrs = append(addrs, s.lookupStack(key.KernelStack)...)
	}
	if key.UserStack >= 0 {
		addrs = append(addrs, s.lookupStack(key.UserStack)...)
	}
	return addrs
}

func (s *linuxSampler) lookupStack(id int32) []uint64 {
	frames := make([]uint64, stackDepth)
	if err := s.stacks.Lookup(uint32(id), &frames); err != nil {
		// A stack id can expire between the sample and the drain; that costs one stack, not the interval.
		return nil
	}
	return TrimStack(frames)
}

// Close releases every kernel resource, reporting the first failure but never stopping early: a leaked
// perf event outlives the process that opened it.
func (s *linuxSampler) Close() error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	for _, fd := range s.events {
		keep(unix.Close(fd))
	}
	s.events = nil
	if s.prog != nil {
		keep(s.prog.Close())
	}
	if s.counts != nil {
		keep(s.counts.Close())
	}
	if s.stacks != nil {
		keep(s.stacks.Close())
	}
	if first != nil {
		return fmt.Errorf("sampler: close: %w", first)
	}
	return nil
}
