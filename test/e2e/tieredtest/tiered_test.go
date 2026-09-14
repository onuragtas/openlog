//go:build tieredtest

// Package tieredtest proves tiered storage end to end (D-066, docs/operations/tiered-storage.md): a compose project
// (openlog-tiered) with MinIO and a 2-shard x 2-replica ClickHouse cluster using the storage policy of
// deploy/compose/clickhouse/storage-tiered.xml (+ warm volume). The openlog schema is applied, old-timestamped rows are
// inserted through the Distributed tables, and openlog-migrate's TTL step (migrate.ApplyTableTTLs) switches the tables
// to the policy. The test then checks moves to warm and S3 on every replica, reads through Distributed tables,
// deletion of cold parts, a restart of all nodes, an S3 outage and disabling tiering again.
//
//	go test -tags tieredtest -v -count=1 -timeout 30m ./test/e2e/tieredtest
//
// TIEREDTEST_KEEP=1 keeps the stack (docker compose -p openlog-tiered down -v).
package tieredtest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/schema"
)

const project = "openlog-tiered"

var replicas = []string{"ch-s1r1", "ch-s1r2", "ch-s2r1", "ch-s2r2"}

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	if out, err := compose("up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		fmt.Fprintf(os.Stderr, "tieredtest: compose up: %v\n%s\n", err, out)
		logs, _ := compose("logs", "--tail", "40")
		fmt.Fprintln(os.Stderr, logs)
		down()
		return 1
	}
	code := m.Run()
	down()
	return code
}

func down() {
	if os.Getenv("TIEREDTEST_KEEP") == "1" {
		fmt.Println("tieredtest: TIEREDTEST_KEEP=1, stack left running (docker compose -p " + project + " down -v)")
		return
	}
	if out, err := compose("down", "-v", "--remove-orphans", "--timeout", "10"); err != nil {
		fmt.Fprintf(os.Stderr, "tieredtest: compose down: %v\n%s\n", err, out)
	}
}

func compose(args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", "docker-compose.yml"}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func connect(t *testing.T, ctx context.Context) clickhouse.Conn {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := clickhouse.OpenRetry(cctx, clickhouse.Options{Addr: []string{"127.0.0.1:37000"}, Database: "default", User: "openlog", Password: "openlog"},
		func(err error) { t.Logf("waiting for clickhouse: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func count(t *testing.T, ctx context.Context, conn clickhouse.Conn, query string) uint64 {
	t.Helper()
	var n uint64
	if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func exec1(t *testing.T, ctx context.Context, conn clickhouse.Conn, query string) {
	t.Helper()
	ictx := ch.Context(ctx, ch.WithSettings(ch.Settings{"distributed_foreground_insert": 1}))
	if err := conn.Exec(ictx, query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func loadConfig(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

var tieredEnv = map[string]string{
	"OPENLOG_STORAGE_TIERING_ENABLED":         "true",
	"OPENLOG_STORAGE_WARM_AFTER_DAYS_METRICS": "1",
	"OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS": "5",
	"OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS":    "3",
	"OPENLOG_STORAGE_COLD_AFTER_DAYS_TRACES":  "2",
	"OPENLOG_STORAGE_COLD_AFTER_DAYS_APM":     "3",
}

// counts are the per-table checks that must not change when parts move, restart or tiering is disabled.
var counts = map[string]string{
	"logs":       "SELECT count() + sum(length(body)) FROM openlog.logs",
	"metrics":    "SELECT count() + toUInt64(sum(value)) FROM openlog.metrics",
	"spans":      "SELECT count() + sum(duration_ns) FROM openlog.spans",
	"apm_errors": "SELECT toUInt64(sum(count)) FROM openlog.apm_errors_1m",
}

func snapshot(t *testing.T, ctx context.Context, conn clickhouse.Conn) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for k, q := range counts {
		out[k] = count(t, ctx, conn, q)
	}
	return out
}

func TestTieredStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	conn := connect(t, ctx)
	defer func() { conn.Close() }()

	ms, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, conn, ms, log); err != nil {
		t.Fatal(err)
	}

	// Tiering disabled: nothing changes, not even a query to the replicas' policies.
	off := loadConfig(t, nil)
	if plan, err := migrate.ApplyTableTTLs(ctx, conn, migrate.TTLOptionsFromConfig(off), log); err != nil || plan.Changed() {
		t.Fatalf("disabled: %+v %v", plan, err)
	}

	// Old-timestamped rows through the Distributed tables (hours back, spread over the shards).
	exec1(t, ctx, conn, `INSERT INTO openlog.logs (tenant_id, timestamp, observed_timestamp, service_name, host_id, host_name, severity_text, body)
		SELECT 't' || toString(number % 3), now64(9) - toIntervalHour(number % (13 * 24)), now64(9), 'svc', 'host-' || toString(number % 11), 'h', 'INFO',
		       'log line ' || toString(number) FROM numbers(40000)`)
	exec1(t, ctx, conn, `INSERT INTO openlog.metrics (tenant_id, metric_name, metric_type, temporality, is_monotonic, unit, service_name, host_id, host_name,
		series_id, scope_name, start_timestamp, timestamp, value)
		SELECT 't' || toString(number % 3), 'system.cpu.utilization', 'gauge', 'unspecified', false, '1', 'svc', 'host-' || toString(number % 11), 'h',
		       number % 50, 's', now64(9) - toIntervalHour(number % (20 * 24)), now64(9) - toIntervalHour(number % (20 * 24)), number % 100 FROM numbers(40000)`)
	exec1(t, ctx, conn, `INSERT INTO openlog.spans (tenant_id, timestamp, duration_ns, trace_id, span_id, parent_span_id, name, kind, status_code, service_name, host_id)
		SELECT 't' || toString(number % 3), now64(9) - toIntervalHour(number % (6 * 24)), 1000 + number % 5000, lower(hex(cityHash64(number))),
		       lower(hex(cityHash64(number, 1))), '', 'GET /', 'server', 'ok', 'svc', 'host-' || toString(number % 11) FROM numbers(30000)`)
	exec1(t, ctx, conn, `INSERT INTO openlog.apm_errors_1m (tenant_id, service_name, service_namespace, deployment_environment, timestamp, error_group_id, count, samples)
		SELECT 't' || toString(number % 3), 'svc-' || toString(number % 5), '', 'prod', toStartOfMinute(now() - toIntervalHour(number % (9 * 24))), number % 7, 1, 1
		FROM numbers(20000)`)
	// TTL drops of expired parts normally wait merge_with_ttl_timeout (4 h); make the deletion check fast.
	exec1(t, ctx, conn, "ALTER TABLE openlog.apm_errors_1m_local ON CLUSTER 'openlog' MODIFY SETTING merge_with_ttl_timeout = 5")
	exec1(t, ctx, conn, "SYSTEM SYNC REPLICA ON CLUSTER 'openlog' openlog.logs_local")
	before := snapshot(t, ctx, conn)
	t.Logf("before: %v", before)

	on := loadConfig(t, tieredEnv)
	opts := migrate.TTLOptionsFromConfig(on)
	t.Run("enable", func(t *testing.T) {
		plan, err := migrate.ApplyTableTTLs(ctx, conn, opts, log)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Tiering || len(plan.Steps) == 0 {
			t.Fatalf("plan %+v", plan)
		}
		again, err := migrate.ApplyTableTTLs(ctx, conn, opts, log)
		if err != nil || again.Changed() {
			t.Fatalf("second run not a no-op: %+v %v", again, err)
		}
		rows, err := conn.Query(ctx, "SELECT hostName(), name, storage_policy FROM clusterAllReplicas('openlog', system.tables) "+
			"WHERE database = 'openlog' AND name IN ('logs_local', 'metrics_1m_local', 'hosts_local')")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var host, name, policy string
			if err := rows.Scan(&host, &name, &policy); err != nil {
				t.Fatal(err)
			}
			n++
			// metrics_1m: default cold after 30 days < 395: switched; hosts: not managed.
			want := "openlog_tiered"
			if name == "hosts_local" {
				want = "default"
			}
			if policy != want {
				t.Errorf("%s %s: policy %s, want %s", host, name, policy, want)
			}
		}
		if n != 3*len(replicas) {
			t.Errorf("%d table rows over the replicas", n)
		}
	})

	var status migrate.StorageStatus
	t.Run("moves", func(t *testing.T) {
		deadline := time.Now().Add(4 * time.Minute)
		for {
			st, err := migrate.ReadStorageStatus(ctx, conn, "openlog", on.Storage.Policy)
			if err != nil {
				t.Fatal(err)
			}
			status = st
			ok, why := movesDone(st)
			if ok {
				break
			}
			if time.Now().After(deadline) {
				b, _ := json.MarshalIndent(st, "", "  ")
				t.Fatalf("moves not done: %s\n%s", why, b)
			}
			time.Sleep(5 * time.Second)
		}
		for _, tb := range status.Tables {
			if tb.Replicas > 0 && (tb.Table == "logs_local" || tb.Table == "metrics_local" || tb.Table == "spans_local") {
				t.Logf("%-22s %-8s %-12s replicas=%d parts=%d rows=%d bytes=%d partitions=%s..%s", tb.Table, tb.Volume, tb.Disk, tb.Replicas,
					tb.Parts, tb.Rows, tb.Bytes, tb.OldestPartition, tb.NewestPartition)
			}
		}
		out, err := compose("exec", "-T", "minio", "sh", "-c",
			"mc alias set l http://127.0.0.1:9000 openlog-tiered openlog-tiered-secret >/dev/null && mc du --depth 3 l/openlog-tiered")
		if err != nil {
			t.Fatalf("mc du: %v\n%s", err, out)
		}
		for _, p := range []string{"01/ch-s1r1", "01/ch-s1r2", "02/ch-s2r1", "02/ch-s2r2"} {
			if !strings.Contains(out, "openlog-tiered/"+p) {
				t.Errorf("no objects under %s (credentials of that replica):\n%s", p, out)
			}
		}
		t.Logf("minio:\n%s", out)
	})

	t.Run("read", func(t *testing.T) {
		exec1(t, ctx, conn, "SYSTEM DROP FILESYSTEM CACHE ON CLUSTER 'openlog'")
		if got := snapshot(t, ctx, conn); fmt.Sprint(got) != fmt.Sprint(before) {
			t.Fatalf("after moves %v, before %v", got, before)
		}
		// A query only reading cold partitions.
		if n := count(t, ctx, conn, "SELECT count() FROM openlog.logs WHERE timestamp < now() - INTERVAL 5 DAY"); n == 0 {
			t.Error("no cold log rows")
		}
	})

	t.Run("restart", func(t *testing.T) {
		conn.Close()
		if out, err := compose(append([]string{"restart"}, replicas...)...); err != nil {
			t.Fatalf("restart: %v\n%s", err, out)
		}
		if out, err := compose(append([]string{"up", "-d", "--wait", "--wait-timeout", "180"}, replicas...)...); err != nil {
			t.Fatalf("wait: %v\n%s", err, out)
		}
		conn = connect(t, ctx)
		waitCluster(t, ctx, conn)
		if got := snapshot(t, ctx, conn); fmt.Sprint(got) != fmt.Sprint(before) {
			t.Fatalf("after restart %v, before %v", got, before)
		}
	})

	t.Run("delete", func(t *testing.T) {
		// APM retention 2 days: the move after 3 days is dropped, cold parts expire and are removed from S3.
		env := map[string]string{"OPENLOG_APM_RETENTION_DAYS": "2", "OPENLOG_APM_RELINK_MAX_AGE": "24h"}
		for k, v := range tieredEnv {
			env[k] = v
		}
		short := migrate.TTLOptionsFromConfig(loadConfig(t, env))
		plan, err := migrate.ApplyTableTTLs(ctx, conn, short, log)
		if err != nil || plan.APMRetentionTo != 2 {
			t.Fatalf("plan %+v %v", plan, err)
		}
		deadline := time.Now().Add(3 * time.Minute)
		for {
			old := count(t, ctx, conn, "SELECT count() FROM openlog.apm_errors_1m WHERE timestamp < toStartOfDay(now()) - INTERVAL 2 DAY")
			cold := count(t, ctx, conn, "SELECT count() FROM clusterAllReplicas('openlog', system.parts) WHERE database = 'openlog' "+
				"AND table = 'apm_errors_1m_local' AND active AND disk_name = 'openlog_s3'")
			if old == 0 && cold == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("expired apm rows %d, cold parts %d", old, cold)
			}
			time.Sleep(5 * time.Second)
		}
		if n := count(t, ctx, conn, "SELECT count() FROM openlog.apm_errors_1m"); n == 0 {
			t.Error("recent apm rows deleted too")
		}
		// Back to the 30-day retention for the remaining steps.
		if _, err := migrate.ApplyTableTTLs(ctx, conn, opts, log); err != nil {
			t.Fatal(err)
		}
		before["apm_errors"] = count(t, ctx, conn, counts["apm_errors"])
	})

	t.Run("s3_unavailable", func(t *testing.T) {
		if out, err := compose("stop", "minio"); err != nil {
			t.Fatalf("stop minio: %v\n%s", err, out)
		}
		defer func() {
			if out, err := compose("up", "-d", "--wait", "minio"); err != nil {
				t.Fatalf("start minio: %v\n%s", err, out)
			}
		}()
		exec1(t, ctx, conn, "SYSTEM DROP FILESYSTEM CACHE ON CLUSTER 'openlog'")
		qctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		start := time.Now()
		ictx := ch.Context(qctx, ch.WithSettings(ch.Settings{"distributed_foreground_insert": 1}))
		if err := conn.Exec(ictx, `INSERT INTO openlog.logs (tenant_id, timestamp, observed_timestamp, service_name, host_id, body)
			SELECT 't0', now64(9) - toIntervalHour(number % (10 * 24)), now64(9), 'svc', 'host-1', 'during outage' FROM numbers(1000)`); err != nil {
			t.Fatalf("insert while S3 is down: %v", err)
		}
		t.Logf("insert (incl. rows older than the move age) while S3 is down: ok in %s", time.Since(start).Round(time.Millisecond))
		start = time.Now()
		var hot uint64
		if err := conn.QueryRow(qctx, "SELECT count() FROM openlog.logs WHERE timestamp > now() - INTERVAL 1 DAY").Scan(&hot); err != nil {
			t.Errorf("query of hot partitions while S3 is down: %v", err)
		}
		t.Logf("hot-only query while S3 is down: %d rows in %s", hot, time.Since(start).Round(time.Millisecond))
		start = time.Now()
		var all uint64
		// An unreachable S3 can block the read for minutes (client retries); a deadline counts as the failed read.
		cctx, ccancel := context.WithTimeout(qctx, 60*time.Second)
		defer ccancel()
		err := conn.QueryRow(cctx, "SELECT count() + sum(length(body)) FROM openlog.logs").Scan(&all)
		if err == nil {
			t.Errorf("query of cold parts succeeded while S3 is down (%d)", all)
		} else {
			t.Logf("cold query while S3 is down failed after %s: %s", time.Since(start).Round(time.Millisecond), firstLine(err.Error()))
		}
		// A node restart while S3 is down: the server must come up.
		start = time.Now()
		if out, err := compose("restart", "ch-s2r2"); err != nil {
			t.Fatalf("restart ch-s2r2: %v\n%s", err, out)
		}
		if out, err := compose("up", "-d", "--wait", "--wait-timeout", "180", "ch-s2r2"); err != nil {
			t.Fatalf("ch-s2r2 did not become healthy while S3 is down: %v\n%s", err, out)
		}
		t.Logf("ch-s2r2 restarted while S3 is down in %s", time.Since(start).Round(time.Second))
	})

	t.Run("s3_recovered", func(t *testing.T) {
		before["logs"] += 1000 + uint64(1000*len("during outage"))
		deadline := time.Now().Add(3 * time.Minute)
		for {
			exec1(t, ctx, conn, "SYSTEM DROP FILESYSTEM CACHE ON CLUSTER 'openlog'")
			got := map[string]uint64{}
			var qerr error
			for k, q := range counts {
				var n uint64
				if qerr = conn.QueryRow(ctx, q).Scan(&n); qerr != nil {
					break
				}
				got[k] = n
			}
			if qerr == nil && fmt.Sprint(got) == fmt.Sprint(before) {
				break
			}
			if time.Now().After(deadline) {
				broken := count(t, ctx, conn, "SELECT count() FROM clusterAllReplicas('openlog', system.detached_parts) WHERE database = 'openlog'")
				t.Fatalf("after S3 recovered: %v (err %v), want %v; detached parts: %d", got, qerr, before, broken)
			}
			time.Sleep(5 * time.Second)
		}
		st, err := migrate.ReadStorageStatus(ctx, conn, "openlog", on.Storage.Policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range st.FailedMoves {
			t.Logf("failed moves 24h: %s %s %d, last: %s", f.Host, f.Table, f.Count, firstLine(f.LastError))
		}
		// A restart during the outage may detach empty parts of TTL drops that were not cleaned up yet ("ignored");
		// nothing may be broken.
		for _, d := range st.Detached {
			t.Logf("detached: %+v", d)
			if d.Reason != "ignored" {
				t.Errorf("detached parts after the outage: %+v", d)
			}
		}
		// Both replicas of every shard hold the same rows.
		rows, err := conn.Query(ctx, "SELECT table, hostName() AS h, sum(rows) FROM clusterAllReplicas('openlog', system.parts) "+
			"WHERE database = 'openlog' AND active AND table IN ('logs_local', 'metrics_local', 'spans_local') GROUP BY table, h")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		perHost := map[string]uint64{}
		for rows.Next() {
			var table, host string
			var n uint64
			if err := rows.Scan(&table, &host, &n); err != nil {
				t.Fatal(err)
			}
			perHost[table+"/"+host] = n
		}
		for _, table := range []string{"logs_local", "metrics_local", "spans_local"} {
			if perHost[table+"/ch-s1r1"] != perHost[table+"/ch-s1r2"] || perHost[table+"/ch-s2r1"] != perHost[table+"/ch-s2r2"] {
				t.Errorf("%s replicas differ: %v", table, perHost)
			}
		}
	})

	t.Run("disable", func(t *testing.T) {
		plan, err := migrate.ApplyTableTTLs(ctx, conn, migrate.TTLOptionsFromConfig(off), log)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range plan.Steps {
			if s.Kind != migrate.StepTTL || strings.Contains(s.To, "TO VOLUME") {
				t.Errorf("disable step %+v", s)
			}
		}
		if again, err := migrate.ApplyTableTTLs(ctx, conn, migrate.TTLOptionsFromConfig(off), log); err != nil || again.Changed() {
			t.Fatalf("second disable run: %+v %v", again, err)
		}
		var engine string
		if err := conn.QueryRow(ctx, "SELECT engine_full FROM system.tables WHERE database = 'openlog' AND name = 'logs_local'").Scan(&engine); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(engine, "TO VOLUME") || !strings.Contains(engine, "storage_policy = 'openlog_tiered'") {
			t.Errorf("logs_local after disable: %s", engine)
		}
		if got := snapshot(t, ctx, conn); fmt.Sprint(got) != fmt.Sprint(before) {
			t.Fatalf("after disable %v, before %v", got, before)
		}
	})
}

// movesDone reports whether the managed tables have parts on the warm/cold volumes on every replica and no part past
// its move TTL is left on the hot volume.
func movesDone(st migrate.StorageStatus) (bool, string) {
	on := map[string]int{}
	for _, tb := range st.Tables {
		if tb.OverdueParts > 0 {
			return false, fmt.Sprintf("%s: %d overdue parts on %s", tb.Table, tb.OverdueParts, tb.Volume)
		}
		on[tb.Table+"/"+tb.Volume] = tb.Replicas
	}
	for _, want := range []string{"logs_local/cold", "metrics_local/warm", "metrics_local/cold", "spans_local/cold", "apm_errors_1m_local/cold"} {
		if on[want] != len(replicas) {
			return false, fmt.Sprintf("%s on %d replicas", want, on[want])
		}
	}
	return true, ""
}

func waitCluster(t *testing.T, ctx context.Context, conn clickhouse.Conn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var n uint64
		err := conn.QueryRow(ctx, "SELECT count() FROM clusterAllReplicas('openlog', system.one)").Scan(&n)
		if err == nil && n == uint64(len(replicas)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cluster not reachable: %d %v", n, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
