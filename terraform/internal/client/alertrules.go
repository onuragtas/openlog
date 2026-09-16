package client

import (
	"context"
	"encoding/json"
)

// Alert rules (docs/contracts/api.md "Alerting", alerting.md §2). A rule is replaced as a whole by PUT; the
// stored version travels with the write so a change someone else made in between is refused with
// 409 failed_precondition instead of being overwritten (alerting.md §4, "optimistic concurrency").

// Flapping is the flapping protection of a rule (alerting.md §3.4).
type Flapping struct {
	Enabled       bool `json:"enabled"`
	Transitions   int  `json:"transitions"`
	WindowSeconds int  `json:"window_seconds"`
	HoldSeconds   int  `json:"hold_seconds"`
}

// AlertRule is a rule as the API reads and writes it. Condition is the raw per-type condition document
// (alerting.md §2.2–§2.12): the provider passes it through instead of modelling ten rule types, and the API
// answers with the parsed and defaulted form.
type AlertRule struct {
	ID                      string            `json:"id,omitempty"`
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Type                    string            `json:"type"`
	Severity                string            `json:"severity,omitempty"`
	Enabled                 bool              `json:"enabled"`
	IntervalSeconds         int               `json:"interval_seconds,omitempty"`
	ForSeconds              int               `json:"for_seconds"`
	RecoveryForSeconds      int               `json:"recovery_for_seconds"`
	Condition               json.RawMessage   `json:"condition"`
	ChannelIDs              []string          `json:"channel_ids"`
	RenotifyIntervalSeconds int               `json:"renotify_interval_seconds"`
	Flapping                *Flapping         `json:"flapping,omitempty"`
	RunbookURL              string            `json:"runbook_url"`
	Labels                  map[string]string `json:"labels"`
	// Version is read-only in the response and the expected version in a PUT.
	Version        int    `json:"version,omitempty"`
	CreatedByEmail string `json:"created_by_email,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// CreateAlertRule creates a rule (POST /api/v1/alerts/rules).
func (c *Client) CreateAlertRule(ctx context.Context, in AlertRule) (*AlertRule, error) {
	in.ID, in.Version, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", 0, "", "", ""
	var out AlertRule
	if err := c.post(ctx, "/api/v1/alerts/rules", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAlertRule reads one rule. The response also carries the current series states, which the provider does
// not manage and therefore ignores.
func (c *Client) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	var out AlertRule
	if err := c.get(ctx, "/api/v1/alerts/rules/"+urlPath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateAlertRule replaces a rule. version is the version the provider last read; the API refuses a stale one
// with 409 failed_precondition.
func (c *Client) UpdateAlertRule(ctx context.Context, id string, in AlertRule, version int) (*AlertRule, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", "", "", ""
	in.Version = version
	var out AlertRule
	if err := c.put(ctx, "/api/v1/alerts/rules/"+urlPath(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAlertRule removes a rule. Its incidents are kept and resolved with reason rule_deleted.
func (c *Client) DeleteAlertRule(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/alerts/rules/"+urlPath(id))
}
