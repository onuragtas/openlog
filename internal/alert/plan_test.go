package alert

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/alert/notify"
)

func testRule(t *testing.T, mutate func(in *RuleInput)) *Rule {
	t.Helper()
	in := RuleInput{Name: "High CPU", Type: TypeMetricThreshold, Severity: "critical", IntervalSeconds: 60,
		Condition:  json.RawMessage(`{"metric":"system.cpu.utilization","window_seconds":60,"operator":"gt","threshold":0.9,"recovery_threshold":0.8,"group_by":["host"]}`),
		ChannelIDs: []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"},
		Labels:     map[string]string{"team": "infra"}, Flapping: &Flapping{Enabled: false}}
	if mutate != nil {
		mutate(&in)
	}
	def, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	return &Rule{Definition: *def, ID: "rule-1", OrgID: "org-1", TenantID: "tenant-a", OrgName: "Default", Version: 3}
}

var testChannels = []ChannelRef{
	{ID: "11111111-1111-1111-1111-111111111111", Type: "slack", Enabled: true},
	{ID: "22222222-2222-2222-2222-222222222222", Type: "webhook", Enabled: true},
}

func ids() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("id-%d", n) }
}

func sample(host string, v float64) Sample {
	return Sample{Key: "host.id=" + host, Labels: map[string]string{"host.id": host, "host.name": "web-" + host}, Value: v}
}

// apply stores a plan's results the way the database would, for the next evaluation.
func apply(states map[string]SeriesState, incidents map[string]*Incident, p *Plan) {
	for _, inc := range p.Opens {
		c := inc
		incidents[inc.ID] = &c
	}
	for _, r := range p.Resolves {
		if inc := incidents[r.ID]; inc != nil {
			inc.State = IncidentResolved
		}
	}
	for _, k := range p.SeriesDeletes {
		delete(states, k)
	}
	for _, s := range p.SeriesUpserts {
		if s.IncidentID != "" {
			s.Incident = incidents[s.IncidentID]
		}
		states[s.Key] = s
	}
}

func TestPlanOpenResolveAndIdempotencyKeys(t *testing.T) {
	r := testRule(t, nil)
	states, incidents := map[string]SeriesState{}, map[string]*Incident{}
	newID := ids()
	eval := func(i int, samples ...Sample) *Plan {
		p := BuildPlan(PlanInput{Rule: r, Channels: testChannels, States: states, End: t0.Add(time.Duration(i) * time.Minute),
			PrevEvalEnd: t0.Add(time.Duration(i-1) * time.Minute), Result: &EvalResult{Samples: samples, Unit: "1"},
			PublicURL: "https://ol.example/", NewID: newID})
		apply(states, incidents, p)
		return p
	}

	p := eval(1, sample("h1", 0.95), sample("h2", 0.5))
	if len(p.Opens) != 1 || len(p.Notifications) != 2 || len(p.Events) != 1 {
		t.Fatalf("open plan: opens=%d notifications=%d events=%d", len(p.Opens), len(p.Notifications), len(p.Events))
	}
	inc := p.Opens[0]
	if inc.SeriesKey != "host.id=h1" || inc.Labels["team"] != "infra" || inc.Labels["host.name"] != "web-h1" || inc.Labels["alert.severity"] != "critical" ||
		*inc.Value != 0.95 || *inc.Threshold != 0.9 || len(inc.ChannelIDs) != 2 {
		t.Fatalf("incident %+v", inc)
	}
	if inc.Summary != "system.cpu.utilization avg over 1m is 0.95 (> 0.9) on web-h1" {
		t.Errorf("summary %q", inc.Summary)
	}
	n := p.Notifications[0]
	if n.IdempotencyKey != inc.ID+":opened:11111111-1111-1111-1111-111111111111" || n.Kind != KindOpened || n.ChannelType != "slack" || n.Status != StatusPending {
		t.Fatalf("notification %+v", n)
	}
	var ev notify.Event
	if err := json.Unmarshal(n.Payload, &ev); err != nil || ev.Event != notify.EventOpened || ev.Incident.URL != "https://ol.example/alerts/incidents/"+inc.ID ||
		ev.Organization.Name != "Default" || ev.Rule.Severity != "critical" {
		t.Fatalf("payload %s %v", n.Payload, err)
	}
	if len(p.SeriesUpserts) != 1 || p.SeriesUpserts[0].State != StateFiring {
		t.Fatalf("series upserts %+v", p.SeriesUpserts)
	}

	// Still breaching, and inside the hysteresis band: nothing new is written but the series state.
	for i, v := range []float64{0.97, 0.85} {
		p = eval(2+i, sample("h1", v), sample("h2", 0.5))
		if len(p.Opens)+len(p.Resolves)+len(p.Notifications)+len(p.Events) != 0 {
			t.Fatalf("eval %d produced %+v", 2+i, p)
		}
	}

	// Recovered: one resolve, one resolve notification per opened channel, series row deleted.
	p = eval(4, sample("h1", 0.2), sample("h2", 0.5))
	if len(p.Resolves) != 1 || p.Resolves[0].Reason != ReasonRecovered || len(p.Notifications) != 2 || len(p.SeriesDeletes) != 1 {
		t.Fatalf("resolve plan %+v", p)
	}
	if k := p.Notifications[1].IdempotencyKey; k != inc.ID+":resolved:22222222-2222-2222-2222-222222222222" {
		t.Errorf("resolve key %q", k)
	}
	if err := json.Unmarshal(p.Notifications[0].Payload, &ev); err != nil || ev.Event != notify.EventResolved || ev.Incident.ResolvedAt == nil || *ev.Incident.ResolveReason != "recovered" {
		t.Fatalf("resolve payload %s", p.Notifications[0].Payload)
	}
	if p.Transitions["resolved"] != 1 {
		t.Errorf("transitions %v", p.Transitions)
	}
}

func TestPlanRenotifyAcknowledgedAndManualResolve(t *testing.T) {
	r := testRule(t, func(in *RuleInput) { in.RenotifyIntervalSeconds = 300 })
	states, incidents := map[string]SeriesState{}, map[string]*Incident{}
	newID := ids()
	eval := func(i int, v float64) *Plan {
		p := BuildPlan(PlanInput{Rule: r, Channels: testChannels, States: states, End: t0.Add(time.Duration(i) * time.Minute),
			PrevEvalEnd: t0.Add(time.Duration(i-1) * time.Minute), Result: &EvalResult{Samples: []Sample{sample("h1", v)}}, NewID: newID})
		apply(states, incidents, p)
		return p
	}
	p := eval(0, 0.95)
	incID := p.Opens[0].ID
	renotifies := 0
	for i := 1; i <= 10; i++ {
		p = eval(i, 0.95)
		for _, n := range p.Notifications {
			if n.Kind != KindRenotify {
				t.Fatalf("unexpected %s notification", n.Kind)
			}
			renotifies++
		}
	}
	// 5m and 10m after opening: two rounds × two channels.
	if renotifies != 4 {
		t.Fatalf("renotify notifications = %d, want 4", renotifies)
	}
	if k := IdempotencyKey(incID, KindRenotify, "2", "c"); k != incID+":renotify:2:c" {
		t.Errorf("renotify key %q", k)
	}
	// Acknowledged incidents are not re-notified.
	incidents[incID].State = IncidentAcknowledged
	for i := 11; i <= 20; i++ {
		if p = eval(i, 0.95); len(p.Notifications) != 0 {
			t.Fatalf("acknowledged incident re-notified at %d", i)
		}
	}
	// A manual resolve (outside the evaluator) restarts the series: a still-breaching series opens a new incident.
	incidents[incID].State = IncidentResolved
	p = eval(21, 0.95)
	if len(p.Opens) != 1 || p.Opens[0].ID == incID || len(p.Resolves) != 0 {
		t.Fatalf("after manual resolve: %+v", p)
	}
}

func TestPlanDisabledChannelsAndMissingData(t *testing.T) {
	r := testRule(t, func(in *RuleInput) {
		in.Condition = json.RawMessage(`{"metric":"m","window_seconds":60,"operator":"gt","threshold":1,"group_by":["host"],"missing_data":"ok"}`)
	})
	chans := []ChannelRef{testChannels[0], {ID: testChannels[1].ID, Type: "webhook", Enabled: false}}
	states, incidents := map[string]SeriesState{}, map[string]*Incident{}
	p := BuildPlan(PlanInput{Rule: r, Channels: chans, States: states, End: t0, Result: &EvalResult{Samples: []Sample{sample("h1", 2)}}, NewID: ids()})
	if len(p.Notifications) != 1 || len(p.Opens[0].ChannelIDs) != 1 {
		t.Fatalf("disabled channel notified: %+v", p.Notifications)
	}
	apply(states, incidents, p)
	// The series disappears from the result: missing_data=ok resolves with reason no_data.
	p = BuildPlan(PlanInput{Rule: r, Channels: chans, States: states, End: t0.Add(time.Minute), PrevEvalEnd: t0, Result: &EvalResult{}, NewID: ids()})
	if len(p.Resolves) != 1 || p.Resolves[0].Reason != ReasonNoData || p.Resolves[0].LastValue != nil {
		t.Fatalf("missing data plan %+v", p)
	}
	if !math.IsNaN(math.NaN()) {
		t.Skip()
	}
}

func TestRuleValidation(t *testing.T) {
	cond := `{"metric":"m","operator":"gt","threshold":1}`
	bad := map[string]RuleInput{
		"name":      {Name: "", Type: TypeMetricThreshold, Condition: json.RawMessage(cond)},
		"type":      {Name: "x", Type: "nope", Condition: json.RawMessage(cond)},
		"apm":       {Name: "x", Type: TypeAPM, Condition: json.RawMessage(`{"metric":"p95_ms","operator":"gt","threshold":1}`)},
		"apm-group": {Name: "x", Type: TypeAPM, Condition: json.RawMessage(`{"service_name":"s","metric":"apdex","operator":"lt","threshold":0.8,"group_by":["host"]}`)},
		"interval":  {Name: "x", Type: TypeMetricThreshold, IntervalSeconds: 5, Condition: json.RawMessage(cond)},
		"renotify":  {Name: "x", Type: TypeMetricThreshold, RenotifyIntervalSeconds: 60, Condition: json.RawMessage(cond)},
		"channel":   {Name: "x", Type: TypeMetricThreshold, ChannelIDs: []string{"nope"}, Condition: json.RawMessage(cond)},
		"runbook":   {Name: "x", Type: TypeMetricThreshold, RunbookURL: "javascript:alert(1)", Condition: json.RawMessage(cond)},
		"recovery":  {Name: "x", Type: TypeMetricThreshold, Condition: json.RawMessage(`{"metric":"m","operator":"gt","threshold":1,"recovery_threshold":2}`)},
		"threshold": {Name: "x", Type: TypeMetricThreshold, Condition: json.RawMessage(`{"metric":"m","operator":"gt"}`)},
		"condition": {Name: "x", Type: TypeMetricThreshold},
		"label":     {Name: "x", Type: TypeMetricThreshold, Labels: map[string]string{"bad key": "v"}, Condition: json.RawMessage(cond)},
		"flapping":  {Name: "x", Type: TypeMetricThreshold, Flapping: &Flapping{Enabled: true, Transitions: 1, WindowSeconds: 600, HoldSeconds: 60}, Condition: json.RawMessage(cond)},
	}
	for name, in := range bad {
		if _, err := in.Validate(); err == nil {
			t.Errorf("%s: invalid input accepted", name)
		}
	}
	def, err := RuleInput{Name: " ok ", Type: TypeDiscovery, ForSeconds: 120, Condition: json.RawMessage(`{"event":"port_opened"}`)}.Validate()
	if err != nil || def.Name != "ok" || def.IntervalSeconds != 300 || def.ForSeconds != 0 || def.Severity != SeverityWarning || !def.Flapping.Enabled {
		t.Fatalf("defaults: %+v %v", def, err)
	}
	var ve *ValidationError
	_, err = RuleInput{Name: "x", Type: TypeMetricThreshold, Condition: json.RawMessage(`{"metric":"m","operator":"gt","threshold":1,"window_seconds":5}`)}.Validate()
	if e, ok := err.(*ValidationError); !ok || e.Field != "condition.window_seconds" {
		t.Errorf("field path: %v %v", err, ve)
	}
	infos := RuleTypes()
	if len(infos) != 5 || infos[4].Type != TypeAPM || !infos[4].Available || infos[4].Reason != "" || !infos[0].Available {
		t.Errorf("rule types %+v", infos)
	}
}
