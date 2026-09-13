package updater

import (
	"testing"
	"time"
)

func TestWindows(t *testing.T) {
	ws, err := ParseWindows("sat,sun 02:00-05:00; mon-fri 23:30-00:30")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := map[string]bool{
		"2026-09-12T02:00:00Z":      true,  // Saturday start
		"2026-09-12T04:59:00Z":      true,  // Saturday
		"2026-09-12T05:00:00Z":      false, // end is exclusive
		"2026-09-14T03:00:00Z":      false, // Monday morning
		"2026-09-14T23:45:00Z":      true,  // Monday late
		"2026-09-15T00:15:00Z":      true,  // crosses midnight into Tuesday
		"2026-09-13T00:15:00Z":      false, // Sunday 00:15 belongs to Saturday's window only if sat has 23:30
		"2026-09-19T00:15:00Z":      true,  // Saturday 00:15 continues Friday's 23:30 window
		"2026-09-14T12:00:00+02:00": false,
	}
	for s, want := range cases {
		if got := InWindow(ws, at(s)); got != want {
			t.Errorf("InWindow(%s) = %v, want %v", s, got, want)
		}
	}
	if !InWindow(nil, time.Now()) {
		t.Error("no windows must mean always")
	}
	all, err := ParseWindows("* 00:00-24:00")
	if err != nil || !InWindow(all, at("2026-09-16T13:00:00Z")) {
		t.Errorf("* window: %v", err)
	}
	for _, bad := range []string{"funday 01:00-02:00", "mon 1-2", "mon", "mon 25:00-26:00", "mon-xyz 01:00-02:00"} {
		if _, err := ParseWindows(bad); err == nil {
			t.Errorf("ParseWindows(%q) accepted", bad)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c, err := LoadConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeNotify || c.Channel != "stable" || c.Services[0] != "openlog" || c.BackupKeep != 5 || c.VersionURL != "http://openlog:9464/readyz" {
		t.Errorf("defaults: %+v", c)
	}
	env["OPENLOG_UPDATER_MODE"] = "yolo"
	env["OPENLOG_UPDATER_BACKUP_KEEP"] = "0"
	env["OPENLOG_UPDATER_MAINTENANCE_WINDOW"] = "someday"
	if _, err := LoadConfig(get); err == nil {
		t.Error("invalid config accepted")
	}
}
