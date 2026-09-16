package alert

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// RuleType parses the condition of one rule type. New types (e.g. APM conditions) register an implementation
// in init(); a type that is not Available is listed but cannot be created or previewed.
type RuleType interface {
	Name() string
	Available() bool
	UnavailableReason() string
	// DefaultInterval is interval_seconds when the input omits it.
	DefaultInterval() int
	Parse(raw json.RawMessage) (Condition, error)
}

// Condition is a parsed, validated condition. Implementations must only read ClickHouse through sc, which is
// bound to the rule's organization by the caller.
type Condition interface {
	// Evaluate returns the value of every series for the window ending at end.
	Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error)
	// Range returns, for every step end in (from, to], the value of every series (NaN = no value).
	Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error)
	// Judge returns the breach/recovery comparison.
	Judge() Judge
	// Missing is the missing-data behaviour: keep, ok, breach, zero (value 0) or expire (resolve as expired).
	Missing() string
	// Window is the evaluation window.
	Window() time.Duration
	// IgnoresFor reports that for_seconds does not apply (event conditions).
	IgnoresFor() bool
	// Summary describes a breaching sample for incidents and notifications.
	Summary(s Sample, unit string) string
}

// Limits bound one evaluation or preview.
type Limits struct {
	MaxSeries int // groups per evaluation
	MaxRows   int // raw result rows per query
}

// DefaultLimits are used when a caller passes zero values.
var DefaultLimits = Limits{MaxSeries: 1000, MaxRows: 200000}

func (l Limits) withDefaults() Limits {
	if l.MaxSeries <= 0 {
		l.MaxSeries = DefaultLimits.MaxSeries
	}
	if l.MaxRows <= 0 {
		l.MaxRows = DefaultLimits.MaxRows
	}
	return l
}

// Sample is the value of one series at one evaluation.
type Sample struct {
	Key    string
	Labels map[string]string
	Value  float64
}

// EvalResult is the outcome of Evaluate.
type EvalResult struct {
	Samples []Sample
	Unit    string
}

// RangeSeries is one series of a range evaluation.
type RangeSeries struct {
	Key    string
	Labels map[string]string
	Values []float64 // aligned with RangeResult.Ends; NaN = no value
}

// RangeResult is the outcome of Range.
type RangeResult struct {
	Ends        []time.Time
	Series      []RangeSeries
	Unit        string
	Truncated   bool
	Approximate bool
}

// Judge compares values with the threshold (breach) and the recovery threshold (recovered).
type Judge struct {
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
	Recovery  float64 `json:"recovery_threshold"`
}

func compare(op string, v, t float64) bool {
	switch op {
	case "gt":
		return v > t
	case "gte":
		return v >= t
	case "lt":
		return v < t
	case "lte":
		return v <= t
	}
	return false
}

// Breach reports value OP threshold. NaN never breaches.
func (j Judge) Breach(v float64) bool { return !math.IsNaN(v) && compare(j.Operator, v, j.Threshold) }

// Recovered reports NOT (value OP recovery_threshold). NaN never recovers.
func (j Judge) Recovered(v float64) bool {
	return !math.IsNaN(v) && !compare(j.Operator, v, j.Recovery)
}

// RuleTypeInfo is listed by GET /alerts/rule-types.
type RuleTypeInfo struct {
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

var (
	ruleTypes = map[string]RuleType{}
	typeOrder []string
)

func register(rt RuleType) {
	if _, dup := ruleTypes[rt.Name()]; dup {
		panic("alert: rule type registered twice: " + rt.Name())
	}
	ruleTypes[rt.Name()] = rt
	typeOrder = append(typeOrder, rt.Name())
}

// Register adds a rule type (used by packages that plug in new conditions, e.g. APM).
func Register(rt RuleType) { register(rt) }

// Replace swaps the implementation of an already registered type (e.g. APM becoming available).
func Replace(rt RuleType) {
	if _, ok := ruleTypes[rt.Name()]; !ok {
		register(rt)
		return
	}
	ruleTypes[rt.Name()] = rt
}

// RuleTypes lists the registered types in registration order.
func RuleTypes() []RuleTypeInfo {
	out := make([]RuleTypeInfo, 0, len(typeOrder))
	for _, n := range typeOrder {
		rt := ruleTypes[n]
		info := RuleTypeInfo{Type: n, Available: rt.Available()}
		if !info.Available {
			info.Reason = rt.UnavailableReason()
		}
		out = append(out, info)
	}
	return out
}

func init() {
	register(metricType{})
	register(logType{})
	register(noDataType{})
	register(discoveryType{})
	register(apmType{})
	register(apmNoDataType{})
	register(apmErrorType{})
	register(oqlType{})     // cond_oql.go
	register(sloBurnType{}) // cond_slo.go
}

// decodeStrict unmarshals raw into v rejecting unknown fields.
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("", "invalid condition: %v", err)
	}
	return nil
}
