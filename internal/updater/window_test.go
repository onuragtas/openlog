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

func TestFormatWindows(t *testing.T) {
	for in, want := range map[string]string{
		"": "",
		"sun,sat 02:00-05:00; fri-mon 23:30-00:30": "sat,sun 02:00-05:00; fri-mon 23:30-00:30",
		"mon,tue,wed,thu,fri 03:00-04:00":          "mon-fri 03:00-04:00",
		"sun,mon 01:00-02:00":                      "sun,mon 01:00-02:00",
		"mon,wed,sat,sun 01:00-24:00":              "wed,sat-mon 01:00-24:00",
		"mon-sun 00:00-24:00; sun-sat 01:00-02:00": "* 00:00-24:00; * 01:00-02:00",
		"tue 05:00-05:00":                          "tue 05:00-05:00",
	} {
		ws, err := ParseWindows(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatWindows(ws); got != want {
			t.Errorf("FormatWindows(%q) = %q, want %q", in, got, want)
		}
		again, err := ParseWindows(FormatWindows(ws))
		if err != nil || len(again) != len(ws) {
			t.Fatalf("round trip of %q: %v", in, err)
		}
		for i := range ws {
			if again[i] != ws[i] {
				t.Errorf("round trip of %q: %+v != %+v", in, again[i], ws[i])
			}
		}
	}
}

func TestNextOpening(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		spec, now, want string // want "" = none
		open            bool
	}{
		{"", "2026-09-15T10:00:00Z", "", true},
		{"* 00:00-24:00", "2026-09-15T10:00:00Z", "", true},
		// 2026-09-15 is a Tuesday.
		{"sat,sun 02:00-05:00; mon-fri 03:00-04:00", "2026-09-15T10:00:00Z", "2026-09-16T03:00:00Z", false},
		{"sat,sun 02:00-05:00; mon-fri 03:00-04:00", "2026-09-15T02:59:59Z", "2026-09-15T03:00:00Z", false},
		{"sat,sun 02:00-05:00; mon-fri 03:00-04:00", "2026-09-15T03:00:00Z", "2026-09-16T03:00:00Z", true},  // open: next one
		{"sat,sun 02:00-05:00; mon-fri 03:00-04:00", "2026-09-18T12:00:00Z", "2026-09-19T02:00:00Z", false}, // Fri → Sat
		{"sat,sun 02:00-05:00", "2026-09-20T06:00:00Z", "2026-09-26T02:00:00Z", false},                      // Sun → next Sat
		{"tue 02:00-03:00", "2026-09-15T02:30:00Z", "2026-09-22T02:00:00Z", true},                           // a week later
		{"tue 02:00-03:00", "2026-09-15T03:00:00Z", "2026-09-22T02:00:00Z", false},
		// Day wrap: Friday 23:30 to Saturday 00:30.
		{"fri 23:30-00:30", "2026-09-19T00:15:00Z", "2026-09-25T23:30:00Z", true},
		{"fri 23:30-00:30", "2026-09-18T23:00:00Z", "2026-09-18T23:30:00Z", false},
		// Overlapping ranges open once: 01:00 (the 02:00 start is inside the open 01:00-03:00 window).
		{"* 01:00-03:00; * 02:00-04:00", "2026-09-15T00:30:00Z", "2026-09-15T01:00:00Z", false},
		{"* 01:00-03:00; * 02:00-04:00", "2026-09-15T01:30:00Z", "2026-09-16T01:00:00Z", true},
		// Contiguous ranges across midnight do not reopen at 00:00.
		{"* 22:00-24:00; * 00:00-02:00", "2026-09-15T23:00:00Z", "2026-09-16T22:00:00Z", true},
		// UTC math across a European DST change (2026-10-25): no one-hour shift.
		{"sun 02:00-03:00", "2026-10-24T12:00:00+02:00", "2026-10-25T02:00:00Z", false},
		{"sun 02:00-03:00", "2026-10-25T03:30:00+01:00", "2026-11-01T02:00:00Z", true},
	}
	for _, c := range cases {
		ws, err := ParseWindows(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		st := NewWindowStatus(ws, at(c.now))
		got := ""
		if st.NextOpenAt != nil {
			got = st.NextOpenAt.Format(time.RFC3339)
		}
		if got != c.want || st.OpenNow != c.open {
			t.Errorf("%q at %s: next %q open %v, want %q %v", c.spec, c.now, got, st.OpenNow, c.want, c.open)
		}
		if st.NextOpenAt != nil && st.NextOpenAt.Location() != time.UTC {
			t.Errorf("%q: next opening not UTC", c.spec)
		}
	}
	ws, _ := ParseWindows("sat,sun 02:00-05:00; * 23:00-01:00")
	st := NewWindowStatus(ws, at("2026-09-15T10:00:00Z"))
	if st.Spec != "sat,sun 02:00-05:00; * 23:00-01:00" || len(st.Windows) != 2 || len(st.Windows[1].Days) != 0 ||
		st.Windows[0].Days[0] != "sat" || st.Windows[0].Days[1] != "sun" || st.Windows[1].Start != "23:00" || st.Windows[1].End != "01:00" {
		t.Errorf("window status %+v", st)
	}
	if empty := NewWindowStatus(nil, time.Now()); empty.Spec != "" || !empty.OpenNow || empty.NextOpenAt != nil || empty.Windows == nil {
		t.Errorf("empty window status %+v", empty)
	}
}
