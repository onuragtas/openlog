package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// ShardedOptions configure a ShardedWriter.
type ShardedOptions struct {
	// Conn holds user, password and MaxConns (per replica, default 8). Addr is unused.
	Conn     Options
	Database string
	Cluster  string
	// RefreshInterval re-reads system.clusters (default 1m).
	RefreshInterval time.Duration
	// InsertQuorum > 0 sets insert_quorum on every local insert.
	InsertQuorum int
	// ResolveAddr maps a replica to a dialable address (tests, NAT). Nil: host_name:port.
	ResolveAddr func(Replica) string
	// ReplicaDownFor is how long a replica that failed an insert is tried last (default 10s).
	ReplicaDownFor time.Duration
}

// blockInserter inserts one block on the replica at addr.
type blockInserter func(ctx context.Context, conn Conn, table string, columns []string, settings ch.Settings, rows [][]any) error

// ShardedWriter inserts rows directly into the *_local tables of the shard the
// Distributed engine would choose (D-018), failing over between the replicas of
// a shard. It refreshes the cluster topology periodically.
//
// Deduplication: every per-shard block uses the caller's token suffixed with
// ":<shard_num>/<layout>". ReplicatedMergeTree keeps block ids in Keeper per
// shard, so a retry on any replica of the same shard is deduplicated. The layout
// fingerprint changes when shards or weights change; rows re-sent after such a
// change get new tokens (possible duplicates, never a block wrongly dropped as a
// duplicate).
type ShardedWriter struct {
	opts      ShardedOptions
	bootstrap Conn
	log       *slog.Logger
	insert    blockInserter
	open      func(addr string) (Conn, error)
	now       func() time.Time

	mu    sync.RWMutex
	topo  *Topology
	conns map[string]*replicaConn // by dial address

	rr atomic.Uint64

	doneMu sync.Mutex
	done   map[string]time.Time

	shardInsert *prometheus.HistogramVec
	failovers   *prometheus.CounterVec
	shards      prometheus.Gauge
}

type replicaConn struct {
	addr      string
	conn      Conn
	shared    bool // bootstrap connection; not closed by the writer
	downUntil atomic.Int64
}

// NewShardedWriter loads the topology through bootstrap (the OPENLOG_CLICKHOUSE_ADDR
// connection). reg may be nil.
func NewShardedWriter(ctx context.Context, bootstrap Conn, opts ShardedOptions, log *slog.Logger, reg prometheus.Registerer) (*ShardedWriter, error) {
	w := newShardedWriter(bootstrap, opts, log, reg)
	topo, err := LoadTopology(ctx, bootstrap, opts.Cluster)
	if err != nil {
		return nil, err
	}
	if err := w.setTopology(topo); err != nil {
		return nil, err
	}
	return w, nil
}

func newShardedWriter(bootstrap Conn, opts ShardedOptions, log *slog.Logger, reg prometheus.Registerer) *ShardedWriter {
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = time.Minute
	}
	if opts.ReplicaDownFor <= 0 {
		opts.ReplicaDownFor = 10 * time.Second
	}
	if opts.Conn.MaxConns == 0 {
		opts.Conn.MaxConns = 8
	}
	w := &ShardedWriter{
		opts: opts, bootstrap: bootstrap, log: log, insert: InsertWithSettings, now: time.Now,
		conns: map[string]*replicaConn{}, done: map[string]time.Time{},
		shardInsert: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "openlog_processor_shard_insert_duration_seconds", Help: "Direct insert latency per ClickHouse shard (successful attempts).",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}, []string{"shard"}),
		failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_processor_shard_insert_failures_total", Help: "Failed direct insert attempts per shard replica (the next replica is tried).",
		}, []string{"shard", "replica"}),
		shards: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_processor_clickhouse_shards", Help: "Shards in the ClickHouse topology used for direct inserts.",
		}),
	}
	w.open = func(addr string) (Conn, error) {
		o := opts.Conn
		o.Addr = []string{addr}
		o.Database = opts.Database
		return openLazy(o)
	}
	if reg != nil {
		reg.MustRegister(w.shardInsert, w.failovers, w.shards)
	}
	return w
}

// openLazy creates a connection pool without dialing (dials happen per insert).
func openLazy(o Options) (Conn, error) {
	co, err := lazyOptions(o)
	if err != nil {
		return nil, err
	}
	return ch.Open(co)
}

// lazyOptions builds the driver options of a replica pool. The TLS settings of the
// bootstrap connection apply; with an empty ServerName each replica's host_name from
// system.clusters is verified, so replica certificates must carry those names.
func lazyOptions(o Options) (*ch.Options, error) {
	tc, err := o.TLS.Config()
	if err != nil {
		return nil, fmt.Errorf("clickhouse tls: %w", err)
	}
	return &ch.Options{
		Addr:            o.Addr,
		Auth:            ch.Auth{Database: o.Database, Username: o.User, Password: o.Password},
		DialTimeout:     5 * time.Second,
		MaxOpenConns:    o.MaxConns,
		MaxIdleConns:    o.MaxConns,
		ConnMaxLifetime: time.Hour,
		Compression:     &ch.Compression{Method: ch.CompressionLZ4},
		TLS:             tc,
	}, nil
}

// Topology returns the topology in use.
func (w *ShardedWriter) Topology() *Topology {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.topo
}

// Check reports readiness: a topology has been loaded.
func (w *ShardedWriter) Check(context.Context) error {
	if w.Topology() == nil {
		return errors.New("clickhouse topology not loaded")
	}
	return nil
}

func (w *ShardedWriter) setTopology(t *Topology) error {
	single := len(t.Shards) == 1 && len(t.Shards[0].Replicas) == 1
	conns := map[string]*replicaConn{}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range t.Shards {
		if !s.InternalReplication && len(s.Replicas) > 1 {
			w.log.Warn("clickhouse shard has internal_replication=false; direct inserts write one replica and rely on ReplicatedMergeTree replication",
				"cluster", t.Cluster, "shard", s.Num)
		}
		for _, r := range s.Replicas {
			addr := w.dialAddr(r, single)
			if c, ok := w.conns[addr]; ok {
				conns[addr] = c
				continue
			}
			if single && w.opts.ResolveAddr == nil {
				// 1 shard x 1 replica (single profile): whichever server answers the
				// bootstrap connection is that replica, so reuse it. This keeps
				// OPENLOG_CLICKHOUSE_ADDR=localhost:9000 working when host_name only
				// resolves inside the container network.
				conns[addr] = &replicaConn{addr: addr, conn: w.bootstrap, shared: true}
				continue
			}
			c, err := w.open(addr)
			if err != nil {
				return fmt.Errorf("open clickhouse replica %s: %w", addr, err)
			}
			conns[addr] = &replicaConn{addr: addr, conn: c}
		}
	}
	for addr, c := range w.conns {
		if _, ok := conns[addr]; !ok && !c.shared {
			_ = c.conn.Close()
		}
	}
	changed := w.topo == nil || w.topo.String() != t.String()
	w.conns, w.topo = conns, t
	w.shards.Set(float64(len(t.Shards)))
	if changed {
		w.log.Info("clickhouse topology for direct inserts", "cluster", t.Cluster, "layout", t.Layout, "shards", t.String())
	}
	return nil
}

func (w *ShardedWriter) dialAddr(r Replica, single bool) string {
	if w.opts.ResolveAddr != nil {
		return w.opts.ResolveAddr(r)
	}
	if single {
		return "bootstrap"
	}
	return r.Addr()
}

// Run refreshes the topology every RefreshInterval until ctx is done. Refresh
// failures keep the previous topology.
func (w *ShardedWriter) Run(ctx context.Context) {
	t := time.NewTicker(w.opts.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		topo, err := LoadTopology(rctx, w.bootstrap, w.opts.Cluster)
		cancel()
		if err == nil {
			err = w.setTopology(topo)
		}
		if err != nil && ctx.Err() == nil {
			w.log.Warn("clickhouse topology refresh failed; keeping the previous one", "err", err)
		}
	}
}

// Close closes the replica connections the writer opened.
func (w *ShardedWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.conns {
		if !c.shared {
			_ = c.conn.Close()
		}
	}
	w.conns = map[string]*replicaConn{}
}

// ShardToken is the per-shard deduplication token for a block.
func ShardToken(token string, shardNum uint32, layout string) string {
	return token + ":" + strconv.FormatUint(uint64(shardNum), 10) + "/" + layout
}

// Insert splits rows by shard (cityHash64 over keyColumns, which must be String
// columns present in columns) and inserts each part into <database>.<table>_local
// on a replica of its shard, all shards in parallel. It returns an error if any
// shard failed on all replicas; shards that succeeded are remembered by token and
// skipped when the caller retries with the same token.
func (w *ShardedWriter) Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	w.mu.RLock()
	topo := w.topo
	w.mu.RUnlock()
	if topo == nil {
		return errors.New("clickhouse topology not loaded")
	}
	parts, err := splitByShard(topo, keyColumns, columns, rows)
	if err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	local := "`" + w.opts.Database + "`.`" + table + "_local`"
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for i, part := range parts {
		if len(part) == 0 {
			continue
		}
		shard := topo.Shards[i]
		tok := ShardToken(token, shard.Num, topo.Layout)
		if w.isDone(tok) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.insertShard(ctx, shard, local, columns, tok, part); err != nil {
				errs[i] = err
				return
			}
			w.markDone(tok)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func splitByShard(topo *Topology, keyColumns, columns []string, rows [][]any) ([][][]any, error) {
	parts := make([][][]any, len(topo.Shards))
	if len(topo.Shards) == 1 {
		parts[0] = rows
		return parts, nil
	}
	idx := make([]int, len(keyColumns))
	for k, name := range keyColumns {
		idx[k] = -1
		for i, c := range columns {
			if c == name {
				idx[k] = i
			}
		}
		if idx[k] < 0 {
			return nil, fmt.Errorf("sharding key column %q not in insert columns", name)
		}
	}
	keys := make([]string, len(idx))
	for _, row := range rows {
		for k, i := range idx {
			s, ok := row[i].(string)
			if !ok {
				return nil, fmt.Errorf("sharding key column %q is %T, want string", keyColumns[k], row[i])
			}
			keys[k] = s
		}
		si := topo.ShardIndex(CityHash64(keys...))
		parts[si] = append(parts[si], row)
	}
	return parts, nil
}

// insertShard tries the shard's replicas starting at a rotating position,
// replicas that failed recently last.
func (w *ShardedWriter) insertShard(ctx context.Context, shard Shard, table string, columns []string, token string, rows [][]any) error {
	w.mu.RLock()
	n := len(shard.Replicas)
	order := make([]*replicaConn, 0, n)
	var down []*replicaConn
	start := int(w.rr.Add(1) % uint64(n))
	nowNs := w.now().UnixNano()
	single := len(w.topo.Shards) == 1 && n == 1
	for k := range n {
		r := shard.Replicas[(start+k)%n]
		c := w.conns[w.dialAddr(r, single)]
		if c == nil {
			continue
		}
		if c.downUntil.Load() > nowNs {
			down = append(down, c)
		} else {
			order = append(order, c)
		}
	}
	w.mu.RUnlock()
	order = append(order, down...)
	if len(order) == 0 {
		return fmt.Errorf("shard %d: no replica connections", shard.Num)
	}
	settings := LocalInsertSettings(token, w.opts.InsertQuorum)
	shardLabel := strconv.FormatUint(uint64(shard.Num), 10)
	var errs []error
	for _, c := range order {
		start := time.Now()
		err := w.insert(ctx, c.conn, table, columns, settings, rows)
		if err == nil {
			c.downUntil.Store(0)
			w.shardInsert.WithLabelValues(shardLabel).Observe(time.Since(start).Seconds())
			return nil
		}
		errs = append(errs, fmt.Errorf("replica %s: %w", c.addr, err))
		w.failovers.WithLabelValues(shardLabel, c.addr).Inc()
		c.downUntil.Store(w.now().Add(w.opts.ReplicaDownFor).UnixNano())
		if ctx.Err() != nil {
			break
		}
		if len(order) > 1 {
			w.log.Warn("clickhouse replica insert failed, trying next replica", "shard", shard.Num, "replica", c.addr, "err", err)
		}
	}
	return fmt.Errorf("shard %d: %w", shard.Num, errors.Join(errs...))
}

const doneCacheMax = 100000

func (w *ShardedWriter) isDone(token string) bool {
	w.doneMu.Lock()
	defer w.doneMu.Unlock()
	_, ok := w.done[token]
	return ok
}

// markDone remembers a successfully inserted per-shard block so a retry of the
// same batch (same token, therefore same rows) does not upload it again.
// ClickHouse would deduplicate it anyway; this only saves the upload.
func (w *ShardedWriter) markDone(token string) {
	w.doneMu.Lock()
	defer w.doneMu.Unlock()
	now := w.now()
	if len(w.done) >= doneCacheMax {
		for k, t := range w.done {
			if now.Sub(t) > 10*time.Minute {
				delete(w.done, k)
			}
		}
		if len(w.done) >= doneCacheMax {
			w.done = map[string]time.Time{}
		}
	}
	w.done[token] = now
}
