package statuspage

import (
	"testing"
	"time"
)

func TestEvaluate(t *testing.T) {
	healthy := Probes{PostgresOK: true, ClickHouseOK: true, Live: map[string]int{"openlog-ingest": 2, "openlog-processor": 1},
		AlertEvaluators: 1, EnabledRules: 10, OverdueRules: 0, HasRecentMetrics: true, NewestMetricAge: time.Minute}
	for comp, want := range map[string]string{"ingest": Operational, "query_api": Operational, "alerting": Operational, "processing": Operational} {
		if got := Evaluate(healthy)[comp]; got != want {
			t.Errorf("healthy %s = %s", comp, got)
		}
	}
	p := healthy
	p.Live = map[string]int{"openlog-allinone": 1}
	p.OverdueRules = 2
	p.NewestMetricAge = 15 * time.Minute
	p.Maintenance = map[string]bool{"ingest": true}
	got := Evaluate(p)
	if got["ingest"] != Maintenance || got["alerting"] != Degraded || got["processing"] != Degraded {
		t.Fatalf("allinone/degraded: %v", got)
	}
	p = healthy
	p.Live = map[string]int{}
	p.ClickHouseOK = false
	p.AlertEvaluators = 0
	got = Evaluate(p)
	if got["ingest"] != MajorOutage || got["processing"] != MajorOutage || got["query_api"] != MajorOutage || got["alerting"] != MajorOutage {
		t.Fatalf("outage: %v", got)
	}
	p.EnabledRules = 0
	if Evaluate(p)["alerting"] != Operational {
		t.Fatal("no rules must be operational")
	}
	if Worst(Operational, Maintenance, Degraded) != Degraded || Worst() != Operational || Worst(MajorOutage, Unknown) != Unknown {
		t.Fatal("worst")
	}
}

func TestHistory(t *testing.T) {
	today := time.Date(2026, 9, 14, 17, 0, 0, 0, time.UTC)
	days, uptime := History(map[string]DayCounts{
		"2026-09-14": {Checks: 100, Operational: 90, Degraded: 10},
		"2026-09-13": {Checks: 100, Operational: 50, Outage: 50},
		"2026-06-01": {Checks: 100, Outage: 100}, // outside the window
	}, today, 90)
	if len(days) != 90 || days[89].Date != "2026-09-14" || days[0].Date != "2026-06-17" {
		t.Fatalf("window %s..%s (%d)", days[0].Date, days[89].Date, len(days))
	}
	if days[89].Status != Degraded || *days[89].Uptime != 100 || days[88].Status != "outage" || *days[88].Uptime != 50 || days[0].Status != "no_data" || days[0].Uptime != nil {
		t.Fatalf("days %+v %+v %+v", days[89], days[88], days[0])
	}
	if uptime == nil || *uptime != 75 {
		t.Fatalf("uptime %v", uptime)
	}
	if _, u := History(nil, today, 90); u != nil {
		t.Fatal("no data must have no uptime")
	}
}

func TestIncidentValidation(t *testing.T) {
	start := time.Now()
	in := Incident{Kind: KindIncident, Title: "  Ingest delays ", Status: "investigating", Components: []string{"ingest", "ingest"}, StartsAt: start}
	if err := in.Validate(); err != nil || in.Impact != "minor" || in.Title != "Ingest delays" || len(in.Components) != 1 {
		t.Fatalf("valid: %v %+v", err, in)
	}
	for _, bad := range []Incident{
		{Kind: "outage", Title: "x", Status: "investigating"},
		{Kind: KindIncident, Title: "x", Status: "scheduled"},
		{Kind: KindMaintenance, Title: "x", Status: "scheduled", Impact: "huge"},
		{Kind: KindMaintenance, Title: "", Status: "scheduled"},
		{Kind: KindIncident, Title: "x", Status: "resolved", Components: []string{"billing"}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	end := start.Add(-time.Hour)
	if err := (&Incident{Kind: KindMaintenance, Title: "x", Status: "scheduled", StartsAt: start, EndsAt: &end}).Validate(); err == nil {
		t.Error("ends before starts accepted")
	}
	later := start.Add(time.Hour)
	m := Incident{Kind: KindMaintenance, Status: "scheduled", StartsAt: start, EndsAt: &later}
	if !m.ActiveMaintenance(start.Add(time.Minute)) || m.ActiveMaintenance(start.Add(-time.Minute)) || m.ActiveMaintenance(later) {
		t.Fatal("maintenance window")
	}
	if ValidMessage(" ") == nil {
		t.Fatal("empty message accepted")
	}
}
