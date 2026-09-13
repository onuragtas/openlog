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
