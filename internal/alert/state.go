package alert

import (
	"math"
	"time"
)

// SeriesState is the persisted evaluation state of one series (alert_series_state). The zero value with
// State "" is treated as ok (no row).
type SeriesState struct {
	Key             string
	Labels          map[string]string
	State           string
	PendingSince    time.Time
	FiringSince     time.Time
	RecoveringSince time.Time
	LastValue       float64 // NaN = unknown
	LastSeenAt      time.Time
	IncidentID      string
	Transitions     []time.Time // times the series entered firing (last 10)
	LastNotifiedAt  time.Time
	RenotifyCount   int
	// Incident is the incident of IncidentID, loaded with the row (not stored in the series table).
	Incident *Incident
}

func (s SeriesState) state() string {
	if s.State == "" {
		return StateOK
	}
	return s.State
}

// StepConfig are the rule settings that drive the state machine.
type StepConfig struct {
	Judge       Judge
	For         time.Duration
	RecoveryFor time.Duration
	Interval    time.Duration
	Delay       time.Duration
	Flapping    Flapping
	Missing     string        // keep, ok, breach, zero, expire
	ExpireAfter time.Duration // keep: resolve as expired after this long without data
}

// StepInput is one evaluation of one series.
type StepInput struct {
	End         time.Time
	PrevEvalEnd time.Time // zero = first evaluation
	Present     bool
	Value       float64
}

// StepOutcome is the result of one state machine step.
type StepOutcome struct {
	Next       SeriesState
	Transition string // "", pending, firing, ok (from pending) or resolved
	Open       bool   // entered firing: open an incident
	Resolve    string // resolve reason when the incident resolves
	Flapping   bool   // recovery hold was extended because the series is flapping
	Persist    bool   // write Next (non-ok, or ok with flapping history)
	Delete     bool   // remove the stored row
}

const maxTransitions = 10

// Step advances the state machine of one series (docs/contracts/alerting.md §3.2–3.4).
func Step(prev SeriesState, in StepInput, cfg StepConfig) StepOutcome {
	s := prev
	s.State = prev.state()
	s.Transitions = pruneTransitions(prev.Transitions, in.End, cfg.Flapping)
	out := StepOutcome{}
	end := in.End

	// A pending timer does not survive a gap in evaluation: continuity was not observed.
	if s.State == StatePending && !in.PrevEvalEnd.IsZero() && end.Sub(in.PrevEvalEnd) > 2*cfg.Interval+cfg.Delay {
		s.PendingSince = end
	}

	breach, recovered := false, false
	value := math.NaN()
	missingResolve := ""
	if in.Present {
		value = in.Value
		s.LastValue = value
		s.LastSeenAt = end
		breach = cfg.Judge.Breach(value)
		recovered = cfg.Judge.Recovered(value)
	} else {
		switch cfg.Missing {
		case "breach":
			breach = true
		case "zero":
			value, s.LastValue, s.LastSeenAt = 0, 0, end
			breach, recovered = cfg.Judge.Breach(0), cfg.Judge.Recovered(0)
		case "ok":
			recovered, missingResolve = true, ReasonNoData
		case "expire":
			recovered, missingResolve = true, ReasonExpired
		default: // keep
			if s.State != StateOK && !s.LastSeenAt.IsZero() && cfg.ExpireAfter > 0 && end.Sub(s.LastSeenAt) > cfg.ExpireAfter {
				recovered, missingResolve = true, ReasonExpired
			} else {
				out.Next = s
				out.Persist = s.State != StateOK
				return out
			}
		}
	}

	fire := func() {
		s.State = StateFiring
		s.FiringSince = end
		s.PendingSince = time.Time{}
		s.RecoveringSince = time.Time{}
		s.Transitions = append(s.Transitions, end)
		if len(s.Transitions) > maxTransitions {
			s.Transitions = s.Transitions[len(s.Transitions)-maxTransitions:]
		}
		s.LastNotifiedAt = end
		s.RenotifyCount = 0
		out.Open = true
		out.Transition = StateFiring
	}

	switch s.State {
	case StateOK:
		if breach {
			if cfg.For <= 0 {
				fire()
			} else {
				s.State = StatePending
				s.PendingSince = end
				out.Transition = StatePending
			}
		}
	case StatePending:
		switch {
		case breach && end.Sub(s.PendingSince) >= cfg.For:
			fire()
		case breach:
		default:
			s.State = StateOK
			s.PendingSince = time.Time{}
			out.Transition = StateOK
		}
	case StateFiring:
		switch {
		case breach:
			s.RecoveringSince = time.Time{}
		case recovered:
			hold := cfg.RecoveryFor
			if missingResolve == "" && isFlapping(s.Transitions, end, cfg.Flapping) && time.Duration(cfg.Flapping.HoldSeconds)*time.Second > hold {
				hold = time.Duration(cfg.Flapping.HoldSeconds) * time.Second
				out.Flapping = true
			}
			if missingResolve != "" {
				hold = 0
			}
			if s.RecoveringSince.IsZero() {
				s.RecoveringSince = end
			}
			if end.Sub(s.RecoveringSince) >= hold {
				reason := ReasonRecovered
				if missingResolve != "" {
					reason = missingResolve
				}
				s = resetToOK(s)
				out.Resolve = reason
				out.Transition = "resolved"
			}
		default: // hysteresis band: stay firing, cancel a running recovery hold
			s.RecoveringSince = time.Time{}
		}
	}

	if !cfg.Flapping.Enabled {
		s.Transitions = nil // only needed for flapping detection
	}
	out.Next = s
	switch {
	case s.State != StateOK:
		out.Persist = true
	case len(s.Transitions) > 0:
		out.Persist = true
	default:
		out.Delete = prev.State != "" || prev.IncidentID != ""
	}
	return out
}

func resetToOK(s SeriesState) SeriesState {
	s.State = StateOK
	s.PendingSince, s.FiringSince, s.RecoveringSince = time.Time{}, time.Time{}, time.Time{}
	s.IncidentID = ""
	s.Incident = nil
	s.LastNotifiedAt = time.Time{}
	s.RenotifyCount = 0
	return s
}

func pruneTransitions(ts []time.Time, end time.Time, f Flapping) []time.Time {
	window := time.Duration(f.WindowSeconds) * time.Second
	if window <= 0 {
		window = time.Duration(DefaultFlapping.WindowSeconds) * time.Second
	}
	out := make([]time.Time, 0, len(ts))
	for _, t := range ts {
		if end.Sub(t) <= window {
			out = append(out, t)
		}
	}
	return out
}

// isFlapping reports whether the series entered firing at least f.Transitions times within the flapping window.
func isFlapping(ts []time.Time, end time.Time, f Flapping) bool {
	if !f.Enabled || f.Transitions <= 0 {
		return false
	}
	return len(pruneTransitions(ts, end, f)) >= f.Transitions
}
