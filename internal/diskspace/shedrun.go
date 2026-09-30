package diskspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// RecordDrop writes the audit row of a day that was given up. The actor is the system, not a user: nobody asked
// for this, the disk did, and the row is the only lasting account of which day went and how large it was.
func (st PGStore) RecordDrop(ctx context.Context, d Drop) error {
	details, _ := json.Marshal(d)
	_, err := st.Pool.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
	        VALUES (NULL, NULL, 'system:disk-space', 'disk_space.partition_dropped', 'installation', 'disk_space', $1::jsonb, '')`,
		string(details))
	return err
}

// Carrying out what Plan decided. Kept apart from the planning so the decision stays a pure function that can be
// tested exhaustively, and so the only code that issues a DROP is small enough to read in one go.

// Partition ids of the sheddable tables are YYYYMMDD. Anything else is not a daily partition of a table this
// package owns, and is never interpolated into DDL.
func validPartitionID(id string) bool {
	if len(id) != 8 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

var shedTableSet = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range ShedTables() {
		m[t] = true
	}
	return m
}()

// Shedder reads the partitions of the sheddable tables and drops the days Plan chose.
type Shedder struct {
	CH       clickhouse.Conn
	Cluster  string
	Database string
	// DryRun plans and reports without issuing a single DROP.
	DryRun bool
	// Record is called for every applied drop, for the audit trail; nil skips it.
	Record func(context.Context, Drop) error
	Log    *slog.Logger
}

func (s *Shedder) cluster() string {
	if s.Cluster == "" {
		return "openlog"
	}
	return s.Cluster
}

func (s *Shedder) database() string {
	if s.Database == "" {
		return "openlog"
	}
	return s.Database
}

// Partitions reports the day-partitions of the sheddable tables that live on this disk of this replica.
//
// It asks about one replica and one disk on purpose: the plan has to free space on the disk that is full, and
// summing the same data across replicas would claim several times the space a drop actually frees.
func (s *Shedder) Partitions(ctx context.Context, disk Disk) ([]Partition, error) {
	cluster := s.cluster()
	if !clusterRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid ClickHouse cluster %q", cluster)
	}
	names := ShedTables()
	qctx := ch.Context(ctx, ch.WithSettings(ch.Settings{"max_execution_time": 30}),
		ch.WithParameters(ch.Parameters{
			"db": s.database(), "host": disk.Host, "disk": disk.Name,
			"tables": "['" + strings.Join(names, "','") + "']",
		}))
	rows, err := s.CH.Query(qctx, fmt.Sprintf(
		"SELECT table, partition_id, sum(bytes_on_disk) FROM clusterAllReplicas('%s', system.parts) "+
			"WHERE database = {db:String} AND active AND hostName() = {host:String} AND disk_name = {disk:String} "+
			"AND has({tables:Array(String)}, table) GROUP BY table, partition_id", cluster))
	if err != nil {
		return nil, fmt.Errorf("read system.parts: %w", err)
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		var p Partition
		if err := rows.Scan(&p.Table, &p.ID, &p.Bytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Apply drops the planned days. It stops at the first failure and returns what it managed, so a caller reports
// the space it actually freed rather than the space it hoped to.
func (s *Shedder) Apply(ctx context.Context, plan []Drop) ([]Drop, error) {
	cluster := s.cluster()
	if !clusterRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid ClickHouse cluster %q", cluster)
	}
	var done []Drop
	for _, d := range plan {
		if !validPartitionID(d.Partition) {
			return done, fmt.Errorf("refusing to drop partition %q: not a day", d.Partition)
		}
		for _, table := range d.Tables {
			// Both guards are cheap and this is the one place in openlog that deletes telemetry outright.
			if !shedTableSet[table] {
				return done, fmt.Errorf("refusing to drop from %q: not a sheddable table", table)
			}
			if s.DryRun {
				continue
			}
			stmt := fmt.Sprintf("ALTER TABLE `%s`.`%s` ON CLUSTER '%s' DROP PARTITION ID '%s'",
				s.database(), table, cluster, d.Partition)
			if err := s.CH.Exec(ctx, stmt); err != nil {
				return done, fmt.Errorf("drop %s partition %s: %w", table, d.Partition, err)
			}
		}
		done = append(done, d)
		if s.DryRun {
			continue
		}
		if s.Log != nil {
			// Deleting telemetry is never routine: it says what went, how much, and that it cannot be undone.
			s.Log.Warn("dropped a day to free disk space", "unit", d.Unit, "partition", d.Partition,
				"tables", strings.Join(d.Tables, ","), "bytes", d.Bytes)
		}
		if s.Record != nil {
			if err := s.Record(ctx, d); err != nil && s.Log != nil {
				// The data is already gone; failing here would only hide what happened.
				s.Log.Warn("cannot record a dropped partition", "partition", d.Partition, "err", err)
			}
		}
	}
	return done, nil
}
