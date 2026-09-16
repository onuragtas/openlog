// Package notify renders and delivers alert notifications: Slack, Microsoft Teams, generic webhooks (HMAC-signed),
// e-mail (SMTP), PagerDuty (Events API v2) and Opsgenie (Alerts API). See docs/contracts/alerting.md §5.3.
package notify

// Event names.
const (
	EventOpened = "incident.opened"
	// EventAcknowledged is only delivered to channels that track the incident lifecycle (PagerDuty, Opsgenie).
	EventAcknowledged = "incident.acknowledged"
	EventResolved     = "incident.resolved"
	EventRenotify     = "incident.renotify"
	EventTest         = "test"
)

// Event is the notification payload (the generic webhook body; other formats render the same data). It is built
// when the notification is enqueued and stored in the outbox; SentAt and NotificationID are set at delivery.
type Event struct {
	Version        string       `json:"version"`
	Event          string       `json:"event"`
	IdempotencyKey string       `json:"idempotency_key"`
	NotificationID string       `json:"notification_id"`
	SentAt         string       `json:"sent_at"`
	Organization   Org          `json:"organization"`
	Rule           RuleInfo     `json:"rule"`
	Incident       IncidentInfo `json:"incident"`
}

// Org identifies the organization.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RuleInfo describes the rule.
type RuleInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	RunbookURL string `json:"runbook_url"`
	URL        string `json:"url,omitempty"`
}

// IncidentInfo describes the incident.
type IncidentInfo struct {
	ID             string            `json:"id"`
	State          string            `json:"state"`
	URL            string            `json:"url,omitempty"`
	Summary        string            `json:"summary"`
	Value          *float64          `json:"value"`
	Threshold      *float64          `json:"threshold"`
	Labels         map[string]string `json:"labels"`
	OpenedAt       string            `json:"opened_at"`
	AcknowledgedAt *string           `json:"acknowledged_at"`
	ResolvedAt     *string           `json:"resolved_at"`
	ResolveReason  *string           `json:"resolve_reason"`
}
