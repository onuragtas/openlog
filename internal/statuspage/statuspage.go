// Package statuspage implements the public status page (D-108): component health from periodic self-checks of the api
// leader (ingest and processor liveness, query path, alert evaluators, processing freshness), incidents and
// maintenance windows written by operators, and a 90-day uptime history. It never reads or returns tenant data: the
// only telemetry query is the newest metric timestamp over all tenants.
package statuspage

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Component statuses.
const (
	Operational   = "operational"
	Degraded      = "degraded"
	PartialOutage = "partial_outage"
	MajorOutage   = "major_outage"
	Maintenance   = "maintenance"
	Unknown       = "unknown"
)

// Components are the public components, in display order.
var Components = []string{"ingest", "query_api", "alerting", "processing"}

func knownComponent(id string) bool {
	for _, c := range Components {
		if c == id {
			return true
		}
	}
	return false
}

// severity orders statuses for the overall status.
var severity = map[string]int{Operational: 0, Maintenance: 1, Degraded: 2, PartialOutage: 3, MajorOutage: 4, Unknown: 5}

// Worst returns the most severe status.
func Worst(statuses ...string) string {
	out := Operational
	for _, s := range statuses {
		if severity[s] > severity[out] {
			out = s
		}
	}
	return out
}

// Snapshot is the latest self-check (system_state "status_page").
type Snapshot struct {
	CheckedAt  time.Time         `json:"checked_at"`
	Components map[string]string `json:"components"`
	// ProcessingLagSeconds is the age of the newest stored metric (-1 = no recent data).
	ProcessingLagSeconds int64 `json:"processing_lag_seconds"`
}

// Incident kinds, statuses and impacts.
const (
	KindIncident    = "incident"
	KindMaintenance = "maintenance"
)

var incidentStatuses = map[string][]string{
	KindIncident:    {"investigating", "identified", "monitoring", "resolved"},
	KindMaintenance: {"scheduled", "in_progress", "completed"},
}

var impacts = map[string]bool{"none": true, "minor": true, "major": true, "critical": true}

// Finished reports whether status ends an incident or maintenance.
func Finished(status string) bool { return status == "resolved" || status == "completed" }

// Incident is an incident or maintenance window.
type Incident struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Impact     string     `json:"impact"`
	Components []string   `json:"components"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Updates    []Update   `json:"updates"`
}

// Update is a timeline entry of an incident.
type Update struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrNotFound is returned for an unknown incident.
var ErrNotFound = errors.New("incident not found")

// InvalidError is a validation error (400).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, args ...any) error { return &InvalidError{fmt.Sprintf(format, args...)} }

// Validate checks an incident (Kind, Status, Impact, Components, Title, time window).
func (in *Incident) Validate() error {
	statuses, ok := incidentStatuses[in.Kind]
	if !ok {
		return invalid("kind must be incident or maintenance")
	}
	valid := false
	for _, s := range statuses {
		valid = valid || s == in.Status
	}
	if !valid {
		return invalid("status of a %s must be one of %s", in.Kind, strings.Join(statuses, ", "))
	}
	if in.Impact == "" {
		in.Impact = "minor"
	}
	if !impacts[in.Impact] {
		return invalid("impact must be none, minor, major or critical")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 200 {
		return invalid("title must be 1-200 characters")
	}
	seen := map[string]bool{}
	comps := []string{}
	for _, c := range in.Components {
		if !knownComponent(c) {
			return invalid("unknown component %q (%s)", c, strings.Join(Components, ", "))
		}
		if !seen[c] {
			seen[c] = true
			comps = append(comps, c)
		}
	}
	in.Components = comps
	if in.EndsAt != nil && in.EndsAt.Before(in.StartsAt) {
		return invalid("ends_at must not be before starts_at")
	}
	return nil
}

// ValidMessage checks an update message.
func ValidMessage(msg string) error {
	if strings.TrimSpace(msg) == "" || utf8.RuneCountInString(msg) > 5000 {
		return invalid("message must be 1-5000 characters")
	}
	return nil
}

// ActiveMaintenance reports whether in is a maintenance window in progress at now.
func (in Incident) ActiveMaintenance(now time.Time) bool {
	if in.Kind != KindMaintenance || Finished(in.Status) {
		return false
	}
	if in.Status == "in_progress" {
		return true
	}
	return !now.Before(in.StartsAt) && (in.EndsAt == nil || now.Before(*in.EndsAt))
}

// Day is one day of a component's uptime history.
type Day struct {
	Date   string   `json:"date"`
	Status string   `json:"status"` // operational | degraded | outage | no_data
	Uptime *float64 `json:"uptime"` // percent; null without checks
}

// DayCounts are the stored check counts of one component and day.
type DayCounts struct {
	Checks, Operational, Degraded, Outage int
}

// History renders the last days (oldest first) ending today and the uptime percentage over the days with checks.
func History(counts map[string]DayCounts, today time.Time, days int) ([]Day, *float64) {
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	out := make([]Day, 0, days)
	var checks, up int
	for i := days - 1; i >= 0; i-- {
		date := today.AddDate(0, 0, -i).Format(time.DateOnly)
		c, ok := counts[date]
		d := Day{Date: date, Status: "no_data"}
		if ok && c.Checks > 0 {
			u := float64(c.Checks-c.Outage) / float64(c.Checks) * 100
			d.Uptime = &u
			switch {
			case c.Outage > 0:
				d.Status = "outage"
			case c.Degraded > 0:
				d.Status = "degraded"
			default:
				d.Status = Operational
			}
			checks += c.Checks
			up += c.Checks - c.Outage
		}
		out = append(out, d)
	}
	if checks == 0 {
		return out, nil
	}
	pct := float64(up) / float64(checks) * 100
	return out, &pct
}
