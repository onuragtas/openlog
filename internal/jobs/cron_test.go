package jobs

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q): %v", expr, err)
	}
	return s
}

func TestCronNext(t *testing.T) {
	utc := time.UTC
	base := time.Date(2026, 3, 10, 12, 30, 0, 0, utc) // a Tuesday
	cases := []struct {
		expr string
		want string
	}{
		{"* * * * *", "2026-03-10T12:31:00Z"},
		{"*/15 * * * *", "2026-03-10T12:45:00Z"},
		{"0 * * * *", "2026-03-10T13:00:00Z"},
		{"@hourly", "2026-03-10T13:00:00Z"},
		{"30 2 * * *", "2026-03-11T02:30:00Z"},
		{"@daily", "2026-03-11T00:00:00Z"},
		{"0 0 * * 0", "2026-03-15T00:00:00Z"},   // the next Sunday
		{"0 0 * * sun", "2026-03-15T00:00:00Z"}, // names
		{"0 0 1 * *", "2026-04-01T00:00:00Z"},
		{"0 0 1 jan *", "2027-01-01T00:00:00Z"},
		{"5,35 9-17 * * mon-fri", "2026-03-10T12:35:00Z"},
		{"0 0 29 2 *", "2028-02-29T00:00:00Z"}, // a leap day, four years out
		// Both day fields restricted: either may match, so the 12th (a Thursday) is not needed — the 11th is.
		{"0 0 11 * thu", "2026-03-11T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			next, ok := mustParse(t, tc.expr).Next(base, utc)
			if !ok {
				t.Fatal("no occurrence found")
			}
			if got := next.UTC().Format(time.RFC3339); got != tc.want {
				t.Errorf("next = %s, want %s", got, tc.want)
			}
		})
	}
}

// The schedule is a wall-clock schedule in the monitor's own zone: a nightly job stays at 02:30 local time
// across a DST change instead of drifting by an hour.
func TestCronNextHonoursTheTimeZone(t *testing.T) {
	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	s := mustParse(t, "30 2 * * *")
	base := time.Date(2026, 6, 1, 3, 0, 0, 0, ist)
	next, ok := s.Next(base, ist)
	if !ok {
		t.Fatal("no occurrence found")
	}
	if h, m := next.Hour(), next.Minute(); h != 2 || m != 30 {
		t.Errorf("next = %s, want 02:30 local", next)
	}
	if next.Day() != 2 {
		t.Errorf("next = %s, want the following day", next)
	}
}

// A wall-clock time the DST jump skips must still produce a run, at the first real instant after it.
func TestCronNextAcrossASpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	// 2026-03-08 02:30 does not exist in New York; the clocks go from 02:00 to 03:00.
	s := mustParse(t, "30 2 * * *")
	base := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	next, ok := s.Next(base, loc)
	if !ok {
		t.Fatal("no occurrence found")
	}
	if next.Before(base) {
		t.Fatalf("next = %s, want it after %s", next, base)
	}
	if got := next.UTC().Format(time.RFC3339); got != "2026-03-08T07:30:00Z" {
		t.Errorf("next = %s (%s)", got, next)
	}
}

func TestCronParseErrors(t *testing.T) {
	cases := []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "0 0 32 * *", "0 0 * 13 *",
		"0 0 * * 8", "@never", "a * * * *", "*/0 * * * *", "5-1 * * * *"}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := ParseCron(expr); err == nil {
				t.Fatalf("ParseCron(%q) accepted an invalid expression", expr)
			}
		})
	}
}

func TestCronAcceptsQuestionMark(t *testing.T) {
	s := mustParse(t, "0 3 ? * mon")
	next, ok := s.Next(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), time.UTC)
	if !ok || next.Weekday() != time.Monday || next.Hour() != 3 {
		t.Fatalf("next = %s, ok = %v", next, ok)
	}
}

// An expression that never matches must be reported rather than searched for forever.
func TestCronWithoutAnOccurrence(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *") // there is no 30 February
	if _, ok := s.Next(time.Now(), time.UTC); ok {
		t.Fatal("30 February must have no occurrence")
	}
}
