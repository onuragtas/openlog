package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// Volumes of the tiered storage policy (deploy/compose/clickhouse/storage-tiered.xml, D-066). The hot volume keeps
// the name of the `default` policy's only volume: ClickHouse refuses a new storage_policy that lacks a volume of the
// old one.
const (
	HotVolume  = "default"
	WarmVolume = "warm"
	ColdVolume = "cold"
)

var policyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// TTLSettingPrefix prefixes the table_settings rows holding the TTL openlog-migrate applied to a table ("ttl:<table>").
const TTLSettingPrefix = "ttl:"

// TTLTable is a telemetry table whose whole TTL openlog-migrate owns (D-067): the delete TTL of the schema (APM
// tables: OPENLOG_APM_RETENTION_DAYS) plus the tiered storage moves of its class (OPENLOG_STORAGE_*).
type TTLTable struct {
	Table string // _local table in the openlog database
	Class string // config.StorageClasses name
	Time  string // DateTime expression of the TTL, as in the schema
	Days  int    // delete TTL of the schema; 0 = the APM retention
}

// TTLTables lists the managed tables. Time and Days must match schema/clickhouse (TestTTLTablesMatchSchema): a
// table without a recorded TTL is assumed to still have the schema's.
var TTLTables = []TTLTable{
	{"metrics_local", "metrics", "toDateTime(timestamp)", 30},
	{"metrics_1m_local", "metrics_1m", "timestamp", 395},
	{"logs_local", "logs", "toDateTime(timestamp)", 14},
	{"spans_local", "traces", "toDateTime(timestamp)", 7},
	{"trace_index_local", "traces", "toDateTime(start)", 7},
	// Metric exemplars point into traces, so they follow the traces class, not the metrics one (D-130).
	{"metric_exemplars_local", "traces", "toDateTime(timestamp)", 7},
	{"apm_transactions_1m_local", "apm", "timestamp", 0},
	{"apm_service_edges_1m_local", "apm", "timestamp", 0},
	{"apm_service_links_1m_local", "apm", "timestamp", 0},
	{"apm_db_queries_1m_local", "apm", "timestamp", 0},
	{"apm_errors_1m_local", "apm", "timestamp", 0},
	{"apm_error_groups_local", "apm", "toDateTime(last_seen)", 0},
	{"apm_services_local", "apm", "toDateTime(last_seen)", 0},
	{"apm_service_hosts_local", "apm", "toDateTime(last_seen)", 0},
	{"alert_evaluations_local", "alerts", "toDateTime(evaluated_at)", 30},
	// Synthetic check runs (0093_synthetic_runs, D-132) are operational results of the installation like the
	// alert evaluations, so they share their class and 30-day retention.
	{"synthetic_runs_local", "alerts", "toDateTime(timestamp)", 30},
}

// TTLOptions are the inputs of the TTL plan.
type TTLOptions struct {
	Cluster          string
	APMRetentionDays int
	Storage          config.Storage
	// ClassRetentionDays replaces the schema delete TTL of the logs, traces and metrics classes when > 0: the longest
	// plan retention with per-tenant retention enabled (D-081, internal/quota.TableRetentionDays).
	ClassRetentionDays map[string]int
}

// deleteDays returns the delete TTL of t: the APM retention, a class override or the schema's.
func (o TTLOptions) deleteDays(t TTLTable) int {
	if t.Days == 0 {
		return o.APMRetentionDays
	}
	if d := o.ClassRetentionDays[t.Class]; d > 0 {
		return d
	}
	return t.Days
}

// TTLOptionsFromConfig returns the TTL options of cfg.
func TTLOptionsFromConfig(cfg config.Config) TTLOptions {
	return TTLOptions{Cluster: cfg.ClickHouseCluster, APMRetentionDays: cfg.APM.RetentionDays, Storage: cfg.Storage}
}

func (o TTLOptions) validate() error {
	if !clusterRe.MatchString(o.Cluster) {
		return fmt.Errorf("invalid cluster name %q", o.Cluster)
	}
	if o.APMRetentionDays < 1 || o.APMRetentionDays > 3650 {
		return fmt.Errorf("APM retention must be between 1 and 3650 days, got %d", o.APMRetentionDays)
	}
	if o.Storage.TieringEnabled && !policyRe.MatchString(o.Storage.Policy) {
		return fmt.Errorf("invalid storage policy name %q", o.Storage.Policy)
	}
	return nil
}

// TTLExpression returns the TTL of t: moves to the warm and cold volumes after the tier's ages, then the delete after
// deleteDays. A move that is not earlier than the delete (or, for warm, than the cold move) is left out.
func TTLExpression(t TTLTable, deleteDays int, tier config.StorageTier) string {
	var parts []string
	cold := tier.ColdAfterDays > 0 && tier.ColdAfterDays < deleteDays
	if tier.WarmAfterDays > 0 && tier.WarmAfterDays < deleteDays && (!cold || tier.WarmAfterDays < tier.ColdAfterDays) {
		parts = append(parts, fmt.Sprintf("%s + INTERVAL %d DAY TO VOLUME '%s'", t.Time, tier.WarmAfterDays, WarmVolume))
	}
	if cold {
		parts = append(parts, fmt.Sprintf("%s + INTERVAL %d DAY TO VOLUME '%s'", t.Time, tier.ColdAfterDays, ColdVolume))
	}
	parts = append(parts, fmt.Sprintf("%s + INTERVAL %d DAY", t.Time, deleteDays))
	return strings.Join(parts, ", ")
}

func hasMove(expr, volume string) bool { return strings.Contains(expr, " TO VOLUME '"+volume+"'") }

// TTLStep kinds.
const (
	StepStoragePolicy = "storage_policy"
	StepTTL           = "ttl"
)

// TTLStep is one ALTER TABLE … ON CLUSTER of a TTL plan.
type TTLStep struct {
	Table string
	Kind  string // StepStoragePolicy or StepTTL
	From  string // current policy(s) or TTL
	To    string
	SQL   string
}

// TTLPlan is what ApplyTableTTLs changes; an empty plan changes nothing.
type TTLPlan struct {
	Steps []TTLStep
	// APM retention recorded before and after (apm_retention_days, kept for older openlog-migrate versions).
	APMRetentionFrom, APMRetentionTo int
	// Tiering reports whether any table gets a move (the storage policy was checked on every replica).
	Tiering bool
}

// Changed reports whether the plan alters anything.
func (p TTLPlan) Changed() bool { return len(p.Steps) > 0 || p.APMRetentionFrom != p.APMRetentionTo }

// TTLState is the recorded and live state a plan is computed from.
type TTLState struct {
	// Recorded holds the table_settings rows apm_retention_days and ttl:<table>.
	Recorded map[string]string
	// Policies holds the distinct storage_policy of each table over all replicas; only read when a move is wanted.
	Policies map[string][]string
}

// BuildTTLPlan computes the ALTERs that bring the managed tables to opts. Storage policy switches come first (a TTL
// naming a volume requires it), then the TTLs. Without a recorded TTL a table is assumed to have the schema's TTL
// (APM tables: the recorded APM retention), so nothing changes while tiering was never enabled and the APM retention
// is unchanged.
func BuildTTLPlan(opts TTLOptions, st TTLState) (TTLPlan, error) {
	if err := opts.validate(); err != nil {
		return TTLPlan{}, err
	}
	applied := DefaultAPMRetentionDays
	if v := strings.TrimSpace(st.Recorded[APMRetentionSetting]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return TTLPlan{}, fmt.Errorf("recorded %s %q: %w", APMRetentionSetting, v, err)
		}
		applied = n
	}
	plan := TTLPlan{APMRetentionFrom: applied, APMRetentionTo: opts.APMRetentionDays}
	var policySteps, ttlSteps []TTLStep
	for _, t := range TTLTables {
		days, current := opts.deleteDays(t), t.Days
		if t.Days == 0 {
			current = applied
		}
		from := st.Recorded[TTLSettingPrefix+t.Table]
		if from == "" {
			from = TTLExpression(t, current, config.StorageTier{})
		}
		to := TTLExpression(t, days, opts.Storage.Tier(t.Class))
		if hasMove(to, WarmVolume) || hasMove(to, ColdVolume) {
			plan.Tiering = true
			pols := st.Policies[t.Table]
			if len(pols) != 1 || pols[0] != opts.Storage.Policy {
				policySteps = append(policySteps, TTLStep{Table: t.Table, Kind: StepStoragePolicy, From: strings.Join(pols, ","),
					To: opts.Storage.Policy, SQL: StoragePolicyStatement(opts.Cluster, t.Table, opts.Storage.Policy)})
			}
		}
		if from != to {
			ttlSteps = append(ttlSteps, TTLStep{Table: t.Table, Kind: StepTTL, From: from, To: to, SQL: TTLStatement(opts.Cluster, t.Table, to)})
		}
	}
	plan.Steps = append(policySteps, ttlSteps...)
	return plan, nil
}

// StoragePolicyStatement switches table to policy. materialize_ttl_recalculate_only makes the MATERIALIZE TTL that
// follows MODIFY TTL only recompute the TTL info of existing parts (rows past the delete TTL are removed by the next
// TTL merge or part drop) instead of rewriting every part.
func StoragePolicyStatement(cluster, table, policy string) string {
	return fmt.Sprintf("ALTER TABLE openlog.%s ON CLUSTER '%s' MODIFY SETTING storage_policy = '%s', materialize_ttl_recalculate_only = 1",
		table, cluster, policy)
}

// TTLStatement replaces the TTL of table.
func TTLStatement(cluster, table, expr string) string {
	return fmt.Sprintf("ALTER TABLE openlog.%s ON CLUSTER '%s' MODIFY TTL %s", table, cluster, expr)
}

// LoadTTLState reads the recorded TTLs and, when opts wants moves, checks that every replica defines the storage
// policy with the needed volumes and reads the tables' current policies.
func LoadTTLState(ctx context.Context, conn clickhouse.Conn, opts TTLOptions) (TTLState, error) {
	st := TTLState{Recorded: map[string]string{}}
	if err := opts.validate(); err != nil {
		return st, err
	}
	var exists uint8
	if err := conn.QueryRow(ctx, "EXISTS TABLE openlog.table_settings").Scan(&exists); err != nil {
		return st, fmt.Errorf("check table_settings: %w", err)
	}
	if exists == 1 {
		// All replicas: a record written by the previous run may not have reached every replica yet, and the
		// Distributed table reads one replica per shard.
		qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"apm": APMRetentionSetting, "prefix": TTLSettingPrefix}),
			ch.WithSettings(ch.Settings{"skip_unavailable_shards": 1}))
		rows, err := conn.Query(qctx, fmt.Sprintf("SELECT name, argMax(value, updated_at) FROM clusterAllReplicas('%s', openlog.table_settings_local) "+
			"WHERE name = {apm:String} OR startsWith(name, {prefix:String}) GROUP BY name", opts.Cluster))
		if err != nil {
			return st, fmt.Errorf("read table_settings: %w", err)
		}
		for rows.Next() {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				rows.Close()
				return st, err
			}
			st.Recorded[name] = value
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return st, err
		}
	}
	// Moves wanted? Plan with unknown policies first; only then touch the replicas.
	probe, err := BuildTTLPlan(opts, st)
	if err != nil || !probe.Tiering {
		return st, err
	}
	wantWarm := false
	for _, t := range TTLTables {
		days := opts.deleteDays(t)
		wantWarm = wantWarm || hasMove(TTLExpression(t, days, opts.Storage.Tier(t.Class)), WarmVolume)
	}
	if err := CheckStoragePolicy(ctx, conn, opts.Cluster, opts.Storage.Policy, wantWarm); err != nil {
		return st, err
	}
	st.Policies, err = tablePolicies(ctx, conn, opts.Cluster)
	return st, err
}

// CheckStoragePolicy verifies that every replica of cluster defines policy with the volumes default (hot), cold and,
// with warm, warm. clusterAllReplicas fails when a replica is unreachable, like ON CLUSTER DDL would stall.
func CheckStoragePolicy(ctx context.Context, conn clickhouse.Conn, cluster, policy string, warm bool) error {
	if !clusterRe.MatchString(cluster) || !policyRe.MatchString(policy) {
		return fmt.Errorf("invalid cluster %q or storage policy %q", cluster, policy)
	}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"policy": policy}))
	rows, err := conn.Query(qctx, fmt.Sprintf("SELECT hostName() AS host, groupArrayIf(volume_name, policy_name = {policy:String}) "+
		"FROM clusterAllReplicas('%s', system.storage_policies) GROUP BY host ORDER BY host", cluster))
	if err != nil {
		return fmt.Errorf("read storage policies of cluster %s: %w", cluster, err)
	}
	defer rows.Close()
	var problems []string
	hosts := 0
	for rows.Next() {
		var host string
		var volumes []string
		if err := rows.Scan(&host, &volumes); err != nil {
			return err
		}
		hosts++
		need := []string{HotVolume, ColdVolume}
		if warm {
			need = append(need, WarmVolume)
		}
		switch {
		case len(volumes) == 0:
			problems = append(problems, fmt.Sprintf("%s: policy not defined", host))
		case volumes[0] != HotVolume:
			problems = append(problems, fmt.Sprintf("%s: first volume is %q, must be %q (the tables' current volume)", host, volumes[0], HotVolume))
		default:
			for _, v := range need {
				if !contains(volumes, v) {
					problems = append(problems, fmt.Sprintf("%s: no volume %q (volumes %s)", host, v, strings.Join(volumes, ",")))
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hosts == 0 {
		return fmt.Errorf("cluster %s has no replicas", cluster)
	}
	if len(problems) > 0 {
		return fmt.Errorf("OPENLOG_STORAGE_TIERING_ENABLED: storage policy %q is not usable (configure storage_configuration on every "+
			"ClickHouse server, docs/operations/tiered-storage.md): %s", policy, strings.Join(problems, "; "))
	}
	return nil
}

func tablePolicies(ctx context.Context, conn clickhouse.Conn, cluster string) (map[string][]string, error) {
	names := make([]string, len(TTLTables))
	for i, t := range TTLTables {
		names[i] = t.Table
	}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"tables": "['" + strings.Join(names, "','") + "']"}))
	rows, err := conn.Query(qctx, fmt.Sprintf("SELECT name, groupUniqArray(storage_policy) FROM clusterAllReplicas('%s', system.tables) "+
		"WHERE database = 'openlog' AND has({tables:Array(String)}, name) GROUP BY name", cluster))
	if err != nil {
		return nil, fmt.Errorf("read table storage policies: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var name string
		var pols []string
		if err := rows.Scan(&name, &pols); err != nil {
			return nil, err
		}
		sort.Strings(pols)
		out[name] = pols
	}
	return out, rows.Err()
}

// PlanTableTTLs loads the state and builds the plan without changing anything (openlog-migrate -plan).
func PlanTableTTLs(ctx context.Context, conn clickhouse.Conn, opts TTLOptions) (TTLPlan, error) {
	st, err := LoadTTLState(ctx, conn, opts)
	if err != nil {
		return TTLPlan{}, err
	}
	return BuildTTLPlan(opts, st)
}

// ApplyTableTTLs brings the TTLs and storage policies of the managed tables to opts: the APM retention
// (OPENLOG_APM_RETENTION_DAYS, apm.md §8) and tiered storage moves (OPENLOG_STORAGE_*, D-066). Every ALTER runs ON
// CLUSTER and is idempotent; the TTL of a table is recorded after its ALTER, so a failure part way is repaired by the
// next run. It returns the applied plan.
func ApplyTableTTLs(ctx context.Context, conn clickhouse.Conn, opts TTLOptions, log *slog.Logger) (TTLPlan, error) {
	plan, err := PlanTableTTLs(ctx, conn, opts)
	if err != nil {
		return plan, err
	}
	if !plan.Changed() {
		log.Debug("table TTLs unchanged", "apm_retention_days", opts.APMRetentionDays, "tiering", opts.Storage.TieringEnabled)
		return plan, nil
	}
	log.Info("changing table TTLs", "steps", len(plan.Steps), "apm_retention_from", plan.APMRetentionFrom,
		"apm_retention_to", plan.APMRetentionTo, "tiering", plan.Tiering, "policy", opts.Storage.Policy)
	for _, s := range plan.Steps {
		log.Info("alter table", "table", s.Table, "kind", s.Kind, "from", s.From, "to", s.To)
		if err := conn.Exec(ctx, s.SQL); err != nil {
			return plan, fmt.Errorf("table TTLs: %w\n%s", err, s.SQL)
		}
		if s.Kind == StepTTL {
			if err := recordSetting(ctx, conn, TTLSettingPrefix+s.Table, s.To); err != nil {
				return plan, err
			}
		}
	}
	if plan.APMRetentionFrom != plan.APMRetentionTo {
		if err := recordSetting(ctx, conn, APMRetentionSetting, strconv.Itoa(plan.APMRetentionTo)); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func recordSetting(ctx context.Context, conn clickhouse.Conn, name, value string) error {
	ictx := ch.Context(ctx, ch.WithSettings(ch.Settings{"distributed_foreground_insert": 1}))
	if err := conn.Exec(ictx, "INSERT INTO openlog.table_settings (name, value) VALUES (?, ?)", name, value); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
