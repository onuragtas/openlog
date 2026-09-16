package client

import (
	"context"
	"fmt"
	"net/url"
)

// Notification channels (docs/contracts/api.md "Alerting", alerting.md §5.3–§5.4). Channel secrets are
// write-only: the API stores them encrypted and never returns them, so a read answers with masked
// secret_hints only. The provider therefore keeps the configured secrets in state and never expects the API
// to echo them back.

// SMTPOverride is a per-channel SMTP server of an email channel; its password is a secret.
type SMTPOverride struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	From     string `json:"from"`
	TLS      string `json:"tls,omitempty"`
}

// PagerDutyConfig is the non-secret part of a pagerduty channel; its integration key is the secret
// routing_key.
type PagerDutyConfig struct {
	Region string `json:"region,omitempty"`
}

// OpsgenieResponder is one team, user, escalation or schedule an Opsgenie alert is assigned to.
type OpsgenieResponder struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// OpsgenieConfig is the non-secret part of an opsgenie channel; its API key is the secret api_key.
type OpsgenieConfig struct {
	Region     string              `json:"region,omitempty"`
	Priority   string              `json:"priority,omitempty"`
	Responders []OpsgenieResponder `json:"responders,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
}

// ChannelConfig holds the non-secret settings of a channel; each section belongs to one channel type.
type ChannelConfig struct {
	To        []string         `json:"to,omitempty"`
	SMTP      *SMTPOverride    `json:"smtp,omitempty"`
	PagerDuty *PagerDutyConfig `json:"pagerduty,omitempty"`
	Opsgenie  *OpsgenieConfig  `json:"opsgenie,omitempty"`
}

// Channel is a notification channel as the API reads and writes it.
type Channel struct {
	ID      string        `json:"id,omitempty"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Enabled bool          `json:"enabled"`
	Config  ChannelConfig `json:"config"`
	// Secrets is write-only (url, hmac_secret, smtp_password, routing_key, api_key). Omitted fields keep
	// their stored value, so a write that changes nothing else may send none.
	Secrets map[string]string `json:"secrets,omitempty"`
	// SecretHints are the masked values a read returns instead of the secrets.
	SecretHints map[string]string `json:"secret_hints,omitempty"`
	// GeneratedSecrets carries a secret the API generated (a webhook's hmac_secret); it is returned once,
	// by the create response only.
	GeneratedSecrets map[string]string `json:"generated_secrets,omitempty"`
	CreatedByEmail   string            `json:"created_by_email,omitempty"`
	CreatedAt        string            `json:"created_at,omitempty"`
	UpdatedAt        string            `json:"updated_at,omitempty"`
}

// CreateChannel creates a channel (POST /api/v1/alerts/channels). Without OPENLOG_SECRETS_KEY on the server
// the API refuses channel writes with 409 failed_precondition.
func (c *Client) CreateChannel(ctx context.Context, in Channel) (*Channel, error) {
	in.ID, in.SecretHints, in.GeneratedSecrets, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", nil, nil, "", "", ""
	var out Channel
	if err := c.post(ctx, "/api/v1/alerts/channels", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetChannel reads one channel; secrets are masked as secret_hints.
func (c *Client) GetChannel(ctx context.Context, id string) (*Channel, error) {
	var out Channel
	if err := c.get(ctx, "/api/v1/alerts/channels/"+urlPath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListChannels reads every channel of the organization (data source lookup by name).
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var out struct {
		Channels []Channel `json:"channels"`
	}
	if err := c.get(ctx, "/api/v1/alerts/channels", &out); err != nil {
		return nil, err
	}
	return out.Channels, nil
}

// UpdateChannel replaces a channel. The type cannot change; omitted secret fields keep their stored value.
func (c *Client) UpdateChannel(ctx context.Context, id string, in Channel) (*Channel, error) {
	in.ID, in.SecretHints, in.GeneratedSecrets, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", nil, nil, "", "", ""
	var out Channel
	if err := c.put(ctx, "/api/v1/alerts/channels/"+urlPath(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteChannel removes a channel. Notifications already queued for it fail with "channel deleted".
func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/alerts/channels/"+urlPath(id))
}

// urlPath escapes one path segment (an id) so a malformed id cannot reach another endpoint.
func urlPath(id string) string { return url.PathEscape(id) }

// notFoundByName is the error a data source reports when no object of the organization carries the name.
func notFoundByName(kind, name string) error {
	return fmt.Errorf("no %s named %q in this organization", kind, name)
}
