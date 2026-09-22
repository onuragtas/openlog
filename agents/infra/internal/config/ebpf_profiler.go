package config

import (
	"fmt"
	"time"
)

// eBPF profiler modes (EBPFProfilerConfig.Mode).
const (
	EBPFProfilerModeOff    = "off"
	EBPFProfilerModeManual = "manual"
	EBPFProfilerModeAuto   = "auto"
)

// EBPFProfilerVersionAgent tracks the infra agent's own version.
const EBPFProfilerVersionAgent = "agent"

const (
	// DefaultEBPFProfilerRoot is ebpf_profiler.install_root, the same path the .deb and .rpm use: one profiler
	// per host, whoever installed it. Which of the two owns it is decided by the marker file, not the path.
	DefaultEBPFProfilerRoot = "/opt/openlog/ebpf-profiler"
	// DefaultEBPFProfilerHealthCheckAfter is ebpf_profiler.health_check_after.
	DefaultEBPFProfilerHealthCheckAfter = 2 * time.Minute
)

// EBPFProfilerConfig configures keeping the whole-host CPU profiler current (docs/contracts/ebpf-profiler.md,
// D-149).
//
// Mode auto only acts where the profiler is not owned by a package: install.sh installs the .deb or .rpm by
// default, and a root with no marker file of this agent is left alone. So the two paths never fight over the
// same installation — the package covers hosts installed with install.sh, this covers the rest.
type EBPFProfilerConfig struct {
	// Mode: off (an installation made by this agent is removed), manual (report only) or auto (default:
	// install and keep current where no package owns it).
	Mode string `yaml:"mode"`
	// Version: "agent" (the infra agent's version; default) or a SemVer version.
	Version string `yaml:"version"`
	// RemoteConfig lets agent sync override the settings above (default true).
	RemoteConfig bool `yaml:"remote_config"`
	// HealthCheckAfter is the time between an installation and its health check (default 2m, at least 10s).
	HealthCheckAfter Duration `yaml:"health_check_after"`
	// InstallRoot is the installation directory. The privileged step only uses it from a configuration file
	// that only root can change.
	InstallRoot string `yaml:"install_root"`
}

func defaultEBPFProfiler() EBPFProfilerConfig {
	return EBPFProfilerConfig{
		Mode: EBPFProfilerModeAuto, Version: EBPFProfilerVersionAgent, RemoteConfig: true,
		HealthCheckAfter: Duration(DefaultEBPFProfilerHealthCheckAfter), InstallRoot: DefaultEBPFProfilerRoot,
	}
}

func (p *EBPFProfilerConfig) validate() []error {
	var errs []error
	switch p.Mode {
	case EBPFProfilerModeOff, EBPFProfilerModeManual, EBPFProfilerModeAuto:
	default:
		errs = append(errs, fmt.Errorf("ebpf_profiler.mode must be %q, %q or %q", EBPFProfilerModeOff, EBPFProfilerModeManual, EBPFProfilerModeAuto))
	}
	if p.Version == "" {
		errs = append(errs, fmt.Errorf("ebpf_profiler.version must be %q or a version", EBPFProfilerVersionAgent))
	}
	if d := time.Duration(p.HealthCheckAfter); d < 10*time.Second {
		errs = append(errs, fmt.Errorf("ebpf_profiler.health_check_after must be at least 10s"))
	}
	if p.InstallRoot == "" {
		errs = append(errs, fmt.Errorf("ebpf_profiler.install_root must not be empty"))
	}
	return errs
}
