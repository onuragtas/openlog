package sampler

import (
	"testing"

	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
)

// The invariant that has to hold on every platform, now and once the real sampler lands: a refusal comes
// with no sampler, and a sampler comes with no error. Anything else and the command would either use a nil
// sampler or quietly drop a working one.
func TestNewNeverPairsASamplerWithAnError(t *testing.T) {
	s, err := New(config.Config{Frequency: 99})
	switch {
	case err != nil && s != nil:
		t.Error("an error was returned alongside a sampler")
	case err == nil && s == nil:
		t.Error("no sampler and no error: the caller has nothing to use and nothing to report")
	}
	if err != nil {
		t.Logf("this platform refuses, as expected here: %v", err)
	}
}
