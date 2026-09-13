package alert

import (
	"context"
	"strings"
	"testing"
	"time"
)

func (s *memOutbox) RollRecurringMutes(context.Context, time.Time) (int, error) { return 0, nil }

func rfc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func parseRFC(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

func mustSchedule(t *testing.T, in MuteScheduleInput, now time.Time) *MuteSchedule {
	t.Helper()
	s, err := in.Validate(parseRFC, now)
	if err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return s
}

func TestParseRRule(t *testing.T) {
	days, norm, err := ParseRRule("RRULE:freq=weekly;byday=FR,MO,MO")
	if err != nil || strings.Join(days, ",") != "mon,fri" || norm != "FREQ=WEEKLY;BYDAY=MO,FR" {
		t.Errorf("weekly: %v %q %v", days, norm, err)
	}
	if days, norm, err := ParseRRule("FREQ=DAILY"); err != nil || len(days) != 7 || norm != "FREQ=DAILY" {
		t.Errorf("daily: %v %q %v", days, norm, err)
	}
	for _, bad := range []string{"FREQ=WEEKLY", "FREQ=MONTHLY;BYDAY=MO", "FREQ=WEEKLY;BYDAY=1MO", "FREQ=WEEKLY;BYDAY=MO;COUNT=3", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", "BYDAY=MO", "garbage"} {
		if _, _, err := ParseRRule(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMuteScheduleValidate(t *testing.T) {
	now := rfc("2026-09-13T10:00:00Z")
	bad := []MuteScheduleInput{
		{Timezone: "Mars/Olympus", Days: []string{"mon"}, StartTime: "22:00", EndTime: "06:00"},
		{Days: []string{"mon"}, RRule: "FREQ=DAILY", StartTime: "22:00", EndTime: "06:00"},
		{StartTime: "22:00", EndTime: "06:00"},
		{Days: []string{"monday"}, StartTime: "22:00", EndTime: "06:00"},
		{Days: []string{"mon"}, StartTime: "24:00", EndTime: "06:00"},
		{Days: []string{"mon"}, StartTime: "22:00", EndTime: "22:00"},
		{Days: []string{"mon"}, StartTime: "22:00", EndTime: "06:00", Until: "2026-09-01T00:00:00Z"},
		{Timezone: "Local", Days: []string{"mon"}, StartTime: "22:00", EndTime: "06:00"},
	}
	for _, in := range bad {
		if _, err := in.Validate(parseRFC, now); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	s := mustSchedule(t, MuteScheduleInput{Days: []string{"SUN", "mon"}, StartTime: "01:00", EndTime: "02:00"}, now)
	if s.Timezone != "UTC" || strings.Join(s.Days, ",") != "mon,sun" || !s.From.Equal(now) {
		t.Errorf("defaults: %+v", s)
	}
	// A mute whose schedule has ended cannot be created.
	_, err := MuteInput{Name: "x", Schedule: &MuteScheduleInput{Days: []string{"mon"}, StartTime: "01:00", EndTime: "02:00",
		From: "2026-01-01T00:00:00Z", Until: "2026-01-02T00:00:00Z"}}.Validate(parseRFC, now)
	if err == nil {
		t.Error("ended schedule accepted")
	}
}

func TestMuteScheduleOvernight(t *testing.T) {
	// Fridays 22:00 – 06:00 Istanbul (UTC+3, no DST): Fri 19:00Z – Sat 03:00Z.
	now := rfc("2026-09-01T00:00:00Z")
	s := mustSchedule(t, MuteScheduleInput{Timezone: "Europe/Istanbul", Days: []string{"fri"}, StartTime: "22:00", EndTime: "06:00"}, now)
	cases := []struct {
		at, start, end string
	}{
		{"2026-09-11T12:00:00Z", "2026-09-11T19:00:00Z", "2026-09-12T03:00:00Z"}, // Friday noon: next occurrence
		{"2026-09-12T02:00:00Z", "2026-09-11T19:00:00Z", "2026-09-12T03:00:00Z"}, // Saturday 05:00 local: active
		{"2026-09-12T03:00:00Z", "2026-09-18T19:00:00Z", "2026-09-19T03:00:00Z"}, // end is exclusive
	}
	for _, c := range cases {
		st, en, ok := s.Window(rfc(c.at))
		if !ok || !st.Equal(rfc(c.start)) || !en.Equal(rfc(c.end)) {
			t.Errorf("at %s: %s – %s (%v), want %s – %s", c.at, st, en, ok, c.start, c.end)
		}
	}
	m := Mute{Name: "weekend", Schedule: s, StartsAt: rfc("2026-09-04T19:00:00Z"), EndsAt: rfc("2026-09-05T03:00:00Z")}
	if !m.Active(rfc("2026-09-12T02:00:00Z")) || m.Active(rfc("2026-09-12T04:00:00Z")) {
		t.Error("Active does not follow the schedule")
	}
	got := MatchingMute([]Mute{m}, "r", nil, rfc("2026-09-12T02:00:00Z"))
	if got == nil || !got.EndsAt.Equal(rfc("2026-09-12T03:00:00Z")) {
		t.Errorf("matching mute = %+v", got)
	}
	if m.StartsAt != rfc("2026-09-04T19:00:00Z") {
		t.Error("MatchingMute changed the caller's mute")
	}
}

func TestMuteScheduleDST(t *testing.T) {
	now := rfc("2026-03-01T00:00:00Z")
	berlin := func(start, end string) *MuteSchedule {
		return mustSchedule(t, MuteScheduleInput{Timezone: "Europe/Berlin", RRule: "FREQ=DAILY", StartTime: start, EndTime: end}, now)
	}
	// Spring forward (2026-03-29 02:00 CET → 03:00 CEST): 01:00–04:00 local lasts 2 hours.
	st, en, _ := berlin("01:00", "04:00").Window(rfc("2026-03-28T23:30:00Z"))
	if !st.Equal(rfc("2026-03-29T00:00:00Z")) || !en.Equal(rfc("2026-03-29T02:00:00Z")) {
		t.Errorf("spring: %s – %s", st, en)
	}
	// A start inside the gap moves forward by the gap: 02:30 → 03:30 CEST.
	st, en, _ = berlin("02:30", "05:00").Window(rfc("2026-03-28T23:30:00Z"))
	if !st.Equal(rfc("2026-03-29T01:30:00Z")) || !en.Equal(rfc("2026-03-29T03:00:00Z")) {
		t.Errorf("gap start: %s – %s", st, en)
	}
	// Fall back (2026-10-25 03:00 CEST → 02:00 CET): 01:00–04:00 local lasts 4 hours.
	st, en, _ = berlin("01:00", "04:00").Window(rfc("2026-10-24T22:30:00Z"))
	if !st.Equal(rfc("2026-10-24T23:00:00Z")) || !en.Equal(rfc("2026-10-25T03:00:00Z")) {
		t.Errorf("fall: %s – %s", st, en)
	}
	// An ambiguous local time is the later instant: 02:30 → 02:30 CET = 01:30Z.
	st, _, _ = berlin("02:30", "05:00").Window(rfc("2026-10-24T22:30:00Z"))
	if !st.Equal(rfc("2026-10-25T01:30:00Z")) {
		t.Errorf("ambiguous start: %s", st)
	}
	// Occurrences keep their local wall-clock time across the change.
	s := berlin("22:00", "23:00")
	for _, c := range []struct{ at, start string }{
		{"2026-03-28T12:00:00Z", "2026-03-28T21:00:00Z"}, // CET
		{"2026-03-29T12:00:00Z", "2026-03-29T20:00:00Z"}, // CEST
	} {
		if st, _, _ := s.Window(rfc(c.at)); !st.Equal(rfc(c.start)) {
			t.Errorf("at %s: start %s, want %s", c.at, st, c.start)
		}
	}
}

func TestMuteScheduleBounds(t *testing.T) {
	now := rfc("2026-09-14T10:30:00Z") // Monday
	s := mustSchedule(t, MuteScheduleInput{Days: []string{"mon"}, StartTime: "10:00", EndTime: "12:00",
		Until: "2026-09-21T11:00:00Z"}, now)
	// In progress at creation: clipped to from.
	if st, en, ok := s.Window(now); !ok || !st.Equal(now) || !en.Equal(rfc("2026-09-14T12:00:00Z")) {
		t.Errorf("first: %s – %s %v", st, en, ok)
	}
	// Last occurrence clipped to until, then nothing.
	if st, en, ok := s.Window(rfc("2026-09-15T00:00:00Z")); !ok || !st.Equal(rfc("2026-09-21T10:00:00Z")) || !en.Equal(rfc("2026-09-21T11:00:00Z")) {
		t.Errorf("last: %s – %s %v", st, en, ok)
	}
	if _, _, ok := s.Window(rfc("2026-09-21T11:00:00Z")); ok {
		t.Error("occurrence after until")
	}
	v, err := MuteInput{Name: "standup", Schedule: &MuteScheduleInput{Days: []string{"mon"}, StartTime: "10:00", EndTime: "12:00"}}.Validate(parseRFC, now)
	if err != nil || !v.StartsAt.Equal(now) || !v.EndsAt.Equal(rfc("2026-09-14T12:00:00Z")) {
		t.Errorf("materialized window: %+v %v", v, err)
	}
}
