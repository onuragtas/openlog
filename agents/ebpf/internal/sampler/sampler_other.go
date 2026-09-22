//go:build !linux

// The profiler is Linux-only in function but not in compilation: every package builds and vets on darwin
// and windows so the module is workable anywhere, and this is where that promise is kept.
package sampler

import "github.com/onuragtas/openlog/agents/ebpf/internal/config"

// New reports that there is nothing to sample here. It does not return a sampler that silently produces
// nothing: a profiler that looks healthy and collects no samples is the worst way for this to fail.
func New(config.Config) (Sampler, error) { return nil, ErrUnsupported }
