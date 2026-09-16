package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/slo"
)

// slo_burn rules (alerting.md §2.11, slo.md §3): multi-window multi-burn-rate alerting on the error budget of
// one SLO. A window breaches when the budget burns at least `factor` times too fast over its long window and
// still does so over the trailing short window; the series value is the largest such ratio over the windows,
// so the threshold is always 1 (the factors carry the configuration).

type sloBurnType struct{}

func (sloBurnType) Name() string              { return TypeSLOBurn }
func (sloBurnType) Available() bool           { return true }
func (sloBurnType) UnavailableReason() string { return "" }
func (sloBurnType) DefaultInterval() int      { return 60 }

// Burn window bounds (whole minutes).
const (
	minBurnLong    = 300
	maxBurnLong    = 86400
	minBurnShort   = 60
	maxBurnFactor  = 1000
	maxBurnWindows = 4
)

var burnNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// SLOBurnWindow is one long/short window pair of a slo_burn condition.
type SLOBurnWindow struct {
	Name         string  `json:"name"`
	Factor       float64 `json:"factor"`
	LongSeconds  int     `json:"long_seconds"`
	ShortSeconds int     `json:"short_seconds"`
}

func (w SLOBurnWindow) burnWindow() slo.BurnWindow {
	return slo.BurnWindow{Name: w.Name, Factor: w.Factor,
		Long:  time.Duration(w.LongSeconds) * time.Second,
		Short: time.Duration(w.ShortSeconds) * time.Second}
}

// SLOBurnCondition is a slo_burn condition (alerting.md §2.11).
type SLOBurnCondition struct {
	SLOID       string          `json:"slo_id"`
	Windows     []SLOBurnWindow `json:"windows"`
	MinRequests float64         `json:"min_requests"`
}

// defaultBurnWindows mirrors slo.DefaultBurnWindows as condition input.
func defaultBurnWindows() []SLOBurnWindow {
	out := make([]SLOBurnWindow, 0, len(slo.DefaultBurnWindows))
	for _, w := range slo.DefaultBurnWindows {
		out = append(out, SLOBurnWindow{Name: w.Name, Factor: w.Factor,
			LongSeconds: int(w.Long / time.Second), ShortSeconds: int(w.Short / time.Second)})
	}
	return out
}

func (sloBurnType) Parse(raw json.RawMessage) (Condition, error) {
	var c SLOBurnCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if !ValidUUID(c.SLOID) {
		return nil, invalid("slo_id", "required, must be the id of an SLO of this organization")
	}
	if len(c.Windows) == 0 {
		c.Windows = defaultBurnWindows()
	}
	if len(c.Windows) > maxBurnWindows {
		return nil, invalid("windows", "at most %d windows", maxBurnWindows)
	}
	seen := map[string]bool{}
	for i := range c.Windows {
		w := &c.Windows[i]
		field := fmt.Sprintf("windows[%d]", i)
		if !burnNameRe.MatchString(w.Name) {
			return nil, invalid(field+".name", "must match [a-z0-9_-]{1,32}")
		}
		if seen[w.Name] {
			return nil, invalid(field+".name", "duplicate window name %q", w.Name)
		}
		seen[w.Name] = true
		if !finite(w.Factor) || w.Factor <= 0 || w.Factor > maxBurnFactor {
			return nil, invalid(field+".factor", "must be greater than 0 and at most %d", maxBurnFactor)
		}
		if err := validateWindow(field+".long_seconds", &w.LongSeconds, 3600, minBurnLong, maxBurnLong); err != nil {
			return nil, err
		}
		if err := validateWindow(field+".short_seconds", &w.ShortSeconds, w.LongSeconds/12, minBurnShort, maxBurnLong); err != nil {
			return nil, err
		}
		w.LongSeconds = int(ceilDiv(int64(w.LongSeconds), 60) * 60) // whole minutes (1-minute rollup)
		w.ShortSeconds = int(ceilDiv(int64(w.ShortSeconds), 60) * 60)
		if w.ShortSeconds > w.LongSeconds {
			return nil, invalid(field+".short_seconds", "must not be longer than long_seconds")
		}
	}
	if c.MinRequests < 0 || !finite(c.MinRequests) {
		return nil, invalid("min_requests", "must be >= 0")
	}
	return c, nil
}

// Judge is fixed: the value is the burn ratio, which reaches 1 exactly at the window's factor.
func (c SLOBurnCondition) Judge() Judge { return Judge{Operator: "gte", Threshold: 1, Recovery: 1} }

// Window is the longest long window (the data a single evaluation needs).
func (c SLOBurnCondition) Window() time.Duration {
	var longest time.Duration
	for _, w := range c.Windows {
		longest = max(longest, time.Duration(w.LongSeconds)*time.Second)
	}
	return longest
}

func (c SLOBurnCondition) IgnoresFor() bool { return false }
func (c SLOBurnCondition) Missing() string  { return "keep" }

func (c SLOBurnCondition) window(name string) (SLOBurnWindow, bool) {
	for _, w := range c.Windows {
		if w.Name == name {
			return w, true
		}
	}
	return SLOBurnWindow{}, false
}

func (c SLOBurnCondition) Summary(s Sample, _ string) string {
	name := s.Labels["slo.name"]
	if name == "" {
		name = "SLO"
	}
	w, ok := c.window(s.Labels["slo.window"])
	if !ok {
		return fmt.Sprintf("error budget of %s burns %s times its alert threshold", name, formatValue(s.Value))
	}
	return fmt.Sprintf("error budget of %s burns %s× too fast over %s and %s (≥ %s×)", name,
		formatValue(s.Value*w.Factor), humanDuration(time.Duration(w.LongSeconds)*time.Second),
		humanDuration(time.Duration(w.ShortSeconds)*time.Second), formatValue(w.Factor))
}

// SLOLookup gives slo_burn conditions the SLO definitions of one organization.
type SLOLookup struct {
	OrgID string
	Store slo.Store
}

type sloKey struct{}

// WithSLOs makes the organization's SLO definitions available to slo_burn conditions evaluated with ctx.
func WithSLOs(ctx context.Context, l SLOLookup) context.Context {
	return context.WithValue(ctx, sloKey{}, l)
}

func sloLookupFrom(ctx context.Context) (SLOLookup, bool) {
	l, ok := ctx.Value(sloKey{}).(SLOLookup)
	return l, ok && l.Store != nil && l.OrgID != ""
}

// ErrSLOUnavailable is returned when slo_burn rules are evaluated without the SLO store (PostgreSQL).
var ErrSLOUnavailable = errors.New("service level objectives (PostgreSQL) are not available")

func (c SLOBurnCondition) definition(ctx context.Context) (*slo.SLO, error) {
	l, ok := sloLookupFrom(ctx)
	if !ok {
		return nil, ErrSLOUnavailable
	}
	s, err := l.Store.Get(ctx, l.OrgID, c.SLOID)
	if errors.Is(err, slo.ErrNotFound) {
		return nil, fmt.Errorf("SLO %s no longer exists", c.SLOID)
	}
	return s, err
}

func (c SLOBurnCondition) labels(s *slo.SLO, window string) (string, map[string]string) {
	labels := map[string]string{"slo.id": s.ID, "slo.name": s.Name, "slo.window": window,
		"service.name": s.ServiceName}
	if s.ServiceNamespace != nil {
		labels["service.namespace"] = *s.ServiceNamespace
	}
	if s.Environment != nil {
		labels["environment"] = *s.Environment
	}
	return "slo.id=" + s.ID, labels
}

// burnWindows converts the condition's windows for the slo package.
func (c SLOBurnCondition) burnWindows() []slo.BurnWindow {
	out := make([]slo.BurnWindow, 0, len(c.Windows))
	for _, w := range c.Windows {
		out = append(out, w.burnWindow())
	}
	return out
}

// worst returns the largest burn ratio over the windows ending at end and the name of that window.
// NaN when no window has enough requests (missing data).
func (c SLOBurnCondition) worst(buckets []slo.Bucket, s *slo.SLO, end time.Time) (float64, string) {
	value, name := math.NaN(), ""
	for _, w := range c.burnWindows() {
		b := slo.BurnAt(buckets, slo.BurnStep, end, s.ObjectiveFraction(), w)
		if math.IsNaN(b.Ratio) || b.Long.Requests < c.MinRequests {
			continue
		}
		if math.IsNaN(value) || b.Ratio > value {
			value, name = b.Ratio, w.Name
		}
	}
	return value, name
}

func (c SLOBurnCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, _ Limits) (*EvalResult, error) {
	s, err := c.definition(ctx)
	if err != nil {
		return nil, err
	}
	endM := end.Truncate(time.Minute) // complete minutes only (apm.md §10)
	buckets, err := slo.LoadBurn(ctx, sc, s, endM, c.burnWindows())
	if err != nil {
		return nil, err
	}
	res := &EvalResult{Unit: "1"}
	if v, name := c.worst(buckets, s, endM); !math.IsNaN(v) {
		key, labels := c.labels(s, name)
		res.Samples = append(res.Samples, Sample{Key: key, Labels: labels, Value: v})
	}
	return res, nil
}

func (c SLOBurnCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, _ Limits) (*RangeResult, error) {
	s, err := c.definition(ctx)
	if err != nil {
		return nil, err
	}
	stepM := time.Duration(ceilDiv(int64(step), int64(time.Minute))) * time.Minute
	fromM, toM := from.Truncate(time.Minute), to.Truncate(time.Minute)
	ends := rangeEnds(fromM, toM, stepM)
	res := &RangeResult{Ends: ends, Unit: "1", Approximate: stepM != step}
	if len(ends) == 0 {
		return res, nil
	}
	buckets, err := slo.Load(ctx, sc, s, fromM.Add(-c.Window()), toM, slo.BurnStep)
	if err != nil {
		return nil, err
	}
	key, labels := c.labels(s, "")
	rs := RangeSeries{Key: key, Labels: labels, Values: nanSlice(len(ends))}
	for i, e := range ends {
		rs.Values[i], _ = c.worst(buckets, s, e)
	}
	res.Series = append(res.Series, rs)
	return res, nil
}
