package migrate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/schema"
)

func tieredStorage(t *testing.T, env map[string]string) config.Storage {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Storage
}

// Every managed table has the schema's TTL at the default retention, and every other _local table with a TTL is
// deliberately left out of tiering (small entity tables and queues).
func TestTTLTablesMatchSchema(t *testing.T) {
	ms, err := Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]string{}
	for _, m := range ms {
		for _, st := range m.Statements {
			st = strings.Join(strings.Fields(st), " ")
			const prefix = "CREATE TABLE IF NOT EXISTS openlog."
			if strings.HasPrefix(st, prefix) && strings.Contains(st, " TTL ") {
				created[strings.Fields(st[len(prefix):])[0]] = st
			}
		}
	}
	unmanaged := map[string]bool{"hosts_local": true, "inventory_items_local": true, "inventory_snapshots_local": true,
		"containers_local": true, "apm_service_containers_local": true, "apm_relink_queue_local": true,
		// APM GA (0035_apm_ga): small aggregates with a fixed 30-day TTL, like apm_service_containers.
		"apm_error_group_dims_local": true, "apm_service_versions_1m_local": true,
		// Language agent versions (0090_apm_agent_versions, D-124): fixed 30-day TTL, like apm_service_versions_1m.
		"apm_agent_versions_1h_local": true,
		// Kubernetes entities (0040–0042): fixed 30-day TTL after the last point, like containers.
		"k8s_clusters_local": true, "k8s_nodes_local": true, "k8s_workloads_local": true, "k8s_pods_local": true,
		// Usage metering (0050_usage, D-079): fixed 400-day retention, independent of the telemetry retention.
		"usage_signals_1h_local": true, "usage_entities_1d_local": true, "usage_ingest_1h_local": true, "usage_queries_1h_local": true,
		// Attribute key index (0080_attribute_keys, D-118): hourly key counts with a fixed 30-day TTL, no telemetry values.
		"attribute_keys_local": true,
		// Log patterns (0091_log_patterns, D-128): hourly pattern counts with a fixed 30-day TTL, like attribute_keys.
		"log_patterns_1h_local": true}
	managed := map[string]bool{}
	for _, tt := range TTLTables {
		managed[tt.Table] = true
		days := tt.Days
		if days == 0 {
			days = DefaultAPMRetentionDays
		}
		st, ok := created[tt.Table]
		if !ok {
			t.Errorf("%s: no CREATE TABLE with a TTL in schema/clickhouse", tt.Table)
			continue
		}
		if want := "TTL " + TTLExpression(tt, days, config.StorageTier{}); !strings.Contains(st, want) {
			t.Errorf("%s: schema does not contain %q", tt.Table, want)
		}
	}
	for table := range created {
		if !managed[table] && !unmanaged[table] {
			t.Errorf("%s has a TTL but is neither in TTLTables nor listed as unmanaged", table)
		}
	}
}

func TestTTLExpression(t *testing.T) {
	tt := TTLTable{Table: "x_local", Class: "metrics", Time: "toDateTime(ts)", Days: 30}
	cases := []struct {
		tier config.StorageTier
		days int
		want string
	}{
		{config.StorageTier{}, 30, "toDateTime(ts) + INTERVAL 30 DAY"},
		{config.StorageTier{ColdAfterDays: 7}, 30, "toDateTime(ts) + INTERVAL 7 DAY TO VOLUME 'cold', toDateTime(ts) + INTERVAL 30 DAY"},
		{config.StorageTier{WarmAfterDays: 2, ColdAfterDays: 7}, 30,
			"toDateTime(ts) + INTERVAL 2 DAY TO VOLUME 'warm', toDateTime(ts) + INTERVAL 7 DAY TO VOLUME 'cold', toDateTime(ts) + INTERVAL 30 DAY"},
		{config.StorageTier{WarmAfterDays: 3}, 30, "toDateTime(ts) + INTERVAL 3 DAY TO VOLUME 'warm', toDateTime(ts) + INTERVAL 30 DAY"},
		// Moves not before the delete are dropped.
		{config.StorageTier{ColdAfterDays: 30}, 30, "toDateTime(ts) + INTERVAL 30 DAY"},
		{config.StorageTier{WarmAfterDays: 10, ColdAfterDays: 40}, 30, "toDateTime(ts) + INTERVAL 10 DAY TO VOLUME 'warm', toDateTime(ts) + INTERVAL 30 DAY"},
	}
	for _, c := range cases {
		if got := TTLExpression(tt, c.days, c.tier); got != c.want {
			t.Errorf("%+v/%d:\n got %s\nwant %s", c.tier, c.days, got, c.want)
		}
	}
}

// applied returns the state after plan was applied on top of st (what ApplyTableTTLs records and changes).
func applied(st TTLState, opts TTLOptions, plan TTLPlan) TTLState {
	next := TTLState{Recorded: map[string]string{}, Policies: map[string][]string{}}
	for k, v := range st.Recorded {
		next.Recorded[k] = v
	}
	for k, v := range st.Policies {
		next.Policies[k] = v
	}
	for _, s := range plan.Steps {
		switch s.Kind {
		case StepTTL:
			next.Recorded[TTLSettingPrefix+s.Table] = s.To
		case StepStoragePolicy:
			next.Policies[s.Table] = []string{opts.Storage.Policy}
		}
	}
	if plan.APMRetentionFrom != plan.APMRetentionTo {
		next.Recorded[APMRetentionSetting] = strconv.Itoa(plan.APMRetentionTo)
	}
	return next
}

func TestBuildTTLPlan(t *testing.T) {
	empty := TTLState{Recorded: map[string]string{}}
	apmTables := 0
	for _, tt := range TTLTables {
		if tt.Days == 0 {
			apmTables++
		}
	}

	// Tiering never enabled, default APM retention: nothing, and no replica is asked for policies.
	off := TTLOptions{Cluster: "openlog", APMRetentionDays: 30, Storage: tieredStorage(t, nil)}
	plan, err := BuildTTLPlan(off, empty)
	if err != nil || plan.Changed() || plan.Tiering {
		t.Fatalf("disabled: %+v %v", plan, err)
	}

	// APM retention only (the former ApplyAPMRetention).
	apm := off
	apm.APMRetentionDays = 7
	plan, _ = BuildTTLPlan(apm, empty)
	if len(plan.Steps) != apmTables || plan.APMRetentionFrom != 30 || plan.APMRetentionTo != 7 {
		t.Fatalf("apm retention: %+v", plan)
	}
	stmts, _ := APMRetentionStatements("openlog", 7)
	for i, s := range plan.Steps {
		if s.Kind != StepTTL || s.SQL != stmts[i] {
			t.Errorf("apm step %d: %+v, want %s", i, s, stmts[i])
		}
	}
	st := applied(empty, apm, plan)
	if plan, _ = BuildTTLPlan(apm, st); plan.Changed() {
		t.Fatalf("apm retention not idempotent: %+v", plan)
	}
	// A retention recorded by an older openlog-migrate (apm_retention_days only) is the current state.
	if plan, _ = BuildTTLPlan(apm, TTLState{Recorded: map[string]string{APMRetentionSetting: "7"}}); plan.Changed() {
		t.Fatalf("older record: %+v", plan)
	}

	// Enable tiering with the defaults.
	on := TTLOptions{Cluster: "openlog", APMRetentionDays: 7, Storage: tieredStorage(t, map[string]string{"OPENLOG_STORAGE_TIERING_ENABLED": "true"})}
	plan, err = BuildTTLPlan(on, st)
	if err != nil || !plan.Tiering {
		t.Fatalf("enable: %+v %v", plan, err)
	}
	var policies, ttls int
	seenTTL := false
	for _, s := range plan.Steps {
		switch s.Kind {
		case StepStoragePolicy:
			policies++
			if seenTTL {
				t.Errorf("storage policy step after a TTL step: %s", s.SQL)
			}
			if !strings.HasSuffix(s.SQL, "ON CLUSTER 'openlog' MODIFY SETTING storage_policy = 'openlog_tiered', materialize_ttl_recalculate_only = 1") {
				t.Errorf("policy SQL %s", s.SQL)
			}
		case StepTTL:
			ttls++
			seenTTL = true
		}
	}
	// APM default cold 7 equals the 7-day retention: the APM tables get no move and keep the default policy.
	if policies != 8 || ttls != 8 {
		t.Fatalf("enable: %d policy and %d TTL steps: %+v", policies, ttls, plan.Steps)
	}
	want := map[string]string{
		"metrics_local":     "toDateTime(timestamp) + INTERVAL 7 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 30 DAY",
		"metrics_1m_local":  "timestamp + INTERVAL 30 DAY TO VOLUME 'cold', timestamp + INTERVAL 395 DAY",
		"logs_local":        "toDateTime(timestamp) + INTERVAL 3 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 14 DAY",
		"spans_local":       "toDateTime(timestamp) + INTERVAL 3 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 7 DAY",
		"trace_index_local": "toDateTime(start) + INTERVAL 3 DAY TO VOLUME 'cold', toDateTime(start) + INTERVAL 7 DAY",
		// Metric exemplars follow the traces class, so they move and expire with the spans (D-130).
		"metric_exemplars_local":  "toDateTime(timestamp) + INTERVAL 3 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 7 DAY",
		"alert_evaluations_local": "toDateTime(evaluated_at) + INTERVAL 7 DAY TO VOLUME 'cold', toDateTime(evaluated_at) + INTERVAL 30 DAY",
		// Synthetic check runs share the alerts class (D-132).
		"synthetic_runs_local": "toDateTime(timestamp) + INTERVAL 7 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 30 DAY",
	}
	for _, s := range plan.Steps {
		if s.Kind == StepTTL && (want[s.Table] != s.To || s.SQL != "ALTER TABLE openlog."+s.Table+" ON CLUSTER 'openlog' MODIFY TTL "+s.To) {
			t.Errorf("%s: %s", s.Table, s.SQL)
		}
	}
	tiered := applied(st, on, plan)
	if plan, _ = BuildTTLPlan(on, tiered); plan.Changed() || !plan.Tiering {
		t.Fatalf("tiering not idempotent: %+v", plan)
	}

	// A replica still on the old policy (a node added later) gets the switch again, TTLs stay.
	partial := applied(tiered, on, TTLPlan{})
	partial.Policies["logs_local"] = []string{"default", "openlog_tiered"}
	plan, _ = BuildTTLPlan(on, partial)
	if len(plan.Steps) != 1 || plan.Steps[0].Table != "logs_local" || plan.Steps[0].Kind != StepStoragePolicy {
		t.Fatalf("partial policy: %+v", plan)
	}

	// APM retention change while tiering: the APM tables now get a move and the policy first.
	on30 := on
	on30.APMRetentionDays = 30
	plan, _ = BuildTTLPlan(on30, tiered)
	if len(plan.Steps) != 2*apmTables || plan.Steps[0].Kind != StepStoragePolicy || !strings.HasPrefix(plan.Steps[0].Table, "apm_") {
		t.Fatalf("apm + tiering: %+v", plan)
	}
	for _, s := range plan.Steps {
		if s.Kind == StepTTL && !strings.HasSuffix(s.To, "+ INTERVAL 7 DAY TO VOLUME 'cold', "+strings.SplitN(s.To, " + ", 2)[0]+" + INTERVAL 30 DAY") {
			t.Errorf("apm tiered TTL %s", s.To)
		}
	}

	// Disable again: the move clauses go, the policy stays.
	plan, _ = BuildTTLPlan(apm, tiered)
	if len(plan.Steps) != 8 || plan.Tiering {
		t.Fatalf("disable: %+v", plan)
	}
	for _, s := range plan.Steps {
		if s.Kind != StepTTL || strings.Contains(s.To, "TO VOLUME") {
			t.Errorf("disable step %+v", s)
		}
	}
	if plan, _ = BuildTTLPlan(apm, applied(tiered, apm, plan)); plan.Changed() {
		t.Fatalf("disable not idempotent: %+v", plan)
	}

	// Per-class ages, warm volume, class turned off.
	custom := on
	custom.Storage = tieredStorage(t, map[string]string{"OPENLOG_STORAGE_TIERING_ENABLED": "true", "OPENLOG_STORAGE_POLICY": "tiers",
		"OPENLOG_STORAGE_WARM_AFTER_DAYS_LOGS": "1", "OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS": "5",
		"OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS": "0", "OPENLOG_STORAGE_COLD_AFTER_DAYS_TRACES": "7"})
	plan, _ = BuildTTLPlan(custom, empty)
	for _, s := range plan.Steps {
		switch s.Table {
		case "metrics_local", "spans_local", "trace_index_local":
			t.Errorf("no move wanted for %s: %+v", s.Table, s)
		case "logs_local":
			if s.Kind == StepTTL && s.To != "toDateTime(timestamp) + INTERVAL 1 DAY TO VOLUME 'warm', toDateTime(timestamp) + INTERVAL 5 DAY TO VOLUME 'cold', toDateTime(timestamp) + INTERVAL 14 DAY" {
				t.Errorf("logs: %s", s.To)
			}
			if s.Kind == StepStoragePolicy && !strings.Contains(s.SQL, "storage_policy = 'tiers'") {
				t.Errorf("logs policy: %s", s.SQL)
			}
		}
	}

	// Hostile names never reach SQL.
	bad := on
	bad.Cluster = "x'; DROP"
	if _, err := BuildTTLPlan(bad, empty); err == nil {
		t.Error("accepted a hostile cluster name")
	}
	bad = on
	bad.Storage.Policy = "p'"
	if _, err := BuildTTLPlan(bad, empty); err == nil {
		t.Error("accepted a hostile policy name")
	}
	if _, err := BuildTTLPlan(on, TTLState{Recorded: map[string]string{APMRetentionSetting: "x"}}); err == nil {
		t.Error("accepted a broken recorded retention")
	}
}
