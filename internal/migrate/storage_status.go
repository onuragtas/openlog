package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// StorageStatus is the tiered storage view of `openlog-admin storage status` (docs/operations/tiered-storage.md).
type StorageStatus struct {
	Cluster string `json:"cluster"`
	Policy  string `json:"policy"`
	// Volumes of Policy in order (empty when the servers do not define it).
	Volumes []PolicyVolume `json:"volumes"`
	Disks   []DiskStatus   `json:"disks"`
	Tables  []TableStorage `json:"tables"`
	Moves   []MoveStatus   `json:"moves_in_progress"`
	// FailedMoves counts MovePart errors of the last 24 hours from system.part_log (nil when part_log is unavailable).
	FailedMoves []FailedMoves `json:"failed_moves_24h"`
	// PartLogError explains why FailedMoves is unavailable.
	PartLogError string `json:"part_log_error,omitempty"`
	// Detached counts detached parts of the openlog database by reason (broken parts after S3 failures show up here).
	Detached []DetachedParts `json:"detached_parts"`
}

// DetachedParts counts detached parts of one table on one replica with one reason.
type DetachedParts struct {
	Host   string `json:"host"`
	Table  string `json:"table"`
	Reason string `json:"reason"`
	Disk   string `json:"disk"`
	Count  uint64 `json:"count"`
}

// PolicyVolume is one volume of the storage policy.
type PolicyVolume struct {
	Name  string   `json:"name"`
	Disks []string `json:"disks"`
}

// DiskStatus is one disk on one replica.
type DiskStatus struct {
	Host       string `json:"host"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Remote     bool   `json:"remote"`
	Broken     bool   `json:"broken"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	CachePath  string `json:"cache_path,omitempty"`
}

// TableStorage is one managed table on one volume, summed over the replicas.
type TableStorage struct {
	Table    string `json:"table"`
	Class    string `json:"class"`
	Volume   string `json:"volume"` // "" when the disk is not in the policy (table still on another policy)
	Disk     string `json:"disk"`
	Replicas int    `json:"replicas"`
	Parts    uint64 `json:"parts"`
	Rows     uint64 `json:"rows"`
	Bytes    uint64 `json:"bytes_on_disk"`
	// OldestPartition / NewestPartition are the partition IDs (dates) on this volume.
	OldestPartition string `json:"oldest_partition"`
	NewestPartition string `json:"newest_partition"`
	// OverdueParts are parts whose move TTL has expired but are still on this (hot) volume: pending moves.
	OverdueParts uint64 `json:"overdue_parts"`
	OverdueBytes uint64 `json:"overdue_bytes"`
	// Policies are the table's storage policies over the replicas.
	Policies []string `json:"policies"`
}

// MoveStatus is a part being moved (system.moves).
type MoveStatus struct {
	Host       string  `json:"host"`
	Table      string  `json:"table"`
	Part       string  `json:"part"`
	TargetDisk string  `json:"target_disk"`
	Elapsed    float64 `json:"elapsed_seconds"`
	Bytes      uint64  `json:"bytes"`
}

// FailedMoves summarizes failed part moves of one table on one replica.
type FailedMoves struct {
	Host      string    `json:"host"`
	Table     string    `json:"table"`
	Count     uint64    `json:"count"`
	Last      time.Time `json:"last"`
	LastError string    `json:"last_error"`
}

func managedTableList() string {
	names := make([]string, len(TTLTables))
	for i, t := range TTLTables {
		names[i] = t.Table
	}
	return "['" + strings.Join(names, "','") + "']"
}

// ReadStorageStatus reads the storage policy, disks, bytes per table per volume and moves from every replica of cluster.
func ReadStorageStatus(ctx context.Context, conn clickhouse.Conn, cluster, policy string) (StorageStatus, error) {
	st := StorageStatus{Cluster: cluster, Policy: policy}
	if !clusterRe.MatchString(cluster) || !policyRe.MatchString(policy) {
		return st, fmt.Errorf("invalid cluster %q or storage policy %q", cluster, policy)
	}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"policy": policy, "tables": managedTableList()}))
	all := func(table string) string { return fmt.Sprintf("clusterAllReplicas('%s', %s)", cluster, table) }

	// Volumes (from the connected server; CheckStoragePolicy compares all replicas).
	rows, err := conn.Query(qctx, "SELECT volume_name, disks FROM system.storage_policies WHERE policy_name = {policy:String} ORDER BY volume_priority")
	if err != nil {
		return st, fmt.Errorf("read storage policy: %w", err)
	}
	diskVolume := map[string]string{}
	for rows.Next() {
		var v PolicyVolume
		if err := rows.Scan(&v.Name, &v.Disks); err != nil {
			rows.Close()
			return st, err
		}
		for _, d := range v.Disks {
			diskVolume[d] = v.Name
		}
		st.Volumes = append(st.Volumes, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	// A cache disk is transparent here: the policy and system.parts both name the wrapped S3 disk.

	if err := scanAll(qctx, conn, "SELECT hostName(), name, type, is_remote, is_broken, free_space, total_space, cache_path FROM "+all("system.disks")+
		" ORDER BY 1, 2", func(r driverRows) error {
		var d DiskStatus
		if err := r.Scan(&d.Host, &d.Name, &d.Type, &d.Remote, &d.Broken, &d.FreeBytes, &d.TotalBytes, &d.CachePath); err != nil {
			return err
		}
		st.Disks = append(st.Disks, d)
		return nil
	}); err != nil {
		return st, fmt.Errorf("read disks: %w", err)
	}

	policies := map[string][]string{}
	if err := scanAll(qctx, conn, "SELECT name, groupUniqArray(storage_policy) FROM "+all("system.tables")+
		" WHERE database = 'openlog' AND has({tables:Array(String)}, name) GROUP BY name", func(r driverRows) error {
		var name string
		var pols []string
		if err := r.Scan(&name, &pols); err != nil {
			return err
		}
		sort.Strings(pols)
		policies[name] = pols
		return nil
	}); err != nil {
		return st, fmt.Errorf("read tables: %w", err)
	}

	class := map[string]string{}
	for _, t := range TTLTables {
		class[t.Table] = t.Class
	}
	hot := HotVolume
	if len(st.Volumes) > 0 {
		hot = st.Volumes[0].Name
	}
	if err := scanAll(qctx, conn, "SELECT table, disk_name, uniqExact(hostName()), count(), sum(rows), sum(bytes_on_disk), min(partition), max(partition), "+
		"countIf(arrayExists(x -> toUInt32(x) > 0 AND x <= now(), move_ttl_info.max)), sumIf(bytes_on_disk, arrayExists(x -> toUInt32(x) > 0 AND x <= now(), move_ttl_info.max)) "+
		"FROM "+all("system.parts")+" WHERE database = 'openlog' AND active AND has({tables:Array(String)}, table) "+
		"GROUP BY table, disk_name ORDER BY table, disk_name", func(r driverRows) error {
		var t TableStorage
		var replicas uint64
		if err := r.Scan(&t.Table, &t.Disk, &replicas, &t.Parts, &t.Rows, &t.Bytes, &t.OldestPartition, &t.NewestPartition,
			&t.OverdueParts, &t.OverdueBytes); err != nil {
			return err
		}
		t.Replicas, t.Class, t.Volume, t.Policies = int(replicas), class[t.Table], diskVolume[t.Disk], policies[t.Table]
		if t.Volume != hot || len(st.Volumes) == 0 {
			// Only parts on the hot volume are overdue (a part on warm may still wait for its cold move).
			t.OverdueParts, t.OverdueBytes = 0, 0
		}
		st.Tables = append(st.Tables, t)
		return nil
	}); err != nil {
		return st, fmt.Errorf("read parts: %w", err)
	}

	if err := scanAll(qctx, conn, "SELECT hostName(), table, part_name, target_disk_name, elapsed, part_size FROM "+all("system.moves")+
		" WHERE database = 'openlog' ORDER BY elapsed DESC", func(r driverRows) error {
		var m MoveStatus
		if err := r.Scan(&m.Host, &m.Table, &m.Part, &m.TargetDisk, &m.Elapsed, &m.Bytes); err != nil {
			return err
		}
		st.Moves = append(st.Moves, m)
		return nil
	}); err != nil {
		return st, fmt.Errorf("read moves: %w", err)
	}

	st.Detached = []DetachedParts{}
	if err := scanAll(qctx, conn, "SELECT hostName(), table, reason, disk, count() FROM "+all("system.detached_parts")+
		" WHERE database = 'openlog' GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3", func(r driverRows) error {
		var d DetachedParts
		if err := r.Scan(&d.Host, &d.Table, &d.Reason, &d.Disk, &d.Count); err != nil {
			return err
		}
		st.Detached = append(st.Detached, d)
		return nil
	}); err != nil {
		return st, fmt.Errorf("read detached parts: %w", err)
	}

	err = scanAll(qctx, conn, "SELECT hostName(), table, count(), max(event_time), argMax(exception, event_time) FROM "+all("system.part_log")+
		" WHERE event_type = 'MovePart' AND error != 0 AND database = 'openlog' AND event_time > now() - INTERVAL 1 DAY GROUP BY 1, 2 ORDER BY 1, 2",
		func(r driverRows) error {
			var f FailedMoves
			if err := r.Scan(&f.Host, &f.Table, &f.Count, &f.Last, &f.LastError); err != nil {
				return err
			}
			st.FailedMoves = append(st.FailedMoves, f)
			return nil
		})
	if err != nil {
		st.FailedMoves, st.PartLogError = nil, err.Error()
	} else if st.FailedMoves == nil {
		st.FailedMoves = []FailedMoves{}
	}
	return st, nil
}

type driverRows interface{ Scan(dest ...any) error }

func scanAll(ctx context.Context, conn clickhouse.Conn, query string, fn func(driverRows) error) error {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
