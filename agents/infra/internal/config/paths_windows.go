//go:build windows

package config

// Default paths of Windows hosts (D-104): binaries under Program Files, configuration and state under ProgramData
// (both restricted to SYSTEM and Administrators by the installers and -configure).
const (
	// DefaultPath is the configuration file used when -config is not given.
	DefaultPath = `C:\ProgramData\openlog\infra-agent\config.yaml`
	// DefaultInstallRoot is the default update.install_root.
	DefaultInstallRoot = `C:\Program Files\openlog\infra-agent`
	// DefaultStateDir is the default state_dir.
	DefaultStateDir = `C:\ProgramData\openlog\infra-agent\state`
	// DefaultBufferDir is the default buffer.dir.
	DefaultBufferDir = `C:\ProgramData\openlog\infra-agent\state\buffer`
	// DefaultRulesDir is the default discovery.rules_dir.
	DefaultRulesDir = `C:\ProgramData\openlog\infra-agent\discovery.d`
	// DefaultPHPSocket is empty: Windows has no unix datagram socket for the PHP agent (php_forwarder is disabled).
	DefaultPHPSocket = ""
)
