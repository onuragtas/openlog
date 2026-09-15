// Package fleet implements agent fleet updates (docs/contracts/releases-updates.md §3–§4): the
// per-organization update policy, the pure update decision served by the ingest sync endpoint,
// the rollout controller that advances and halts waves, and the management operations of the API.
package fleet

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Mode is the policy mode.
type Mode string

// Policy modes.
const (
	ModeOff    Mode = "off"
	ModeNotify Mode = "notify"
	ModeAuto   Mode = "auto"
)

// Target selects the version a policy updates to.
type Target string

// Policy targets.
const (
	TargetLatest Target = "latest"
	TargetPatch  Target = "patch"
	TargetPinned Target = "pinned"
)

// Window is a maintenance window in UTC. Days empty = every day. End <= Start spans midnight (the
// window belongs to the day it starts on). End may be "24:00".
type Window struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// Policy is an organization's agent update policy (contract §4).
type Policy struct {
	Mode               Mode     `json:"mode"`
	Channel            string   `json:"channel"`
	Target             Target   `json:"target"`
	PinnedVersion      *string  `json:"pinned_version"`
	Waves              []int    `json:"waves"`
	WaveSoakMinutes    int      `json:"wave_soak_minutes"`
	HaltFailureRate    float64  `json:"halt_failure_rate"`
	MaintenanceWindows []Window `json:"maintenance_windows"`
	// PHPAgent configures PHP agent installation through the infra agent (phpagent.go).
	PHPAgent PHPAgentPolicy `json:"php_agent"`
	// JavaAgent configures managing the Java agent jar through the infra agent (javaagent.go).
	JavaAgent JavaAgentPolicy `json:"java_agent"`
}

// DefaultPolicy is used for organizations without a stored policy.
func DefaultPolicy() Policy {
	return Policy{
		Mode: ModeAuto, Channel: lib.ChannelStable, Target: TargetLatest,
		Waves: []int{10, 50, 100}, WaveSoakMinutes: 60, HaltFailureRate: 0.05,
		MaintenanceWindows: []Window{}, PHPAgent: DefaultPHPAgentPolicy(), JavaAgent: DefaultJavaAgentPolicy(),
	}
}

// MaxSoakMinutes bounds wave_soak_minutes (30 days).
const MaxSoakMinutes = 43200

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ValidationError is a policy or request error that is safe to show to clients.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalidf(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Normalize validates p and returns it in canonical form (lower-case days, pinned version without
// "v", nil slices replaced by empty ones).
func (p Policy) Normalize() (Policy, error) {
	switch p.Mode {
	case ModeOff, ModeNotify, ModeAuto:
	default:
		return p, invalidf("mode must be off, notify or auto")
	}
	switch p.Channel {
	case lib.ChannelStable, lib.ChannelBeta:
	default:
		return p, invalidf("channel must be stable or beta")
	}
	switch p.Target {
	case TargetLatest, TargetPatch:
		p.PinnedVersion = nil
	case TargetPinned:
		if p.PinnedVersion == nil || strings.TrimSpace(*p.PinnedVersion) == "" {
			return p, invalidf("pinned_version is required when target is pinned")
		}
		v, err := lib.ParseVersion(strings.TrimSpace(*p.PinnedVersion))
		if err != nil {
			return p, invalidf("pinned_version: %v", err)
		}
		s := v.String()
		p.PinnedVersion = &s
	default:
		return p, invalidf("target must be latest, patch or pinned")
	}
	if err := ValidateWaves(p.Waves); err != nil {
		return p, err
	}
	if p.WaveSoakMinutes < 0 || p.WaveSoakMinutes > MaxSoakMinutes {
		return p, invalidf("wave_soak_minutes must be between 0 and %d", MaxSoakMinutes)
	}
	if !(p.HaltFailureRate >= 0 && p.HaltFailureRate <= 1) {
		return p, invalidf("halt_failure_rate must be between 0 and 1")
	}
	if p.MaintenanceWindows == nil {
		p.MaintenanceWindows = []Window{}
	}
	if len(p.MaintenanceWindows) > 50 {
		return p, invalidf("at most 50 maintenance windows")
	}
	out := make([]Window, 0, len(p.MaintenanceWindows))
	for i, w := range p.MaintenanceWindows {
		nw, err := w.normalize()
		if err != nil {
			return p, invalidf("maintenance_windows[%d]: %v", i, err)
		}
		out = append(out, nw)
	}
	p.MaintenanceWindows = out
	// An omitted php_agent section (mode "") keeps the stored one (Manager.PutPolicy).
	if p.PHPAgent.Mode != "" {
		php, err := p.PHPAgent.Normalize()
		if err != nil {
			return p, err
		}
		p.PHPAgent = php
	}
	// The same for java_agent.
	if p.JavaAgent.Mode != "" {
		java, err := p.JavaAgent.Normalize()
		if err != nil {
			return p, err
		}
		p.JavaAgent = java
	}
	return p, nil
}

// ValidateWaves checks that waves are strictly increasing percentages in 1..100 ending at 100.
func ValidateWaves(waves []int) error {
	if len(waves) == 0 || len(waves) > 20 {
		return invalidf("waves must have 1 to 20 entries")
	}
	prev := 0
	for i, w := range waves {
		if w <= prev || w > 100 {
			return invalidf("waves must be strictly increasing percentages between 1 and 100 (waves[%d]=%d)", i, w)
		}
		prev = w
	}
	if prev != 100 {
		return invalidf("the last wave must be 100")
	}
	return nil
}

func (w Window) normalize() (Window, error) {
	days := make([]string, 0, len(w.Days))
	seen := map[string]bool{}
	for _, d := range w.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if _, ok := weekdays[d]; !ok {
			return w, fmt.Errorf("unknown day %q (use mon, tue, wed, thu, fri, sat, sun)", d)
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	start, err := parseClock(w.Start, false)
	if err != nil {
		return w, fmt.Errorf("start: %w", err)
	}
	end, err := parseClock(w.End, true)
	if err != nil {
		return w, fmt.Errorf("end: %w", err)
	}
	if start == end {
		return w, fmt.Errorf("start and end must differ")
	}
	return Window{Days: days, Start: w.Start, End: w.End}, nil
}

// parseClock parses "HH:MM" into minutes since midnight. "24:00" is allowed for ends.
func parseClock(s string, allow24 bool) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || mm < 0 || mm > 59 || hh < 0 || hh > 24 || (hh == 24 && (mm != 0 || !allow24)) {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return hh*60 + mm, nil
}

// InWindow reports whether now (any zone; evaluated in UTC) falls in a maintenance window and
// returns the latest end of the windows that contain it. No windows means always open (zero end).
func (p Policy) InWindow(now time.Time) (bool, time.Time) {
	if len(p.MaintenanceWindows) == 0 {
		return true, time.Time{}
	}
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	open := false
	var end time.Time
	for _, w := range p.MaintenanceWindows {
		start, err1 := parseClock(w.Start, false)
		stop, err2 := parseClock(w.End, true)
		if err1 != nil || err2 != nil || start == stop {
			continue
		}
		// A window that started yesterday may still be open today.
		for _, dayOffset := range []int{0, -1} {
			day := midnight.AddDate(0, 0, dayOffset)
			if !w.onDay(day.Weekday()) {
				continue
			}
			ws := day.Add(time.Duration(start) * time.Minute)
			we := day.Add(time.Duration(stop) * time.Minute)
			if stop <= start {
				we = we.Add(24 * time.Hour)
			}
			if !now.Before(ws) && now.Before(we) {
				open = true
				if we.After(end) {
					end = we
				}
			}
		}
	}
	return open, end
}

func (w Window) onDay(d time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, s := range w.Days {
		if wd, ok := weekdays[strings.ToLower(s)]; ok && wd == d {
			return true
		}
	}
	return false
}
