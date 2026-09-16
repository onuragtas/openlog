package alert

import (
	"testing"
	"time"
)

// routeNow is a Wednesday, 10:30 UTC (13:30 in Europe/Istanbul).
var routeNow = time.Date(2026, 9, 16, 10, 30, 0, 0, time.UTC)

func routeRule(id string, pos int, m RouteMatch, channels ...string) RoutingRule {
	return RoutingRule{ID: id, Name: id, Position: pos, Enabled: true, Match: m, ChannelIDs: channels}
}

func testRouting() *Routing {
	return &Routing{
		Channels: []ChannelRef{
			{ID: "ch-pager", Type: "pagerduty", Enabled: true},
			{ID: "ch-slack", Type: "slack", Enabled: true},
			{ID: "ch-off", Type: "opsgenie", Enabled: false},
		},
		Rules: []RoutingRule{
			// Disabled: never matches, although its condition would.
			{ID: "disabled", Name: "disabled", Position: 0, Match: RouteMatch{Severities: []string{"critical"}}, ChannelIDs: []string{"ch-slack"}},
			routeRule("critical", 1, RouteMatch{Severities: []string{"critical"}}, "ch-pager", "ch-off"),
			routeRule("checkout", 2, RouteMatch{Services: []string{"checkout"}}, "ch-slack"),
			{ID: "default", Name: "default", Position: 3, Enabled: true, IsDefault: true, ChannelIDs: []string{"ch-slack"}},
		},
	}
}

func TestRoutingFirstMatchWinsAndDefault(t *testing.T) {
	rt := testRouting()
	critical := RouteTarget{Severity: "critical", RuleType: TypeMetricThreshold, Service: "checkout",
		Labels: map[string]string{"service.name": "checkout", "env": "prod"}}
	// Both "critical" and "checkout" apply: the earlier route wins (a disabled one is skipped).
	if m := rt.Match(critical, routeNow); m == nil || m.ID != "critical" {
		t.Fatalf("first match = %v", m)
	}
	// Nothing matches a warning incident: the default route takes it.
	warning := RouteTarget{Severity: "warning", RuleType: TypeLogMatch, Labels: map[string]string{}}
	if m := rt.Match(warning, routeNow); m == nil || m.ID != "default" {
		t.Fatalf("default match = %v", m)
	}
	// Without a default route an unmatched incident has no route at all.
	rt.Rules = rt.Rules[:3]
	if m := rt.Match(warning, routeNow); m != nil {
		t.Fatalf("no match expected, got %v", m.ID)
	}
	// An organization without routing rules never matches.
	if m := (&Routing{}).Match(critical, routeNow); m != nil {
		t.Fatalf("empty routing matched %v", m.ID)
	}
	if m := (*Routing)(nil).Match(critical, routeNow); m != nil {
		t.Fatal("nil routing matched")
	}
}

func TestRouteMatchConditions(t *testing.T) {
	target := RouteTarget{Severity: "critical", RuleType: TypeAPM, Service: "checkout",
		Labels: map[string]string{"service.name": "checkout", "env": "prod", "team": "payments"}}
	cases := map[string]struct {
		match RouteMatch
		want  bool
	}{
		"empty matches everything": {RouteMatch{}, true},
		"severity":                 {RouteMatch{Severities: []string{"critical", "warning"}}, true},
		"other severity":           {RouteMatch{Severities: []string{"info"}}, false},
		"rule type":                {RouteMatch{RuleTypes: []string{TypeAPM}}, true},
		"other rule type":          {RouteMatch{RuleTypes: []string{TypeLogMatch}}, false},
		"service":                  {RouteMatch{Services: []string{"cart", "checkout"}}, true},
		"other service":            {RouteMatch{Services: []string{"cart"}}, false},
		"label eq":                 {RouteMatch{Labels: []RouteMatcher{{Label: "env", Op: "eq", Value: "prod"}}}, true},
		"label eq missing":         {RouteMatch{Labels: []RouteMatcher{{Label: "zone", Op: "eq", Value: "eu"}}}, false},
		"label neq":                {RouteMatch{Labels: []RouteMatcher{{Label: "env", Op: "neq", Value: "staging"}}}, true},
		"label contains":           {RouteMatch{Labels: []RouteMatcher{{Label: "team", Op: "contains", Value: "PAY"}}}, true},
		"all parts are and-ed": {RouteMatch{Severities: []string{"critical"}, Services: []string{"checkout"},
			Labels: []RouteMatcher{{Label: "env", Op: "eq", Value: "staging"}}}, false},
	}
	for name, c := range cases {
		if got := c.match.Matches(target, routeNow); got != c.want {
			t.Errorf("%s: matched = %v, want %v", name, got, c.want)
		}
	}
}

func TestRouteTimeWindow(t *testing.T) {
	office := &RouteWindow{Timezone: "Europe/Istanbul", Days: []string{"mon", "tue", "wed", "thu", "fri"}, StartTime: "09:00", EndTime: "18:00"}
	if !office.Contains(routeNow) { // Wednesday 13:30 local
		t.Error("office hours: inside window not matched")
	}
	if office.Contains(time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)) { // 08:00 local
		t.Error("office hours: before the window matched")
	}
	if office.Contains(time.Date(2026, 9, 19, 10, 30, 0, 0, time.UTC)) { // Saturday
		t.Error("office hours: weekend matched")
	}
	// Overnight window: it starts on a selected day and ends the next morning.
	night := &RouteWindow{Timezone: "UTC", Days: []string{"wed"}, StartTime: "22:00", EndTime: "06:00"}
	if !night.Contains(time.Date(2026, 9, 16, 23, 0, 0, 0, time.UTC)) {
		t.Error("overnight: evening of the selected day not matched")
	}
	if !night.Contains(time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC)) {
		t.Error("overnight: next morning not matched")
	}
	if night.Contains(time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC)) {
		t.Error("overnight: evening of another day matched")
	}
	// Without days every day counts.
	always := &RouteWindow{Timezone: "UTC", StartTime: "00:00", EndTime: "23:59"}
	if !always.Contains(routeNow) {
		t.Error("window without days did not match")
	}
}

func TestRouteChannelsFallsBackToRuleChannels(t *testing.T) {
	r := testRule(t, nil) // severity critical, type metric_threshold, two channels
	rt := testRouting()
	labels := map[string]string{"service.name": "checkout"}
	got := RouteChannels(rt, r, labels, routeNow, testChannels)
	if len(got) != 1 || got[0].ID != "ch-pager" {
		t.Fatalf("routed channels %+v (disabled channels must be dropped)", got)
	}
	// No routing configuration at all: the rule's own channels.
	if got := RouteChannels(nil, r, labels, routeNow, testChannels); len(got) != 2 {
		t.Fatalf("without routing: %+v", got)
	}
	// Rules that do not match and no default route: the rule's own channels.
	rt.Rules = []RoutingRule{routeRule("other", 0, RouteMatch{Severities: []string{"info"}}, "ch-slack")}
	if got := RouteChannels(rt, r, labels, routeNow, testChannels); len(got) != 2 {
		t.Fatalf("no match: %+v", got)
	}
	// A matching route whose channels are all gone notifies nobody (the route decided).
	rt.Rules = []RoutingRule{routeRule("gone", 0, RouteMatch{}, "ch-deleted")}
	if got := RouteChannels(rt, r, labels, routeNow, testChannels); len(got) != 0 {
		t.Fatalf("unknown channel id: %+v", got)
	}
}

func TestRoutingRuleValidation(t *testing.T) {
	ch := []string{"11111111-1111-1111-1111-111111111111"}
	bad := map[string]RoutingRuleInput{
		"name":       {Name: "", ChannelIDs: ch},
		"channels":   {Name: "x"},
		"channel id": {Name: "x", ChannelIDs: []string{"nope"}},
		"position":   {Name: "x", ChannelIDs: ch, Position: -1},
		"severity":   {Name: "x", ChannelIDs: ch, Match: RouteMatch{Severities: []string{"fatal"}}},
		"rule type":  {Name: "x", ChannelIDs: ch, Match: RouteMatch{RuleTypes: []string{"nope"}}},
		"label op":   {Name: "x", ChannelIDs: ch, Match: RouteMatch{Labels: []RouteMatcher{{Label: "env", Op: "like", Value: "p"}}}},
		"label name": {Name: "x", ChannelIDs: ch, Match: RouteMatch{Labels: []RouteMatcher{{Label: "", Op: "eq"}}}},
		"timezone": {Name: "x", ChannelIDs: ch, Match: RouteMatch{TimeWindow: &RouteWindow{Timezone: "Mars/Olympus",
			StartTime: "09:00", EndTime: "18:00"}}},
		"times":              {Name: "x", ChannelIDs: ch, Match: RouteMatch{TimeWindow: &RouteWindow{StartTime: "09:00", EndTime: "09:00"}}},
		"day":                {Name: "x", ChannelIDs: ch, Match: RouteMatch{TimeWindow: &RouteWindow{Days: []string{"funday"}, StartTime: "09:00", EndTime: "18:00"}}},
		"default with match": {Name: "x", ChannelIDs: ch, IsDefault: true, Match: RouteMatch{Severities: []string{"critical"}}},
	}
	for name, in := range bad {
		if _, err := in.Validate(); err == nil {
			t.Errorf("%s: invalid input accepted", name)
		}
	}
	v, err := RoutingRuleInput{Name: "  ops  ", ChannelIDs: []string{ch[0], ch[0]},
		Match: RouteMatch{Severities: []string{"CRITICAL", "critical"}, RuleTypes: []string{TypeAPM},
			TimeWindow: &RouteWindow{Days: []string{"fri", "mon"}, StartTime: "09:00", EndTime: "18:00"}}}.Validate()
	if err != nil {
		t.Fatalf("valid input: %v", err)
	}
	if v.Name != "ops" || !v.Enabled || len(v.ChannelIDs) != 1 || len(v.Match.Severities) != 1 || v.Match.Severities[0] != "critical" {
		t.Fatalf("normalization: %+v", v)
	}
	if w := v.Match.TimeWindow; w == nil || w.Timezone != "UTC" || len(w.Days) != 2 || w.Days[0] != "mon" {
		t.Fatalf("window normalization: %+v", v.Match.TimeWindow)
	}
	// The default route matches everything, so its match must stay empty.
	d, err := RoutingRuleInput{Name: "fallback", ChannelIDs: ch, IsDefault: true}.Validate()
	if err != nil || !d.IsDefault || !d.Match.Empty() {
		t.Fatalf("default route: %+v %v", d, err)
	}
}
