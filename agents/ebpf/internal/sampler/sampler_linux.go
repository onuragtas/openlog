//go:build linux

package sampler

import (
	"fmt"

	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
)

// ErrNotImplemented is returned until the perf-event sampler and its BPF program land. It is deliberately
// distinct from ErrUnsupported: this kernel may well be able to do the work, the component cannot yet ask
// it to.
var ErrNotImplemented = fmt.Errorf("the perf-event sampler has not landed yet")

// New will open one perf event per CPU and attach the BPF program that walks stacks. Until it does, it
// refuses. It does not return a sampler that produces nothing: a profiler that looks healthy and collects
// no samples is the worst way for this to fail (docs/contracts/ebpf-profiler.md §3).
func New(config.Config) (Sampler, error) {
	return nil, fmt.Errorf("%w: see docs/contracts/ebpf-profiler.md", ErrNotImplemented)
}
