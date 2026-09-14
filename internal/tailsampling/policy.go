// Package tailsampling implements the tail-based sampling stage between ingest and the processor
// (D-075, docs/contracts/apm.md §4.2, docs/operations/tail-sampling.md): spans of the raw traces topic
// are buffered per trace, a per-tenant policy decides once the trace is complete, and the kept spans
// are produced to the sampled traces topic with tracestate `ot=th` rewritten to the composite
// head × tail probability so APM counts stay unbiased.
package tailsampling

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Rule types.
const (
	RuleError     = "error"     // any span has status ERROR
	RuleLatency   = "latency"   // trace duration (or a span of Service) >= ThresholdMs
	RuleService   = "service"   // any span of one of Services
	RuleRoute     = "route"     // any span whose http.route / span name matches Route (glob), optionally of Service
	RuleAttribute = "attribute" // any span or its resource has Key (= Value when Value is set)
)

// Limits of a policy document.
const (
	MaxRules        = 50
	MaxNameBytes    = 64
	MaxValueBytes   = 512
	MaxServices     = 50
	MaxPolicyBytes  = 64 << 10
	MaxRateLimit    = 10_000_000
	MaxThresholdMs  = 3_600_000
	DefaultRuleName = "baseline"
)

// Rule is one entry of a policy. Rules are evaluated in order; the first matching rule gives the
// trace's keep probability (Ratio), traces matching no rule use Policy.BaselineRatio.
type Rule struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Ratio       *float64 `json:"ratio,omitempty"` // keep probability in [0, 1]; default 1
	ThresholdMs int64    `json:"threshold_ms,omitempty"`
	Service     string   `json:"service,omitempty"`
	Services    []string `json:"services,omitempty"`
	Route       string   `json:"route,omitempty"`
	Key         string   `json:"key,omitempty"`
	Value       string   `json:"value,omitempty"`
}

// KeepRatio returns the rule's keep probability.
func (r Rule) KeepRatio() float64 {
	if r.Ratio == nil {
		return 1
	}
	return *r.Ratio
}

// Policy is a tenant's tail sampling policy (PostgreSQL tail_sampling_policies.policy).
type Policy struct {
	// Enabled false keeps every trace of the tenant unchanged (the policy is stored but inactive).
	Enabled       bool    `json:"enabled"`
	BaselineRatio float64 `json:"baseline_ratio"`
	// MaxSpansPerSecond > 0 limits the kept spans of the tenant per sampler instance; excess traces
	// are sampled out with a probability that is folded into their weights (unbiased).
	MaxSpansPerSecond float64 `json:"max_spans_per_second"`
	Rules             []Rule  `json:"rules"`
}

// KeepAll is the policy of tenants without a stored policy and without a configured default.
func KeepAll() Policy { return Policy{Enabled: false, BaselineRatio: 1, Rules: []Rule{}} }

// ParsePolicy decodes and validates a policy document. Unknown fields are rejected.
func ParsePolicy(b []byte) (Policy, error) {
	if len(b) > MaxPolicyBytes {
		return Policy{}, fmt.Errorf("policy is larger than %d bytes", MaxPolicyBytes)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("policy: %w", err)
	}
	if p.Rules == nil {
		p.Rules = []Rule{}
	}
	return p, p.Validate()
}

// ErrInvalidPolicy wraps validation errors.
var ErrInvalidPolicy = errors.New("invalid tail sampling policy")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}

func validRatio(r float64) bool { return r >= 0 && r <= 1 }

// Validate checks the policy.
func (p Policy) Validate() error {
	if !validRatio(p.BaselineRatio) {
		return invalid("baseline_ratio must be between 0 and 1")
	}
	if p.MaxSpansPerSecond < 0 || p.MaxSpansPerSecond > MaxRateLimit {
		return invalid("max_spans_per_second must be between 0 (unlimited) and %d", MaxRateLimit)
	}
	if len(p.Rules) > MaxRules {
		return invalid("at most %d rules", MaxRules)
	}
	names := map[string]bool{}
	for i, r := range p.Rules {
		at := fmt.Sprintf("rules[%d]", i)
		if r.Name == "" || len(r.Name) > MaxNameBytes || strings.ContainsAny(r.Name, "\"\\\n\r\t") {
			return invalid("%s.name must be 1-%d bytes without quotes or control characters", at, MaxNameBytes)
		}
		if r.Name == DefaultRuleName || r.Name == "rate_limit" || names[r.Name] {
			return invalid("%s.name %q is reserved or used twice", at, r.Name)
		}
		names[r.Name] = true
		if r.Ratio != nil && !validRatio(*r.Ratio) {
			return invalid("%s.ratio must be between 0 and 1", at)
		}
		for _, s := range append([]string{r.Service, r.Route, r.Key, r.Value}, r.Services...) {
			if len(s) > MaxValueBytes {
				return invalid("%s: values are limited to %d bytes", at, MaxValueBytes)
			}
		}
		switch r.Type {
		case RuleError:
			if r.ThresholdMs != 0 || r.Route != "" || r.Key != "" || len(r.Services) > 0 {
				return invalid("%s: an error rule only takes name, ratio and service", at)
			}
		case RuleLatency:
			if r.ThresholdMs <= 0 || r.ThresholdMs > MaxThresholdMs {
				return invalid("%s.threshold_ms must be between 1 and %d", at, MaxThresholdMs)
			}
		case RuleService:
			if len(r.Services) == 0 || len(r.Services) > MaxServices {
				return invalid("%s.services must list 1-%d service names", at, MaxServices)
			}
			for _, s := range r.Services {
				if s == "" {
					return invalid("%s.services must not contain empty names", at)
				}
			}
		case RuleRoute:
			if r.Route == "" {
				return invalid("%s.route is required", at)
			}
			if _, err := path.Match(r.Route, ""); err != nil {
				return invalid("%s.route is not a valid pattern", at)
			}
		case RuleAttribute:
			if r.Key == "" {
				return invalid("%s.key is required", at)
			}
		default:
			return invalid("%s.type must be one of error, latency, service, route, attribute", at)
		}
	}
	return nil
}

// TraceSummary is what policies look at. It is computed from the buffered spans of one trace.
type TraceSummary struct {
	Spans      int
	DurationMs float64
	// Per span facts (one entry per span).
	Items []SpanFacts
}

// SpanFacts are the policy-relevant properties of one span.
type SpanFacts struct {
	Service    string
	Name       string
	Route      string
	Error      bool
	DurationMs float64
	Attributes map[string]string
	Resource   map[string]string
}

// Decision is the policy outcome for one trace before rate limiting.
type Decision struct {
	Rule  string  // matching rule name, DefaultRuleName for the baseline
	Ratio float64 // keep probability
}

// Evaluate returns the first matching rule (or the baseline) for the trace.
func (p Policy) Evaluate(t *TraceSummary) Decision {
	if !p.Enabled {
		return Decision{Rule: DefaultRuleName, Ratio: 1}
	}
	for _, r := range p.Rules {
		if r.Matches(t) {
			return Decision{Rule: r.Name, Ratio: r.KeepRatio()}
		}
	}
	return Decision{Rule: DefaultRuleName, Ratio: p.BaselineRatio}
}

// Matches reports whether the rule applies to the trace.
func (r Rule) Matches(t *TraceSummary) bool {
	switch r.Type {
	case RuleLatency:
		if r.Service == "" {
			return t.DurationMs >= float64(r.ThresholdMs)
		}
	}
	for i := range t.Items {
		s := &t.Items[i]
		if r.Service != "" && r.Type != RuleService && s.Service != r.Service {
			continue
		}
		switch r.Type {
		case RuleError:
			if s.Error {
				return true
			}
		case RuleLatency:
			if s.DurationMs >= float64(r.ThresholdMs) {
				return true
			}
		case RuleService:
			for _, name := range r.Services {
				if s.Service == name {
					return true
				}
			}
		case RuleRoute:
			if MatchRoute(r.Route, s.Route) || (s.Route == "" && MatchRoute(r.Route, s.Name)) {
				return true
			}
		case RuleAttribute:
			if v, ok := s.Attributes[r.Key]; ok && (r.Value == "" || v == r.Value) {
				return true
			}
			if v, ok := s.Resource[r.Key]; ok && (r.Value == "" || v == r.Value) {
				return true
			}
		}
	}
	return false
}

// MatchRoute matches a glob pattern (path.Match syntax, and a trailing "*" also matches "/").
func MatchRoute(pattern, value string) bool {
	if value == "" {
		return false
	}
	if pattern == value {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok && !strings.ContainsAny(prefix, "*?[\\") {
		return strings.HasPrefix(value, prefix)
	}
	ok, _ := path.Match(pattern, value)
	return ok
}
