package api

import (
	"math"
	"testing"

	"github.com/onuragtas/openlog/internal/tailsampling"
)

func TestEstimateKeptWeightsFirstMatch(t *testing.T) {
	half := 0.5
	policy := tailsampling.Policy{Enabled: true, BaselineRatio: 0.1, Rules: []tailsampling.Rule{
		{Name: "health", Type: tailsampling.RuleRoute, Route: "/health*", Ratio: new(float64)},
		{Name: "errors", Type: tailsampling.RuleError},
		{Name: "slow", Type: tailsampling.RuleLatency, ThresholdMs: 1000},
		{Name: "pay", Type: tailsampling.RuleService, Services: []string{"pay"}, Ratio: &half},
	}}
	mk := func(w, spans, dur float64, routes []string, flags ...bool) previewTrace {
		return previewTrace{weight: w, spans: spans, durMs: dur, match: flags, routes: [][]string{routes, nil, nil, nil}}
	}
	traces := []previewTrace{
		mk(2, 3, 10, []string{"/healthz"}, false, true, false, false),  // health first: error ignored, kept 0
		mk(2, 3, 10, []string{"/cart"}, false, true, false, false),     // errors: kept 1
		mk(1, 1, 5000, []string{"/cart"}, false, false, false, false),  // slow: kept 1
		mk(4, 2, 10, []string{"/pay"}, false, false, false, true),      // pay: kept 0.5
		mk(1, 10, 10, []string{"/cart"}, false, false, false, false),   // baseline 0.1
		mk(0, 10, 10, []string{"/ignored"}, false, false, false, true), // weight 0 (ot=p:63) not counted
	}
	got := estimateKept(policy, traces)
	// total weight 10; kept = 0 + 2 + 1 + 2 + 0.1 = 5.1
	if math.Abs(got.KeptTraceRatio-0.51) > 1e-9 {
		t.Errorf("kept trace ratio = %v, want 0.51", got.KeptTraceRatio)
	}
	// span weight: 6+6+1+8+10 = 31; kept: 0+6+1+4+1 = 12
	if math.Abs(got.KeptSpanRatio-12.0/31) > 1e-9 {
		t.Errorf("kept span ratio = %v, want %v", got.KeptSpanRatio, 12.0/31)
	}
	want := map[string][2]float64{"health": {0.2, 0}, "errors": {0.2, 0.2}, "slow": {0.1, 0.1}, "pay": {0.4, 0.2}, "baseline": {0.1, 0.01}}
	for _, r := range got.Rules {
		w := want[r.Name]
		if math.Abs(r.MatchedTraceRate-w[0]) > 1e-9 || math.Abs(r.KeptTraceRate-w[1]) > 1e-9 {
			t.Errorf("rule %s: matched %v kept %v, want %v", r.Name, r.MatchedTraceRate, r.KeptTraceRate, w)
		}
	}
	policy.Enabled = false
	if got := estimateKept(policy, traces); got.KeptTraceRatio != 1 {
		t.Errorf("disabled policy keeps everything, got %v", got.KeptTraceRatio)
	}
}
