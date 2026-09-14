package alert

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Holiday calendars (alerting.md §5.2): named date sets of an organization that recurring mutes reference as
// exceptions. Dates are local calendar dates in the mute's time zone: YYYY-MM-DD, or MM-DD for every year.

const (
	maxCalendarDates = 1000
)

// HolidayCalendarInput is the API representation of a calendar write.
type HolidayCalendarInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Dates       []string `json:"dates"`
}

// HolidayCalendar is a stored calendar.
type HolidayCalendar struct {
	ID             string
	OrgID          string
	Name           string
	Description    string
	Dates          []string
	MuteCount      int // mutes referencing the calendar
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ValidHolidayCalendar is a validated calendar write.
type ValidHolidayCalendar struct {
	Name        string
	Description string
	Dates       []string
}

// normalizeCalendarDate accepts YYYY-MM-DD, YYYYMMDD, MM-DD or --MM-DD and returns YYYY-MM-DD or MM-DD.
func normalizeCalendarDate(v string) (string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "--")
	if len(v) == 5 && v[2] == '-' {
		// Validated against a leap year so that 02-29 is accepted.
		t, err := time.Parse(time.DateOnly, "2000-"+v)
		if err != nil {
			return "", fmt.Errorf("must be YYYY-MM-DD or MM-DD")
		}
		return t.Format("01-02"), nil
	}
	d, err := parseLocalDate(v)
	if err != nil {
		return "", fmt.Errorf("must be YYYY-MM-DD or MM-DD")
	}
	return d, nil
}

// Validate checks a calendar write.
func (in HolidayCalendarInput) Validate() (*ValidHolidayCalendar, error) {
	c := &ValidHolidayCalendar{Name: strings.TrimSpace(in.Name), Description: in.Description, Dates: []string{}}
	if n := len([]rune(c.Name)); n < 1 || n > 200 {
		return nil, invalid("name", "must be 1-200 characters")
	}
	if len([]rune(c.Description)) > 2000 {
		return nil, invalid("description", "must be at most 2000 characters")
	}
	if len(in.Dates) > maxCalendarDates {
		return nil, invalid("dates", "at most %d dates", maxCalendarDates)
	}
	seen := map[string]bool{}
	for i, v := range in.Dates {
		d, err := normalizeCalendarDate(v)
		if err != nil {
			return nil, invalid(fmt.Sprintf("dates[%d]", i), "%v", err)
		}
		if !seen[d] {
			seen[d] = true
			c.Dates = append(c.Dates, d)
		}
	}
	sort.Strings(c.Dates)
	return c, nil
}

// holidayDates merges the dates of the calendars a schedule references (unknown ids are ignored).
func holidayDates(ids []string, calendars map[string][]string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, calendars[id]...)
	}
	return out
}

// AttachHolidays sets the holiday dates of every recurring mute from calendars (id → dates).
func AttachHolidays(mutes []Mute, calendars map[string][]string) {
	for i := range mutes {
		if sc := mutes[i].Schedule; sc != nil && len(sc.HolidayCalendarIDs) > 0 {
			sc.SetHolidays(holidayDates(sc.HolidayCalendarIDs, calendars))
		}
	}
}

// ApplyHolidays sets the holiday dates of a validated recurring mute and recomputes its first occurrence.
func (v *ValidMute) ApplyHolidays(calendars map[string][]string, now time.Time) error {
	if v.Schedule == nil {
		return nil
	}
	for i, id := range v.Schedule.HolidayCalendarIDs {
		if _, ok := calendars[id]; !ok {
			return invalid(fmt.Sprintf("schedule.holiday_calendar_ids[%d]", i), "no such holiday calendar")
		}
	}
	v.Schedule.SetHolidays(holidayDates(v.Schedule.HolidayCalendarIDs, calendars))
	var ok bool
	if v.StartsAt, v.EndsAt, ok = v.Schedule.Window(now); !ok {
		return invalid("schedule", "the schedule has no occurrence after now")
	}
	return nil
}

// Occurrences returns up to n occurrences active at or after at (the current one first).
func (s *MuteSchedule) Occurrences(at time.Time, n int) [][2]time.Time {
	var out [][2]time.Time
	for len(out) < n {
		st, en, ok := s.Window(at)
		if !ok {
			break
		}
		out = append(out, [2]time.Time{st, en})
		at = en
	}
	return out
}
