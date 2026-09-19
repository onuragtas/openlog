package dbmon

import (
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

var t0 = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func attr(kvs []*commonpb.KeyValue, k string) *commonpb.AnyValue {
	for _, kv := range kvs {
		if kv.Key == k {
			return kv.Value
		}
	}
	return nil
}

func TestTrackerDeltas(t *testing.T) {
	var tr Tracker
	if iv, d := tr.Deltas(t0, []Stat{{Key: "a", Calls: 10, TimeMs: 100}}, 10); iv != 0 || d != nil {
		t.Fatalf("first call must only record a baseline: %v %v", iv, d)
	}
	iv, d := tr.Deltas(t0.Add(30*time.Second), []Stat{
		{Key: "a", Calls: 15, TimeMs: 160, Rows: 5},
		{Key: "b", Calls: 3, TimeMs: 999}, // new: not attributed to this interval
		{Key: "c", Calls: 0, TimeMs: 0},   // not in the baseline either
	}, 10)
	if iv != 30*time.Second || len(d) != 1 || d[0].Calls != 5 || d[0].TimeMs != 60 || d[0].Rows != 5 {
		t.Fatalf("deltas = %v %+v", iv, d)
	}
	// a was reset (calls went down), b ran twice and costs more than c; top 1 keeps b.
	_, d = tr.Deltas(t0.Add(60*time.Second), []Stat{
		{Key: "a", Calls: 1, TimeMs: 1},
		{Key: "b", Calls: 5, TimeMs: 1099},
		{Key: "c", Calls: 1, TimeMs: 1},
	}, 1)
	if len(d) != 1 || d[0].Key != "b" || d[0].Calls != 2 || d[0].TimeMs != 100 {
		t.Fatalf("after reset = %+v", d)
	}
}

func TestPlans(t *testing.T) {
	p := Plans{Interval: time.Hour}
	if !p.Due("q", t0) {
		t.Fatal("unknown statement not due")
	}
	if !p.Explained("q", "h1", t0) {
		t.Fatal("first plan not sent")
	}
	if p.Due("q", t0.Add(59*time.Minute)) || !p.Due("q", t0.Add(time.Hour)) {
		t.Fatal("interval not respected")
	}
	if p.Explained("q", "h1", t0.Add(time.Hour)) {
		t.Fatal("unchanged plan sent again within a day")
	}
	if !p.Explained("q", "h2", t0.Add(2*time.Hour)) {
		t.Fatal("changed plan not sent")
	}
	if !p.Explained("q", "h2", t0.Add(26*time.Hour+time.Minute)) {
		t.Fatal("unchanged plan not re-sent after a day")
	}
	if p.Explained("x", "", t0) || p.Due("x", t0.Add(time.Minute)) {
		t.Fatal("failed explain must not send and must wait for the interval")
	}
}

func TestJSONPlanHashIgnoresEstimates(t *testing.T) {
	vol := map[string]bool{"Total Cost": true, "Plan Rows": true}
	a := JSONPlanHash([]byte(`[{"Plan":{"Node Type":"Index Scan","Total Cost":8.3,"Plan Rows":1}}]`), vol)
	b := JSONPlanHash([]byte(`[{"Plan":{"Plan Rows":900,"Total Cost":1200.5,"Node Type":"Index Scan"}}]`), vol)
	c := JSONPlanHash([]byte(`[{"Plan":{"Node Type":"Seq Scan","Total Cost":8.3,"Plan Rows":1}}]`), vol)
	if a == "" || a != b || a == c || JSONPlanHash([]byte("<xml/>"), vol) != "" {
		t.Fatalf("hashes %q %q %q", a, b, c)
	}
}

func TestRecordEvents(t *testing.T) {
	b := integrations.NewBatch(t0, 0)
	RecordStats(b, SystemMySQL, 29600*time.Millisecond, []Stat{{QueryID: "d1", DB: "shop", Text: "SELECT * FROM t WHERE email = 'a@b.c'",
		Calls: 3, TimeMs: 12.5, Rows: 3, RowsExamined: 300, NoIndexUsed: 3}})
	RecordSessions(b, SystemPostgreSQL, []Session{{ID: "7", State: "active", WaitType: "Lock", WaitEvent: "tuple",
		Text: "UPDATE t SET a = 'secret' WHERE id = 9", DurationMs: 40, BlockedBy: []string{"5"}}})
	RecordPlan(b, SystemPostgreSQL, "shop", "SELECT 1", "json", `[{"Plan":{}}]`, "abcd", 1.5)
	ev := b.Events()
	if len(ev) != 3 {
		t.Fatalf("events = %d", len(ev))
	}
	s := ev[0].Attributes
	if attr(s, "event.name").GetStringValue() != EventQueryStats || attr(s, "openlog.db.interval_seconds").GetIntValue() != 30 ||
		attr(s, "db.query.text").GetStringValue() != "SELECT * FROM t WHERE email = ?" || attr(s, "openlog.db.rows_examined").GetIntValue() != 300 ||
		attr(s, "openlog.db.total_time_ms").GetDoubleValue() != 12.5 || attr(s, "openlog.db.errors") != nil {
		t.Errorf("stats = %v", s)
	}
	ss := ev[1].Attributes
	if attr(ss, "db.query.text").GetStringValue() != "UPDATE t SET a = ? WHERE id = ?" ||
		attr(ss, "openlog.db.blocking_session_ids").GetArrayValue().GetValues()[0].GetStringValue() != "5" {
		t.Errorf("session = %v", ss)
	}
	if ev[2].Body.GetStringValue() != `[{"Plan":{}}]` || attr(ev[2].Attributes, "openlog.db.plan.hash").GetStringValue() != "abcd" {
		t.Errorf("plan = %v", ev[2])
	}
}
