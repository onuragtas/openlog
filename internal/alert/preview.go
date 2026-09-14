package alert

import (
	"context"
	"math"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// PreviewTransition is a state change a series would have had.
type PreviewTransition struct {
	At    time.Time
	State string
	Value float64 // NaN = no value
}

// PreviewIncident is an incident a series would have had.
type PreviewIncident struct {
	OpenedAt   time.Time
	ResolvedAt *time.Time
	Peak       float64 // NaN = unknown
}

// PreviewSeries is one series of a preview.
type PreviewSeries struct {
	Key         string
	Labels      map[string]string
	Ends        []time.Time
	Values      []float64
	Transitions []PreviewTransition
	Incidents   []PreviewIncident
}

// PreviewResult is the response of a rule preview.
type PreviewResult struct {
	From, To    time.Time
	Step        time.Duration
	Judge       Judge
	HasOperator bool
	Unit        string
	Series      []PreviewSeries
	Truncated   bool
	Approximate bool
}

// maxPreviewSteps bounds the number of simulated evaluations per series.
const maxPreviewSteps = 1440

// PreviewStep chooses the simulation step: the rule interval, raised to a multiple of it so that at most
// maxPreviewSteps evaluations cover the range.
func PreviewStep(interval time.Duration, rng time.Duration) time.Duration {
	step := interval
	if n := int64(rng / step); n > maxPreviewSteps {
		mult := ceilDiv(int64(rng), int64(interval)*maxPreviewSteps)
		step = time.Duration(mult) * interval
	}
	return step
}

// Preview evaluates def over the last hours before now (docs/contracts/alerting.md, POST /alerts/rules/preview).
func Preview(ctx context.Context, sc *query.Scope, def *Definition, hours int, now time.Time, delay time.Duration, lim Limits) (*PreviewResult, error) {
	if hours < 1 || hours > 24 {
		return nil, invalid("hours", "must be between 1 and 24")
	}
	rng := time.Duration(hours) * time.Hour
	step := PreviewStep(def.Interval(), rng)
	to := now.Add(-delay).Truncate(step)
	from := to.Add(-rng)
	rr, err := def.Condition.Range(ctx, sc, from, to, step, lim)
	if err != nil {
		return nil, err
	}
	res := &PreviewResult{From: from, To: to, Step: step, Judge: def.Condition.Judge(), Unit: rr.Unit,
		Truncated: rr.Truncated, Approximate: rr.Approximate || step != def.Interval()}
	switch def.Type {
	case TypeMetricThreshold, TypeLogMatch, TypeOQL:
		res.HasOperator = true
	}
	cfg := StepConfigFor(def, 0)
	cfg.Interval = step
	for _, s := range rr.Series {
		res.Series = append(res.Series, simulate(s, rr.Ends, cfg, def.Condition.Judge()))
	}
	return res, nil
}

func simulate(s RangeSeries, ends []time.Time, cfg StepConfig, j Judge) PreviewSeries {
	ps := PreviewSeries{Key: s.Key, Labels: s.Labels, Ends: ends, Values: s.Values}
	st := SeriesState{Key: s.Key, LastValue: math.NaN()}
	var prevEnd time.Time
	var open *PreviewIncident
	seen := false
	for i, e := range ends {
		v := s.Values[i]
		present := !math.IsNaN(v)
		if !present && !seen {
			prevEnd = e
			continue // the series did not exist yet
		}
		seen = seen || present
		before := st.state()
		out := Step(st, StepInput{End: e, PrevEvalEnd: prevEnd, Present: present, Value: v}, cfg)
		st = out.Next
		prevEnd = e
		if out.Open {
			// Simulated incident id so later steps see an open incident (renotify/resolve bookkeeping).
			st.IncidentID = "preview"
			st.Incident = &Incident{State: IncidentOpen}
		}
		if after := st.state(); after != before || out.Resolve != "" {
			ps.Transitions = append(ps.Transitions, PreviewTransition{At: e, State: after, Value: v})
		}
		switch {
		case out.Open:
			ps.Incidents = append(ps.Incidents, PreviewIncident{OpenedAt: e, Peak: v})
			open = &ps.Incidents[len(ps.Incidents)-1]
		case out.Resolve != "" && open != nil:
			t := e
			open.ResolvedAt = &t
			open = nil
		case open != nil && present:
			if math.IsNaN(open.Peak) || worse(j.Operator, v, open.Peak) {
				open.Peak = v
			}
		}
	}
	return ps
}

// worse reports whether v is further on the breaching side than cur.
func worse(op string, v, cur float64) bool {
	if op == "lt" || op == "lte" {
		return v < cur
	}
	return v > cur
}
