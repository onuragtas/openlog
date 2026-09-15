package updater

import (
	"fmt"
	"strings"
	"time"
)

// Window is a weekly maintenance window in UTC. End <= Start means the window crosses midnight
// (it belongs to the day it starts on).
type Window struct {
	Days       [7]bool // indexed by time.Weekday
	Start, End int     // minutes since midnight
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseWindows parses "sat,sun 02:00-05:00; mon-fri 03:00-03:30; * 01:00-02:00". Empty = none
// (updates may run at any time).
func ParseWindows(s string) ([]Window, error) {
	var out []Window
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%q: want \"<days> HH:MM-HH:MM\"", part)
		}
		var w Window
		for _, d := range strings.Split(strings.ToLower(fields[0]), ",") {
			switch {
			case d == "*":
				for i := range w.Days {
					w.Days[i] = true
				}
			case strings.Contains(d, "-"):
				a, b, _ := strings.Cut(d, "-")
				from, ok1 := dayNames[a]
				to, ok2 := dayNames[b]
				if !ok1 || !ok2 {
					return nil, fmt.Errorf("%q: unknown day range %q", part, d)
				}
				for i := from; ; i = (i + 1) % 7 {
					w.Days[i] = true
					if i == to {
						break
					}
				}
			default:
				wd, ok := dayNames[d]
				if !ok {
					return nil, fmt.Errorf("%q: unknown day %q (mon..sun or *)", part, d)
				}
				w.Days[wd] = true
			}
		}
		a, b, ok := strings.Cut(fields[1], "-")
		if !ok {
			return nil, fmt.Errorf("%q: want HH:MM-HH:MM", part)
		}
		var err error
		if w.Start, err = parseClock(a); err != nil {
			return nil, fmt.Errorf("%q: %w", part, err)
		}
		if w.End, err = parseClock(b); err != nil {
			return nil, fmt.Errorf("%q: %w", part, err)
		}
		out = append(out, w)
	}
	return out, nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		if s == "24:00" {
			return 24 * 60, nil
		}
		return 0, fmt.Errorf("invalid time %q (HH:MM, UTC)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// InWindow reports whether t is inside any window; no windows means always.
func InWindow(ws []Window, t time.Time) bool {
	if len(ws) == 0 {
		return true
	}
	t = t.UTC()
	m := t.Hour()*60 + t.Minute()
	today, yesterday := t.Weekday(), (t.Weekday()+6)%7
	for _, w := range ws {
		if w.End > w.Start {
			if w.Days[today] && m >= w.Start && m < w.End {
				return true
			}
			continue
		}
		if (w.Days[today] && m >= w.Start) || (w.Days[yesterday] && m < w.End) {
			return true
		}
	}
	return false
}

var dayOrder = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} // by time.Weekday

// FormatWindows renders windows in the normalized OPENLOG_UPDATER_MAINTENANCE_WINDOW syntax ("sat,sun 02:00-05:00;
// mon-fri 03:00-04:00"; "*" for every day; "" = any time). ParseWindows(FormatWindows(ws)) equals ws.
func FormatWindows(ws []Window) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, formatDays(w.Days)+" "+formatClock(w.Start)+"-"+formatClock(w.End))
	}
	return strings.Join(parts, "; ")
}

// WindowDays lists the days of w in the order of FormatWindows (nil = every day).
func WindowDays(w Window) []string {
	var out []string
	for _, run := range dayRuns(w.Days) {
		for i := 0; i < run[1]; i++ {
			out = append(out, dayOrder[(run[0]+i)%7])
		}
	}
	if len(out) == 7 {
		return nil
	}
	return out
}

// dayRuns returns runs of consecutive selected days as {first weekday, length}, starting after an unselected day
// (Monday-first when possible), so "sat,sun,mon" stays one run.
func dayRuns(days [7]bool) [][2]int {
	first := -1
	for i := 0; i < 7; i++ {
		if d := (int(time.Monday) + 6 + i) % 7; !days[d] { // sun, mon, …: the day before Monday first
			first = (d + 1) % 7
			break
		}
	}
	if first < 0 {
		return [][2]int{{int(time.Monday), 7}}
	}
	var runs [][2]int
	for i := 0; i < 7; i++ {
		d := (first + i) % 7
		if !days[d] {
			continue
		}
		if n := len(runs); n > 0 && (runs[n-1][0]+runs[n-1][1])%7 == d {
			runs[n-1][1]++
			continue
		}
		runs = append(runs, [2]int{d, 1})
	}
	return runs
}

func formatDays(days [7]bool) string {
	runs := dayRuns(days)
	if len(runs) == 1 && runs[0][1] == 7 {
		return "*"
	}
	var parts []string
	for _, r := range runs {
		switch r[1] {
		case 1:
			parts = append(parts, dayOrder[r[0]])
		case 2:
			parts = append(parts, dayOrder[r[0]], dayOrder[(r[0]+1)%7])
		default:
			parts = append(parts, dayOrder[r[0]]+"-"+dayOrder[(r[0]+r[1]-1)%7])
		}
	}
	return strings.Join(parts, ",")
}

func formatClock(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// NextOpening returns the next time after t (UTC) at which the windows open, i.e. a window start that is not
// already covered by an open window the minute before. ok is false without windows (any time) and when the windows
// never close (e.g. "* 00:00-24:00"). All arithmetic is in UTC, so there are no daylight saving jumps.
func NextOpening(ws []Window, t time.Time) (time.Time, bool) {
	if len(ws) == 0 {
		return time.Time{}, false
	}
	t = t.UTC()
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	var best time.Time
	for day := 0; day <= 7; day++ {
		base := midnight.AddDate(0, 0, day)
		for _, w := range ws {
			if !w.Days[base.Weekday()] {
				continue
			}
			s := base.Add(time.Duration(w.Start) * time.Minute)
			if !s.After(t) || (!best.IsZero() && !s.Before(best)) || InWindow(ws, s.Add(-time.Minute)) {
				continue
			}
			best = s
		}
		if !best.IsZero() {
			return best, true
		}
	}
	return time.Time{}, false
}

// WindowStatus is Status.MaintenanceWindow: OPENLOG_UPDATER_MAINTENANCE_WINDOW as evaluated at a point in time.
type WindowStatus struct {
	// Spec is the normalized window ("" = updates may run at any time).
	Spec    string       `json:"spec"`
	Windows []WindowJSON `json:"windows"`
	OpenNow bool         `json:"open_now"`
	// NextOpenAt is the next opening (absent without windows and when the windows never close).
	NextOpenAt *time.Time `json:"next_open_at,omitempty"`
}

// WindowJSON is one window of WindowStatus (days empty = every day; times HH:MM UTC).
type WindowJSON struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// NewWindowStatus evaluates ws at now.
func NewWindowStatus(ws []Window, now time.Time) *WindowStatus {
	out := &WindowStatus{Spec: FormatWindows(ws), Windows: []WindowJSON{}, OpenNow: InWindow(ws, now)}
	for _, w := range ws {
		days := WindowDays(w)
		if days == nil {
			days = []string{}
		}
		out.Windows = append(out.Windows, WindowJSON{Days: days, Start: formatClock(w.Start), End: formatClock(w.End)})
	}
	if next, ok := NextOpening(ws, now); ok {
		next = next.UTC()
		out.NextOpenAt = &next
	}
	return out
}
