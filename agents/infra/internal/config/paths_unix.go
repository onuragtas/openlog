//go:build !windows

package config

// Default paths of Linux and macOS hosts (macOS uses the Linux layout, D-104).
const (
	// DefaultPath is the configuration file used when -config is not given.
	DefaultPath = "/etc/openlog-infra-agent/config.yaml"
	// DefaultInstallRoot is the default update.install_root.
	DefaultInstallRoot = "/opt/openlog/infra-agent"
	// DefaultStateDir is the default state_dir.
	DefaultStateDir = "/var/lib/openlog-infra-agent"
	// DefaultBufferDir is the default buffer.dir.
	DefaultBufferDir = "/var/lib/openlog-infra-agent/buffer"
	// DefaultRulesDir is the default discovery.rules_dir.
	DefaultRulesDir = "/etc/openlog-infra-agent/discovery.d"
	// DefaultPHPSocket is the default php_forwarder.socket.
	DefaultPHPSocket = "/run/openlog-infra-agent/php.sock"
)
