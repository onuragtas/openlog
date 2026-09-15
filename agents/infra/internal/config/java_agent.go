package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Java agent fleet management modes (docs/contracts/java-agent.md §2, D-123). The values equal the PHP agent's.
const (
	JavaAgentModeOff    = "off"
	JavaAgentModeManual = "manual"
	JavaAgentModeAuto   = "auto"

	// JavaAgentVersionAgent installs the Java agent of the infra agent's own version.
	JavaAgentVersionAgent = "agent"

	// DefaultJavaAgentRoot and DefaultJavaAgentLink are java_agent.install_root and link_path on Linux and macOS.
	DefaultJavaAgentRoot = "/opt/openlog/java-agent"
	DefaultJavaAgentLink = "/opt/openlog/openlog-javaagent.jar"
	// WindowsJavaAgentRoot and WindowsJavaAgentLink are the Windows defaults.
	WindowsJavaAgentRoot = `C:\Program Files\openlog\java-agent`
	WindowsJavaAgentLink = `C:\Program Files\openlog\openlog-javaagent.jar`
	// DefaultJavaAgentHealthCheckAfter is java_agent.health_check_after.
	DefaultJavaAgentHealthCheckAfter = 2 * time.Minute
)

// JavaAgentConfig configures managing openlog-javaagent.jar through the infra agent (java_agent). The backend can
// override mode and version through agent sync unless remote_config is false.
type JavaAgentConfig struct {
	// Mode: off (a fleet installation is removed), manual (inventory only; default) or auto (install and upgrade).
	Mode string `yaml:"mode"`
	// Version: "agent" (the infra agent's version; default) or a SemVer version.
	Version string `yaml:"version"`
	// RemoteConfig lets agent sync override mode and version (default true).
	RemoteConfig bool `yaml:"remote_config"`
	// HealthCheckAfter is the time between a switch and its verification (default 2m, at least 10s).
	HealthCheckAfter Duration `yaml:"health_check_after"`
	// InstallRoot holds versions/<v>/openlog-javaagent.jar and current (default /opt/openlog/java-agent). The
	// privileged step only uses it from a configuration file that only root can change.
	InstallRoot string `yaml:"install_root"`
	// LinkPath is the stable path JVMs use in -javaagent: (default /opt/openlog/openlog-javaagent.jar). A file there
	// that the infra agent did not create is never changed.
	LinkPath string `yaml:"link_path"`
}

func defaultJavaAgent() JavaAgentConfig {
	return JavaAgentConfig{
		Mode: JavaAgentModeManual, Version: JavaAgentVersionAgent, RemoteConfig: true,
		HealthCheckAfter: Duration(DefaultJavaAgentHealthCheckAfter), InstallRoot: DefaultJavaAgentRoot, LinkPath: DefaultJavaAgentLink,
	}
}

func (j *JavaAgentConfig) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	switch j.Mode {
	case JavaAgentModeOff, JavaAgentModeManual, JavaAgentModeAuto:
	default:
		add("java_agent.mode must be off, manual or auto")
	}
	if j.Version != JavaAgentVersionAgent {
		if _, err := lib.ParseVersion(j.Version); err != nil {
			add("java_agent.version must be agent or a SemVer version: %v", err)
		}
	}
	if j.HealthCheckAfter.D() < 10*time.Second {
		add("java_agent.health_check_after must be at least 10s")
	}
	root := filepath.ToSlash(filepath.Clean(j.InstallRoot))
	if !isAbsPath(j.InstallRoot) || root == "/" || strings.HasSuffix(root, ":/") {
		add("java_agent.install_root must be an absolute path other than /")
	}
	link := filepath.ToSlash(filepath.Clean(j.LinkPath))
	switch {
	case !isAbsPath(j.LinkPath) || !strings.HasSuffix(strings.ToLower(link), ".jar"):
		add("java_agent.link_path must be an absolute path ending in .jar")
	case strings.HasPrefix(strings.ToLower(link)+"/", strings.ToLower(root)+"/"):
		add("java_agent.link_path must be outside java_agent.install_root")
	}
	return errs
}
