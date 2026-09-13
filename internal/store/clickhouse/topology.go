package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Replica is one server of a shard as listed in system.clusters.
type Replica struct {
	Num     uint32
	Host    string
	Port    uint16
	IsLocal bool
}

// Addr is host:port of the replica's native protocol endpoint.
func (r Replica) Addr() string { return net.JoinHostPort(r.Host, strconv.Itoa(int(r.Port))) }

// Shard is one shard of a cluster.
type Shard struct {
	Num                 uint32
	Weight              uint32
	InternalReplication bool
	Replicas            []Replica
}

// Topology is the shard layout of a cluster. ShardIndex selects a shard exactly
// like the Distributed engine: shards get one slot per unit of weight in
// shard_num order, and a row goes to slot[key % total_weight].
type Topology struct {
	Cluster string
	Shards  []Shard
	// Layout fingerprints shard numbers and weights (not replicas): two topologies
	// with the same Layout place every key on the same shard.
	Layout string
	slots  []int
}

// NewTopology validates shards and builds the slot table.
func NewTopology(cluster string, shards []Shard) (*Topology, error) {
	shards = append([]Shard(nil), shards...)
	sort.Slice(shards, func(i, j int) bool { return shards[i].Num < shards[j].Num })
	t := &Topology{Cluster: cluster, Shards: shards}
	h := fnv.New32a()
	for i, s := range shards {
		if len(s.Replicas) == 0 {
			return nil, fmt.Errorf("cluster %q shard %d has no replicas", cluster, s.Num)
		}
		for range s.Weight {
			t.slots = append(t.slots, i)
		}
		fmt.Fprintf(h, "%d:%d;", s.Num, s.Weight)
	}
	if len(t.slots) == 0 {
		return nil, fmt.Errorf("cluster %q has no shard with weight > 0", cluster)
	}
	t.Layout = fmt.Sprintf("%08x", h.Sum32())
	return t, nil
}

// ShardIndex returns the index into Shards for a sharding key value.
func (t *Topology) ShardIndex(key uint64) int {
	return t.slots[key%uint64(len(t.slots))]
}

// String describes the topology for logs.
func (t *Topology) String() string {
	parts := make([]string, len(t.Shards))
	for i, s := range t.Shards {
		addrs := make([]string, len(s.Replicas))
		for j, r := range s.Replicas {
			addrs[j] = r.Addr()
		}
		parts[i] = fmt.Sprintf("shard %d (weight %d): %s", s.Num, s.Weight, strings.Join(addrs, ","))
	}
	return strings.Join(parts, "; ")
}

// LoadTopology reads the layout of cluster from system.clusters.
func LoadTopology(ctx context.Context, conn Conn, cluster string) (*Topology, error) {
	rows, err := conn.Query(ctx, `SELECT shard_num, shard_weight, internal_replication, replica_num, host_name, port, is_local
		FROM system.clusters WHERE cluster = ? ORDER BY shard_num, replica_num`, cluster)
	if err != nil {
		return nil, fmt.Errorf("query system.clusters: %w", err)
	}
	defer rows.Close()
	var shards []Shard
	for rows.Next() {
		var (
			shardNum, weight, replicaNum uint32
			internal, local              uint8
			host                         string
			port                         uint16
		)
		if err := rows.Scan(&shardNum, &weight, &internal, &replicaNum, &host, &port, &local); err != nil {
			return nil, fmt.Errorf("scan system.clusters: %w", err)
		}
		if len(shards) == 0 || shards[len(shards)-1].Num != shardNum {
			shards = append(shards, Shard{Num: shardNum, Weight: weight, InternalReplication: internal == 1})
		}
		s := &shards[len(shards)-1]
		s.Replicas = append(s.Replicas, Replica{Num: replicaNum, Host: host, Port: port, IsLocal: local == 1})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(shards) == 0 {
		return nil, fmt.Errorf("cluster %q not found in system.clusters", cluster)
	}
	return NewTopology(cluster, shards)
}

// Errors returned by VerifyShardingKeys.
var (
	// ErrSchemaNotReady: Distributed tables are missing (migrations may still be running).
	ErrSchemaNotReady = errors.New("distributed tables missing")
	// ErrShardingKeyMismatch: a Distributed table does not use the expected sharding key.
	ErrShardingKeyMismatch = errors.New("sharding key mismatch")
)

// VerifyShardingKeys checks that every Distributed table in expected (table name
// -> sharding expression, e.g. "cityHash64(tenant_id, host_id)") exists in
// database, points at <table>_local and uses exactly that sharding expression.
// Missing tables yield an error wrapping ErrSchemaNotReady (migrations may still
// be running); a different expression is a configuration error.
func VerifyShardingKeys(ctx context.Context, conn Conn, database string, expected map[string]string) error {
	rows, err := conn.Query(ctx, `SELECT name, engine_full FROM system.tables WHERE database = ? AND engine = 'Distributed'`, database)
	if err != nil {
		return fmt.Errorf("query system.tables: %w", err)
	}
	defer rows.Close()
	engines := map[string]string{}
	for rows.Next() {
		var name, engine string
		if err := rows.Scan(&name, &engine); err != nil {
			return err
		}
		engines[name] = engine
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return checkShardingKeys(engines, expected)
}

func checkShardingKeys(engines, expected map[string]string) error {
	names := make([]string, 0, len(expected))
	for n := range expected {
		names = append(names, n)
	}
	sort.Strings(names)
	var missing []string
	var errs []error
	for _, name := range names {
		engine, ok := engines[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		local, key, ok := parseDistributed(engine)
		if !ok {
			errs = append(errs, fmt.Errorf("%w: table %s: cannot parse engine %q", ErrShardingKeyMismatch, name, engine))
			continue
		}
		if local != name+"_local" || key != normalizeExpr(expected[name]) {
			errs = append(errs, fmt.Errorf("%w: table %s: engine %q, want local table %s_local with sharding key %s",
				ErrShardingKeyMismatch, name, engine, name, expected[name]))
		}
	}
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrSchemaNotReady, strings.Join(missing, ", ")))
	}
	return errors.Join(errs...)
}

// parseDistributed extracts the local table and the normalized sharding key from
// Distributed(cluster, database, table, sharding_key[, policy]) engine_full text.
func parseDistributed(engine string) (local, key string, ok bool) {
	open := strings.Index(engine, "Distributed(")
	if open < 0 {
		return "", "", false
	}
	s := engine[open+len("Distributed("):]
	// Split top-level arguments up to the matching ')'.
	var args []string
	depth, start, end := 0, 0, -1
	inQuote := false
	for i := 0; i < len(s) && end < 0; i++ {
		switch c := s[i]; {
		case c == '\'':
			inQuote = !inQuote
		case inQuote:
		case c == '(':
			depth++
		case c == ')' && depth == 0:
			args = append(args, s[start:i])
			end = i
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			args = append(args, s[start:i])
			start = i + 1
		}
	}
	if end < 0 || len(args) < 4 {
		return "", "", false
	}
	return normalizeExpr(args[2]), normalizeExpr(args[3]), true
}

func normalizeExpr(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\'', '`', '"':
			return -1
		}
		return r
	}, s)
}
