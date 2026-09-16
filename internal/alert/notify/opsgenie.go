package notify

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

// Opsgenie Alerts API (docs/contracts/alerting.md §5.3). One openlog incident is one Opsgenie alert: the alias is
// derived from the incident id, so acknowledge and close reach the alert the create opened.

// OpsgenieResponder is a team, user, escalation or schedule notified for an alert (exactly one of Name, ID).
type OpsgenieResponder struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// OpsgenieConfig holds the non-secret settings of an opsgenie channel (the API key is a secret).
type OpsgenieConfig struct {
	// Region selects the service region: "" or "us" (default) and "eu".
	Region string `json:"region,omitempty"`
	// Priority overrides the severity mapping (P1 … P5); empty = mapped from the rule severity.
	Priority   string              `json:"priority,omitempty"`
	Responders []OpsgenieResponder `json:"responders,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
}

// OpsgenieEndpoint returns the Alerts API base URL of a region.
func OpsgenieEndpoint(region string) string {
	if strings.EqualFold(region, "eu") {
		return "https://api.eu.opsgenie.com/v2/alerts"
	}
	return "https://api.opsgenie.com/v2/alerts"
}

// opsgeniePriority maps an openlog severity to an Opsgenie priority.
func opsgeniePriority(severity string) string {
	switch severity {
	case "critical":
		return "P1"
	case "info":
		return "P5"
	default:
		return "P3"
	}
}

// opsgenieTags are the configured tags plus openlog's own ones (at most 20, Opsgenie's limit).
func opsgenieTags(ev Event, cfg *OpsgenieConfig) []string {
	out := []string{"openlog"}
	if ev.Rule.Severity != "" {
		out = append(out, "severity:"+ev.Rule.Severity)
	}
	seen := map[string]bool{out[0]: true}
	for _, t := range out[1:] {
		seen[t] = true
	}
	for _, t := range cfg.Tags {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out[:min(len(out), 20)]
}

// opsgenieDescription is the human-readable body of the alert.
func opsgenieDescription(ev Event) string {
	lines := []string{summaryOrTest(ev), ""}
	d := providerDetails(ev)
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if d[k] != "" {
			lines = append(lines, k+": "+d[k])
		}
	}
	return truncate(strings.Join(lines, "\n"), 15000)
}

// OpsgenieMessage renders the create-alert body.
func OpsgenieMessage(ev Event, cfg *OpsgenieConfig) map[string]any {
	if cfg == nil {
		cfg = &OpsgenieConfig{}
	}
	prefix, _ := title(ev)
	priority := cfg.Priority
	if priority == "" {
		priority = opsgeniePriority(ev.Rule.Severity)
	}
	m := map[string]any{
		"message":     truncate(prefix+": "+orDash(ev.Rule.Name), 130),
		"alias":       DedupKey(ev),
		"description": opsgenieDescription(ev),
		"priority":    priority,
		"source":      "openlog",
		"entity":      providerSource(ev),
		"tags":        opsgenieTags(ev, cfg),
		"details":     providerDetails(ev),
	}
	if len(cfg.Responders) > 0 {
		m["responders"] = cfg.Responders
	}
	return m
}

// OpsgenieNote is the note attached to an acknowledge or close request.
func OpsgenieNote(ev Event) map[string]any {
	note := "acknowledged in openlog"
	if ev.Event == EventResolved {
		note = "resolved in openlog"
		if r := derefString(ev.Incident.ResolveReason); r != "" {
			note += " (" + r + ")"
		}
	}
	return map[string]any{"user": "openlog", "source": "openlog", "note": note}
}

func (s *Sender) sendOpsgenie(ctx context.Context, t Target, ev Event) Result {
	cfg := t.Opsgenie
	if cfg == nil {
		cfg = &OpsgenieConfig{}
	}
	base := s.opsgenieURL
	if base == "" {
		base = OpsgenieEndpoint(cfg.Region)
	}
	headers := map[string]string{"Authorization": "GenieKey " + t.Key}
	alias := url.PathEscape(DedupKey(ev))
	switch ProviderAction(ev.Event) {
	case PagerDutyAcknowledge:
		return s.postJSON(ctx, base+"/"+alias+"/acknowledge?identifierType=alias", OpsgenieNote(ev), headers)
	case PagerDutyResolve:
		return s.postJSON(ctx, base+"/"+alias+"/close?identifierType=alias", OpsgenieNote(ev), headers)
	}
	res := s.postJSON(ctx, base, OpsgenieMessage(ev, cfg), headers)
	// A test notification creates and closes the alert at once, so testing a channel leaves nothing open.
	if res.OK() && ev.Event == EventTest {
		_ = s.postJSON(ctx, base+"/"+alias+"/close?identifierType=alias", OpsgenieNote(ev), headers)
	}
	return res
}
