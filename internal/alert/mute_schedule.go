package alert

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MuteScheduleInput is the API representation of a recurring mute schedule (alerting.md §5.2).
type MuteScheduleInput struct {
	Timezone  string   `json:"timezone"`
	Days      []string `json:"days"`
	RRule     string   `json:"rrule"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
	From      string   `json:"from"`
	Until     string   `json:"until"`
}

// MuteSchedule is a validated recurring schedule, stored as alert_mutes.schedule (jsonb).
type MuteSchedule struct {
	Timezone  string     `json:"timezone"`
	Days      []string   `json:"days"`            // mon … sun, week order
	RRule     string     `json:"rrule,omitempty"` // normalized, when given instead of days
	StartTime string     `json:"start_time"`      // HH:MM local
	EndTime   string     `json:"end_time"`        // HH:MM local; ≤ start_time = next day
	From      time.Time  `json:"from"`
	Until     *time.Time `json:"until,omitempty"`

	loc *time.Location
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

var rruleDays = map[string]string{"SU": "sun", "MO": "mon", "TU": "tue", "WE": "wed", "TH": "thu", "FR": "fri", "SA": "sat"}

func dayIndex(d string) int {
	for i, w := range weekdays {
		if w == d {
			return i
		}
	}
	return -1
}

func parseClock(field, v string) (int, error) {
	if len(v) != 5 || v[2] != ':' {
		return 0, invalid(field, "must be HH:MM")
	}
	h, err1 := strconv.Atoi(v[:2])
	m, err2 := strconv.Atoi(v[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, invalid(field, "must be HH:MM between 00:00 and 23:59")
	}
	return h*60 + m, nil
}

// ParseRRule accepts the subset FREQ=WEEKLY;BYDAY=MO,TU,… and FREQ=DAILY (every day), optionally prefixed with
// "RRULE:". It returns the days (mon … sun) and the normalized rule.
func ParseRRule(s string) ([]string, string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "RRULE:"))
	var freq string
	var byDay []string
	for _, part := range strings.Split(s, ";") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, "", fmt.Errorf("invalid part %q", part)
		}
		switch k {
		case "FREQ":
			freq = v
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				if _, ok := rruleDays[d]; !ok {
					return nil, "", fmt.Errorf("invalid BYDAY value %q (MO … SU without ordinals)", d)
				}
				byDay = append(byDay, d)
			}
		case "INTERVAL":
			if v != "1" {
				return nil, "", fmt.Errorf("only INTERVAL=1 is supported")
			}
		default:
			return nil, "", fmt.Errorf("unsupported part %s (supported: FREQ=WEEKLY;BYDAY=… or FREQ=DAILY)", k)
		}
	}
	var days []string
	switch freq {
	case "WEEKLY":
		if len(byDay) == 0 {
			return nil, "", fmt.Errorf("FREQ=WEEKLY requires BYDAY")
		}
		for _, d := range byDay {
			days = append(days, rruleDays[d])
		}
	case "DAILY":
		if len(byDay) > 0 {
			return nil, "", fmt.Errorf("FREQ=DAILY does not take BYDAY; use FREQ=WEEKLY")
		}
		days = append(days, weekdays...)
	default:
		return nil, "", fmt.Errorf("FREQ must be WEEKLY or DAILY")
	}
	days = normalizeDays(days)
	if freq == "DAILY" {
		return days, "FREQ=DAILY", nil
	}
	codes := make([]string, 0, len(days))
	for _, d := range days {
		for code, name := range rruleDays {
			if name == d {
				codes = append(codes, code)
			}
		}
	}
	return days, "FREQ=WEEKLY;BYDAY=" + strings.Join(codes, ","), nil
}

// normalizeDays deduplicates and orders days mon … sun.
func normalizeDays(days []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range days {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	order := func(d string) int { return (dayIndex(d) + 6) % 7 } // monday first
	sort.Slice(out, func(i, j int) bool { return order(out[i]) < order(out[j]) })
	return out
}

// Validate checks a schedule. now is the default of from.
func (in MuteScheduleInput) Validate(parseTime func(string) (time.Time, error), now time.Time) (*MuteSchedule, error) {
	s := &MuteSchedule{Timezone: strings.TrimSpace(in.Timezone), StartTime: in.StartTime, EndTime: in.EndTime}
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Timezone == "Local" {
		return nil, invalid("timezone", "unknown IANA time zone %q", s.Timezone)
	}
	s.loc = loc
	switch {
	case len(in.Days) > 0 && strings.TrimSpace(in.RRule) != "":
		return nil, invalid("days", "give days or rrule, not both")
	case strings.TrimSpace(in.RRule) != "":
		if s.Days, s.RRule, err = ParseRRule(in.RRule); err != nil {
			return nil, invalid("rrule", "%v", err)
		}
	case len(in.Days) > 0:
		for i, d := range in.Days {
			d = strings.ToLower(strings.TrimSpace(d))
			if dayIndex(d) < 0 {
				return nil, invalid(fmt.Sprintf("days[%d]", i), "must be mon, tue, wed, thu, fri, sat or sun")
			}
			s.Days = append(s.Days, d)
		}
		s.Days = normalizeDays(s.Days)
	default:
		return nil, invalid("days", "required (or rrule)")
	}
	start, err := parseClock("start_time", in.StartTime)
	if err != nil {
		return nil, err
	}
	end, err := parseClock("end_time", in.EndTime)
	if err != nil {
		return nil, err
	}
	if start == end {
		return nil, invalid("end_time", "must differ from start_time")
	}
	s.From = now.UTC().Truncate(time.Second)
	if strings.TrimSpace(in.From) != "" {
		if s.From, err = parseTime(in.From); err != nil {
			return nil, invalid("from", "%v", err)
		}
		s.From = s.From.UTC()
	}
	if strings.TrimSpace(in.Until) != "" {
		u, err := parseTime(in.Until)
		if err != nil {
			return nil, invalid("until", "%v", err)
		}
		u = u.UTC()
		if !u.After(s.From) {
			return nil, invalid("until", "must be after from")
		}
		s.Until = &u
	}
	return s, nil
}

func (s *MuteSchedule) location() *time.Location {
	if s.loc == nil {
		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			loc = time.UTC
		}
		s.loc = loc
	}
	return s.loc
}

func (s *MuteSchedule) hasDay(wd time.Weekday) bool {
	for _, d := range s.Days {
		if dayIndex(d) == int(wd) {
			return true
		}
	}
	return false
}

// occurrenceOn returns the occurrence starting on the local date y-m-d. Local times are converted with
// time.Date: a time inside a DST gap moves forward by the gap (02:30 → 03:30), an ambiguous time (clocks set
// back) is the later instant. ok is false when the conversion leaves no time between start and end.
func (s *MuteSchedule) occurrenceOn(y int, m time.Month, d int) (time.Time, time.Time, bool) {
	loc := s.location()
	sm, _ := parseClock("", s.StartTime)
	em, _ := parseClock("", s.EndTime)
	start := time.Date(y, m, d, sm/60, sm%60, 0, 0, loc)
	endDay := d
	if em <= sm {
		endDay++
	}
	end := time.Date(y, m, endDay, em/60, em%60, 0, 0, loc)
	return start.UTC(), end.UTC(), end.After(start)
}

// Window returns the occurrence active at at, else the next one, clipped to [from, until). ok is false when the
// schedule has no occurrence ending after at.
func (s *MuteSchedule) Window(at time.Time) (time.Time, time.Time, bool) {
	loc := s.location()
	ref := at
	if s.From.After(ref) {
		ref = s.From
	}
	local := ref.In(loc)
	// Start one day early (an overnight occurrence that began yesterday); 9 days cover every weekday.
	for i := -1; i < 8; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+i, 12, 0, 0, 0, loc)
		if !s.hasDay(day.Weekday()) {
			continue
		}
		start, end, ok := s.occurrenceOn(day.Year(), day.Month(), day.Day())
		if !ok || !end.After(s.From) {
			continue
		}
		if start.Before(s.From) {
			start = s.From
		}
		if s.Until != nil {
			if !start.Before(*s.Until) {
				return time.Time{}, time.Time{}, false
			}
			if end.After(*s.Until) {
				end = *s.Until
			}
		}
		if end.After(at) {
			return start, end, true
		}
	}
	return time.Time{}, time.Time{}, false
}
