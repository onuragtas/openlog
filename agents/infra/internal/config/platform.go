package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// UnifiedLogInput configures reading the macOS unified log through `log stream --style ndjson` (D-104).
type UnifiedLogInput struct {
	Enabled bool `yaml:"enabled"`
	// LogPath is the log(1) binary.
	LogPath string `yaml:"log_path"`
	// Predicate is a log(1) --predicate filter, e.g. `subsystem == "com.example.app"`; empty = everything
	// at Level (which is a lot: set a predicate).
	Predicate string `yaml:"predicate"`
	// Level is default, info or debug (log stream --level).
	Level string `yaml:"level"`
}

// EventLogInput configures Windows Event Log collection (D-104).
type EventLogInput struct {
	Enabled  bool              `yaml:"enabled"`
	Channels []EventLogChannel `yaml:"channels"`
}

// EventLogChannel is one subscribed Event Log channel.
type EventLogChannel struct {
	// Name is the channel, e.g. System, Application, Security, Microsoft-Windows-PowerShell/Operational.
	Name string `yaml:"name"`
	// EventIDs restricts the channel to these event IDs; empty = all.
	EventIDs []int `yaml:"event_ids"`
	// Levels restricts to critical, error, warning, information, verbose; empty = all.
	Levels []string `yaml:"levels"`
	// Query is a raw XPath query that replaces event_ids and levels, e.g. "*[System[Provider[@Name='MSSQLSERVER']]]".
	Query string `yaml:"query"`
}

// EventLogLevels maps level names to Event Log level numbers.
var EventLogLevels = map[string]int{"critical": 1, "error": 2, "warning": 3, "information": 4, "verbose": 5}

var unifiedLogLevels = map[string]bool{"default": true, "info": true, "debug": true}

var windowsAbsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// isAbsPath accepts POSIX absolute paths on every OS and drive-letter paths (C:\…, C:/…) too, so that one
// configuration schema validates Linux, macOS and Windows paths regardless of the OS the agent runs on.
func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || windowsAbsPath.MatchString(p) || filepath.IsAbs(p)
}

// applyPlatformDefaults adjusts Default() for the OS the agent runs on (goos = runtime.GOOS).
func applyPlatformDefaults(c *Config, goos string) {
	switch goos {
	case "linux":
		// DefaultPHPSocket is empty in a Windows build; keep DefaultFor("linux") valid there (tests, config tooling).
		if c.PHPForwarder.Socket == "" {
			c.PHPForwarder.Socket = "/run/openlog-infra-agent/php.sock"
		}
	case "darwin":
		c.Containers.CRISockets = []string{}                            // no containerd/CRI-O hosts
		c.PHPForwarder.Socket = "/var/run/openlog-infra-agent/php.sock" // macOS has no /run
		c.PHPForwarder.SocketGroup = "_www"                             // Apache/PHP-FPM user of macOS
		c.PHPAgent.Mode = PHPAgentModeOff                               // the PHP agent packages are Linux-only
	case "windows":
		c.Containers.Enabled = false // Docker Engine named pipes and Windows containers are not supported
		c.Containers.CRISockets = []string{}
		c.Logs.Containers.Enabled = false
		off := false
		c.PHPForwarder.Enabled = &off // no unix datagram socket; the PHP agent is Linux-only
		c.PHPForwarder.Socket = ""
		c.PHPAgent.Mode = PHPAgentModeOff
		c.Logs.WindowsEventLog.Channels = []EventLogChannel{
			{Name: "System", Levels: []string{"critical", "error", "warning"}},
			{Name: "Application", Levels: []string{"critical", "error", "warning"}},
		}
	}
	if c.Logs.UnifiedLog.LogPath == "" {
		c.Logs.UnifiedLog.LogPath = "/usr/bin/log"
	}
	if c.Logs.UnifiedLog.Level == "" {
		c.Logs.UnifiedLog.Level = "default"
	}
}

// platformWarnings reports configured features that do not exist on goos; they are ignored there.
func (c *Config) platformWarnings(goos string) []string {
	var out []string
	na := func(key, where string) {
		out = append(out, fmt.Sprintf("%s is only available on %s (this host runs %s)", key, where, goos))
	}
	if c.Logs.Journald.Enabled && goos != "linux" {
		na("logs.journald", "Linux")
	}
	if c.Logs.UnifiedLog.Enabled && goos != "darwin" {
		na("logs.unified_log", "macOS")
	}
	if c.Logs.WindowsEventLog.Enabled && goos != "windows" {
		na("logs.windows_event_log", "Windows")
	}
	if goos == "windows" {
		if c.Containers.Enabled {
			na("containers", "Linux and macOS")
		}
		if c.PHPForwarder.Enabled != nil && *c.PHPForwarder.Enabled && c.PHPForwarder.UDPListen == "" {
			out = append(out, "php_forwarder on Windows needs php_forwarder.udp_listen (there is no unix socket)")
		}
	}
	return out
}

func (l *LogsConfig) validatePlatformInputs() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if u := l.UnifiedLog; u.Enabled {
		if !unifiedLogLevels[u.Level] {
			add("logs.unified_log.level must be default, info or debug")
		}
		if u.LogPath == "" {
			add("logs.unified_log.log_path must not be empty")
		}
		if strings.ContainsAny(u.Predicate, "\x00\n") {
			add("logs.unified_log.predicate must be a single line")
		}
	}
	if e := l.WindowsEventLog; e.Enabled {
		if len(e.Channels) == 0 {
			add("logs.windows_event_log.channels must not be empty")
		}
		seen := map[string]bool{}
		for i, ch := range e.Channels {
			if strings.TrimSpace(ch.Name) == "" || strings.ContainsAny(ch.Name, "\x00\"'") {
				add("logs.windows_event_log.channels[%d].name is invalid", i)
			}
			if seen[strings.ToLower(ch.Name)] {
				add("logs.windows_event_log.channels[%d]: channel %q is listed twice", i, ch.Name)
			}
			seen[strings.ToLower(ch.Name)] = true
			for _, id := range ch.EventIDs {
				if id < 0 || id > 65535 {
					add("logs.windows_event_log.channels[%d].event_ids: %d is not 0..65535", i, id)
				}
			}
			for _, lv := range ch.Levels {
				if _, ok := EventLogLevels[lv]; !ok {
					add("logs.windows_event_log.channels[%d].levels: %q must be critical, error, warning, information or verbose", i, lv)
				}
			}
			if ch.Query != "" && (len(ch.EventIDs) > 0 || len(ch.Levels) > 0) {
				add("logs.windows_event_log.channels[%d]: query replaces event_ids and levels; set one or the other", i)
			}
		}
	}
	return errs
}
