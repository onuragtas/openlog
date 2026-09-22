package config

import (
	"strings"
	"testing"
	"time"
)

func TestEBPFProfilerValidate(t *testing.T) {
	def := defaultEBPFProfiler()
	if errs := def.validate(); len(errs) != 0 {
		t.Fatalf("the default configuration is invalid: %v", errs)
	}
	// Auto by default: the profiler is standard equipment (D-149), and the marker file is what keeps it from
	// touching an installation a package owns.
	if def.Mode != EBPFProfilerModeAuto {
		t.Errorf("default mode = %q, want %q", def.Mode, EBPFProfilerModeAuto)
	}
	if def.Version != EBPFProfilerVersionAgent {
		t.Errorf("default version = %q, want %q", def.Version, EBPFProfilerVersionAgent)
	}

	for _, c := range []struct {
		mut  func(*EBPFProfilerConfig)
		want string
	}{
		{func(c *EBPFProfilerConfig) { c.Mode = "sometimes" }, "ebpf_profiler.mode"},
		{func(c *EBPFProfilerConfig) { c.Version = "" }, "ebpf_profiler.version"},
		{func(c *EBPFProfilerConfig) { c.HealthCheckAfter = Duration(time.Second) }, "health_check_after"},
		{func(c *EBPFProfilerConfig) { c.InstallRoot = "" }, "install_root"},
	} {
		cfg := def
		c.mut(&cfg)
		errs := cfg.validate()
		if len(errs) == 0 {
			t.Errorf("expected an error mentioning %q", c.want)
			continue
		}
		found := false
		for _, e := range errs {
			if strings.Contains(e.Error(), c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("errors %v mention none of %q", errs, c.want)
		}
	}
}
