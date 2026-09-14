package migrate

import (
	"strings"
	"testing"
)

// Per-tenant retention (D-081) widens the delete TTL of the logs, traces and metrics classes only.
func TestTTLPlanClassRetention(t *testing.T) {
	opts := TTLOptions{Cluster: "openlog", APMRetentionDays: DefaultAPMRetentionDays,
		ClassRetentionDays: map[string]int{"logs": 30, "traces": 7, "metrics": 90, "apm": 400}}
	plan, err := BuildTTLPlan(opts, TTLState{Recorded: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range plan.Steps {
		got[s.Table] = s.To
	}
	if got["logs_local"] != "toDateTime(timestamp) + INTERVAL 30 DAY" || got["metrics_local"] != "toDateTime(timestamp) + INTERVAL 90 DAY" {
		t.Errorf("steps %v", got)
	}
	// traces equals the schema TTL; APM and metrics_1m are not classes of D-081 (apm keeps OPENLOG_APM_RETENTION_DAYS).
	for _, table := range []string{"spans_local", "trace_index_local", "metrics_1m_local", "apm_transactions_1m_local", "alert_evaluations_local"} {
		if to, ok := got[table]; ok && !strings.Contains(to, "INTERVAL 30 DAY") {
			t.Errorf("%s changed: %s", table, to)
		}
	}
	if _, ok := got["spans_local"]; ok {
		t.Error("spans_local changed although its class retention equals the schema")
	}
	if len(plan.Steps) != 2 {
		t.Errorf("want 2 steps, got %v", got)
	}
}
