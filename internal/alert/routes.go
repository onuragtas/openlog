package alert

import (
	"fmt"
	"strings"
	"time"
)

// Routing rules (docs/contracts/alerting.md §5.6, D-127): a per-organization ordered list that decides which
// channels an incident reaches when it opens. The first enabled route whose match applies wins; a route marked
// default matches every incident and is evaluated after all others. Without a match (and without a default route)
// the rule's own channels are notified, so routing never silently drops an alert.

// RouteMatcher matches one incident label (the operators of mute matchers, §5.2).
type RouteMatcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RouteWindow restricts a route to local times (e.g. office hours). Empty days = every day; an end_time at or
// before start_time means the window ends the next day.
type RouteWindow struct {
	Timezone  string   `json:"timezone"`
	Days      []string `json:"days,omitempty"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
}

// RouteMatch is the condition of a route. Its parts are AND-ed; an empty part matches everything.
type RouteMatch struct {
	Severities []string       `json:"severities,omitempty"`
	Services   []string       `json:"services,omitempty"`
	RuleTypes  []string       `json:"rule_types,omitempty"`
	Labels     []RouteMatcher `json:"labels,omitempty"`
	TimeWindow *RouteWindow   `json:"time_window,omitempty"`
}

// Empty reports whether the match applies to every incident.
func (m RouteMatch) Empty() bool {
	return len(m.Severities) == 0 && len(m.Services) == 0 && len(m.RuleTypes) == 0 && len(m.Labels) == 0 && m.TimeWindow == nil
}

// RouteTarget are the incident attributes a route matches on.
type RouteTarget struct {
	Severity string
	RuleType string
	Service  string // service.name label, "" when the incident has none
	Labels   map[string]string
}

// RoutingRuleInput is the API representation of a routing rule write.
type RoutingRuleInput struct {
	Name       string     `json:"name"`
	Enabled    *bool      `json:"enabled"`
	IsDefault  bool       `json:"is_default"`
	Match      RouteMatch `json:"match"`
	ChannelIDs []string   `json:"channel_ids"`
	// Position orders the rules of the organization (0 first); ties keep the stored order.
	Position int `json:"position"`
}

// ValidRoutingRule is a validated routing rule write.
type ValidRoutingRule struct {
	Name       string
	Enabled    bool
	IsDefault  bool
	Match      RouteMatch
	ChannelIDs []string
	Position   int
}

// RoutingRule is a stored routing rule.
type RoutingRule struct {
	ID             string
	OrgID          string
	Name           string
	Position       int
	Enabled        bool
	IsDefault      bool
	Match          RouteMatch
	ChannelIDs     []string
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Routing is the routing configuration of one organization: its rules in evaluation order and the channels they
// may use. The zero value routes nothing (the rule's own channels are notified).
type Routing struct {
	Rules    []RoutingRule
	Channels []ChannelRef
}

// Limits of the routing configuration.
const (
	MaxRoutingRulesPerOrg = 100
	maxRouteChannels      = 20
	maxRouteValues        = 50
	maxRouteMatchers      = 20
)

var (
	routeMatcherOps = map[string]bool{"eq": true, "neq": true, "contains": true}
	routeSeverities = map[string]bool{SeverityCritical: true, SeverityWarning: true, SeverityInfo: true}
)

// Validate checks and normalizes a routing rule write.
func (in RoutingRuleInput) Validate() (*ValidRoutingRule, error) {
	v := &ValidRoutingRule{Name: strings.TrimSpace(in.Name), Enabled: true, IsDefault: in.IsDefault,
		ChannelIDs: []string{}, Position: in.Position}
	if in.Enabled != nil {
		v.Enabled = *in.Enabled
	}
	if n := len([]rune(v.Name)); n < 1 || n > 200 {
		return nil, invalid("name", "must be 1-200 characters")
	}
	if v.Position < 0 || v.Position > MaxRoutingRulesPerOrg {
		return nil, invalid("position", "must be between 0 and %d", MaxRoutingRulesPerOrg)
	}
	seen := map[string]bool{}
	for i, id := range in.ChannelIDs {
		if !ValidUUID(id) {
			return nil, invalid(fmt.Sprintf("channel_ids[%d]", i), "not a valid id")
		}
		if id = strings.ToLower(id); !seen[id] {
			seen[id] = true
			v.ChannelIDs = append(v.ChannelIDs, id)
		}
	}
	switch {
	case len(v.ChannelIDs) == 0:
		return nil, invalid("channel_ids", "at least one channel")
	case len(v.ChannelIDs) > maxRouteChannels:
		return nil, invalid("channel_ids", "at most %d channels", maxRouteChannels)
	}
	m, err := validateRouteMatch(in.Match)
	if err != nil {
		return nil, prefixField("match", err)
	}
	v.Match = *m
	if v.IsDefault && !v.Match.Empty() {
		return nil, invalid("match", "the default route matches every incident; leave its match empty")
	}
	return v, nil
}

// normalizeRouteValues trims, deduplicates and checks a list of match values.
func normalizeRouteValues(field string, in []string, lower bool, ok func(string) bool) ([]string, error) {
	if len(in) > maxRouteValues {
		return nil, invalid(field, "at most %d values", maxRouteValues)
	}
	var out []string
	seen := map[string]bool{}
	for i, v := range in {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" || len(v) > 512 {
			return nil, invalid(fmt.Sprintf("%s[%d]", field, i), "must be 1-512 characters")
		}
		if ok != nil && !ok(v) {
			return nil, invalid(fmt.Sprintf("%s[%d]", field, i), "unknown value %q", v)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func validateRouteMatch(in RouteMatch) (*RouteMatch, error) {
	m := &RouteMatch{}
	var err error
	if m.Severities, err = normalizeRouteValues("severities", in.Severities, true, func(v string) bool { return routeSeverities[v] }); err != nil {
		return nil, err
	}
	if m.RuleTypes, err = normalizeRouteValues("rule_types", in.RuleTypes, true, func(v string) bool { _, ok := ruleTypes[v]; return ok }); err != nil {
		return nil, err
	}
	if m.Services, err = normalizeRouteValues("services", in.Services, false, nil); err != nil {
		return nil, err
	}
	if len(in.Labels) > maxRouteMatchers {
		return nil, invalid("labels", "at most %d matchers", maxRouteMatchers)
	}
	for i, lm := range in.Labels {
		f := fmt.Sprintf("labels[%d]", i)
		lm.Label = strings.TrimSpace(lm.Label)
		if lm.Label == "" || len(lm.Label) > 128 {
			return nil, invalid(f+".label", "required, at most 128 characters")
		}
		if !routeMatcherOps[lm.Op] {
			return nil, invalid(f+".op", "must be eq, neq or contains")
		}
		if len(lm.Value) > 1024 {
			return nil, invalid(f+".value", "at most 1024 characters")
		}
		m.Labels = append(m.Labels, lm)
	}
	if in.TimeWindow != nil {
		w, err := validateRouteWindow(*in.TimeWindow)
		if err != nil {
			return nil, prefixField("time_window", err)
		}
		m.TimeWindow = w
	}
	return m, nil
}

func validateRouteWindow(in RouteWindow) (*RouteWindow, error) {
	w := &RouteWindow{Timezone: strings.TrimSpace(in.Timezone), StartTime: in.StartTime, EndTime: in.EndTime}
	if w.Timezone == "" {
		w.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(w.Timezone); err != nil || w.Timezone == "Local" {
		return nil, invalid("timezone", "unknown IANA time zone %q", w.Timezone)
	}
	for i, d := range in.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if dayIndex(d) < 0 {
			return nil, invalid(fmt.Sprintf("days[%d]", i), "must be mon, tue, wed, thu, fri, sat or sun")
		}
		w.Days = append(w.Days, d)
	}
	w.Days = normalizeDays(w.Days)
	start, err := parseClock("start_time", w.StartTime)
	if err != nil {
		return nil, err
	}
	end, err := parseClock("end_time", w.EndTime)
	if err != nil {
		return nil, err
	}
	if start == end {
		return nil, invalid("end_time", "must differ from start_time")
	}
	return w, nil
}

// matches reports whether the label matcher applies to the incident labels.
func (m RouteMatcher) matches(labels map[string]string) bool {
	v, ok := labels[m.Label]
	switch m.Op {
	case "neq":
		return !ok || v != m.Value
	case "contains":
		return ok && strings.Contains(strings.ToLower(v), strings.ToLower(m.Value))
	default:
		return ok && v == m.Value
	}
}

func routeContains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// Matches reports whether every part of the match applies to t at time at.
func (m RouteMatch) Matches(t RouteTarget, at time.Time) bool {
	if len(m.Severities) > 0 && !routeContains(m.Severities, t.Severity) {
		return false
	}
	if len(m.RuleTypes) > 0 && !routeContains(m.RuleTypes, t.RuleType) {
		return false
	}
	if len(m.Services) > 0 && !routeContains(m.Services, t.Service) {
		return false
	}
	for _, lm := range m.Labels {
		if !lm.matches(t.Labels) {
			return false
		}
	}
	return m.TimeWindow == nil || m.TimeWindow.Contains(at)
}

// Contains reports whether at falls inside the window (local days and times of its zone). An unparsable window
// does not restrict the route (validation refuses such windows).
func (w *RouteWindow) Contains(at time.Time) bool {
	start, err1 := parseClock("start_time", w.StartTime)
	end, err2 := parseClock("end_time", w.EndTime)
	if err1 != nil || err2 != nil || start == end {
		return true
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := at.In(loc)
	mins := local.Hour()*60 + local.Minute()
	onDay := func(wd time.Weekday) bool {
		if len(w.Days) == 0 {
			return true
		}
		for _, d := range w.Days {
			if dayIndex(d) == int(wd) {
				return true
			}
		}
		return false
	}
	if start < end {
		return onDay(local.Weekday()) && mins >= start && mins < end
	}
	// Overnight: the window starts on a selected day and ends the next morning.
	if mins >= start {
		return onDay(local.Weekday())
	}
	return mins < end && onDay((local.Weekday()+6)%7)
}

// Match returns the first enabled route that applies to t at time at, the default route when none matches, or nil.
func (rt *Routing) Match(t RouteTarget, at time.Time) *RoutingRule {
	if rt == nil {
		return nil
	}
	var def *RoutingRule
	for i := range rt.Rules {
		r := &rt.Rules[i]
		if !r.Enabled {
			continue
		}
		if r.IsDefault {
			if def == nil {
				def = r
			}
			continue
		}
		if r.Match.Matches(t, at) {
			return r
		}
	}
	return def
}

// RouteChannels returns the channels an opening incident reaches: the enabled channels of the matching route, or
// the rule's own channels when no route matches (including when the organization has no routing rules).
func RouteChannels(rt *Routing, r *Rule, labels map[string]string, at time.Time, ruleChannels []ChannelRef) []ChannelRef {
	route := rt.Match(RouteTarget{Severity: r.Severity, RuleType: r.Type, Service: labels["service.name"], Labels: labels}, at)
	if route == nil {
		return ruleChannels
	}
	out := make([]ChannelRef, 0, len(route.ChannelIDs))
	for _, id := range route.ChannelIDs {
		for _, c := range rt.Channels {
			if c.ID == id && c.Enabled {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
