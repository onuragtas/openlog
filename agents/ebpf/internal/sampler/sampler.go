// Package sampler collects CPU samples from the kernel (docs/contracts/ebpf-profiler.md §3).
package sampler

import (
	"context"
	"errors"
	"time"

	"github.com/onuragtas/openlog/agents/ebpf/internal/aggregate"
)

// ErrUnsupported is returned where the kernel interfaces this needs do not exist.
var ErrUnsupported = errors.New("whole-host profiling needs Linux with perf events and eBPF")

// Sampler collects raw samples for one window. It mirrors profiler.Sampler; the interface is declared here
// too so the command can name the concrete type without importing the loop.
type Sampler interface {
	Sample(ctx context.Context, d time.Duration) ([]aggregate.RawSample, error)
	Close() error
}
