package jobs

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A five-field cron expression (minute hour day-of-month month day-of-week), which is what a crontab line
// holds and therefore what a person copies into openlog when they register the job it runs. Parsed here
// rather than taken from a dependency: the grammar is small and fully specified, and the agent and the
// server both avoid dependencies they can carry themselves (D-137).
//
// Supported: numbers, names (jan…dec, sun…sat), `*`, ranges `a-b`, steps `a-b/n` and `*/n`, lists `a,b`,
// and the macros @yearly/@annually, @monthly, @weekly, @daily/@midnight and @hourly. `?` is accepted as a
// synonym of `*` in the day fields, because Quartz-style crontabs use it. Seconds are not a field: a job
// that runs more often than once a minute is not what this monitors.

// Schedule is a parsed cron expression bound to nothing; Next takes the location.
type Schedule struct {
	minutes  []int // 0-59
	hours    []int // 0-23
	days     []int // 1-31
	months   []int // 1-12
	weekdays []int // 0-6, Sunday = 0
	// dayRestricted is the classic cron rule: when both day-of-month and day-of-week are restricted, a day
	// matches if *either* does. When only one is restricted, only that one decides.
	domRestricted, dowRestricted bool
}

// MaxCronLength bounds the stored expression.
const MaxCronLength = 200

var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var weekdayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron parses a five-field expression or a macro.
func ParseCron(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("required")
	}
	if len(expr) > MaxCronLength {
		return nil, errors.New("the expression is too long")
	}
	if strings.HasPrefix(expr, "@") {
		macro, ok := cronMacros[strings.ToLower(expr)]
		if !ok {
			return nil, fmt.Errorf("unknown macro %s (@yearly, @monthly, @weekly, @daily, @hourly)", expr)
		}
		expr = macro
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("must have 5 fields (minute hour day month weekday), got %d", len(fields))
	}
	s := &Schedule{}
	var err error
	if s.minutes, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.hours, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.days, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if s.months, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.weekdays, err = parseField(fields[4], 0, 7, weekdayNames); err != nil {
		return nil, fmt.Errorf("weekday: %w", err)
	}
	// 7 is Sunday in many crontabs; both spellings become 0.
	for i, d := range s.weekdays {
		if d == 7 {
			s.weekdays[i] = 0
		}
	}
	s.weekdays = dedup(s.weekdays)
	s.domRestricted = !isWildcard(fields[2])
	s.dowRestricted = !isWildcard(fields[4])
	return s, nil
}

func isWildcard(f string) bool { return f == "*" || f == "?" }

// parseField expands one field into the sorted list of values it matches.
func parseField(field string, min, max int, names map[string]int) ([]int, error) {
	if field == "?" {
		field = "*"
	}
	var out []int
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty entry")
		}
		step := 1
		if at := strings.Index(part, "/"); at >= 0 {
			n, err := strconv.Atoi(part[at+1:])
			if err != nil || n < 1 || n > max-min+1 {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			step = n
			part = part[:at]
			if part == "" || part == "*" {
				part = fmt.Sprintf("%d-%d", min, max)
			}
		}
		lo, hi := min, max
		if part != "*" {
			bounds := strings.SplitN(part, "-", 2)
			var err error
			if lo, err = fieldValue(bounds[0], names); err != nil {
				return nil, err
			}
			hi = lo
			if len(bounds) == 2 {
				if hi, err = fieldValue(bounds[1], names); err != nil {
					return nil, err
				}
			} else if step > 1 {
				hi = max // "5/10" means "from 5 to the end of the range, every 10"
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("%q is outside %d-%d", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			out = append(out, v)
		}
	}
	return dedup(out), nil
}

func fieldValue(s string, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return v, nil
}

func dedup(v []int) []int {
	sort.Ints(v)
	out := v[:0]
	for i, x := range v {
		if i == 0 || x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

// maxCronSearchDays bounds the search for the next occurrence. Four years covers every leap-year pattern;
// beyond it an expression such as "0 0 30 2 *" simply never matches, and the caller is told so.
const maxCronSearchDays = 4 * 366

// Next returns the first occurrence strictly after t in loc, and whether one exists within four years.
func (s *Schedule) Next(t time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	// Start at the next whole minute: an occurrence is a minute, and "after t" must not return t itself.
	cur := t.In(loc).Truncate(time.Minute).Add(time.Minute)
	for day := 0; day <= maxCronSearchDays; day++ {
		if !s.matchDay(cur) {
			cur = startOfNextDay(cur, loc)
			continue
		}
		for _, h := range s.hours {
			if h < cur.Hour() {
				continue
			}
			for _, m := range s.minutes {
				if h == cur.Hour() && m < cur.Minute() {
					continue
				}
				return atWallClock(cur, h, m, loc), true
			}
		}
		cur = startOfNextDay(cur, loc)
	}
	return time.Time{}, false
}

// atWallClock returns the instant of h:m on the day of cur. A wall-clock minute that does not exist — the
// hour a spring-forward DST jump skips — is shifted by the size of the jump, so the job is expected an hour
// late instead of at a moment the clock never showed, which is what cron implementations do with it.
func atWallClock(cur time.Time, h, m int, loc *time.Location) time.Time {
	t := time.Date(cur.Year(), cur.Month(), cur.Day(), h, m, 0, 0, loc)
	if got := t.Hour()*60 + t.Minute(); got != h*60+m {
		t = t.Add(time.Duration(h*60+m-got) * time.Minute)
	}
	return t
}

func startOfNextDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

func (s *Schedule) matchDay(t time.Time) bool {
	if !contains(s.months, int(t.Month())) {
		return false
	}
	dom := contains(s.days, t.Day())
	dow := contains(s.weekdays, int(t.Weekday()))
	switch {
	case s.domRestricted && s.dowRestricted:
		return dom || dow // the classic cron rule: either restriction may match
	case s.domRestricted:
		return dom
	case s.dowRestricted:
		return dow
	}
	return true
}

func contains(list []int, v int) bool {
	i := sort.SearchInts(list, v)
	return i < len(list) && list[i] == v
}
