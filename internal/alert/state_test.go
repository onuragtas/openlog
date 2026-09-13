package alert

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

func cfgGT(threshold, recovery float64, forD time.Duration) StepConfig {
	return StepConfig{
		Judge: Judge{Operator: "gt", Threshold: threshold, Recovery: recovery}, For: forD,
		Interval: time.Minute, Delay: 15 * time.Second, Flapping: Flapping{Enabled: false, WindowSeconds: 3600},
		Missing: "keep", ExpireAfter: time.Hour,
	}
}

// run feeds values (NaN = missing) at one-minute steps and returns the outcomes.
func run(cfg StepConfig, values ...float64) ([]StepOutcome, SeriesState) {
	s := SeriesState{LastValue: math.NaN()}
	var outs []StepOutcome
	prev := time.Time{}
	for i, v := range values {
		end := t0.Add(time.Duration(i) * time.Minute)
		out := Step(s, StepInput{End: end, PrevEvalEnd: prev, Present: !math.IsNaN(v), Value: v}, cfg)
		if out.Open {
			out.Next.IncidentID = "inc"
			out.Next.Incident = &Incident{ID: "inc", State: IncidentOpen}
		}
		s = out.Next
		prev = end
		outs = append(outs, out)
	}
	return outs, s
}

func transitions(outs []StepOutcome) []string {
	var tr []string
	for _, o := range outs {
		tr = append(tr, o.Transition)
	}
	return tr
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStepForDuration(t *testing.T) {
	outs, s := run(cfgGT(0.9, 0.9, 2*time.Minute), 0.5, 0.95, 0.96, 0.97, 0.98)
	want := []string{"", "pending", "", "firing", ""}
	if got := transitions(outs); !eq(got, want) {
		t.Fatalf("transitions %v, want %v", got, want)
	}
	if !outs[3].Open || s.State != StateFiring || !s.FiringSince.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("not firing after for: %+v", s)
	}
	// A breach that ends before `for` returns to ok without an incident.
	outs, s = run(cfgGT(0.9, 0.9, 3*time.Minute), 0.95, 0.95, 0.5)
	if got := transitions(outs); !eq(got, []string{"pending", "", "ok"}) || s.State != StateOK {
		t.Fatalf("short breach: %v %+v", got, s)
	}
	for _, o := range outs {
		if o.Open {
			t.Fatal("incident opened before for elapsed")
		}
	}
	if !outs[2].Delete {
		t.Error("ok series without history not deleted")
	}
}

func TestStepForZeroFiresImmediately(t *testing.T) {
	outs, _ := run(cfgGT(0.9, 0.9, 0), 0.95, 0.2)
	if !outs[0].Open || outs[0].Transition != StateFiring {
		t.Fatalf("for=0 did not fire: %+v", outs[0])
	}
	if outs[1].Resolve != ReasonRecovered || outs[1].Transition != "resolved" {
		t.Fatalf("did not resolve: %+v", outs[1])
	}
}

func TestStepHysteresis(t *testing.T) {
	// threshold 0.9, recovery 0.8: 0.85 keeps firing, 0.79 recovers.
	outs, s := run(cfgGT(0.9, 0.8, 0), 0.95, 0.85, 0.88, 0.79)
	if !outs[0].Open || outs[1].Resolve != "" || outs[2].Resolve != "" {
		t.Fatalf("hysteresis band resolved: %v", transitions(outs))
	}
	if outs[3].Resolve != ReasonRecovered || s.State != StateOK {
		t.Fatalf("not recovered below recovery threshold: %+v", outs[3])
	}
	// A pending series in the band returns to ok (not breaching).
	outs, _ = run(cfgGT(0.9, 0.8, 5*time.Minute), 0.95, 0.85)
	if outs[1].Transition != StateOK {
		t.Fatalf("pending in band: %v", transitions(outs))
	}
}

func TestStepRecoveryFor(t *testing.T) {
	cfg := cfgGT(0.9, 0.9, 0)
	cfg.RecoveryFor = 2 * time.Minute
	outs, _ := run(cfg, 0.95, 0.5, 0.5, 0.95, 0.5, 0.5, 0.5)
	for i, o := range outs[:6] {
		if o.Resolve != "" {
			t.Fatalf("resolved at %d before recovery hold: %v", i, transitions(outs))
		}
		if i > 0 && o.Open {
			t.Fatalf("breach during hold opened a new incident at %d", i)
		}
	}
	if outs[6].Resolve != ReasonRecovered {
		t.Fatalf("not resolved after hold: %v", transitions(outs))
	}
}

func TestStepMissingData(t *testing.T) {
	nan := math.NaN()
	// keep: a firing series stays firing while data is missing, then expires.
	cfg := cfgGT(0.9, 0.9, 0)
	cfg.ExpireAfter = 3 * time.Minute
	outs, _ := run(cfg, 0.95, nan, nan, nan, nan)
	if outs[1].Resolve != "" || outs[3].Resolve != "" || !outs[1].Persist {
		t.Fatalf("keep resolved too early: %v", transitions(outs))
	}
	if outs[4].Resolve != ReasonExpired {
		t.Fatalf("keep did not expire: %+v", outs[4])
	}
	// ok: missing resolves with reason no_data, regardless of the recovery hold.
	cfg = cfgGT(0.9, 0.9, 0)
	cfg.Missing = "ok"
	cfg.RecoveryFor = time.Hour
	outs, _ = run(cfg, 0.95, nan)
	if outs[1].Resolve != ReasonNoData {
		t.Fatalf("missing=ok: %+v", outs[1])
	}
	// breach: missing counts as breaching.
	cfg = cfgGT(0.9, 0.9, time.Minute)
	cfg.Missing = "breach"
	outs, _ = run(cfg, 0.95, nan)
	if !outs[1].Open {
		t.Fatalf("missing=breach did not fire: %v", transitions(outs))
	}
	// zero (log counts): missing = 0.
	cfg = StepConfig{Judge: Judge{Operator: "lt", Threshold: 1, Recovery: 1}, Interval: time.Minute, Missing: "zero"}
	outs, _ = run(cfg, nan)
	if !outs[0].Open {
		t.Fatalf("missing=zero with lt 1 did not fire: %+v", outs[0])
	}
	// A series without state and without data stays absent.
	out := Step(SeriesState{}, StepInput{End: t0, Present: false}, cfgGT(0.9, 0.9, 0))
	if out.Persist || out.Open || out.Delete {
		t.Fatalf("unknown missing series produced %+v", out)
	}
}

func TestStepPendingGapRestartsTimer(t *testing.T) {
	cfg := cfgGT(0.9, 0.9, 3*time.Minute)
	s := SeriesState{LastValue: math.NaN()}
	out := Step(s, StepInput{End: t0, Present: true, Value: 1}, cfg)
	// Next evaluation 10 minutes later (evaluator outage): continuity not observed, timer restarts.
	out = Step(out.Next, StepInput{End: t0.Add(10 * time.Minute), PrevEvalEnd: t0, Present: true, Value: 1}, cfg)
	if out.Open || !out.Next.PendingSince.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("gap did not restart the for timer: %+v", out)
	}
	out = Step(out.Next, StepInput{End: t0.Add(13 * time.Minute), PrevEvalEnd: t0.Add(12 * time.Minute), Present: true, Value: 1}, cfg)
	if !out.Open {
		t.Fatalf("did not fire 3m after restart: %+v", out)
	}
}

func TestStepFlapping(t *testing.T) {
	cfg := cfgGT(0.9, 0.9, 0)
	cfg.Flapping = Flapping{Enabled: true, Transitions: 3, WindowSeconds: 3600, HoldSeconds: 600}
	// Alternating signal: fires, resolves, fires, resolves, fires (3rd transition → flapping).
	vals := []float64{1, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	outs, _ := run(cfg, vals...)
	opens, resolves := 0, 0
	flapping := false
	for i, o := range outs {
		if o.Open {
			opens++
		}
		if o.Resolve != "" {
			resolves++
			t.Logf("resolved at %dm", i)
		}
		flapping = flapping || o.Flapping
	}
	if opens != 3 {
		t.Fatalf("opens = %d, want 3 (then held open): %v", opens, transitions(outs))
	}
	if !flapping {
		t.Fatal("flapping not reported")
	}
	// After the third open (minute 4) the breach at minute 6 must not open a 4th incident, and the resolve waits
	// for 10 minutes of continuous recovery (from minute 7 → minute 17).
	if resolves != 3 || outs[16].Resolve != "" || outs[17].Resolve != ReasonRecovered {
		t.Fatalf("flapping hold: resolves=%d transitions=%v", resolves, transitions(outs))
	}
}

func TestIsFlappingWindow(t *testing.T) {
	f := Flapping{Enabled: true, Transitions: 2, WindowSeconds: 600}
	ts := []time.Time{t0, t0.Add(5 * time.Minute)}
	if !isFlapping(ts, t0.Add(6*time.Minute), f) {
		t.Error("2 transitions in 10m not flapping")
	}
	if isFlapping(ts, t0.Add(11*time.Minute), f) {
		t.Error("old transition counted")
	}
	f.Enabled = false
	if isFlapping(ts, t0.Add(6*time.Minute), f) {
		t.Error("disabled flapping reported")
	}
}

func TestJudge(t *testing.T) {
	cases := []struct {
		op        string
		v         float64
		breach    bool
		recovered bool
	}{
		{"gt", 1, false, true}, {"gt", 1.01, true, false}, {"gte", 1, true, false},
		{"lt", 1, false, true}, {"lt", 0.99, true, false}, {"lte", 1, true, false},
		{"gt", math.NaN(), false, false},
	}
	for _, c := range cases {
		j := Judge{Operator: c.op, Threshold: 1, Recovery: 1}
		if j.Breach(c.v) != c.breach || j.Recovered(c.v) != c.recovered {
			t.Errorf("%s %v: breach %v recovered %v", c.op, c.v, j.Breach(c.v), j.Recovered(c.v))
		}
	}
}
