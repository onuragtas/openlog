package alert

import (
	"fmt"
	"strings"
	"time"
)

// MuteMatcher matches one incident label.
type MuteMatcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// MuteInput is the API representation of a mute write.
type MuteInput struct {
	Name     string        `json:"name"`
	Comment  string        `json:"comment"`
	StartsAt string        `json:"starts_at"`
	EndsAt   string        `json:"ends_at"`
	RuleIDs  []string      `json:"rule_ids"`
	Matchers []MuteMatcher `json:"matchers"`
}

// Mute is a stored mute window.
type Mute struct {
	ID             string
	OrgID          string
	Name           string
	Comment        string
	StartsAt       time.Time
	EndsAt         time.Time
	RuleIDs        []string
	Matchers       []MuteMatcher
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ValidMute is a validated mute write.
type ValidMute struct {
	Name     string
	Comment  string
	StartsAt time.Time
	EndsAt   time.Time
	RuleIDs  []string
	Matchers []MuteMatcher
}

const maxMuteSpan = 90 * 24 * time.Hour

// Validate checks a mute write. parseTime accepts RFC3339 or unix milliseconds.
func (in MuteInput) Validate(parseTime func(string) (time.Time, error)) (*ValidMute, error) {
	m := &ValidMute{Name: strings.TrimSpace(in.Name), Comment: in.Comment, RuleIDs: []string{}, Matchers: []MuteMatcher{}}
	if n := len([]rune(m.Name)); n < 1 || n > 200 {
		return nil, invalid("name", "must be 1-200 characters")
	}
	if len([]rune(m.Comment)) > 2000 {
		return nil, invalid("comment", "must be at most 2000 characters")
	}
	var err error
	if m.StartsAt, err = parseTime(in.StartsAt); err != nil {
		return nil, invalid("starts_at", "%v", err)
	}
	if m.EndsAt, err = parseTime(in.EndsAt); err != nil {
		return nil, invalid("ends_at", "%v", err)
	}
	if !m.EndsAt.After(m.StartsAt) {
		return nil, invalid("ends_at", "must be after starts_at")
	}
	if m.EndsAt.Sub(m.StartsAt) > maxMuteSpan {
		return nil, invalid("ends_at", "at most 90 days after starts_at")
	}
	if len(in.RuleIDs) > 100 {
		return nil, invalid("rule_ids", "at most 100 rules")
	}
	for i, id := range in.RuleIDs {
		if !ValidUUID(id) {
			return nil, invalid(fmt.Sprintf("rule_ids[%d]", i), "not a valid id")
		}
		m.RuleIDs = append(m.RuleIDs, strings.ToLower(id))
	}
	if len(in.Matchers) > 20 {
		return nil, invalid("matchers", "at most 20 matchers")
	}
	for i, mm := range in.Matchers {
		f := fmt.Sprintf("matchers[%d]", i)
		if mm.Label == "" || len(mm.Label) > 128 {
			return nil, invalid(f+".label", "required, at most 128 characters")
		}
		switch mm.Op {
		case "eq", "neq", "contains":
		default:
			return nil, invalid(f+".op", "must be eq, neq or contains")
		}
		if len(mm.Value) > 1024 {
			return nil, invalid(f+".value", "at most 1024 characters")
		}
		m.Matchers = append(m.Matchers, mm)
	}
	return m, nil
}

// Active reports whether the mute covers at.
func (m *Mute) Active(at time.Time) bool { return !at.Before(m.StartsAt) && at.Before(m.EndsAt) }

// Matches reports whether the mute applies to an incident of ruleID with labels (ignoring time).
func (m *Mute) Matches(ruleID string, labels map[string]string) bool {
	if len(m.RuleIDs) > 0 {
		found := false
		for _, id := range m.RuleIDs {
			if strings.EqualFold(id, ruleID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, mm := range m.Matchers {
		v, ok := labels[mm.Label]
		switch mm.Op {
		case "eq":
			if !ok || v != mm.Value {
				return false
			}
		case "neq":
			if ok && v == mm.Value {
				return false
			}
		case "contains":
			if !ok || !strings.Contains(strings.ToLower(v), strings.ToLower(mm.Value)) {
				return false
			}
		}
	}
	return true
}

// MatchingMute returns the active mute with the latest end that applies, or nil.
func MatchingMute(mutes []Mute, ruleID string, labels map[string]string, at time.Time) *Mute {
	var best *Mute
	for i := range mutes {
		m := &mutes[i]
		if m.Active(at) && m.Matches(ruleID, labels) && (best == nil || m.EndsAt.After(best.EndsAt)) {
			best = m
		}
	}
	return best
}
