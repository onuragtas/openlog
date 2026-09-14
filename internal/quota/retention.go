package quota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// Per-tenant retention (D-081). Table TTLs (owned by openlog-migrate, D-067) keep data for the longest plan
// retention; tenants whose effective retention is shorter lose their older rows through daily partition-scoped
// mutations: ALTER TABLE <t>_local ON CLUSTER DELETE IN PARTITION ID '<day>' WHERE tenant_id IN (…). Only one day
// partition per (table, retention group) is rewritten per day in steady state.

// DefaultRetentionDays are the schema TTLs of the raw signal tables (schema/clickhouse 0002-0004).
var DefaultRetentionDays = map[string]int{SignalLogs: 14, SignalTraces: 7, SignalMetrics: 30}

// RetentionTable is a raw table with a per-tenant retention; all are partitioned by toDate of their time column.
type RetentionTable struct {
	Table  string // *_local table
	Signal string
}

// RetentionTables lists the tables of D-081 (aggregates such as metrics_1m and APM keep the global retention).
var RetentionTables = []RetentionTable{
	{"logs_local", SignalLogs},
	{"spans_local", SignalTraces},
	{"trace_index_local", SignalTraces},
	{"metrics_local", SignalMetrics},
}

// TableRetentionDays returns the table TTL per signal when per-tenant retention is enabled: the longest effective
// retention of any plan, where a plan without a retention for the signal keeps the schema default. openlog-migrate
// applies it (migrate.TTLOptions); per-organization overrides can only be shorter (the API rejects longer ones).
func TableRetentionDays(c *Catalog) map[string]int {
	out := map[string]int{}
	for s, def := range DefaultRetentionDays {
		longest := 0
		for _, p := range c.Plans {
			d := p.Limits.RetentionDays[s]
			if d <= 0 {
				d = def
			}
			longest = max(longest, d)
		}
		out[s] = longest
	}
	return out
}

// RetentionTask is one mutation.
type RetentionTask struct {
	Table       string
	PartitionID string // YYYYMMDD
	Days        int
	Tenants     []string
	Hash        string
}

// SQL returns the mutation statement.
func (t RetentionTask) SQL(database, cluster string) string {
	return fmt.Sprintf("ALTER TABLE `%s`.%s ON CLUSTER '%s' DELETE IN PARTITION ID '%s' WHERE tenant_id IN (%s)",
		database, t.Table, cluster, t.PartitionID, quoteTenants(t.Tenants))
}

var partitionIDRe = regexp.MustCompile(`^\d{8}$`)

// PlanRetention computes the mutations for today. tenantDays holds each tenant's effective retention per signal (0 or
// missing = table retention); tableDays the table TTL per signal; partitions the active partition ids per table.
// A partition day D is deleted for tenants with retention N once D + N + 1 days <= today (every row of D is older
// than N days) and while the table TTL has not dropped it. Tasks are ordered oldest partition first.
func PlanRetention(tenantDays map[string]map[string]int, tableDays map[string]int, partitions map[string][]string, today time.Time) []RetentionTask {
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	var tasks []RetentionTask
	for _, rt := range RetentionTables {
		tdays := tableDays[rt.Signal]
		groups := map[int][]string{}
		for tenant, sig := range tenantDays {
			d := sig[rt.Signal]
			if d > 0 && (tdays <= 0 || d < tdays) && validTenant.MatchString(tenant) {
				groups[d] = append(groups[d], tenant)
			}
		}
		if len(groups) == 0 {
			continue
		}
		for _, p := range partitions[rt.Table] {
			if !partitionIDRe.MatchString(p) {
				continue
			}
			day, err := time.Parse("20060102", p)
			if err != nil {
				continue
			}
			if tdays > 0 && !day.AddDate(0, 0, tdays+1).After(today) {
				continue // dropped by the table TTL
			}
			for d, tenants := range groups {
				if day.AddDate(0, 0, d+1).After(today) {
					continue
				}
				sort.Strings(tenants)
				h := sha256.Sum256([]byte(strconv.Itoa(d) + ":" + strings.Join(tenants, ",")))
				tasks = append(tasks, RetentionTask{Table: rt.Table, PartitionID: p, Days: d, Tenants: append([]string(nil), tenants...),
					Hash: hex.EncodeToString(h[:8])})
			}
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].PartitionID != tasks[j].PartitionID {
			return tasks[i].PartitionID < tasks[j].PartitionID
		}
		if tasks[i].Table != tasks[j].Table {
			return tasks[i].Table < tasks[j].Table
		}
		return tasks[i].Days < tasks[j].Days
	})
	return tasks
}

// RetentionStore is the bookkeeping of RetentionJob (PGStore).
type RetentionStore interface {
	ListOrgPlans(ctx context.Context) ([]OrgPlan, error)
	RecentMutation(ctx context.Context, table, partition, hash string, since time.Time) (bool, error)
	RecordMutation(ctx context.Context, t RetentionTask) error
}

// RetentionJob runs PlanRetention against ClickHouse (api leader task, SaaS mode).
type RetentionJob struct {
	Conn         clickhouse.Conn
	Database     string
	Cluster      string
	Catalog      *Catalog
	Store        RetentionStore
	MaxMutations int
	Interval     time.Duration // default 1h
	Log          *slog.Logger
	Now          func() time.Time
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// TenantRetention returns the effective retention per tenant (only tenants whose plan sets one).
func TenantRetention(c *Catalog, orgs []OrgPlan) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, op := range orgs {
		p := c.EffectivePlan(op)
		if len(p.Limits.RetentionDays) > 0 {
			out[op.TenantID] = p.Limits.RetentionDays
		}
	}
	return out
}

// RunOnce submits due mutations (at most MaxMutations) and returns how many were submitted.
func (j *RetentionJob) RunOnce(ctx context.Context) (int, error) {
	db := j.Database
	if db == "" {
		db = "openlog"
	}
	if !identRe.MatchString(db) || !identRe.MatchString(j.Cluster) {
		return 0, fmt.Errorf("invalid database %q or cluster %q", db, j.Cluster)
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	log := j.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	orgs, err := j.Store.ListOrgPlans(ctx)
	if err != nil {
		return 0, err
	}
	tenantDays := TenantRetention(j.Catalog, orgs)
	if len(tenantDays) == 0 {
		return 0, nil
	}
	partitions, err := j.partitions(ctx, db)
	if err != nil {
		return 0, err
	}
	tasks := PlanRetention(tenantDays, TableRetentionDays(j.Catalog), partitions, now().UTC())
	limit := j.MaxMutations
	if limit <= 0 {
		limit = 20
	}
	submitted := 0
	for _, t := range tasks {
		if submitted >= limit {
			log.Info("retention mutations limit reached; continuing next run", "pending", len(tasks)-submitted)
			break
		}
		recent, err := j.Store.RecentMutation(ctx, t.Table, t.PartitionID, t.Hash, now().Add(-24*time.Hour))
		if err != nil {
			return submitted, err
		}
		if recent {
			continue
		}
		has, err := j.hasRows(ctx, db, t)
		if err != nil {
			return submitted, err
		}
		if !has {
			if err := j.Store.RecordMutation(ctx, t); err != nil {
				return submitted, err
			}
			continue
		}
		if err := j.Conn.Exec(ctx, t.SQL(db, j.Cluster)); err != nil {
			return submitted, fmt.Errorf("retention mutation %s %s: %w", t.Table, t.PartitionID, err)
		}
		if err := j.Store.RecordMutation(ctx, t); err != nil {
			return submitted, err
		}
		submitted++
		log.Info("per-tenant retention mutation submitted", "table", t.Table, "partition", t.PartitionID, "retention_days", t.Days, "tenants", len(t.Tenants))
	}
	return submitted, nil
}

func (j *RetentionJob) partitions(ctx context.Context, db string) (map[string][]string, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"db": db}), ch.WithSettings(ch.Settings{"skip_unavailable_shards": 1}))
	rows, err := j.Conn.Query(qctx, fmt.Sprintf("SELECT table, groupUniqArray(partition_id) FROM clusterAllReplicas('%s', system.parts) "+
		"WHERE database = {db:String} AND active AND table IN ('logs_local', 'spans_local', 'trace_index_local', 'metrics_local') GROUP BY table", j.Cluster))
	if err != nil {
		return nil, fmt.Errorf("read partitions: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t string
		var ps []string
		if err := rows.Scan(&t, &ps); err != nil {
			return nil, err
		}
		sort.Strings(ps)
		out[t] = ps
	}
	return out, rows.Err()
}

func (j *RetentionJob) hasRows(ctx context.Context, db string, t RetentionTask) (bool, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"p": t.PartitionID, "tenants": "['" + strings.Join(t.Tenants, "','") + "']"}),
		ch.WithSettings(ch.Settings{"skip_unavailable_shards": 1}))
	var n uint64
	err := j.Conn.QueryRow(qctx, fmt.Sprintf("SELECT count() FROM clusterAllReplicas('%s', `%s`.%s) WHERE _partition_id = {p:String} "+
		"AND has({tenants:Array(String)}, tenant_id)", j.Cluster, db, t.Table)).Scan(&n)
	return n > 0, err
}

// Run runs every Interval until ctx is done (leader task).
func (j *RetentionJob) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		_, err := j.RunOnce(rctx)
		cancel()
		if err != nil && ctx.Err() == nil && j.Log != nil {
			j.Log.Warn("per-tenant retention run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
