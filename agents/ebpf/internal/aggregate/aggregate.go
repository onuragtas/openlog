// Package aggregate turns raw kernel samples into per-service profiles (docs/contracts/ebpf-profiler.md §4, §6).
//
// It sits between the sampler and the converter and is where every limit in the contract is enforced. That
// makes it the piece most worth testing: a mistake here does not crash anything, it quietly bills someone
// for series they did not ask for, or drops the stacks that mattered and keeps the slivers.
package aggregate

import (
	"sort"
	"strings"

	"github.com/onuragtas/openlog/agents/ebpf/internal/otlpprofiles"
)

const (
	// MaxFrames is the BPF stack map's own depth limit.
	MaxFrames = 127
	// MaxStacks bounds the distinct stacks of one interval. Beyond it the smallest are dropped: a flame
	// graph cannot draw a sliver that thin either.
	MaxStacks = 20_000
	// MaxServices bounds the names one interval may produce, so a machine that forks a new binary per
	// request cannot turn one host into thousands of series.
	MaxServices = 256
	// TruncatedFrame marks a stack that was cut short, so a flame graph shows the cut instead of implying
	// the root was reached.
	TruncatedFrame = "[truncated]"
)

// RawSample is one entry of the BPF hash map: a process, the addresses of its stack (leaf first) and how
// many times the sampler saw it.
type RawSample struct {
	PID   int
	Addrs []uint64
	Count int64
}

// Symbolizer resolves an address in a process to a frame. Injected so tests need no real binaries.
type Symbolizer interface {
	Resolve(pid int, addr uint64) otlpprofiles.Frame
}

// Namer says which service a process belongs to; attribute.Resolver satisfies it. An empty name means the
// process could not be identified, and its samples are dropped rather than filed under a guess.
type Namer interface {
	Name(pid int) string
}

// Run groups one interval's samples by service. periodNanos converts a sample count into CPU nanoseconds:
// the sampler fires at a fixed frequency, so each sample stands for one period of CPU time.
func Run(raw []RawSample, sym Symbolizer, namer Namer, periodNanos int64) map[string][]otlpprofiles.Sample {
	type key struct {
		service string
		stack   string
	}
	values := map[key]int64{}
	stacks := map[key][]otlpprofiles.Frame{}
	names := map[int]string{}

	for _, s := range raw {
		if s.Count <= 0 || len(s.Addrs) == 0 {
			continue
		}
		name, ok := names[s.PID]
		if !ok {
			name = namer.Name(s.PID)
			names[s.PID] = name
		}
		if name == "" {
			// The process vanished between the sample and the lookup, or could not be identified. A
			// sample with no name cannot be opened on any screen, so filing it under a guess would only
			// make a wrong flame graph.
			continue
		}
		frames := resolve(s.PID, s.Addrs, sym)
		if len(frames) == 0 {
			continue
		}
		k := key{service: name, stack: stackKey(frames)}
		if _, seen := stacks[k]; !seen {
			stacks[k] = frames
		}
		values[k] += s.Count * periodNanos
	}

	out := map[string][]otlpprofiles.Sample{}
	for k, v := range values {
		out[k.service] = append(out[k.service], otlpprofiles.Sample{Frames: stacks[k], Value: v})
	}
	for name := range out {
		sortByValue(out[name])
	}
	capStacks(out)
	capServices(out)
	return out
}

// resolve symbolizes a stack, truncating at the root. Stacks are leaf first, so the frames kept are the
// ones nearest the work being done — the end a flame graph is read from.
func resolve(pid int, addrs []uint64, sym Symbolizer) []otlpprofiles.Frame {
	truncated := false
	if len(addrs) > MaxFrames {
		addrs = addrs[:MaxFrames-1]
		truncated = true
	}
	frames := make([]otlpprofiles.Frame, 0, len(addrs)+1)
	for _, a := range addrs {
		frames = append(frames, sym.Resolve(pid, a))
	}
	if truncated {
		frames = append(frames, otlpprofiles.Frame{Function: TruncatedFrame})
	}
	return frames
}

func stackKey(frames []otlpprofiles.Frame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(f.Function)
		b.WriteByte(0)
	}
	return b.String()
}

func sortByValue(s []otlpprofiles.Sample) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].Value > s[j].Value })
}

// capStacks keeps the largest stacks of the interval, counted across every service: the limit is what the
// receiving side will expand, and it does not care which service filled it.
func capStacks(out map[string][]otlpprofiles.Sample) {
	total := 0
	for _, s := range out {
		total += len(s)
	}
	if total <= MaxStacks {
		return
	}
	type ref struct {
		service string
		sample  otlpprofiles.Sample
	}
	all := make([]ref, 0, total)
	for name, samples := range out {
		for _, s := range samples {
			all = append(all, ref{service: name, sample: s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].sample.Value > all[j].sample.Value })
	all = all[:MaxStacks]
	kept := map[string][]otlpprofiles.Sample{}
	for _, r := range all {
		kept[r.service] = append(kept[r.service], r.sample)
	}
	for name := range out {
		delete(out, name)
	}
	for name, samples := range kept {
		out[name] = samples
	}
}

// capServices keeps the busiest names. Dropping the busiest would hide exactly the process worth looking at.
func capServices(out map[string][]otlpprofiles.Sample) {
	if len(out) <= MaxServices {
		return
	}
	type tot struct {
		name  string
		value int64
	}
	totals := make([]tot, 0, len(out))
	for name, samples := range out {
		var v int64
		for _, s := range samples {
			v += s.Value
		}
		totals = append(totals, tot{name, v})
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].value != totals[j].value {
			return totals[i].value > totals[j].value
		}
		return totals[i].name < totals[j].name
	})
	for _, t := range totals[MaxServices:] {
		delete(out, t.name)
	}
}
