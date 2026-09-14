package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestPlatformDefaults(t *testing.T) {
	linux := DefaultFor("linux")
	if !linux.Containers.Enabled || linux.PHPAgent.Mode != PHPAgentModeManual || linux.PHPForwarder.Enabled != nil ||
		len(linux.Containers.CRISockets) != 3 || len(linux.Logs.WindowsEventLog.Channels) != 0 {
		t.Errorf("linux defaults changed: %+v", linux)
	}

	mac := DefaultFor("darwin")
	if mac.PHPForwarder.Socket != "/var/run/openlog-infra-agent/php.sock" || mac.PHPForwarder.SocketGroup != "_www" ||
		mac.PHPAgent.Mode != PHPAgentModeOff || len(mac.Containers.CRISockets) != 0 || !mac.Containers.Enabled {
		t.Errorf("darwin defaults = %+v / %+v", mac.PHPForwarder, mac.Containers)
	}
	if err := mac.Validate(false); err != nil {
		t.Errorf("darwin defaults invalid: %v", err)
	}

	win := DefaultFor("windows")
	if win.Containers.Enabled || win.Logs.Containers.Enabled || win.PHPForwarder.Enabled == nil || *win.PHPForwarder.Enabled ||
		win.PHPForwarder.Socket != "" || win.PHPAgent.Mode != PHPAgentModeOff {
		t.Errorf("windows defaults = %+v", win)
	}
	if n := len(win.Logs.WindowsEventLog.Channels); n != 2 || win.Logs.WindowsEventLog.Enabled {
		t.Errorf("windows event log defaults: %d channels, enabled %v", n, win.Logs.WindowsEventLog.Enabled)
	}
	// A disabled forwarder without socket or UDP listener is valid (Windows default).
	if err := win.Validate(false); err != nil {
		t.Errorf("windows defaults invalid: %v", err)
	}
}

func TestIsAbsPath(t *testing.T) {
	cases := map[string]bool{
		"/var/log/x.log": true, `C:\logs\*.log`: true, "C:/inetpub/logs/*.log": true, "d:\\x": true,
		"relative.log": false, "C:relative": false, "": false,
	}
	if runtime.GOOS != "windows" {
		cases[`\\no-drive`] = false // a UNC path on Windows
	}
	for p, want := range cases {
		if got := isAbsPath(p); got != want {
			t.Errorf("isAbsPath(%q) = %v, want %v", p, got, want)
		}
	}
	cfg := DefaultFor("linux")
	cfg.Logs.Files = []LogFile{{Path: `C:\inetpub\logs\LogFiles\W3SVC1\*.log`}}
	if err := cfg.Validate(false); err != nil {
		t.Errorf("windows log path rejected: %v", err)
	}
}

func TestPlatformWarnings(t *testing.T) {
	cfg := DefaultFor("linux")
	cfg.Logs.Journald.Enabled = true
	cfg.Logs.UnifiedLog.Enabled = true
	cfg.Logs.WindowsEventLog.Enabled = true
	for goos, want := range map[string][]string{
		"linux":   {"logs.unified_log", "logs.windows_event_log"},
		"darwin":  {"logs.journald", "logs.windows_event_log"},
		"windows": {"logs.journald", "logs.unified_log", "containers"},
	} {
		got := strings.Join(cfg.platformWarnings(goos), "\n")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: warnings %q lack %s", goos, got, w)
			}
		}
	}
	if w := DefaultFor("darwin").platformWarnings("darwin"); len(w) != 0 {
		t.Errorf("darwin defaults warn: %v", w)
	}
	if w := DefaultFor("windows").platformWarnings("windows"); len(w) != 0 {
		t.Errorf("windows defaults warn: %v", w)
	}
}

func TestPlatformLogInputsValidation(t *testing.T) {
	yml := `
logs:
  unified_log: { enabled: true, level: info, predicate: 'subsystem == "com.example.app"' }
  windows_event_log:
    enabled: true
    channels:
      - { name: System, levels: [error, warning] }
      - { name: Security, event_ids: [4624, 4625] }
      - { name: Microsoft-Windows-PowerShell/Operational, query: "*[System[Level<=3]]" }
`
	cfg := DefaultFor("linux")
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid inputs rejected: %v", err)
	}
	if cfg.Logs.UnifiedLog.LogPath != "/usr/bin/log" || len(cfg.Logs.WindowsEventLog.Channels) != 3 {
		t.Errorf("parsed = %+v", cfg.Logs)
	}

	bad := DefaultFor("linux")
	bad.Logs.UnifiedLog = UnifiedLogInput{Enabled: true, LogPath: "/usr/bin/log", Level: "loud"}
	bad.Logs.WindowsEventLog = EventLogInput{Enabled: true, Channels: []EventLogChannel{
		{Name: "System", Levels: []string{"fatal"}},
		{Name: "system"},
		{Name: "Application", EventIDs: []int{70000}},
		{Name: "Setup", Levels: []string{"error"}, Query: "*"},
	}}
	err := bad.Validate(false)
	if err == nil {
		t.Fatal("invalid inputs accepted")
	}
	for _, want := range []string{"unified_log.level", "channels[0].levels", "listed twice", "channels[2].event_ids", "query replaces"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
