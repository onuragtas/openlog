package config

import (
	"fmt"
	"path/filepath"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// PHP agent fleet installation modes (docs/contracts/php-agent.md §7.3).
const (
	PHPAgentModeOff    = "off"
	PHPAgentModeManual = "manual"
	PHPAgentModeAuto   = "auto"

	// PHPAgentVersionAgent installs the PHP agent of the infra agent's own version.
	PHPAgentVersionAgent = "agent"

	PHPAgentReloadNone     = "none"
	PHPAgentReloadGraceful = "graceful"

	// DefaultPHPAgentRoot is php_agent.install_root.
	DefaultPHPAgentRoot = "/opt/openlog/php-agent"
	// DefaultPHPAgentHealthCheckAfter is php_agent.health_check_after.
	DefaultPHPAgentHealthCheckAfter = 5 * time.Minute
)

// PHPAgentConfig configures installing the PHP agent through the infra agent (php_agent). The backend can override
// mode, version, reload and exclude_bins through agent sync unless remote_config is false.
type PHPAgentConfig struct {
	// Mode: off (a fleet installation is removed), manual (inventory only; default) or auto (install and upgrade
	// where a supported PHP runtime is found).
	Mode string `yaml:"mode"`
	// Version: "agent" (the infra agent's version; default) or a SemVer version.
	Version string `yaml:"version"`
	// Reload: none (default; PHP-FPM/Apache load the module on their next reload) or graceful.
	Reload string `yaml:"reload"`
	// ExcludeBins are globs of PHP binaries that are never enabled (e.g. /usr/bin/php7.4).
	ExcludeBins []string `yaml:"exclude_bins"`
	// RemoteConfig lets agent sync override the settings above (default true).
	RemoteConfig bool `yaml:"remote_config"`
	// HealthCheckAfter is the time between an installation and its health check (default 5m, at least 10s).
	HealthCheckAfter Duration `yaml:"health_check_after"`
	// InstallRoot is the fleet installation directory (default /opt/openlog/php-agent). The privileged step only uses
	// it from a configuration file that only root can change.
	InstallRoot string `yaml:"install_root"`
}

func defaultPHPAgent() PHPAgentConfig {
	return PHPAgentConfig{
		Mode: PHPAgentModeManual, Version: PHPAgentVersionAgent, Reload: PHPAgentReloadNone, RemoteConfig: true,
		HealthCheckAfter: Duration(DefaultPHPAgentHealthCheckAfter), InstallRoot: DefaultPHPAgentRoot,
	}
}

func (p *PHPAgentConfig) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	switch p.Mode {
	case PHPAgentModeOff, PHPAgentModeManual, PHPAgentModeAuto:
	default:
		add("php_agent.mode must be off, manual or auto")
	}
	if p.Version != PHPAgentVersionAgent {
		if _, err := lib.ParseVersion(p.Version); err != nil {
			add("php_agent.version must be agent or a SemVer version: %v", err)
		}
	}
	switch p.Reload {
	case PHPAgentReloadNone, PHPAgentReloadGraceful:
	default:
		add("php_agent.reload must be none or graceful")
	}
	if err := ValidatePHPExcludeBins(p.ExcludeBins); err != nil {
		add("php_agent.exclude_bins: %v", err)
	}
	if p.HealthCheckAfter.D() < 10*time.Second {
		add("php_agent.health_check_after must be at least 10s")
	}
	if !filepath.IsAbs(p.InstallRoot) || filepath.Clean(p.InstallRoot) == "/" {
		add("php_agent.install_root must be an absolute path other than /")
	}
	return errs
}

// ValidatePHPExcludeBins checks exclude_bins globs (also used for values received through sync).
func ValidatePHPExcludeBins(globs []string) error {
	if len(globs) > 50 {
		return fmt.Errorf("at most 50 globs")
	}
	for _, g := range globs {
		if g == "" || len(g) > 512 {
			return fmt.Errorf("glob %q must be 1 to 512 bytes", g)
		}
		if _, err := filepath.Match(g, ""); err != nil {
			return fmt.Errorf("invalid glob %q", g)
		}
	}
	return nil
}
