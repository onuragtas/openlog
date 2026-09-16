package alert

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// SMTPOverride is a per-channel SMTP server (password in the channel secrets).
type SMTPOverride struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	From     string `json:"from"`
	TLS      string `json:"tls"`
}

// ChannelConfig holds non-secret channel settings.
type ChannelConfig struct {
	To   []string      `json:"to,omitempty"`
	SMTP *SMTPOverride `json:"smtp,omitempty"`
	// On-call providers (§5.3); their integration key / API key is a secret.
	PagerDuty *notify.PagerDutyConfig `json:"pagerduty,omitempty"`
	Opsgenie  *notify.OpsgenieConfig  `json:"opsgenie,omitempty"`
}

// ChannelInput is the API representation of a channel write.
type ChannelInput struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled *bool             `json:"enabled"`
	Config  ChannelConfig     `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// DeliverySummary is the last delivery of a channel.
type DeliverySummary struct {
	At     time.Time
	Status string
	Error  string
}

// Channel is a stored channel. Secrets is the ciphertext; it never leaves the server.
type Channel struct {
	ID             string
	OrgID          string
	Name           string
	Type           string
	Enabled        bool
	Config         ChannelConfig
	Secrets        string
	SecretsKeyID   string
	SecretHints    map[string]string
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastDelivery   *DeliverySummary
}

var channelSecretKeys = map[string]map[string]bool{
	notify.TypeSlack:     {"url": true},
	notify.TypeTeams:     {"url": true},
	notify.TypeWebhook:   {"url": true, "hmac_secret": true},
	notify.TypeEmail:     {"smtp_password": true},
	notify.TypePagerDuty: {"routing_key": true},
	notify.TypeOpsgenie:  {"api_key": true},
}

// PreparedChannel is a validated channel write.
type PreparedChannel struct {
	Name      string
	Type      string
	Enabled   bool
	Config    ChannelConfig
	Secrets   map[string]string // complete plaintext secret set to store
	Hints     map[string]string
	Generated map[string]string // shown once in the create response
}

// PrepareChannel validates a channel write. existing/existingSecrets are the stored channel and its decrypted
// secrets on update (nil on create); omitted or empty secret fields keep their stored values.
func PrepareChannel(in ChannelInput, existing *Channel, existingSecrets map[string]string) (*PreparedChannel, error) {
	p := &PreparedChannel{Name: strings.TrimSpace(in.Name), Type: in.Type, Enabled: true, Config: in.Config,
		Secrets: map[string]string{}, Generated: map[string]string{}}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if n := len([]rune(p.Name)); n < 1 || n > 200 {
		return nil, invalid("name", "must be 1-200 characters")
	}
	allowed, ok := channelSecretKeys[p.Type]
	if !ok {
		return nil, invalid("type", "must be slack, email, webhook, teams, pagerduty or opsgenie")
	}
	if existing != nil && existing.Type != p.Type {
		return nil, invalid("type", "cannot be changed")
	}
	for k, v := range existingSecrets {
		if allowed[k] {
			p.Secrets[k] = v
		}
	}
	for k, v := range in.Secrets {
		if !allowed[k] {
			return nil, invalid("secrets."+k, "not a secret of %s channels", p.Type)
		}
		if v != "" {
			p.Secrets[k] = v
		}
	}
	// Each config section belongs to one channel type.
	if (p.Config.PagerDuty != nil && p.Type != notify.TypePagerDuty) || (p.Config.Opsgenie != nil && p.Type != notify.TypeOpsgenie) {
		return nil, invalid("config", "%s channels have no pagerduty or opsgenie settings", p.Type)
	}
	switch p.Type {
	case notify.TypeSlack, notify.TypeTeams, notify.TypeWebhook:
		if len(p.Config.To) > 0 || p.Config.SMTP != nil {
			return nil, invalid("config", "%s channels have no config", p.Type)
		}
		u := p.Secrets["url"]
		if u == "" {
			return nil, invalid("secrets.url", "required")
		}
		pu, err := url.Parse(u)
		schemeOK := pu != nil && (pu.Scheme == "https" || (p.Type == notify.TypeWebhook && pu.Scheme == "http"))
		if err != nil || !schemeOK || pu.Host == "" || len(u) > 2000 {
			if p.Type == notify.TypeWebhook {
				return nil, invalid("secrets.url", "must be an http(s) URL of at most 2000 characters")
			}
			return nil, invalid("secrets.url", "must be an https URL of at most 2000 characters")
		}
		if p.Type == notify.TypeWebhook && p.Secrets["hmac_secret"] == "" {
			p.Secrets["hmac_secret"] = randomHex(32)
			p.Generated["hmac_secret"] = p.Secrets["hmac_secret"]
		}
		if s := p.Secrets["hmac_secret"]; len(s) > 512 {
			return nil, invalid("secrets.hmac_secret", "at most 512 characters")
		}
	case notify.TypeEmail:
		if n := len(p.Config.To); n < 1 || n > 50 {
			return nil, invalid("config.to", "1-50 recipients")
		}
		for i, a := range p.Config.To {
			addr, err := mail.ParseAddress(a)
			if err != nil {
				return nil, invalid(fmt.Sprintf("config.to[%d]", i), "invalid e-mail address")
			}
			p.Config.To[i] = addr.Address
		}
		if o := p.Config.SMTP; o != nil {
			if o.Port == 0 {
				o.Port = 587
				if o.TLS == "tls" {
					o.Port = 465
				}
			}
			if o.TLS == "" {
				o.TLS = "starttls"
			}
			cfg := notify.SMTPConfig{Host: o.Host, Port: o.Port, Username: o.Username, From: o.From, TLS: o.TLS}
			if err := notify.ValidateSMTP(cfg); err != nil {
				return nil, invalid("config.smtp", "%s", strings.TrimPrefix(err.Error(), "SMTP "))
			}
			if len(o.Host) > 253 || len(o.Username) > 256 {
				return nil, invalid("config.smtp", "host or username too long")
			}
		} else if _, ok := p.Secrets["smtp_password"]; ok {
			return nil, invalid("secrets.smtp_password", "only used with config.smtp")
		}
	case notify.TypePagerDuty:
		if len(p.Config.To) > 0 || p.Config.SMTP != nil {
			return nil, invalid("config", "%s channels only have config.pagerduty", p.Type)
		}
		if key := p.Secrets["routing_key"]; key == "" {
			return nil, invalid("secrets.routing_key", "required")
		} else if len(key) > 128 {
			return nil, invalid("secrets.routing_key", "at most 128 characters")
		}
		if p.Config.PagerDuty == nil {
			p.Config.PagerDuty = &notify.PagerDutyConfig{}
		}
		region, err := providerRegion(p.Config.PagerDuty.Region)
		if err != nil {
			return nil, prefixField("config.pagerduty", err)
		}
		p.Config.PagerDuty.Region = region
	case notify.TypeOpsgenie:
		if len(p.Config.To) > 0 || p.Config.SMTP != nil {
			return nil, invalid("config", "%s channels only have config.opsgenie", p.Type)
		}
		if key := p.Secrets["api_key"]; key == "" {
			return nil, invalid("secrets.api_key", "required")
		} else if len(key) > 256 {
			return nil, invalid("secrets.api_key", "at most 256 characters")
		}
		if p.Config.Opsgenie == nil {
			p.Config.Opsgenie = &notify.OpsgenieConfig{}
		}
		if err := validateOpsgenie(p.Config.Opsgenie); err != nil {
			return nil, prefixField("config.opsgenie", err)
		}
	}
	p.Hints = SecretHints(p.Secrets)
	return p, nil
}

// providerRegion normalizes the service region of an on-call channel.
func providerRegion(v string) (string, error) {
	switch r := strings.ToLower(strings.TrimSpace(v)); r {
	case "", "us":
		return "us", nil
	case "eu":
		return "eu", nil
	default:
		return "", invalid("region", "must be us or eu")
	}
}

var (
	opsgenieResponderTypes = map[string]bool{"team": true, "user": true, "escalation": true, "schedule": true}
	opsgeniePriorities     = map[string]bool{"P1": true, "P2": true, "P3": true, "P4": true, "P5": true}
)

// validateOpsgenie checks and normalizes the non-secret settings of an opsgenie channel.
func validateOpsgenie(c *notify.OpsgenieConfig) error {
	region, err := providerRegion(c.Region)
	if err != nil {
		return err
	}
	c.Region = region
	if c.Priority = strings.ToUpper(strings.TrimSpace(c.Priority)); c.Priority != "" && !opsgeniePriorities[c.Priority] {
		return invalid("priority", "must be empty (mapped from the rule severity) or P1-P5")
	}
	if len(c.Responders) > 20 {
		return invalid("responders", "at most 20 responders")
	}
	for i := range c.Responders {
		r := &c.Responders[i]
		f := fmt.Sprintf("responders[%d]", i)
		r.Type, r.Name, r.ID = strings.ToLower(strings.TrimSpace(r.Type)), strings.TrimSpace(r.Name), strings.TrimSpace(r.ID)
		if !opsgenieResponderTypes[r.Type] {
			return invalid(f+".type", "must be team, user, escalation or schedule")
		}
		if (r.Name == "") == (r.ID == "") {
			return invalid(f, "set exactly one of name and id")
		}
		if len(r.Name) > 512 || len(r.ID) > 512 {
			return invalid(f, "name and id are at most 512 characters")
		}
	}
	tags := make([]string, 0, len(c.Tags))
	seen := map[string]bool{}
	for _, t := range c.Tags {
		if t = strings.TrimSpace(t); t == "" || seen[t] {
			continue
		}
		if len(t) > 50 {
			return invalid("tags", "a tag is at most 50 characters")
		}
		seen[t] = true
		tags = append(tags, t)
	}
	// openlog adds two tags of its own (§5.3) and Opsgenie allows 20.
	if len(tags) > 18 {
		return invalid("tags", "at most 18 tags")
	}
	c.Tags = tags
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SecretHints masks secret values for API responses.
func SecretHints(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" {
			continue
		}
		if k == "url" {
			out[k] = maskURL(v)
			continue
		}
		out[k] = "•••••••• (set)"
	}
	return out
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "•••"
	}
	tail := raw
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return u.Scheme + "://" + u.Host + "/…/•••" + tail
}

func channelAAD(orgID, channelID string) string { return orgID + "/" + channelID }

// EncryptSecrets seals a channel's secrets.
func EncryptSecrets(kr *secrets.Keyring, orgID, channelID string, m map[string]string) (stored, keyID string, err error) {
	if len(m) == 0 {
		return "", "", nil
	}
	if !kr.Configured() {
		return "", "", ErrNoSecretsKey
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, err := json.Marshal(m)
	if err != nil {
		return "", "", err
	}
	return kr.Encrypt(b, channelAAD(orgID, channelID))
}

// DecryptSecrets opens a channel's secrets.
func DecryptSecrets(kr *secrets.Keyring, orgID, channelID, stored string) (map[string]string, error) {
	out := map[string]string{}
	if stored == "" {
		return out, nil
	}
	b, err := kr.Decrypt(stored, channelAAD(orgID, channelID))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TargetFor builds a delivery target from a channel and its decrypted secrets.
func TargetFor(ch *Channel, sec map[string]string) notify.Target {
	t := notify.Target{Type: ch.Type, URL: sec["url"], HMACSecret: sec["hmac_secret"], To: ch.Config.To}
	if o := ch.Config.SMTP; o != nil && o.Host != "" {
		t.SMTP = &notify.SMTPConfig{Host: o.Host, Port: o.Port, Username: o.Username, Password: sec["smtp_password"], From: o.From, TLS: o.TLS}
	}
	switch ch.Type {
	case notify.TypePagerDuty:
		t.Key, t.PagerDuty = sec["routing_key"], ch.Config.PagerDuty
	case notify.TypeOpsgenie:
		t.Key, t.Opsgenie = sec["api_key"], ch.Config.Opsgenie
	}
	return t
}
