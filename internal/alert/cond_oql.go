package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/oql"
)

// TypeOQL is a threshold on an OQL query (docs/contracts/alerting.md §2.10, D-065).
const TypeOQL = "oql"

// maxOQLWindowSteps bounds window/step in Range (each event is counted in that many windows).
const maxOQLWindowSteps = 720

type oqlType struct{}

func (oqlType) Name() string              { return TypeOQL }
func (oqlType) Available() bool           { return true }
func (oqlType) UnavailableReason() string { return "" }
func (oqlType) DefaultInterval() int      { return 60 }

// OQLCondition is an oql condition: one numeric column, optionally faceted (one series per facet group).
type OQLCondition struct {
	Query             string   `json:"query"`
	WindowSeconds     int      `json:"window_seconds"`
	Operator          string   `json:"operator"`
	Threshold         *float64 `json:"threshold"`
	RecoveryThreshold *float64 `json:"recovery_threshold"`
	MissingData       string   `json:"missing_data"`

	judge  Judge
	column string
	facets []string
}

func (oqlType) Parse(raw json.RawMessage) (Condition, error) {
	var c OQLCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	c.Query = strings.TrimSpace(c.Query)
	if c.Query == "" {
		return nil, invalid("query", "required")
	}
	p, err := oql.Compile(c.Query, oql.Options{Now: time.Now()})
	if err != nil {
		return nil, invalid("query", "%s", oql.Describe(c.Query, err))
	}
	if msg := alertPlanProblem(p); msg != "" {
		return nil, invalid("query", "%s", msg)
	}
	c.column, c.facets = p.Columns[0].Name, p.FacetNames()
	if err := validateWindow("window_seconds", &c.WindowSeconds, defaultWindow, 60, maxMetricWin); err != nil {
		return nil, err
	}
	if c.judge, err = validateThreshold(c.Operator, c.Threshold, c.RecoveryThreshold); err != nil {
		return nil, err
	}
	switch c.MissingData {
	case "":
		c.MissingData = "keep"
	case "keep", "ok", "breach":
	default:
		return nil, invalid("missing_data", "must be keep, ok or breach")
	}
	return c, nil
}

// alertPlanProblem explains why a query cannot be an alert condition ("" = it can).
func alertPlanProblem(p *oql.Plan) string {
	q := p.Query
	switch {
	case q.Timeseries != nil:
		return "TIMESERIES is not allowed: the rule is evaluated every interval over window_seconds"
	case q.Since != nil || q.Until != nil:
		return "SINCE and UNTIL are not allowed: window_seconds sets the range"
	case q.Compare != nil:
		return "COMPARE WITH is not allowed in alert conditions"
	case p.Kind == oql.KindHistogram:
		return "histogram is not allowed in alert conditions"
	case len(p.Columns) != 1:
		return "an alert condition needs exactly one result column"
	case p.Columns[0].Type != oql.TNumber:
		return "the result column must be a number"
	case len(p.Variables) > 0:
		return "variables are not allowed in alert conditions"
	}
	return ""
}

func (c OQLCondition) Judge() Judge          { return c.judge }
func (c OQLCondition) Window() time.Duration { return time.Duration(c.WindowSeconds) * time.Second }
func (c OQLCondition) IgnoresFor() bool      { return false }
func (c OQLCondition) Missing() string       { return c.MissingData }

func (c OQLCondition) Summary(s Sample, unit string) string {
	return fmt.Sprintf("%s over %s is %s (%s %s)%s", c.column, humanDuration(c.Window()), formatValue(s.Value),
		opSymbol(c.Operator), formatValue(c.judge.Threshold), labelSuffix(s.Labels))
}

// plan compiles the query for [from, to): raw data, and up to MaxSeries+1 facet groups unless the query has a LIMIT.
func (c OQLCondition) plan(from, to time.Time, lim Limits) (*oql.Plan, error) {
	p, err := oql.Compile(c.Query, oql.Options{Now: to, From: from, To: to, NoRollup: true,
		DefaultLimit: lim.MaxSeries + 1, MaxLimit: lim.MaxSeries + 1})
	if err != nil {
		return nil, oqlRuntimeError(c.Query, err)
	}
	return p, nil
}

func oqlRuntimeError(src string, err error) error {
	var oe *oql.Error
	if errors.As(err, &oe) {
		return &LimitError{Msg: "oql condition: " + oql.Describe(src, oe)}
	}
	var tm *oql.ErrTooManyRows
	if errors.As(err, &tm) {
		return &LimitError{Msg: tm.Error()}
	}
	return err
}

func (c OQLCondition) labels(facets []string) (string, map[string]string) {
	// Same key format as seriesKey: "*" without facets, else "name=value|…" with \ and | escaped.
	labels := make(map[string]string, len(facets))
	if len(facets) == 0 {
		return "*", labels
	}
	esc := strings.NewReplacer(`\`, `\\`, `|`, `\|`)
	parts := make([]string, 0, len(facets))
	for i, v := range facets {
		if i < len(c.facets) {
			labels[c.facets[i]] = v
			parts = append(parts, c.facets[i]+"="+esc.Replace(v))
		}
	}
	return strings.Join(parts, "|"), labels
}

func (c OQLCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	p, err := c.plan(end.Add(-c.Window()), end, lim)
	if err != nil {
		return nil, err
	}
	res, err := oql.Execute(ctx, sc, p)
	if err != nil {
		return nil, oqlRuntimeError(c.Query, err)
	}
	if res.Metadata.Truncated || len(res.Rows) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (> %d); add filters or a smaller LIMIT", lim.MaxSeries)}
	}
	out := &EvalResult{}
	for _, row := range res.Rows {
		v, ok := row.Values[0].(float64)
		if !ok {
			continue
		}
		key, labels := c.labels(row.Facets)
		out.Samples = append(out.Samples, Sample{Key: key, Labels: labels, Value: v})
	}
	sort.Slice(out.Samples, func(i, j int) bool { return out.Samples[i].Key < out.Samples[j].Key })
	return out, nil
}

func (c OQLCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	res := &RangeResult{Ends: ends}
	if len(ends) == 0 {
		return res, nil
	}
	w := c.Window()
	if k := ceilDiv(int64(w), int64(step)); k > maxOQLWindowSteps {
		return nil, &LimitError{Msg: fmt.Sprintf("window_seconds is more than %d times the preview step; shorten the window or the preview", maxOQLWindowSteps)}
	}
	origin := ends[0].Add(-step)
	p, err := c.plan(ends[0].Add(-w), ends[len(ends)-1], lim)
	if err != nil {
		return nil, err
	}
	series, err := oql.ExecuteWindows(ctx, sc, p, origin, step, w, len(ends), lim.MaxRows)
	if err != nil {
		return nil, oqlRuntimeError(c.Query, err)
	}
	for _, s := range series {
		key, labels := c.labels(s.Facets)
		res.Series = append(res.Series, RangeSeries{Key: key, Labels: labels, Values: s.Values})
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
