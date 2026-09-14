// Package usage reads usage metering data (docs/contracts/usage.md, D-079): per-tenant items and bytes per signal,
// ingested OTLP bytes, active hosts/containers/services and query compute from the usage_* ClickHouse tables, the
// query_log collector, billing period arithmetic, month-end projection and invoice-period export.
package usage

import (
	"fmt"
	"strings"
	"time"
)

// Period is a billing period [Start, End) in UTC. Periods are calendar months.
type Period struct {
	Start time.Time
	End   time.Time
}

// ID returns the period as YYYY-MM.
func (p Period) ID() string { return p.Start.Format("2006-01") }

// Contains reports whether t is inside the period.
func (p Period) Contains(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }

// PeriodOf returns the calendar month (UTC) containing t.
func PeriodOf(t time.Time) Period {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return Period{Start: start, End: start.AddDate(0, 1, 0)}
}

// ParsePeriod parses "YYYY-MM"; "" and "current" return the period of now.
func ParsePeriod(v string, now time.Time) (Period, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "current" {
		return PeriodOf(now), nil
	}
	if v == "previous" {
		return PeriodOf(PeriodOf(now).Start.Add(-time.Hour)), nil
	}
	t, err := time.Parse("2006-01", v)
	if err != nil {
		return Period{}, fmt.Errorf("invalid period %q (use YYYY-MM, current or previous)", v)
	}
	return PeriodOf(t), nil
}

// Elapsed returns the end of the data range of the period at now: now inside the period, End for past periods and
// Start for future ones.
func (p Period) Elapsed(now time.Time) time.Time {
	switch {
	case now.Before(p.Start):
		return p.Start
	case now.Before(p.End):
		return now
	default:
		return p.End
	}
}

// Project estimates the value of a cumulative usage metric at the end of the period. With recentDailyAvg > 0 (e.g. the
// average of the last complete days) the remaining days are extrapolated at that rate; otherwise the rate of the
// elapsed part of the period is used. Before one hour has elapsed the used value is returned unchanged, and past
// periods return used.
func Project(used, recentDailyAvg float64, p Period, now time.Time) float64 {
	if !now.Before(p.End) {
		return used
	}
	if now.Before(p.Start) {
		return 0
	}
	remainingDays := p.End.Sub(now).Hours() / 24
	if recentDailyAvg > 0 {
		return used + recentDailyAvg*remainingDays
	}
	elapsed := now.Sub(p.Start)
	if elapsed < time.Hour {
		return used
	}
	return used * float64(p.End.Sub(p.Start)) / float64(elapsed)
}

// RecentDailyAverage returns the mean of the last n complete days of values (ordered by day, the last entry being
// today, which is excluded). It returns 0 when fewer than one complete day is available.
func RecentDailyAverage(values []float64, n int) float64 {
	if len(values) < 2 || n <= 0 {
		return 0
	}
	complete := values[:len(values)-1]
	if len(complete) > n {
		complete = complete[len(complete)-n:]
	}
	var sum float64
	for _, v := range complete {
		sum += v
	}
	return sum / float64(len(complete))
}
