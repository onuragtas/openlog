package dashboard_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
)

func TestReportSchedule(t *testing.T) {
	r := &dashboard.Report{Frequency: "daily", Hour: 9, Minute: 30, Timezone: "Europe/Istanbul"} // UTC+3
	at, period, ok := r.Occurrence(time.Date(2026, 9, 14, 7, 0, 0, 0, time.UTC))
	if !ok || !at.Equal(time.Date(2026, 9, 14, 6, 30, 0, 0, time.UTC)) || period != "2026-09-14" {
		t.Errorf("daily after the send: %v %s %v", at, period, ok)
	}
	before := time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)
	at, period, _ = r.Occurrence(before)
	if !at.Equal(time.Date(2026, 9, 13, 6, 30, 0, 0, time.UTC)) || period != "2026-09-13" {
		t.Errorf("daily before the send: %v %s", at, period)
	}
	if next := r.Next(before); !next.Equal(time.Date(2026, 9, 14, 6, 30, 0, 0, time.UTC)) {
		t.Errorf("next %v", next)
	}

	w := &dashboard.Report{Frequency: "weekly", Weekday: int(time.Monday), Hour: 8, Timezone: "UTC"}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	at, _, ok = w.Occurrence(now)
	if !ok || at.Weekday() != time.Monday || at.After(now) || now.Sub(at) >= 7*24*time.Hour || at.Hour() != 8 {
		t.Errorf("weekly occurrence %v", at)
	}
	if next := w.Next(now); next.Weekday() != time.Monday || !next.After(now) || next.Sub(now) > 7*24*time.Hour {
		t.Errorf("weekly next %v", next)
	}

	// Daylight saving time: 02:30 does not exist in New York on 2026-03-08; the send still happens once that day.
	ny := &dashboard.Report{Frequency: "daily", Hour: 2, Minute: 30, Timezone: "America/New_York"}
	loc, _ := time.LoadLocation("America/New_York")
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	next := ny.Next(start)
	if !next.After(start) || next.Sub(start) > 4*time.Hour {
		t.Errorf("DST next %v", next)
	}
	if _, p, _ := ny.Occurrence(next); p != "2026-03-08" {
		t.Errorf("DST period %s", p)
	}
}

func TestReportValidation(t *testing.T) {
	m, store, now, d := sharingFixture(t)
	if err := store.PutSettings(ctx, org, dashboard.Settings{ReportDomains: []string{"partner.io"}}); err != nil {
		t.Fatal(err)
	}
	valid := dashboard.ReportInput{Frequency: "daily", Hour: 9, Recipients: []string{"Alice@Example.com", "ops@partner.io", "alice@example.com"},
		Variables: map[string][]string{"env": {"prod"}}}
	r, err := m.CreateReport(ctx, org, d.ID, valid, memberA)
	if err != nil || r.Range != "24h" || r.Language != "en" || r.Timezone != "UTC" || !r.Enabled ||
		!reflect.DeepEqual(r.Recipients, []string{"alice@example.com", "ops@partner.io"}) || r.CreatedByEmail != "alice@example.com" {
		t.Fatalf("create %+v %v", r, err)
	}
	mutate := func(f func(in *dashboard.ReportInput)) dashboard.ReportInput {
		in := valid
		in.Recipients = append([]string{}, valid.Recipients...)
		f(&in)
		return in
	}
	many := make([]string, dashboard.MaxReportRecipients+1)
	for i := range many {
		many[i] = fmt.Sprintf("u%d@partner.io", i)
	}
	for want, in := range map[string]dashboard.ReportInput{
		"frequency must be":           mutate(func(in *dashboard.ReportInput) { in.Frequency = "hourly" }),
		"weekday must be":             mutate(func(in *dashboard.ReportInput) { in.Weekday = 7 }),
		"hour must be":                mutate(func(in *dashboard.ReportInput) { in.Hour = 24 }),
		"timezone must be":            mutate(func(in *dashboard.ReportInput) { in.Timezone = "Mars/Base" }),
		"language must be":            mutate(func(in *dashboard.ReportInput) { in.Language = "de" }),
		"range must be":               mutate(func(in *dashboard.ReportInput) { in.Range = "90d" }),
		"recipients: 1-20":            mutate(func(in *dashboard.ReportInput) { in.Recipients = nil }),
		"recipients[0] is not valid":  mutate(func(in *dashboard.ReportInput) { in.Recipients = []string{"not an email"} }),
		"recipients[0] is not a vali": mutate(func(in *dashboard.ReportInput) { in.Recipients = []string{"a@b.io\r\nBcc: x@y.io"} }),
		"neither a member":            mutate(func(in *dashboard.ReportInput) { in.Recipients = []string{"x@evil.com"} }),
		"1-20 e-mail addresses":       mutate(func(in *dashboard.ReportInput) { in.Recipients = many }),
		"has no variable":             mutate(func(in *dashboard.ReportInput) { in.Variables = map[string][]string{"x": {"1"}} }),
	} {
		_, err := m.CreateReport(ctx, org, d.ID, in, memberA)
		var ve *dashboard.ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, strings.TrimSuffix(strings.TrimSuffix(want, "valid"), "vali")) {
			t.Errorf("%s: %v", want, err)
		}
	}
	if _, err := m.CreateReport(ctx, org, d.ID, valid, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("member who cannot edit: %v", err)
	}
	if _, err := m.Reports(ctx, org, d.ID, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("member lists: %v", err)
	}

	weekly := mutate(func(in *dashboard.ReportInput) {
		in.Frequency, in.Weekday, in.Language, in.Timezone = "weekly", 5, "tr", "Europe/Istanbul"
	})
	*now = now.Add(time.Minute)
	u, err := m.UpdateReport(ctx, org, d.ID, r.ID, weekly, adminC)
	if err != nil || u.Frequency != "weekly" || u.Range != "7d" || u.Weekday != 5 || !u.UpdatedAt.After(r.UpdatedAt) {
		t.Fatalf("update %+v %v", u, err)
	}
	if list, err := m.Reports(ctx, org, d.ID, adminC); err != nil || len(list) != 1 {
		t.Errorf("list %+v %v", list, err)
	}

	// Private dashboards report only to their creator.
	priv := sample()
	priv.Visibility = "private"
	pd, err := m.Create(ctx, org, priv, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateReport(ctx, org, pd.ID, mutate(func(in *dashboard.ReportInput) { in.Recipients = []string{"bob@example.com"} }), memberA); err == nil ||
		!strings.Contains(err.Error(), "private dashboard") {
		t.Errorf("private dashboard to another member: %v", err)
	}
	if _, err := m.CreateReport(ctx, org, pd.ID, mutate(func(in *dashboard.ReportInput) { in.Recipients = []string{"alice@example.com"} }), memberA); err != nil {
		t.Errorf("private dashboard to its creator: %v", err)
	}

	for i := 1; i < dashboard.MaxReportsPerDashboard; i++ {
		if _, err := m.CreateReport(ctx, org, d.ID, valid, memberA); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.CreateReport(ctx, org, d.ID, valid, memberA); err == nil || !strings.Contains(err.Error(), "at most 10") {
		t.Errorf("limit: %v", err)
	}
	if _, err := m.DeleteReport(ctx, org, d.ID, r.ID, memberA); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteReport(ctx, org, d.ID, r.ID, memberA); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
}
