//go:build integration

// Integration test for direct shard inserts (D-018) against a real 2-shard
// ClickHouse cluster (testdata/shardtest: shard 1 with two replicas, shard 2 with
// one). Run with:
//
//	go test -tags integration -count=1 -run TestShardedClickHouse ./internal/processor
//
// The test starts and removes its own compose project (openlog-shardtest).
// OPENLOG_SHARDTEST_KEEP=1 leaves it running; OPENLOG_SHARDTEST_NO_UP=1 uses a
// running one.
package processor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/schema"
)

const shardtestCompose = "testdata/shardtest/docker-compose.yml"

// Host ports of the compose project's native endpoints.
var shardtestAddrs = map[string]string{
	"ch-s1r1:9000": "127.0.0.1:19101",
	"ch-s1r2:9000": "127.0.0.1:19102",
	"ch-s2r1:9000": "127.0.0.1:19103",
}

func compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-p", "openlog-shardtest", "-f", shardtestCompose}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func TestMain(m *testing.M) {
	if os.Getenv("OPENLOG_SHARDTEST_NO_UP") == "" {
		if err := compose("up", "-d", "--wait", "keeper", "ch-s1r1", "ch-s1r2", "ch-s2r1"); err != nil {
			fmt.Fprintln(os.Stderr, "compose up:", err)
			_ = compose("down", "-v")
			os.Exit(1)
		}
	}
	code := m.Run()
	if os.Getenv("OPENLOG_SHARDTEST_KEEP") == "" && os.Getenv("OPENLOG_SHARDTEST_NO_UP") == "" {
		_ = compose("down", "-v")
	}
	os.Exit(code)
}

func openCH(t *testing.T, addr, database string) clickhouse.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: database, User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func shardedWriter(t *testing.T, bootstrap clickhouse.Conn, resolve map[string]string) *clickhouse.ShardedWriter {
	t.Helper()
	w, err := clickhouse.NewShardedWriter(context.Background(), bootstrap, clickhouse.ShardedOptions{
		Conn: clickhouse.Options{User: "openlog", Password: "openlog"}, Database: "openlog", Cluster: "openlog",
		ResolveAddr: func(r clickhouse.Replica) string { return resolve[r.Addr()] },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

// testRows builds rows for every table for tenant: hosts host-0..n-1, one trace per host.
func testRows(tenant string, n int, now time.Time) map[string][][]any {
	rows := map[string][][]any{}
	for i := range n {
		host := "host-" + strconv.Itoa(i)
		trace := fmt.Sprintf("%032x", uint64(i)*0x9e3779b97f4a7c15+1)
		ts := now.Add(time.Duration(i) * time.Millisecond)
		m := MetricRow{TenantID: tenant, MetricName: "system.cpu.utilization", MetricType: "gauge", Temporality: "unspecified",
			HostID: host, HostName: host, SeriesID: uint64(i), Timestamp: ts, StartTimestamp: ts, Value: float64(i)}
		rows[TableMetrics] = append(rows[TableMetrics], m.Values())
		l := LogRow{TenantID: tenant, Timestamp: ts, ObservedTimestamp: ts, HostID: host, Body: "log " + host, TraceID: trace}
		rows[TableLogs] = append(rows[TableLogs], l.Values())
		for s := range 3 {
			sp := SpanRow{TenantID: tenant, Timestamp: ts, DurationNs: 1000, TraceID: trace, SpanID: fmt.Sprintf("%016x", i*10+s),
				Name: "op", Kind: "server", StatusCode: "ok", HostID: host}
			rows[TableSpans] = append(rows[TableSpans], sp.Values())
		}
		// metric_exemplars is sharded by cityHash64(tenant_id, trace_id) (D-130), so an exemplar rides on the
		// same trace id as the log and the spans above and lands on the same shard they do.
		ex := ExemplarRow{TenantID: tenant, MetricName: "system.cpu.utilization", MetricType: "gauge", HostID: host, HostName: host,
			SeriesID: uint64(i), Timestamp: ts, Value: float64(i), TraceID: trace, SpanID: fmt.Sprintf("%016x", i*10)}
		rows[TableMetricExemplars] = append(rows[TableMetricExemplars], ex.Values())
		q := RelinkQueueRow{TenantID: tenant, Minute: ts.Truncate(time.Minute), TraceID: trace, Spans: 1, EnqueuedAt: ts}
		rows[TableRelinkQueue] = append(rows[TableRelinkQueue], q.Values())
		h := HostRow{TenantID: tenant, HostID: host, HostName: host, LastSeen: ts}
		rows[TableHosts] = append(rows[TableHosts], h.Values())
		it := InventoryItemRow{TenantID: tenant, HostID: host, SnapshotID: "s1", SnapshotTime: ts, Category: "package", ItemKey: "k" + host, Data: "{}"}
		rows[TableInventoryItems] = append(rows[TableInventoryItems], it.Values())
		sn := InventorySnapshotRow{TenantID: tenant, HostID: host, SnapshotID: "s1", SnapshotTime: ts, ItemCount: 1}
		rows[TableInventorySnapshots] = append(rows[TableInventorySnapshots], sn.Values())
		// usage_ingest_1h is sharded by cityHash64(tenant_id, signal) and sums rows with the same (tenant_id, hour,
		// signal): one distinct signal value per row keeps the rows apart and spreads them over both shards.
		u := UsageIngestRow{TenantID: tenant, Hour: now.Truncate(time.Hour), Signal: "signal-" + strconv.Itoa(i), Requests: 1, Bytes: uint64(100 + i)}
		rows[TableUsageIngest] = append(rows[TableUsageIngest], u.Values())
		// profiles is sharded by cityHash64(tenant_id, service_name) (0095_profiles): a flame graph is one
		// service over one window, so the service name varies per row to spread a tenant over both shards.
		svc := "svc-" + strconv.Itoa(i)
		pr := ProfileRow{TenantID: tenant, Timestamp: ts, ServiceName: svc, ServiceNamespace: "ns", Environment: "prod",
			HostID: host, ProfileType: "cpu", Unit: "nanoseconds", Stack: []string{"main", "work"}, Leaf: "work",
			Value: int64(i + 1), DurationNs: uint64(time.Second), ResourceAttributes: map[string]string{}, Attributes: map[string]string{}}
		rows[TableProfiles] = append(rows[TableProfiles], pr.Values())
		// The three db_monitoring tables are sharded by cityHash64(tenant_id, instance) (0096_db_monitoring,
		// D-138), so the instance varies for the same reason.
		inst, qid := "db-"+strconv.Itoa(i), "q"+strconv.Itoa(i)
		qs := DBQueryStatRow{TenantID: tenant, Timestamp: ts, IntervalSeconds: 60, HostID: host, HostName: host,
			DBSystem: "postgresql", Instance: inst, ServerAddress: "127.0.0.1", ServerPort: 5432, DBName: "app", DBUser: "app",
			QueryID: qid, QueryText: "SELECT 1", Fingerprint: uint64(i + 1), Calls: 1, TotalTimeMs: float64(i),
			Rows: 1, RowsExamined: 1}
		rows[TableDBQueryStats] = append(rows[TableDBQueryStats], qs.Values())
		ss := DBSessionSampleRow{TenantID: tenant, Timestamp: ts, HostID: host, HostName: host, DBSystem: "postgresql",
			Instance: inst, DBName: "app", DBUser: "app", SessionID: "s" + strconv.Itoa(i), State: "active",
			WaitEventType: "CPU", WaitEvent: "cpu", QueryID: qid, QueryText: "SELECT 1", Fingerprint: uint64(i + 1),
			DurationMs: float64(i), BlockingSessionIDs: []string{}, Application: "app", ClientAddress: "127.0.0.1"}
		rows[TableDBSessionSamples] = append(rows[TableDBSessionSamples], ss.Values())
		pl := DBQueryPlanRow{TenantID: tenant, CapturedAt: ts, Instance: inst, Fingerprint: uint64(i + 1),
			PlanHash: "h" + strconv.Itoa(i), DBSystem: "postgresql", HostID: host, DBName: "app", QueryText: "SELECT 1",
			PlanFormat: "json", Plan: "{}", TotalCost: float64(i)}
		rows[TableDBQueryPlans] = append(rows[TableDBQueryPlans], pl.Values())
	}
	return rows
}

// syncReplicas waits until both replicas of shard 1 have fetched every part.
func syncReplicas(t *testing.T, conns ...clickhouse.Conn) {
	t.Helper()
	for _, c := range conns {
		for _, tbl := range append(append([]string{}, Tables...), "metrics_1m", "trace_index") {
			if err := c.Exec(context.Background(), "SYSTEM SYNC REPLICA openlog."+tbl+"_local"); err != nil {
				t.Fatalf("sync replica %s: %v", tbl, err)
			}
		}
	}
}

func queryUint(t *testing.T, conn clickhouse.Conn, q string, args ...any) uint64 {
	t.Helper()
	var v uint64
	if err := conn.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestShardedClickHouse(t *testing.T) {
	ctx := context.Background()
	boot := openCH(t, "127.0.0.1:19101", "default")
	s1r2 := openCH(t, "127.0.0.1:19102", "default")

	migrations, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	mctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := migrate.Run(mctx, boot, migrations, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if err := clickhouse.VerifyShardingKeys(ctx, boot, "openlog", ShardingExpressions()); err != nil {
		t.Fatalf("sharding keys: %v", err)
	}

	sw := shardedWriter(t, boot, shardtestAddrs)
	topo := sw.Topology()
	if len(topo.Shards) != 2 || len(topo.Shards[0].Replicas) != 2 || len(topo.Shards[1].Replicas) != 1 || !topo.Shards[0].InternalReplication {
		t.Fatalf("topology: %s", topo)
	}
	t.Logf("topology %s (layout %s)", topo, topo.Layout)

	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	now := time.Now().UTC().Truncate(time.Minute)
	distTenant, directTenant := "dist-"+run, "direct-"+run
	distRows, directRows := testRows(distTenant, 40, now), testRows(directTenant, 40, now)
	// A table that joins Tables without joining testRows is a mistake this test has now made twice: first
	// metric_exemplars (fixed 2026-09-17), then profiles and the three db_monitoring tables. The placement
	// subtest reports it as "rows not spread over both shards: map[]", which names the table but not the
	// cause, and only the nightly run sees it. Fail here instead, saying what to do.
	for _, tbl := range Tables {
		if len(distRows[tbl]) == 0 || len(directRows[tbl]) == 0 {
			t.Fatalf("testRows produces no rows for %s: add them there when a table joins Tables", tbl)
		}
	}
	dist := ClickHouseWriter{Conn: boot, Database: "openlog"}
	direct := DirectWriter{W: sw}
	for _, tbl := range Tables {
		if err := dist.Write(ctx, tbl, "it:dist:"+run+":"+tbl, Columns[tbl], distRows[tbl]); err != nil {
			t.Fatalf("distributed insert %s: %v", tbl, err)
		}
		if err := direct.Write(ctx, tbl, "it:direct:"+run+":"+tbl, Columns[tbl], directRows[tbl]); err != nil {
			t.Fatalf("direct insert %s: %v", tbl, err)
		}
	}
	syncReplicas(t, boot, s1r2)

	t.Run("placement matches Distributed and server cityHash64", func(t *testing.T) {
		for _, tbl := range Tables {
			keys := ShardingKeys[tbl]
			q := fmt.Sprintf("SELECT tenant_id, %s, cityHash64(tenant_id, %s), _shard_num FROM openlog.%s WHERE tenant_id IN (?, ?)",
				keys[1], keys[1], tbl)
			rows, err := boot.Query(ctx, q, distTenant, directTenant)
			if err != nil {
				t.Fatal(err)
			}
			perShard := map[string]map[uint32]int{}
			n := 0
			for rows.Next() {
				var tenant, key string
				var serverHash uint64
				var shard uint32
				if err := rows.Scan(&tenant, &key, &serverHash, &shard); err != nil {
					t.Fatal(err)
				}
				n++
				goHash := clickhouse.CityHash64(tenant, key)
				if goHash != serverHash {
					t.Errorf("%s: cityHash64(%q, %q) server %d, go %d", tbl, tenant, key, serverHash, goHash)
				}
				if want := topo.Shards[topo.ShardIndex(goHash)].Num; shard != want {
					t.Errorf("%s: row (%s, %s) on shard %d, processor computes %d", tbl, tenant, key, shard, want)
				}
				if perShard[tenant] == nil {
					perShard[tenant] = map[uint32]int{}
				}
				perShard[tenant][shard]++
			}
			rows.Close()
			if want := len(distRows[tbl]) + len(directRows[tbl]); n != want {
				t.Errorf("%s: %d rows, want %d", tbl, n, want)
			}
			if len(perShard[directTenant]) != 2 || len(perShard[distTenant]) != 2 {
				t.Errorf("%s: rows not spread over both shards: %v", tbl, perShard)
			}
			t.Logf("%s: rows per shard %v", tbl, perShard)
		}
	})

	t.Run("materialized views fire on direct local inserts", func(t *testing.T) {
		q := "SELECT count(DISTINCT _shard_num) FROM openlog.%s WHERE tenant_id = ?"
		if n := queryUint(t, boot, "SELECT toUInt64(sum(value_count)) FROM openlog.metrics_1m WHERE tenant_id = ?", directTenant); n != 40 {
			t.Errorf("metrics_1m value_count = %d, want 40", n)
		}
		if n := queryUint(t, boot, fmt.Sprintf(q, "metrics_1m"), directTenant); n != 2 {
			t.Errorf("metrics_1m on %d shards", n)
		}
		if n := queryUint(t, boot, "SELECT toUInt64(sum(span_count)) FROM openlog.trace_index WHERE tenant_id = ?", directTenant); n != 120 {
			t.Errorf("trace_index span_count = %d, want 120", n)
		}
		if n := queryUint(t, boot, fmt.Sprintf(q, "trace_index"), directTenant); n != 2 {
			t.Errorf("trace_index on %d shards", n)
		}
	})

	t.Run("dedup token on retry, across replicas", func(t *testing.T) {
		// A fresh writer (no in-memory done cache) whose shard 1 replica 1 is unreachable:
		// the retry goes to replica 2, which must deduplicate via Keeper.
		failover := map[string]string{"ch-s1r1:9000": "127.0.0.1:1", "ch-s1r2:9000": "127.0.0.1:19102", "ch-s2r1:9000": "127.0.0.1:19103"}
		retry := DirectWriter{W: shardedWriter(t, boot, failover)}
		for _, tbl := range Tables {
			if err := retry.Write(ctx, tbl, "it:direct:"+run+":"+tbl, Columns[tbl], directRows[tbl]); err != nil {
				t.Fatalf("retry %s: %v", tbl, err)
			}
		}
		syncReplicas(t, boot, s1r2)
		for _, tbl := range Tables {
			if n := queryUint(t, boot, "SELECT count() FROM openlog."+tbl+" WHERE tenant_id = ?", directTenant); n != uint64(len(directRows[tbl])) {
				t.Errorf("%s: %d rows after retry, want %d", tbl, n, len(directRows[tbl]))
			}
		}
		if n := queryUint(t, boot, "SELECT toUInt64(sum(value_count)) FROM openlog.metrics_1m WHERE tenant_id = ?", directTenant); n != 40 {
			t.Errorf("metrics_1m value_count after retry = %d, want 40 (MV must not see deduplicated blocks)", n)
		}
		if n := queryUint(t, boot, "SELECT toUInt64(sum(span_count)) FROM openlog.trace_index WHERE tenant_id = ?", directTenant); n != 120 {
			t.Errorf("trace_index span_count after retry = %d, want 120", n)
		}
		// Same for the Distributed path.
		for _, tbl := range Tables {
			if err := dist.Write(ctx, tbl, "it:dist:"+run+":"+tbl, Columns[tbl], distRows[tbl]); err != nil {
				t.Fatalf("distributed retry %s: %v", tbl, err)
			}
		}
		syncReplicas(t, boot, s1r2)
		for _, tbl := range Tables {
			if n := queryUint(t, boot, "SELECT count() FROM openlog."+tbl+" WHERE tenant_id = ?", distTenant); n != uint64(len(distRows[tbl])) {
				t.Errorf("distributed %s: %d rows after retry, want %d", tbl, n, len(distRows[tbl]))
			}
		}
		if n := queryUint(t, boot, "SELECT toUInt64(sum(value_count)) FROM openlog.metrics_1m WHERE tenant_id = ?", distTenant); n != 40 {
			t.Errorf("distributed: metrics_1m value_count after retry = %d, want 40", n)
		}
		if n := queryUint(t, boot, "SELECT toUInt64(sum(span_count)) FROM openlog.trace_index WHERE tenant_id = ?", distTenant); n != 120 {
			t.Errorf("distributed: trace_index span_count after retry = %d, want 120", n)
		}
		// The token, not the content, decides: a new token inserts the same rows again.
		if err := retry.Write(ctx, TableLogs, "it:direct:"+run+":logs:other", Columns[TableLogs], directRows[TableLogs]); err != nil {
			t.Fatal(err)
		}
		syncReplicas(t, boot, s1r2)
		if n := queryUint(t, boot, "SELECT count() FROM openlog.logs WHERE tenant_id = ?", directTenant); n != 80 {
			t.Errorf("logs with a new token: %d rows, want 80", n)
		}
	})

	t.Run("ReplacingMergeTree hosts dedup across insert modes", func(t *testing.T) {
		tenant := "rmt-" + run
		older := testRows(tenant, 10, now)
		newer := testRows(tenant, 10, now.Add(time.Hour))
		if err := dist.Write(ctx, TableHosts, "it:rmt1:"+run, Columns[TableHosts], older[TableHosts]); err != nil {
			t.Fatal(err)
		}
		if err := direct.Write(ctx, TableHosts, "it:rmt2:"+run, Columns[TableHosts], newer[TableHosts]); err != nil {
			t.Fatal(err)
		}
		if err := direct.Write(ctx, TableInventorySnapshots, "it:rmt3:"+run, Columns[TableInventorySnapshots], newer[TableInventorySnapshots]); err != nil {
			t.Fatal(err)
		}
		if err := dist.Write(ctx, TableInventorySnapshots, "it:rmt4:"+run, Columns[TableInventorySnapshots], older[TableInventorySnapshots]); err != nil {
			t.Fatal(err)
		}
		syncReplicas(t, boot, s1r2)
		for _, tbl := range []string{TableHosts, TableInventorySnapshots} {
			if n := queryUint(t, boot, "SELECT count() FROM openlog."+tbl+" WHERE tenant_id = ?", tenant); n != 20 {
				t.Errorf("%s: %d raw rows, want 20", tbl, n)
			}
			if n := queryUint(t, boot, "SELECT uniqExact(tenant_id, host_id, _shard_num) FROM openlog."+tbl+" WHERE tenant_id = ?", tenant); n != 10 {
				t.Errorf("%s: a host's rows are on %d (host, shard) pairs, want 10 (same shard for both inserts)", tbl, n)
			}
			if n := queryUint(t, boot, "SELECT count() FROM openlog."+tbl+" FINAL WHERE tenant_id = ?", tenant); n != 10 {
				t.Errorf("%s FINAL: %d rows, want 10", tbl, n)
			}
		}
		if n := queryUint(t, boot, "SELECT countIf(last_seen >= ?) FROM openlog.hosts FINAL WHERE tenant_id = ?", now.Add(time.Hour), tenant); n != 10 {
			t.Errorf("hosts FINAL kept %d newest rows, want 10", n)
		}
	})
}
