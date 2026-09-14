package alert

import (
	"strings"
	"testing"
	"time"
)

func TestParseRRuleMonthly(t *testing.T) {
	cases := []struct{ in, norm string }{
		{"FREQ=MONTHLY;BYMONTHDAY=15,1,-1,1", "FREQ=MONTHLY;BYMONTHDAY=-1,1,15"},
		{"rrule:freq=monthly;byday=fr;bysetpos=-1", "FREQ=MONTHLY;BYDAY=FR;BYSETPOS=-1"},
		{"FREQ=MONTHLY;BYDAY=-1FR,1MO,+2MO", "FREQ=MONTHLY;BYDAY=1MO,2MO,-1FR"},
		{"FREQ=MONTHLY;INTERVAL=1;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1"},
		{"FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13", "FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13"},
	}
	for _, c := range cases {
		days, norm, err := ParseRRule(c.in)
		if err != nil || norm != c.norm || days == nil || len(days) != 0 {
			t.Errorf("%s: %v %q %v", c.in, days, norm, err)
		}
	}
}

// dates returns the local start dates of the next n occurrences from at.
func occurrenceDates(t *testing.T, s *MuteSchedule, at time.Time, n int) string {
	t.Helper()
	var out []string
	for _, o := range s.Occurrences(at, n) {
		out = append(out, o[0].In(s.location()).Format(time.DateOnly))
	}
	return strings.Join(out, ",")
}

func TestMuteScheduleMonthly(t *testing.T) {
	// 2025-12-31 03:00 Istanbul: the occurrence of that day has just ended, 2026-01-01 01:00 is next.
	now := rfc("2025-12-31T00:00:00Z")
	sched := func(rrule string, ex ...string) *MuteSchedule {
		return mustSchedule(t, MuteScheduleInput{Timezone: "Europe/Istanbul", RRule: rrule, StartTime: "01:00", EndTime: "03:00", ExDates: ex}, now)
	}
	cases := []struct {
		name, rrule, want string
		ex                []string
	}{
		// Day 31 only exists in some months.
		{"monthday 31", "FREQ=MONTHLY;BYMONTHDAY=31", "2026-01-31,2026-03-31,2026-05-31,2026-07-31", nil},
		// Last day of the month, February of a common year.
		{"last day", "FREQ=MONTHLY;BYMONTHDAY=-1", "2026-01-31,2026-02-28,2026-03-31,2026-04-30", nil},
		{"first and fifteenth", "FREQ=MONTHLY;BYMONTHDAY=1,15", "2026-01-01,2026-01-15,2026-02-01,2026-02-15", nil},
		// Last Friday: 2026-01-30, 02-27, 03-27, 04-24.
		{"last friday setpos", "FREQ=MONTHLY;BYDAY=FR;BYSETPOS=-1", "2026-01-30,2026-02-27,2026-03-27,2026-04-24", nil},
		{"last friday ordinal", "FREQ=MONTHLY;BYDAY=-1FR", "2026-01-30,2026-02-27,2026-03-27,2026-04-24", nil},
		// First Monday: 2026-01-05, 02-02, 03-02, 04-06.
		{"first monday", "FREQ=MONTHLY;BYDAY=1MO", "2026-01-05,2026-02-02,2026-03-02,2026-04-06", nil},
		// First weekday of the month: Thu 01-01, Mon 02-02, Mon 03-02, Wed 04-01.
		{"first weekday", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1", "2026-01-01,2026-02-02,2026-03-02,2026-04-01", nil},
		// Last weekday: Fri 01-30, Fri 02-27, Tue 03-31, Thu 04-30.
		{"last weekday", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1", "2026-01-30,2026-02-27,2026-03-31,2026-04-30", nil},
		// Friday the 13th: 2026-02-13, 2026-03-13, 2026-11-13, 2027-08-13.
		{"friday 13th", "FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13", "2026-02-13,2026-03-13,2026-11-13,2027-08-13", nil},
		// Exception dates skip occurrences (both date forms).
		{"exdates", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-02-01,2026-04-01,2026-05-01,2026-06-01", []string{"2026-01-01", "20260301"}},
		// Weekly schedules honour exdates too.
		{"weekly exdates", "FREQ=WEEKLY;BYDAY=MO", "2026-01-12,2026-01-26,2026-02-02,2026-02-09", []string{"2026-01-05", "2026-01-19"}},
	}
	for _, c := range cases {
		if got := occurrenceDates(t, sched(c.rrule, c.ex...), now, 4); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if s := sched("FREQ=MONTHLY;BYMONTHDAY=-1"); len(s.Days) != 0 || s.RRule != "FREQ=MONTHLY;BYMONTHDAY=-1" {
		t.Errorf("stored days/rrule: %+v", s)
	}
}

func TestMuteScheduleHolidays(t *testing.T) {
	now := rfc("2026-12-20T00:00:00Z")
	s := mustSchedule(t, MuteScheduleInput{Timezone: "UTC", RRule: "FREQ=DAILY", StartTime: "22:00", EndTime: "06:00",
		HolidayCalendarIDs: []string{"6F1C9C1E-9B7B-4C1A-8E57-000000000001"}}, now)
	if s.HolidayCalendarIDs[0] != "6f1c9c1e-9b7b-4c1a-8e57-000000000001" {
		t.Fatalf("calendar ids not normalized: %v", s.HolidayCalendarIDs)
	}
	// Without dates loaded every day is an occurrence.
	if got := occurrenceDates(t, s, rfc("2026-12-24T12:00:00Z"), 3); got != "2026-12-24,2026-12-25,2026-12-26" {
		t.Errorf("no holidays: %s", got)
	}
	// MM-DD repeats every year; YYYY-MM-DD is one date.
	s.SetHolidays([]string{"12-25", "01-01", "2026-12-26"})
	if got := occurrenceDates(t, s, rfc("2026-12-24T12:00:00Z"), 4); got != "2026-12-24,2026-12-27,2026-12-28,2026-12-29" {
		t.Errorf("holidays 2026: %s", got)
	}
	if got := occurrenceDates(t, s, rfc("2027-12-24T12:00:00Z"), 3); got != "2027-12-24,2027-12-26,2027-12-27" {
		t.Errorf("holidays 2027: %s", got)
	}
	// An overnight occurrence that started the evening before a holiday still runs into the holiday.
	st, en, ok := s.Window(rfc("2026-12-25T03:00:00Z"))
	if !ok || !st.Equal(rfc("2026-12-24T22:00:00Z")) || !en.Equal(rfc("2026-12-25T06:00:00Z")) {
		t.Errorf("overnight into holiday: %s – %s %v", st, en, ok)
	}
	// The mute is not active on the holiday evening.
	m := Mute{Name: "nightly", Schedule: s}
	if m.Active(rfc("2026-12-25T23:00:00Z")) || !m.Active(rfc("2026-12-27T23:00:00Z")) {
		t.Error("Active ignores holidays")
	}
	AttachHolidays([]Mute{m}, map[string][]string{})
	if !m.Active(rfc("2026-12-25T23:00:00Z")) {
		t.Error("AttachHolidays with a missing calendar should clear holidays")
	}
}

func TestMuteScheduleMonthlyDST(t *testing.T) {
	now := rfc("2026-01-01T00:00:00Z")
	// Last Sunday of the month in Berlin, 01:00–04:00 local: 2026-03-29 and 2026-10-25 are the DST change days.
	s := mustSchedule(t, MuteScheduleInput{Timezone: "Europe/Berlin", RRule: "FREQ=MONTHLY;BYDAY=SU;BYSETPOS=-1", StartTime: "01:00", EndTime: "04:00"}, now)
	st, en, ok := s.Window(rfc("2026-03-01T00:00:00Z"))
	if !ok || !st.Equal(rfc("2026-03-29T00:00:00Z")) || !en.Equal(rfc("2026-03-29T02:00:00Z")) {
		t.Errorf("spring forward (2 h): %s – %s %v", st, en, ok)
	}
	st, en, ok = s.Window(rfc("2026-10-01T00:00:00Z"))
	if !ok || !st.Equal(rfc("2026-10-24T23:00:00Z")) || !en.Equal(rfc("2026-10-25T03:00:00Z")) {
		t.Errorf("fall back (4 h): %s – %s %v", st, en, ok)
	}
	// Exception on the DST day moves to the next month's last Sunday (2026-04-26, CEST: 23:00Z the day before).
	s2 := mustSchedule(t, MuteScheduleInput{Timezone: "Europe/Berlin", RRule: "FREQ=MONTHLY;BYDAY=-1SU", StartTime: "01:00", EndTime: "04:00",
		ExDates: []string{"2026-03-29"}}, now)
	if st, _, ok := s2.Window(rfc("2026-03-01T00:00:00Z")); !ok || !st.Equal(rfc("2026-04-25T23:00:00Z")) {
		t.Errorf("exdate on DST day: %s %v", st, ok)
	}
}

func TestMuteScheduleExceptionValidation(t *testing.T) {
	now := rfc("2026-09-13T10:00:00Z")
	bad := []MuteScheduleInput{
		{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00", ExDates: []string{"2026-02-30"}},
		{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00", ExDates: []string{"13/01/2026"}},
		{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00", HolidayCalendarIDs: []string{"not-a-uuid"}},
		{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00", ExDates: make([]string, maxExDates+1)},
	}
	for _, in := range bad {
		if _, err := in.Validate(parseRFC, now); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	s := mustSchedule(t, MuteScheduleInput{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00", ExDates: []string{"20261002", "2026-10-01", "2026-10-01"}}, now)
	if strings.Join(s.ExDates, ",") != "2026-10-01,2026-10-02" {
		t.Errorf("exdates not normalized: %v", s.ExDates)
	}
	// Every date excluded: no occurrence within the scan bound → the mute cannot be created.
	var all []string
	for d := now; len(all) < maxExDates; d = d.AddDate(0, 0, 1) {
		all = append(all, d.Format(time.DateOnly))
	}
	_, err := MuteInput{Name: "x", Schedule: &MuteScheduleInput{RRule: "FREQ=MONTHLY;BYMONTHDAY=13", StartTime: "01:00", EndTime: "02:00",
		ExDates: all, Until: "2027-09-01T00:00:00Z"}}.Validate(parseRFC, now)
	if err == nil {
		t.Error("schedule without occurrences accepted")
	}
}

func TestHolidayCalendarValidate(t *testing.T) {
	v, err := HolidayCalendarInput{Name: " TR resmi tatiller ", Dates: []string{"2026-04-23", "--01-01", "05-19", "20260423", "02-29"}}.Validate()
	if err != nil || v.Name != "TR resmi tatiller" || strings.Join(v.Dates, ",") != "01-01,02-29,05-19,2026-04-23" {
		t.Errorf("valid calendar: %+v %v", v, err)
	}
	for _, in := range []HolidayCalendarInput{{Name: ""}, {Name: "x", Dates: []string{"13-01"}}, {Name: "x", Dates: []string{"2026-13-01"}},
		{Name: "x", Dates: make([]string, maxCalendarDates+1)}} {
		if _, err := in.Validate(); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	vm, err := MuteInput{Name: "x", Schedule: &MuteScheduleInput{RRule: "FREQ=DAILY", StartTime: "01:00", EndTime: "02:00",
		HolidayCalendarIDs: []string{"6f1c9c1e-9b7b-4c1a-8e57-000000000001"}}}.Validate(parseRFC, rfc("2026-12-31T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if err := vm.ApplyHolidays(map[string][]string{}, rfc("2026-12-31T12:00:00Z")); err == nil {
		t.Error("unknown calendar accepted")
	}
	if err := vm.ApplyHolidays(map[string][]string{"6f1c9c1e-9b7b-4c1a-8e57-000000000001": {"01-01"}}, rfc("2026-12-31T12:00:00Z")); err != nil ||
		!vm.StartsAt.Equal(rfc("2027-01-02T01:00:00Z")) {
		t.Errorf("first occurrence after holiday: %s %v", vm.StartsAt, err)
	}
}
