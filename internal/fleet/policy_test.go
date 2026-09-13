package fleet

import (
	"strings"
	"testing"
	"time"
)

func sp(s string) *string { return &s }

func TestPolicyNormalize(t *testing.T) {
	ok := DefaultPolicy()
	if _, err := ok.Normalize(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
	cases := []struct {
		name string
		mod  func(p *Policy)
		want string
	}{
		{"mode", func(p *Policy) { p.Mode = "sometimes" }, "mode"},
		{"channel", func(p *Policy) { p.Channel = "nightly" }, "channel"},
		{"target", func(p *Policy) { p.Target = "newest" }, "target"},
		{"pinned without version", func(p *Policy) { p.Target = TargetPinned }, "pinned_version is required"},
		{"pinned bad version", func(p *Policy) { p.Target, p.PinnedVersion = TargetPinned, sp("1.2") }, "pinned_version"},
		{"waves empty", func(p *Policy) { p.Waves = nil }, "waves"},
		{"waves not increasing", func(p *Policy) { p.Waves = []int{10, 10, 100} }, "strictly increasing"},
		{"waves last not 100", func(p *Policy) { p.Waves = []int{10, 50} }, "last wave"},
		{"waves zero", func(p *Policy) { p.Waves = []int{0, 100} }, "strictly increasing"},
		{"waves over 100", func(p *Policy) { p.Waves = []int{50, 101} }, "strictly increasing"},
		{"soak negative", func(p *Policy) { p.WaveSoakMinutes = -1 }, "wave_soak_minutes"},
		{"rate", func(p *Policy) { p.HaltFailureRate = 1.5 }, "halt_failure_rate"},
		{"window day", func(p *Policy) {
			p.MaintenanceWindows = []Window{{Days: []string{"funday"}, Start: "01:00", End: "02:00"}}
		}, "unknown day"},
		{"window clock", func(p *Policy) { p.MaintenanceWindows = []Window{{Start: "1:00", End: "02:00"}} }, "HH:MM"},
		{"window equal", func(p *Policy) { p.MaintenanceWindows = []Window{{Start: "02:00", End: "02:00"}} }, "differ"},
		{"window 24 start", func(p *Policy) { p.MaintenanceWindows = []Window{{Start: "24:00", End: "02:00"}} }, "invalid time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultPolicy()
			tc.mod(&p)
			_, err := p.Normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	p := DefaultPolicy()
	p.Target, p.PinnedVersion = TargetPinned, sp("v0.4.0")
	p.MaintenanceWindows = []Window{{Days: []string{"MON", "mon", "Tue"}, Start: "22:00", End: "24:00"}}
	n, err := p.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if *n.PinnedVersion != "0.4.0" || strings.Join(n.MaintenanceWindows[0].Days, ",") != "mon,tue" {
		t.Errorf("normalized = %+v", n)
	}
	p = DefaultPolicy()
	p.PinnedVersion = sp("0.4.0")
	if n, _ := p.Normalize(); n.PinnedVersion != nil {
		t.Error("pinned_version kept for target=latest")
	}
}

func TestInWindow(t *testing.T) {
	// 2026-09-14 is a Monday.
	at := func(day int, hh, mm int) time.Time { return time.Date(2026, 9, day, hh, mm, 0, 0, time.UTC) }
	win := func(ws ...Window) Policy { p := DefaultPolicy(); p.MaintenanceWindows = ws; return p }
	cases := []struct {
		name    string
		p       Policy
		now     time.Time
		open    bool
		wantEnd time.Time
	}{
		{"no windows", win(), at(14, 12, 0), true, time.Time{}},
		{"inside", win(Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}), at(14, 3, 0), true, at(14, 5, 0)},
		{"at start", win(Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}), at(14, 2, 0), true, at(14, 5, 0)},
		{"at end", win(Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}), at(14, 5, 0), false, time.Time{}},
		{"wrong day", win(Window{Days: []string{"tue"}, Start: "02:00", End: "05:00"}), at(14, 3, 0), false, time.Time{}},
		{"every day", win(Window{Start: "02:00", End: "05:00"}), at(16, 4, 59), true, at(16, 5, 0)},
		{"overnight before midnight", win(Window{Days: []string{"sun"}, Start: "23:00", End: "02:00"}), at(13, 23, 30), true, at(14, 2, 0)},
		{"overnight after midnight", win(Window{Days: []string{"sun"}, Start: "23:00", End: "02:00"}), at(14, 1, 0), true, at(14, 2, 0)},
		{"overnight belongs to start day", win(Window{Days: []string{"sun"}, Start: "23:00", End: "02:00"}), at(14, 23, 30), false, time.Time{}},
		{"end 24:00", win(Window{Days: []string{"mon"}, Start: "22:00", End: "24:00"}), at(14, 23, 59), true, at(15, 0, 0)},
		{"latest end of overlapping", win(Window{Start: "01:00", End: "03:00"}, Window{Start: "02:00", End: "06:00"}), at(14, 2, 30), true, at(14, 6, 0)},
		{"non-UTC input", win(Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}), at(14, 3, 0).In(time.FixedZone("x", 3*3600)), true, at(14, 5, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, end := tc.p.InWindow(tc.now)
			if open != tc.open || !end.Equal(tc.wantEnd) {
				t.Fatalf("InWindow = %v, %v; want %v, %v", open, end, tc.open, tc.wantEnd)
			}
		})
	}
}
