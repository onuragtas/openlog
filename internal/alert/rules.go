// Package alert implements alert rules, their evaluation, incidents and notifications
// (docs/contracts/alerting.md, D-030).
//
// Layout: rules.go (model + validation), filters.go (filter/group-by → tenant-scoped query fragments),
// cond_*.go (rule types), window.go (aggregation math), state.go (state machine), preview.go,
// lease.go + evaluator.go (sharding and scheduling), dispatcher.go (outbox), pgstore.go (PostgreSQL),
// manager.go (API-facing service), notify/ (channel senders), secrets/ (encryption at rest).
package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Rule types.
const (
	TypeMetricThreshold = "metric_threshold"
	TypeLogMatch        = "log_match"
	TypeNoData          = "no_data"
	TypeDiscovery       = "discovery"
	TypeAPM             = "apm"
	TypeAPMNoData       = "apm_no_data"
	TypeAPMError        = "apm_error"
	TypeSLOBurn         = "slo_burn"
)

// Severities.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Series states.
const (
	StateOK      = "ok"
	StatePending = "pending"
	StateFiring  = "firing"
)

// Incident states.
const (
	IncidentOpen         = "open"
	IncidentAcknowledged = "acknowledged"
	IncidentResolved     = "resolved"
)

// Resolve reasons.
const (
	ReasonRecovered    = "recovered"
	ReasonManual       = "manual"
	ReasonNoData       = "no_data"
	ReasonExpired      = "expired"
	ReasonRuleDisabled = "rule_disabled"
	ReasonRuleDeleted  = "rule_deleted"
	ReasonRuleChanged  = "rule_changed"
)

// Notification kinds.
const (
	KindOpened   = "opened"
	KindResolved = "resolved"
	KindRenotify = "renotify"
	KindTest     = "test"
)

// Notification statuses.
const (
	StatusPending    = "pending"
	StatusSending    = "sending"
	StatusDelivered  = "delivered"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// ValidationError is a rule/channel/mute input error (400 invalid_argument).
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Errors returned by stores and the manager.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrLeaseLost    = errors.New("lease lost")
	ErrStale        = errors.New("evaluation already committed")
	ErrRuleChanged  = errors.New("rule changed during evaluation")
	ErrNoSecretsKey = errors.New("OPENLOG_SECRETS_KEY is not configured")
)

// PreconditionError is a 409 failed_precondition.
type PreconditionError struct{ Msg string }

func (e *PreconditionError) Error() string { return e.Msg }

// Flapping configures flapping protection (§3.4).
type Flapping struct {
	Enabled       bool `json:"enabled"`
	Transitions   int  `json:"transitions"`
	WindowSeconds int  `json:"window_seconds"`
	HoldSeconds   int  `json:"hold_seconds"`
}

// DefaultFlapping is used when the input omits flapping.
var DefaultFlapping = Flapping{Enabled: true, Transitions: 4, WindowSeconds: 3600, HoldSeconds: 600}

// RuleInput is the API representation of a rule definition.
type RuleInput struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Type                    string            `json:"type"`
	Severity                string            `json:"severity"`
	Enabled                 *bool             `json:"enabled"`
	IntervalSeconds         int               `json:"interval_seconds"`
	ForSeconds              int               `json:"for_seconds"`
	RecoveryForSeconds      int               `json:"recovery_for_seconds"`
	Condition               json.RawMessage   `json:"condition"`
	ChannelIDs              []string          `json:"channel_ids"`
	RenotifyIntervalSeconds int               `json:"renotify_interval_seconds"`
	Flapping                *Flapping         `json:"flapping"`
	RunbookURL              string            `json:"runbook_url"`
	Labels                  map[string]string `json:"labels"`
	Version                 int               `json:"version,omitempty"`
}

// Definition is a validated rule definition.
type Definition struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Type                    string            `json:"type"`
	Severity                string            `json:"severity"`
	Enabled                 bool              `json:"enabled"`
	IntervalSeconds         int               `json:"interval_seconds"`
	ForSeconds              int               `json:"for_seconds"`
	RecoveryForSeconds      int               `json:"recovery_for_seconds"`
	Condition               Condition         `json:"-"`
	ConditionJSON           json.RawMessage   `json:"condition"`
	ChannelIDs              []string          `json:"channel_ids"`
	RenotifyIntervalSeconds int               `json:"renotify_interval_seconds"`
	Flapping                Flapping          `json:"flapping"`
	RunbookURL              string            `json:"runbook_url"`
	Labels                  map[string]string `json:"labels"`
}

// Interval returns the evaluation interval.
func (d *Definition) Interval() time.Duration { return time.Duration(d.IntervalSeconds) * time.Second }

// Rule is a stored rule.
type Rule struct {
	Definition
	ID             string
	OrgID          string
	TenantID       string // organizations.tenant_id, joined by the store (never from the definition)
	OrgName        string
	Version        int
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var (
	labelKeyRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,64}$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ValidUUID reports whether s is a canonical UUID.
func ValidUUID(s string) bool { return uuidRe.MatchString(s) }

// Validate checks and normalizes a rule input, parsing its condition with the registered rule type.
func (in RuleInput) Validate() (*Definition, error) {
	d := &Definition{
		Name: strings.TrimSpace(in.Name), Description: in.Description, Type: in.Type, Severity: in.Severity,
		Enabled: true, IntervalSeconds: in.IntervalSeconds, ForSeconds: in.ForSeconds, RecoveryForSeconds: in.RecoveryForSeconds,
		RenotifyIntervalSeconds: in.RenotifyIntervalSeconds, RunbookURL: strings.TrimSpace(in.RunbookURL), Labels: in.Labels,
	}
	if in.Enabled != nil {
		d.Enabled = *in.Enabled
	}
	if n := len([]rune(d.Name)); n < 1 || n > 200 {
		return nil, invalid("name", "must be 1-200 characters")
	}
	if len([]rune(d.Description)) > 2000 {
		return nil, invalid("description", "must be at most 2000 characters")
	}
	rt, ok := ruleTypes[d.Type]
	if !ok {
		return nil, invalid("type", "must be one of metric_threshold, log_match, no_data, discovery, apm, apm_no_data, apm_error, oql, slo_burn")
	}
	if !rt.Available() {
		return nil, invalid("type", "rule type %q is not available yet", d.Type)
	}
	switch d.Severity {
	case "":
		d.Severity = SeverityWarning
	case SeverityCritical, SeverityWarning, SeverityInfo:
	default:
		return nil, invalid("severity", "must be critical, warning or info")
	}
	if d.IntervalSeconds == 0 {
		d.IntervalSeconds = rt.DefaultInterval()
	}
	if d.IntervalSeconds < 10 || d.IntervalSeconds > 3600 {
		return nil, invalid("interval_seconds", "must be between 10 and 3600")
	}
	if d.ForSeconds < 0 || d.ForSeconds > 86400 {
		return nil, invalid("for_seconds", "must be between 0 and 86400")
	}
	if d.RecoveryForSeconds < 0 || d.RecoveryForSeconds > 86400 {
		return nil, invalid("recovery_for_seconds", "must be between 0 and 86400")
	}
	if r := d.RenotifyIntervalSeconds; r != 0 && (r < 300 || r > 604800) {
		return nil, invalid("renotify_interval_seconds", "must be 0 (off) or between 300 and 604800")
	}
	if len(in.Condition) == 0 || string(in.Condition) == "null" {
		return nil, invalid("condition", "required")
	}
	cond, err := rt.Parse(in.Condition)
	if err != nil {
		return nil, prefixField("condition", err)
	}
	d.Condition = cond
	if d.ConditionJSON, err = json.Marshal(cond); err != nil {
		return nil, err
	}
	if cond.IgnoresFor() {
		d.ForSeconds = 0
	}
	seen := map[string]bool{}
	d.ChannelIDs = []string{}
	for i, id := range in.ChannelIDs {
		if !ValidUUID(id) {
			return nil, invalid(fmt.Sprintf("channel_ids[%d]", i), "not a valid id")
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			d.ChannelIDs = append(d.ChannelIDs, id)
		}
	}
	if len(d.ChannelIDs) > 20 {
		return nil, invalid("channel_ids", "at most 20 channels")
	}
	d.Flapping = DefaultFlapping
	if in.Flapping != nil {
		f := *in.Flapping
		if f.Enabled {
			if f.Transitions < 2 || f.Transitions > 20 {
				return nil, invalid("flapping.transitions", "must be between 2 and 20")
			}
			if f.WindowSeconds < 300 || f.WindowSeconds > 86400 {
				return nil, invalid("flapping.window_seconds", "must be between 300 and 86400")
			}
			if f.HoldSeconds < 60 || f.HoldSeconds > 86400 {
				return nil, invalid("flapping.hold_seconds", "must be between 60 and 86400")
			}
		} else {
			f = Flapping{Enabled: false, Transitions: DefaultFlapping.Transitions, WindowSeconds: DefaultFlapping.WindowSeconds, HoldSeconds: DefaultFlapping.HoldSeconds}
		}
		d.Flapping = f
	}
	if d.RunbookURL != "" {
		u, err := url.Parse(d.RunbookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(d.RunbookURL) > 2000 {
			return nil, invalid("runbook_url", "must be an http(s) URL of at most 2000 characters")
		}
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	if len(d.Labels) > 20 {
		return nil, invalid("labels", "at most 20 labels")
	}
	for k, v := range d.Labels {
		if !labelKeyRe.MatchString(k) {
			return nil, invalid("labels", "invalid key %q", k)
		}
		if len(v) > 256 {
			return nil, invalid("labels."+k, "value longer than 256 characters")
		}
	}
	return d, nil
}

func prefixField(prefix string, err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		f := prefix
		if ve.Field != "" {
			f += "." + ve.Field
		}
		return &ValidationError{Field: f, Msg: ve.Msg}
	}
	return err
}

// ConditionChanged reports whether a rule edit changes what is evaluated (type or condition), which resets state.
func ConditionChanged(old *Rule, next *Definition) bool {
	return old.Type != next.Type || !jsonEqual(old.ConditionJSON, next.ConditionJSON)
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// ParseStoredRule rebuilds the condition of a rule loaded from the database.
func ParseStoredRule(r *Rule) error {
	rt, ok := ruleTypes[r.Type]
	if !ok {
		return fmt.Errorf("unknown rule type %q", r.Type)
	}
	cond, err := rt.Parse(r.ConditionJSON)
	if err != nil {
		return fmt.Errorf("stored condition of rule %s: %w", r.ID, err)
	}
	r.Condition = cond
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
