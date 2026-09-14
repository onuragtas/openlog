package deletion

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// Purger deletes one tenant's rows from every ClickHouse table with a tenant_id column: raw signals, rollups (metrics_1m,
// apm_*), inventories, Kubernetes and container tables and the usage_* tables. Like per-tenant retention (D-081) it
// submits partition-scoped mutations (ALTER TABLE <t> ON CLUSTER … DELETE IN PARTITION ID '<p>' WHERE tenant_id = …)
// for partitions that hold the tenant's rows only, a bounded number per step. Mutations rewrite parts on the disk they
// live on, so parts moved to S3 by tiered storage are rewritten there and the old objects are removed with the old
// parts. A step reports done once no mutation of the tenant is running and a re-count finds no row in any table.
type Purger struct {
	Conn     clickhouse.Conn
	Database string
	Cluster  string
	// MaxMutations bounds mutations submitted per step (default 50).
	MaxMutations int
	// Resubmit is how long a submitted mutation may stay invisible before it is submitted again (default 30m).
	Resubmit time.Duration
	Log      *slog.Logger
	Now      func() time.Time
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// Mutation is one partition-scoped delete.
type Mutation struct {
	Table       string
	PartitionID string
}

// SQL renders the mutation; the tenant must be ValidTenant and the identifiers identRe (checked by the caller).
func (m Mutation) SQL(database, cluster, tenant string) string {
	return fmt.Sprintf("ALTER TABLE `%s`.`%s` ON CLUSTER '%s' DELETE IN PARTITION ID '%s' WHERE tenant_id = '%s'",
		database, m.Table, cluster, m.PartitionID, tenant)
}

// PlanMutations chooses the partitions to delete now: partitions with rows that were not submitted within resubmit,
// oldest table/partition order, at most max.
func PlanMutations(partitions map[string]map[string]uint64, submitted map[string]map[string]time.Time, now time.Time, resubmit time.Duration, max int) []Mutation {
	var out []Mutation
	tables := make([]string, 0, len(partitions))
	for t := range partitions {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		ps := make([]string, 0, len(partitions[t]))
		for p, n := range partitions[t] {
			if n > 0 && identRe.MatchString(p) {
				ps = append(ps, p)
			}
		}
		sort.Strings(ps)
		for _, p := range ps {
			if at, ok := submitted[t][p]; ok && now.Sub(at) < resubmit {
				continue
			}
			if len(out) >= max {
				return out
			}
			out = append(out, Mutation{Table: t, PartitionID: p})
		}
	}
	return out
}

func (p *Purger) db() string {
	if p.Database == "" {
		return "openlog"
	}
	return p.Database
}

func (p *Purger) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Tables lists the MergeTree tables of the database with a tenant_id column.
func (p *Purger) Tables(ctx context.Context) ([]string, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"db": p.db()}))
	rows, err := p.Conn.Query(qctx, `SELECT name FROM system.tables
		WHERE database = {db:String} AND engine LIKE '%MergeTree%' AND NOT startsWith(name, '.')
		  AND name IN (SELECT table FROM system.columns WHERE database = {db:String} AND name = 'tenant_id')
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if identRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// Partitions counts the tenant's rows per table and partition (one replica per shard).
func (p *Purger) Partitions(ctx context.Context, tenant string, tables []string) (map[string]map[string]uint64, error) {
	out := map[string]map[string]uint64{}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"t": tenant}), ch.WithSettings(ch.Settings{"skip_unavailable_shards": 0}))
	for _, t := range tables {
		rows, err := p.Conn.Query(qctx, fmt.Sprintf("SELECT _partition_id, count() FROM cluster('%s', `%s`.`%s`) WHERE tenant_id = {t:String} GROUP BY _partition_id",
			p.Cluster, p.db(), t))
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		for rows.Next() {
			var pid string
			var n uint64
			if err := rows.Scan(&pid, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if out[t] == nil {
				out[t] = map[string]uint64{}
			}
			out[t][pid] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PendingMutations returns unfinished mutations of the tenant and the latest failure reason, if any.
func (p *Purger) PendingMutations(ctx context.Context, tenant string) (uint64, string, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"db": p.db(), "needle": "'" + tenant + "'"}))
	var n uint64
	var reason string
	err := p.Conn.QueryRow(qctx, fmt.Sprintf(`SELECT count(), any(latest_fail_reason) FROM clusterAllReplicas('%s', system.mutations)
		WHERE database = {db:String} AND NOT is_done AND position(command, {needle:String}) > 0`, p.Cluster)).Scan(&n, &reason)
	return n, reason, err
}

// Step advances the deletion of tenant: it records counts before the first mutation, submits due mutations and
// reports done (with prog.Verified) once nothing is left.
func (p *Purger) Step(ctx context.Context, tenant string, prog *Progress) (bool, error) {
	if !ValidTenant(tenant) || !identRe.MatchString(p.db()) || !identRe.MatchString(p.Cluster) {
		return false, fmt.Errorf("invalid tenant %q, database %q or cluster %q", tenant, p.db(), p.Cluster)
	}
	tables, err := p.Tables(ctx)
	if err != nil {
		return false, err
	}
	parts, err := p.Partitions(ctx, tenant, tables)
	if err != nil {
		return false, err
	}
	remaining := map[string]uint64{}
	var total uint64
	for t, ps := range parts {
		for _, n := range ps {
			remaining[t] += n
			total += n
		}
	}
	if prog.CountsBefore == nil {
		prog.CountsBefore = remaining
		if prog.CountsBefore == nil {
			prog.CountsBefore = map[string]uint64{}
		}
	}
	prog.Remaining = remaining
	pending, reason, err := p.PendingMutations(ctx, tenant)
	if err != nil {
		return false, err
	}
	if total == 0 && pending == 0 {
		prog.Verified = true
		return true, nil
	}
	if pending > 0 {
		if reason != "" {
			return false, fmt.Errorf("mutation failing: %s", strings.TrimSpace(reason))
		}
		return false, nil
	}
	max := p.MaxMutations
	if max <= 0 {
		max = 50
	}
	resubmit := p.Resubmit
	if resubmit <= 0 {
		resubmit = 30 * time.Minute
	}
	now := p.now().UTC()
	if prog.Submitted == nil {
		prog.Submitted = map[string]map[string]time.Time{}
	}
	for _, m := range PlanMutations(parts, prog.Submitted, now, resubmit, max) {
		if err := p.Conn.Exec(ctx, m.SQL(p.db(), p.Cluster, tenant)); err != nil {
			return false, fmt.Errorf("delete %s partition %s: %w", m.Table, m.PartitionID, err)
		}
		if prog.Submitted[m.Table] == nil {
			prog.Submitted[m.Table] = map[string]time.Time{}
		}
		prog.Submitted[m.Table][m.PartitionID] = now
		if p.Log != nil {
			p.Log.Info("tenant deletion mutation submitted", "table", m.Table, "partition", m.PartitionID)
		}
	}
	return false, nil
}
