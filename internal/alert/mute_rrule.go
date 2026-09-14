package alert

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Recurrence subset of RFC 5545 for mute schedules (alerting.md §5.2).
const (
	freqDaily   = "DAILY"
	freqWeekly  = "WEEKLY"
	freqMonthly = "MONTHLY"

	// maxScanDays bounds the search for the next occurrence (monthly rules with exceptions may skip months).
	maxScanDays = 800
	// maxExDates and maxMuteCalendars bound the exceptions of one schedule.
	maxExDates       = 366
	maxMuteCalendars = 10
)

// byDayEntry is one BYDAY value: a weekday (0 = sunday) with an optional ordinal (1..5, -1..-5; 0 = every).
type byDayEntry struct {
	ord int
	wd  int
}

// recurrence is a parsed RRULE.
type recurrence struct {
	freq       string
	days       []string // weekly/daily: mon … sun; monthly: empty
	byDay      []byDayEntry
	byMonthDay []int
	bySetPos   []int
	normalized string
}

var rruleCodes = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func weekdayCode(code string) int {
	for i, c := range rruleCodes {
		if c == code {
			return i
		}
	}
	return -1
}

// parseIntList parses comma-separated non-zero integers within ±max.
func parseIntList(part, v string, max int) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, x := range strings.Split(v, ",") {
		n, err := strconv.Atoi(strings.TrimPrefix(x, "+"))
		if err != nil || n == 0 || n < -max || n > max {
			return nil, fmt.Errorf("invalid %s value %q (1..%d or -1..-%d)", part, x, max, max)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

// parseRecurrence accepts FREQ=DAILY, FREQ=WEEKLY;BYDAY=MO,… (no ordinals) and FREQ=MONTHLY with BYMONTHDAY
// (1..31, -1 = last day), BYDAY (weekdays, optionally with an ordinal: 1MO = first Monday, -1FR = last Friday)
// and BYSETPOS (1..31, -1..-31: positions in the month's set of matching days). INTERVAL=1 and an "RRULE:" prefix
// are allowed; other parts (COUNT, UNTIL, BYHOUR, …) are rejected.
func parseRecurrence(s string) (*recurrence, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "RRULE:"))
	r := &recurrence{}
	var rawByDay []string
	seenParts := map[string]bool{}
	for _, part := range strings.Split(s, ";") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || v == "" {
			return nil, fmt.Errorf("invalid part %q", part)
		}
		if seenParts[k] {
			return nil, fmt.Errorf("duplicate part %s", k)
		}
		seenParts[k] = true
		var err error
		switch k {
		case "FREQ":
			r.freq = v
		case "BYDAY":
			rawByDay = strings.Split(v, ",")
		case "BYMONTHDAY":
			if r.byMonthDay, err = parseIntList(k, v, 31); err != nil {
				return nil, err
			}
		case "BYSETPOS":
			if r.bySetPos, err = parseIntList(k, v, 31); err != nil {
				return nil, err
			}
		case "INTERVAL":
			if v != "1" {
				return nil, fmt.Errorf("only INTERVAL=1 is supported")
			}
		default:
			return nil, fmt.Errorf("unsupported part %s (supported: FREQ=DAILY, FREQ=WEEKLY;BYDAY=…, FREQ=MONTHLY;BYMONTHDAY=…|BYDAY=…;BYSETPOS=…)", k)
		}
	}
	seenDay := map[byDayEntry]bool{}
	for _, d := range rawByDay {
		code := d
		ord := 0
		if len(d) > 2 {
			n, err := strconv.Atoi(strings.TrimPrefix(d[:len(d)-2], "+"))
			if err != nil || n == 0 || n < -5 || n > 5 {
				return nil, fmt.Errorf("invalid BYDAY value %q", d)
			}
			ord, code = n, d[len(d)-2:]
		}
		wd := weekdayCode(code)
		if wd < 0 {
			return nil, fmt.Errorf("invalid BYDAY value %q", d)
		}
		e := byDayEntry{ord: ord, wd: wd}
		if !seenDay[e] {
			seenDay[e] = true
			r.byDay = append(r.byDay, e)
		}
	}
	// Monday first, then ordinal.
	sort.Slice(r.byDay, func(i, j int) bool {
		a, b := (r.byDay[i].wd+6)%7, (r.byDay[j].wd+6)%7
		if a != b {
			return a < b
		}
		return r.byDay[i].ord < r.byDay[j].ord
	})
	switch r.freq {
	case freqDaily:
		if len(r.byDay) > 0 || len(r.byMonthDay) > 0 || len(r.bySetPos) > 0 {
			return nil, fmt.Errorf("FREQ=DAILY takes no BY… parts; use FREQ=WEEKLY or FREQ=MONTHLY")
		}
		r.days = append([]string(nil), weekdays...)
		r.days = normalizeDays(r.days)
		r.normalized = "FREQ=DAILY"
	case freqWeekly:
		if len(r.byDay) == 0 {
			return nil, fmt.Errorf("FREQ=WEEKLY requires BYDAY")
		}
		if len(r.byMonthDay) > 0 || len(r.bySetPos) > 0 {
			return nil, fmt.Errorf("FREQ=WEEKLY takes only BYDAY")
		}
		codes := make([]string, 0, len(r.byDay))
		for _, e := range r.byDay {
			if e.ord != 0 {
				return nil, fmt.Errorf("BYDAY ordinals (%d%s) need FREQ=MONTHLY", e.ord, rruleCodes[e.wd])
			}
			r.days = append(r.days, weekdays[e.wd])
			codes = append(codes, rruleCodes[e.wd])
		}
		r.days = normalizeDays(r.days)
		r.normalized = "FREQ=WEEKLY;BYDAY=" + strings.Join(codes, ",")
	case freqMonthly:
		if len(r.byDay) == 0 && len(r.byMonthDay) == 0 {
			return nil, fmt.Errorf("FREQ=MONTHLY requires BYMONTHDAY or BYDAY")
		}
		r.days = []string{}
		parts := []string{"FREQ=MONTHLY"}
		if len(r.byDay) > 0 {
			codes := make([]string, 0, len(r.byDay))
			for _, e := range r.byDay {
				c := rruleCodes[e.wd]
				if e.ord != 0 {
					c = strconv.Itoa(e.ord) + c
				}
				codes = append(codes, c)
			}
			parts = append(parts, "BYDAY="+strings.Join(codes, ","))
		}
		if len(r.byMonthDay) > 0 {
			parts = append(parts, "BYMONTHDAY="+joinInts(r.byMonthDay))
		}
		if len(r.bySetPos) > 0 {
			parts = append(parts, "BYSETPOS="+joinInts(r.bySetPos))
		}
		r.normalized = strings.Join(parts, ";")
	case "":
		return nil, fmt.Errorf("FREQ is required (DAILY, WEEKLY or MONTHLY)")
	default:
		return nil, fmt.Errorf("FREQ must be DAILY, WEEKLY or MONTHLY")
	}
	return r, nil
}

// ParseRRule parses a schedule rule (see parseRecurrence) and returns the weekly days (empty for monthly rules)
// and the normalized rule.
func ParseRRule(s string) ([]string, string, error) {
	r, err := parseRecurrence(s)
	if err != nil {
		return nil, "", err
	}
	return r.days, r.normalized, nil
}

func daysInMonth(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// candidate reports whether day d of a month with dim days (first day on weekday wd1) passes BYMONTHDAY and BYDAY.
func (r *recurrence) candidate(d, dim, wd1 int) bool {
	if len(r.byMonthDay) > 0 {
		ok := false
		for _, md := range r.byMonthDay {
			if (md > 0 && d == md) || (md < 0 && d == dim+md+1) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.byDay) > 0 {
		wd := (wd1 + d - 1) % 7
		ok := false
		for _, e := range r.byDay {
			if e.wd != wd {
				continue
			}
			if e.ord == 0 || (e.ord > 0 && (d-1)/7+1 == e.ord) || (e.ord < 0 && -((dim-d)/7+1) == e.ord) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// matches reports whether a monthly rule selects the date y-m-d (RFC 5545: BYMONTHDAY and BYDAY restrict each
// other; BYSETPOS then picks positions in the month's resulting set).
func (r *recurrence) matches(y int, m time.Month, d int) bool {
	dim := daysInMonth(y, m)
	wd1 := int(time.Date(y, m, 1, 12, 0, 0, 0, time.UTC).Weekday())
	if len(r.bySetPos) == 0 {
		return d <= dim && r.candidate(d, dim, wd1)
	}
	var set []int
	for x := 1; x <= dim; x++ {
		if r.candidate(x, dim, wd1) {
			set = append(set, x)
		}
	}
	for _, p := range r.bySetPos {
		i := p - 1
		if p < 0 {
			i = len(set) + p
		}
		if i >= 0 && i < len(set) && set[i] == d {
			return true
		}
	}
	return false
}

// parseLocalDate accepts YYYY-MM-DD or the RFC 5545 DATE form YYYYMMDD and returns YYYY-MM-DD.
func parseLocalDate(v string) (string, error) {
	v = strings.TrimSpace(v)
	layout := time.DateOnly
	if len(v) == 8 {
		layout = "20060102"
	}
	t, err := time.Parse(layout, v)
	if err != nil {
		return "", fmt.Errorf("must be a date YYYY-MM-DD")
	}
	return t.Format(time.DateOnly), nil
}

// normalizeExDates validates, deduplicates and sorts exception dates.
func normalizeExDates(in []string) ([]string, error) {
	if len(in) > maxExDates {
		return nil, invalid("exdates", "at most %d dates", maxExDates)
	}
	seen := map[string]bool{}
	var out []string
	for i, v := range in {
		d, err := parseLocalDate(v)
		if err != nil {
			return nil, invalid(fmt.Sprintf("exdates[%d]", i), "%v", err)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, nil
}
