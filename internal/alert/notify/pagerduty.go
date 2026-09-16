package notify

import (
	"context"
	"strings"
)

// PagerDuty Events API v2 (docs/contracts/alerting.md §5.3). One openlog incident is one PagerDuty alert: the
// dedup_key is derived from the incident id, so acknowledge and resolve reach the alert the trigger opened.

// PagerDuty event actions.
const (
	PagerDutyTrigger     = "trigger"
	PagerDutyAcknowledge = "acknowledge"
	PagerDutyResolve     = "resolve"
)

// PagerDutyConfig holds the non-secret settings of a pagerduty channel (the integration key is a secret).
type PagerDutyConfig struct {
	// Region selects the service region: "" or "us" (default) and "eu".
	Region string `json:"region,omitempty"`
}

// PagerDutyEndpoint returns the Events API v2 URL of a region.
func PagerDutyEndpoint(region string) string {
	if strings.EqualFold(region, "eu") {
		return "https://events.eu.pagerduty.com/v2/enqueue"
	}
	return "https://events.pagerduty.com/v2/enqueue"
}

// DedupKey identifies an incident at the on-call providers (PagerDuty dedup_key, Opsgenie alias). Every
// notification of one incident carries the same key, so updates land on the same alert. It has no "/" so it can be
// used inside a URL path.
func DedupKey(ev Event) string {
	if ev.Incident.ID != "" {
		return "openlog-" + ev.Incident.ID
	}
	return "openlog-test-" + ev.NotificationID
}

// ProviderAction maps the notification event to the lifecycle action of the on-call providers.
// A re-notification triggers again with the same key (the provider updates its open alert).
func ProviderAction(event string) string {
	switch event {
	case EventAcknowledged:
		return PagerDutyAcknowledge
	case EventResolved:
		return PagerDutyResolve
	default:
		return PagerDutyTrigger
	}
}

// pagerDutySeverity maps an openlog severity to an Events API v2 severity.
func pagerDutySeverity(severity string) string {
	switch severity {
	case "critical", "warning", "info":
		return severity
	default:
		return "warning"
	}
}

// providerSource is what raised the alert: the host or service of the incident, else the organization.
func providerSource(ev Event) string {
	for _, k := range []string{"host.name", "service.name", "host.id"} {
		if v := ev.Incident.Labels[k]; v != "" {
			return v
		}
	}
	return orDash(ev.Organization.Name)
}

// providerDetails are the incident fields sent as custom details (PagerDuty) / details (Opsgenie).
func providerDetails(ev Event) map[string]string {
	d := map[string]string{
		"rule":         ev.Rule.Name,
		"rule_type":    ev.Rule.Type,
		"severity":     ev.Rule.Severity,
		"state":        ev.Incident.State,
		"organization": ev.Organization.Name,
		"value":        fmtFloat(ev.Incident.Value),
		"threshold":    fmtFloat(ev.Incident.Threshold),
	}
	for k, v := range ev.Incident.Labels {
		if !strings.HasPrefix(k, "alert.") {
			d[k] = v
		}
	}
	for k, v := range map[string]string{"incident_id": ev.Incident.ID, "incident_url": ev.Incident.URL,
		"rule_url": ev.Rule.URL, "runbook_url": ev.Rule.RunbookURL, "resolve_reason": derefString(ev.Incident.ResolveReason),
		"opened_at": ev.Incident.OpenedAt} {
		if v != "" {
			d[k] = v
		}
	}
	return d
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// PagerDutyMessage renders an Events API v2 event. Acknowledge and resolve carry only the keys: PagerDuty ignores
// the payload of those actions.
func PagerDutyMessage(ev Event, key, action string) map[string]any {
	m := map[string]any{"routing_key": key, "event_action": action, "dedup_key": DedupKey(ev)}
	if action != PagerDutyTrigger {
		return m
	}
	prefix, _ := title(ev)
	payload := map[string]any{
		"summary":        truncate(prefix+": "+ev.Rule.Name+" — "+summaryOrTest(ev), 1024),
		"severity":       pagerDutySeverity(ev.Rule.Severity),
		"source":         providerSource(ev),
		"class":          orEmpty(ev.Rule.Type, "alert"),
		"group":          ev.Organization.Name,
		"custom_details": providerDetails(ev),
	}
	if c := ev.Incident.Labels["service.name"]; c != "" {
		payload["component"] = c
	}
	if ev.Incident.OpenedAt != "" {
		payload["timestamp"] = ev.Incident.OpenedAt
	}
	m["payload"] = payload
	m["client"] = "openlog"
	if ev.Incident.URL != "" {
		m["client_url"] = ev.Incident.URL
	}
	if links := providerLinks(ev); len(links) > 0 {
		m["links"] = links
	}
	return m
}

func orEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func providerLinks(ev Event) []map[string]string {
	var out []map[string]string
	for _, l := range []struct{ href, text string }{
		{ev.Incident.URL, "Open incident"}, {ev.Rule.URL, "Alert rule"}, {ev.Rule.RunbookURL, "Runbook"},
	} {
		if l.href != "" {
			out = append(out, map[string]string{"href": l.href, "text": l.text})
		}
	}
	return out
}

func (s *Sender) sendPagerDuty(ctx context.Context, t Target, ev Event) Result {
	cfg := t.PagerDuty
	if cfg == nil {
		cfg = &PagerDutyConfig{}
	}
	endpoint := s.pagerDutyURL
	if endpoint == "" {
		endpoint = PagerDutyEndpoint(cfg.Region)
	}
	res := s.postJSON(ctx, endpoint, PagerDutyMessage(ev, t.Key, ProviderAction(ev.Event)), nil)
	// A test notification triggers and resolves at once, so testing a channel leaves no open PagerDuty incident.
	if res.OK() && ev.Event == EventTest {
		_ = s.postJSON(ctx, endpoint, PagerDutyMessage(ev, t.Key, PagerDutyResolve), nil)
	}
	return res
}
