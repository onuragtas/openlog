// Package jobs implements cron and heartbeat monitoring (D-141): openlog knows when a job was supposed to
// run, the job tells openlog that it ran, and openlog raises the alarm when the message does not arrive.
//
// This is the one signal a metrics pipeline cannot produce, because it is about something that did *not*
// happen. A backup script that stops running sends nothing at all: no logs, no spans, no metric that goes
// to zero — the series simply stops, and "no data" is indistinguishable from "the host is being rebuilt".
// A monitor turns that silence into a fact by writing down the schedule in advance.
//
// Layout: jobs.go (model + validation), cron.go (the schedule expression), pgstore.go (PostgreSQL),
// runs.go (ClickHouse history and the mirrored metrics), sweeper.go (the api leader's overdue check).
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Schedule kinds. A job either runs on a cron expression — the crontab line that starts it — or simply
// reports at least this often, which is what a daemon's heartbeat is.
const (
	KindCron     = "cron"
	KindInterval = "interval"
)

// Kinds are the schedule kinds in display order.
var Kinds = []string{KindCron, KindInterval}

// Run statuses. The first three are what a job reports, the last two are what openlog concludes.
const (
	// StatusRunning: a start ping arrived and the matching finish has not.
	StatusRunning = "running"
	// StatusSuccess: the job reported that it finished.
	StatusSuccess = "success"
	// StatusFailure: the job reported that it failed (a fail ping, or a non-zero exit code).
	StatusFailure = "failure"
	// StatusMissed: the run was due and nothing arrived within the grace period.
	StatusMissed = "missed"
	// StatusOverrun: a start ping arrived while the previous run was still marked running past its grace.
	StatusOverrun = "overrun"
)

// Limits of a definition and a ping.
const (
	MaxNameRunes      = 200
	MaxTimeZoneBytes  = 64
	MinIntervalSecs   = 60
	MaxIntervalSecs   = 90 * 24 * 3600
	MinGraceSecs      = 0
	MaxGraceSecs      = 24 * 3600
	DefaultGraceSecs  = 300
	DefaultInterval   = 3600
	MaxPerOrg         = 200
	MaxMessageBytes   = 4096
	MaxExitCode       = 255
	MaxTagRunes       = 64
	MaxTags           = 10
	DefaultCron       = "0 * * * *"
	tokenRandomBytes  = 20
	tokenPrefix       = "olj_"
	maxRunsPerMonitor = 1000
)

var (
	// ErrNotFound is an unknown monitor of the organization.
	ErrNotFound = errors.New("job monitor not found")
	// ErrLimit reports that the organization already has MaxPerOrg monitors.
	ErrLimit = errors.New("job monitor limit reached")
	// ErrUnknownToken is a ping for a token that belongs to no monitor (or a deleted one).
	ErrUnknownToken = errors.New("unknown job token")
)

// ValidationError is an invalid definition (400 invalid_argument with the field path).
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }

// Actor is who changed a definition (audit log), as in synthetics.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// Input is the writable part of a monitor.
type Input struct {
	Name string `json:"name"`
	// Kind is KindCron or KindInterval.
	Kind string `json:"kind"`
	// Cron is the five-field expression (KindCron).
	Cron string `json:"cron"`
	// TimeZone is the IANA zone the cron expression is read in (KindCron); "" = UTC.
	TimeZone string `json:"time_zone"`
	// IntervalSeconds is how often the job is expected to report (KindInterval).
	IntervalSeconds int `json:"interval_seconds"`
	// GraceSeconds is how long after the expected time a run may still arrive before it counts as missed.
	GraceSeconds int  `json:"grace_seconds"`
	Enabled      bool `json:"enabled"`
	// Tags group monitors in the list (e.g. "backup", "billing").
	Tags []string `json:"tags"`
	// Description is what the job does, shown next to an alert about it.
	Description string `json:"description"`
}

// Monitor is a stored definition with its current state.
type Monitor struct {
	ID    string
	OrgID string
	Input
	// Token is the value in the ping URL. It is stored and shown as it is, not hashed: the URL lives in a
	// crontab and has to stay re-readable, and what it authorizes is one monitor's own status — a copy can
	// report a run that did not happen, which is the same capability as not pinging at all (D-141).
	Token          string
	CreatedByEmail string
	UpdatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	State          State
}

// State is what the pings and the sweeper have recorded about a monitor.
type State struct {
	// Status is the last concluded run status ("" until the first run).
	Status string
	// LastPingAt is when any ping last arrived.
	LastPingAt *time.Time
	// LastStartedAt / LastFinishedAt bound the last run openlog saw.
	LastStartedAt  *time.Time
	LastFinishedAt *time.Time
	// LastDurationMs is the duration of the last finished run.
	LastDurationMs float64
	// LastExitCode is what the last finishing ping reported (0 when it reported none).
	LastExitCode int
	// LastMessage is the output the last ping carried.
	LastMessage string
	// ExpectedAt is when the next run is due; the sweeper compares it with now plus the grace period.
	ExpectedAt time.Time
	// ConsecutiveFailures counts failed or missed runs in a row, so an alert can ignore a single blip.
	ConsecutiveFailures int
}

// Late reports whether the monitor is past its expected time plus grace at now.
func (m Monitor) Late(now time.Time) bool {
	return m.Enabled && !m.State.ExpectedAt.IsZero() && now.After(m.State.ExpectedAt.Add(m.Grace()))
}

// Grace is the grace period as a duration.
func (in Input) Grace() time.Duration { return time.Duration(in.GraceSeconds) * time.Second }

// Interval is the reporting interval (KindInterval).
func (in Input) Interval() time.Duration { return time.Duration(in.IntervalSeconds) * time.Second }

// Location is the monitor's time zone, UTC when unset or unknown to this machine.
func (in Input) Location() *time.Location {
	if in.TimeZone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(in.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// NextExpected returns when the monitor is next due after t.
//
// A cron monitor's next run comes from the expression; an interval monitor's from the last report, because
// "at least every 6 hours" is measured from the last time it reported, not from a fixed grid.
func (in Input) NextExpected(t time.Time) (time.Time, error) {
	if in.Kind == KindInterval {
		return t.Add(in.Interval()), nil
	}
	s, err := ParseCron(in.Cron)
	if err != nil {
		return time.Time{}, err
	}
	next, ok := s.Next(t, in.Location())
	if !ok {
		return time.Time{}, errors.New("the expression has no occurrence in the next four years")
	}
	return next, nil
}

// Validate checks and normalizes an input.
func (in *Input) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > MaxNameRunes || !utf8.ValidString(in.Name) {
		return invalid("name", "must be 1-200 characters")
	}
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Description) > 1000 {
		return invalid("description", "at most 1000 characters")
	}
	if in.Kind == "" {
		in.Kind = KindCron
	}
	switch in.Kind {
	case KindCron:
		in.IntervalSeconds = 0
		if in.Cron == "" {
			in.Cron = DefaultCron
		}
		if _, err := ParseCron(in.Cron); err != nil {
			return invalid("cron", err.Error())
		}
		in.Cron = strings.Join(strings.Fields(strings.TrimSpace(in.Cron)), " ")
		if err := in.validateTimeZone(); err != nil {
			return err
		}
	case KindInterval:
		in.Cron, in.TimeZone = "", ""
		if in.IntervalSeconds == 0 {
			in.IntervalSeconds = DefaultInterval
		}
		if in.IntervalSeconds < MinIntervalSecs || in.IntervalSeconds > MaxIntervalSecs {
			return invalid("interval_seconds", "must be between 60 and 7776000 seconds")
		}
	default:
		return invalid("kind", "must be cron or interval")
	}
	if in.GraceSeconds == 0 {
		in.GraceSeconds = DefaultGraceSecs
	}
	if in.GraceSeconds < MinGraceSecs || in.GraceSeconds > MaxGraceSecs {
		return invalid("grace_seconds", "must be between 0 and 86400 seconds")
	}
	return in.validateTags()
}

func (in *Input) validateTimeZone() error {
	in.TimeZone = strings.TrimSpace(in.TimeZone)
	if in.TimeZone == "" {
		return nil
	}
	if len(in.TimeZone) > MaxTimeZoneBytes {
		return invalid("time_zone", "the name is too long")
	}
	if _, err := time.LoadLocation(in.TimeZone); err != nil {
		return invalid("time_zone", "unknown time zone (use an IANA name such as Europe/Istanbul)")
	}
	return nil
}

func (in *Input) validateTags() error {
	if len(in.Tags) > MaxTags {
		return invalid("tags", "at most 10 tags")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in.Tags))
	for _, tag := range in.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > MaxTagRunes {
			return invalid("tags", "a tag is at most 64 characters")
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	in.Tags = out
	return nil
}

// NewToken returns the value that goes in a ping URL: `olj_` plus 32 base32 characters of randomness.
// Base32 without padding keeps it copy-pasteable and case-insensitive in a shell script.
func NewToken() (string, error) {
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return tokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// NormalizeToken accepts the token as it appears in a URL and returns its canonical form; the empty string
// means the value cannot be a token at all, so the lookup is skipped.
func NormalizeToken(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !strings.HasPrefix(v, tokenPrefix) || len(v) != len(tokenPrefix)+32 {
		return ""
	}
	for _, r := range v[len(tokenPrefix):] {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return ""
		}
	}
	return v
}

// Ping is one report from a job.
type Ping struct {
	// Event is "start", "success" or "fail".
	Event string
	// ExitCode is what the job reported (0 when it reported none); a non-zero code makes the run a failure
	// even on the success path, because `curl …/$?` is how a crontab line reports both at once.
	ExitCode int
	// Message is the output the job attached (stdout tail, an error line).
	Message string
	// At is when the ping arrived.
	At time.Time
	// Source is the address the ping came from, kept on the run row so a wrong host is visible.
	Source string
}

// Ping events.
const (
	EventStart   = "start"
	EventSuccess = "success"
	EventFail    = "fail"
)

// ParseEvent maps the URL suffix to an event; "" is a success ping.
func ParseEvent(suffix string) (string, bool) {
	switch strings.ToLower(strings.Trim(suffix, "/")) {
	case "", EventSuccess, "ok", "done", "finish":
		return EventSuccess, true
	case EventStart, "begin":
		return EventStart, true
	case EventFail, "failure", "error":
		return EventFail, true
	}
	return "", false
}

// Status returns the run status a ping concludes: a start is running, a non-zero exit code is a failure
// however the job spelled the event.
func (p Ping) Status() string {
	switch {
	case p.Event == EventStart:
		return StatusRunning
	case p.Event == EventFail || p.ExitCode != 0:
		return StatusFailure
	}
	return StatusSuccess
}

// Store persists monitors per organization (PostgreSQL: PGStore).
type Store interface {
	// List returns the monitors of the organization ordered by name, each with its state.
	List(ctx context.Context, orgID string) ([]Monitor, error)
	// Get returns one monitor (ErrNotFound when it belongs to another organization).
	Get(ctx context.Context, orgID, id string) (*Monitor, error)
	// Create stores a new monitor with a fresh token (ErrLimit at MaxPerOrg).
	Create(ctx context.Context, orgID string, in Input, actor Actor) (*Monitor, error)
	// Update replaces the writable fields; the token is unchanged, so the crontab keeps working.
	Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*Monitor, error)
	// Delete removes a monitor and its state.
	Delete(ctx context.Context, orgID, id string, actor Actor) error
	// Rotate issues a new token for a monitor whose URL leaked.
	Rotate(ctx context.Context, orgID, id string, actor Actor) (*Monitor, error)
}

// PingStore is the path a ping takes: find the monitor by token, then record the outcome.
type PingStore interface {
	// ByToken resolves a ping token to its monitor and the tenant its runs belong to.
	ByToken(ctx context.Context, token string) (Monitor, string, error)
	// RecordPing applies a ping to the monitor's state and returns the run it concluded (nil for a start
	// ping, which begins a run rather than finishing one) together with the updated monitor.
	RecordPing(ctx context.Context, monitorID string, p Ping) (*Run, *Monitor, error)
}

// SweepStore is the overdue check's half of the store (sweeper.go).
type SweepStore interface {
	// ClaimOverdue marks at most limit monitors whose expected time plus grace has passed as missed,
	// advances their expected time and returns the runs that were concluded. Claiming and advancing happen
	// in one statement, so two api pods never report the same missed run twice.
	ClaimOverdue(ctx context.Context, now time.Time, limit int) ([]Run, error)
}
