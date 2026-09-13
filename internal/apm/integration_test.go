//go:build integration

// Integration test of the APM materialized views and the edge-linking job on a real 2-shard
// ClickHouse cluster (testdata/shardtest: shard 1 with two replicas, shard 2 with one), using
// the processor's conversion and direct shard inserts (D-018). Run with:
//
//	go test -tags integration -count=1 -run TestAPMSharded ./internal/apm
//
// The test starts and removes its own compose project (openlog-apmshard, ports 192xx).
// OPENLOG_APMSHARD_KEEP=1 leaves it running; OPENLOG_APMSHARD_NO_UP=1 uses a running one.
package apm_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/processor"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/schema"
)

const shardCompose = "testdata/shardtest/docker-compose.yml"

var shardAddrs = map[string]string{
	"ch-s1r1:9000": "127.0.0.1:19201",
	"ch-s1r2:9000": "127.0.0.1:19202",
	"ch-s2r1:9000": "127.0.0.1:19203",
}

func compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-p", "openlog-apmshard", "-f", shardCompose}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func TestMain(m *testing.M) {
	noUp := os.Getenv("OPENLOG_APMSHARD_NO_UP") != ""
	if !noUp {
		if err := compose("up", "-d", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, "compose up:", err)
			_ = compose("down", "-v")
			os.Exit(1)
		}
	}
	code := m.Run()
	if os.Getenv("OPENLOG_APMSHARD_KEEP") == "" && !noUp {
		_ = compose("down", "-v")
	}
	os.Exit(code)
}

func openCH(t *testing.T, addr string) clickhouse.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func i64(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func res(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", service), str("service.namespace", "shop"),
		str("deployment.environment", "it"), str("host.id", "host-"+service), str("host.name", service+"-1")}}
}

// genTraces builds n traces frontend -> orders -> postgresql (and every 3rd trace frontend ->
// catalog -> redis); every 7th orders request fails with an exception; a quarter of the
// traces carries tracestate ot=th:8 (sample weight 2). Durations vary deterministically.
// Each service's spans are exported in separate requests, like separate processes.
func genTraces(n int, base time.Time) []*coltrace.ExportTraceServiceRequest {
	var reqs []*coltrace.ExportTraceServiceRequest
	for i := range n {
		traceID := make([]byte, 16)
		copy(traceID, fmt.Sprintf("tr%014d", i))
		sid := func(k byte) []byte { return []byte{k, byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i), 1, 2, 3} }
		start := base.Add(time.Duration(i) * 97 * time.Millisecond)
		ns := func(t time.Time) uint64 { return uint64(t.UnixNano()) }
		ts := ""
		if i%4 == 0 {
			ts = "ot=th:8"
		}
		ordersDur := time.Duration(5+(i*37)%400) * time.Millisecond
		fail := i%7 == 0
		span := func(id, parent []byte, name string, kind tracepb.Span_SpanKind, s time.Time, d time.Duration, attrs ...*commonpb.KeyValue) *tracepb.Span {
			return &tracepb.Span{TraceId: traceID, SpanId: id, ParentSpanId: parent, Name: name, Kind: kind, TraceState: ts,
				StartTimeUnixNano: ns(s), EndTimeUnixNano: ns(s.Add(d)), Attributes: attrs}
		}
		feServer := span(sid(1), nil, "GET /api/orders/:id", tracepb.Span_SPAN_KIND_SERVER, start, ordersDur+10*time.Millisecond,
			str("http.request.method", "GET"), str("http.route", "/api/orders/:id"))
		feClient := span(sid(2), sid(1), "GET", tracepb.Span_SPAN_KIND_CLIENT, start.Add(2*time.Millisecond), ordersDur+4*time.Millisecond,
			str("http.request.method", "GET"), str("server.address", "orders"), i64("server.port", 8080))
		orServer := span(sid(3), sid(2), "GET /orders/{id}", tracepb.Span_SPAN_KIND_SERVER, start.Add(3*time.Millisecond), ordersDur,
			str("http.request.method", "GET"), str("http.route", "/orders/{id}"))
		db := span(sid(4), sid(3), "SELECT orders", tracepb.Span_SPAN_KIND_CLIENT, start.Add(4*time.Millisecond), ordersDur/2,
			str("db.system", "postgresql"), str("db.name", "orders"), str("db.statement", fmt.Sprintf("SELECT * FROM orders WHERE id = %d", i)))
		if fail {
			orServer.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
			orServer.Events = []*tracepb.Span_Event{{Name: "exception", TimeUnixNano: ns(start.Add(5 * time.Millisecond)), Attributes: []*commonpb.KeyValue{
				str("exception.type", "*errors.errorString"), str("exception.message", fmt.Sprintf("order %d: inventory shard %d unavailable", i, i%4)),
				str("exception.stacktrace", "main.loadInventory()\n\t/app/main.go:88 +0x1\n")}}}
			feServer.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
		}
		fe := []*tracepb.Span{feServer, feClient}
		orders := []*tracepb.Span{orServer, db}
		var catalog []*tracepb.Span
		if i%3 == 0 {
			fe = append(fe, span(sid(5), sid(1), "GET", tracepb.Span_SPAN_KIND_CLIENT, start.Add(time.Millisecond), 3*time.Millisecond,
				str("http.request.method", "GET"), str("server.address", "catalog"), i64("server.port", 8080)))
			catalog = []*tracepb.Span{
				span(sid(6), sid(5), "GET /products", tracepb.Span_SPAN_KIND_SERVER, start.Add(time.Millisecond), 2*time.Millisecond,
					str("http.request.method", "GET"), str("http.route", "/products")),
				span(sid(7), sid(6), "GET", tracepb.Span_SPAN_KIND_CLIENT, start.Add(time.Millisecond), time.Millisecond,
					str("db.system", "redis"), str("db.statement", "GET catalog:all")),
			}
		}
		for svc, spans := range map[string][]*tracepb.Span{"frontend": fe, "orders": orders, "catalog": catalog} {
			if len(spans) == 0 {
				continue
			}
			reqs = append(reqs, &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
				{Resource: res(svc), ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}},
			}})
		}
	}
	sort.SliceStable(reqs, func(i, j int) bool { return false })
	return reqs
}

func queryFloat(t *testing.T, conn clickhouse.Conn, q string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := conn.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func syncReplicas(t *testing.T, conns ...clickhouse.Conn) {
	t.Helper()
	tables := []string{"spans", "apm_transactions_1m", "apm_service_edges_1m", "apm_service_links_1m", "apm_db_queries_1m",
		"apm_errors_1m", "apm_error_groups", "apm_services", "apm_service_hosts"}
	for _, c := range conns {
		for _, tbl := range tables {
			if err := c.Exec(context.Background(), "SYSTEM SYNC REPLICA openlog."+tbl+"_local"); err != nil {
				t.Fatalf("sync replica %s: %v", tbl, err)
			}
		}
	}
}

func TestAPMSharded(t *testing.T) {
	ctx := context.Background()
	boot := openCH(t, "127.0.0.1:19201")
	s1r2 := openCH(t, "127.0.0.1:19202")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	migrations, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	mctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := migrate.Run(mctx, boot, migrations, log); err != nil {
		t.Fatal(err)
	}
	sw, err := clickhouse.NewShardedWriter(ctx, boot, clickhouse.ShardedOptions{
		Conn: clickhouse.Options{User: "openlog", Password: "openlog"}, Database: "openlog", Cluster: "openlog",
		ResolveAddr: func(r clickhouse.Replica) string { return shardAddrs[r.Addr()] },
	}, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sw.Close()
	if n := len(sw.Topology().Shards); n != 2 {
		t.Fatalf("shards: %d", n)
	}

	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	tenant := "apm-" + run
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	const traces = 600 // ~58 s of traces at 97 ms spacing: spans over 1-2 minutes
	rows := processor.NewRows()
	for _, req := range genTraces(traces, base) {
		rows.AddTraces(tenant, base, req)
	}
	writer := processor.DirectWriter{W: sw}
	values := rows.Values(processor.TableSpans)
	token := "apm-it:" + run
	// Two blocks with distinct tokens, then the first one again (retry): must not double count.
	half := len(values) / 2
	for i, part := range [][][]any{values[:half], values[half:], values[:half]} {
		tok := token + ":" + strconv.Itoa(i%2)
		if i == 2 {
			// A fresh writer without the in-memory done cache, to exercise ClickHouse deduplication.
			sw2, err := clickhouse.NewShardedWriter(ctx, boot, clickhouse.ShardedOptions{
				Conn: clickhouse.Options{User: "openlog", Password: "openlog"}, Database: "openlog", Cluster: "openlog",
				ResolveAddr: func(r clickhouse.Replica) string { return shardAddrs[r.Addr()] },
			}, log, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sw2.Close()
			writer = processor.DirectWriter{W: sw2}
		}
		if err := writer.Write(ctx, processor.TableSpans, tok, processor.Columns[processor.TableSpans], part); err != nil {
			t.Fatalf("insert block %d: %v", i, err)
		}
	}
	syncReplicas(t, boot, s1r2)

	if n := queryFloat(t, boot, "SELECT toFloat64(count(DISTINCT _shard_num)) FROM openlog.spans WHERE tenant_id = ?", tenant); n != 2 {
		t.Fatalf("spans on %v shards", n)
	}

	t.Run("transactions match raw spans", func(t *testing.T) {
		for _, svc := range []string{"frontend", "orders", "catalog"} {
			rawReq := queryFloat(t, boot, "SELECT sum(sample_weight) FROM openlog.spans WHERE tenant_id = ? AND service_name = ? AND is_entry", tenant, svc)
			rawErr := queryFloat(t, boot, "SELECT sumIf(sample_weight, is_error) FROM openlog.spans WHERE tenant_id = ? AND service_name = ? AND is_entry", tenant, svc)
			rawP95 := queryFloat(t, boot, "SELECT quantileExactWeighted(0.95)(duration_ns / 1e6, toUInt64(sample_weight)) FROM openlog.spans WHERE tenant_id = ? AND service_name = ? AND is_entry", tenant, svc)
			var req, errs float64
			var hk []int16
			var hv []float64
			if err := boot.QueryRow(ctx, `SELECT sum(requests), sum(errors), tupleElement(sumMap(duration_hist), 1), tupleElement(sumMap(duration_hist), 2)
				FROM openlog.apm_transactions_1m WHERE tenant_id = ? AND service_name = ?`, tenant, svc).Scan(&req, &errs, &hk, &hv); err != nil {
				t.Fatal(err)
			}
			p95 := apm.HistQuantile(apm.NewHist(hk, hv), 0.95)
			t.Logf("%s: requests raw %.0f mv %.0f, errors raw %.0f mv %.0f, p95 raw %.2f ms hist %.2f ms", svc, rawReq, req, rawErr, errs, rawP95, p95)
			if rawReq == 0 || req != rawReq || errs != rawErr {
				t.Errorf("%s: counts differ", svc)
			}
			if rel := math.Abs(p95-rawP95) / rawP95; rel > 0.045 {
				t.Errorf("%s: p95 %.2f vs raw %.2f (%.1f%%)", svc, p95, rawP95, rel*100)
			}
			shards := queryFloat(t, boot, "SELECT toFloat64(count(DISTINCT _shard_num)) FROM openlog.apm_transactions_1m WHERE tenant_id = ? AND service_name = ?", tenant, svc)
			if svc != "catalog" && shards != 2 {
				t.Errorf("%s: aggregates on %v shards, want 2 (the read must merge across shards)", svc, shards)
			}
		}
		// 600 traces, a quarter with weight 2: 750 weighted frontend requests.
		if v := queryFloat(t, boot, "SELECT sum(requests) FROM openlog.apm_transactions_1m WHERE tenant_id = ? AND service_name = 'frontend'", tenant); v != 750 {
			t.Errorf("frontend weighted requests %v, want 750", v)
		}
	})

	t.Run("db queries, attribute edges, errors, services, hosts", func(t *testing.T) {
		var stmt string
		var calls float64
		if err := boot.QueryRow(ctx, `SELECT db_statement_normalized, sum(calls) FROM openlog.apm_db_queries_1m WHERE tenant_id = ? AND service_name = 'orders'
			GROUP BY db_statement_normalized`, tenant).Scan(&stmt, &calls); err != nil {
			t.Fatal(err)
		}
		if stmt != "SELECT * FROM orders WHERE id = ?" || calls != 750 {
			t.Errorf("db query %q calls %v", stmt, calls)
		}
		for target, want := range map[string]float64{"orders:8080": 750, "postgresql/orders": 750, "catalog:8080": 250, "redis": 250} {
			got := queryFloat(t, boot, "SELECT sum(calls) FROM openlog.apm_service_edges_1m WHERE tenant_id = ? AND target_name = ?", tenant, target)
			if got != want {
				t.Errorf("edge -> %s: %v calls, want %v", target, got, want)
			}
		}
		var groups, count float64
		var msg string
		if err := boot.QueryRow(ctx, `SELECT toFloat64(uniqExact(error_group_id)), sum(c), any(m) FROM
			(SELECT error_group_id, sum(count) AS c, anyLast(error_message) AS m FROM openlog.apm_error_groups WHERE tenant_id = ? AND service_name = 'orders' GROUP BY error_group_id)`,
			tenant).Scan(&groups, &count, &msg); err != nil {
			t.Fatal(err)
		}
		rawErrors := queryFloat(t, boot, "SELECT sumIf(sample_weight, error_group_id != 0) FROM openlog.spans WHERE tenant_id = ? AND service_name = 'orders'", tenant)
		if groups != 1 || count != rawErrors || msg != "order <n>: inventory shard <n> unavailable" {
			t.Errorf("error groups %v count %v (raw %v) message %q", groups, count, rawErrors, msg)
		}
		if n := queryFloat(t, boot, "SELECT sum(count) FROM openlog.apm_errors_1m WHERE tenant_id = ? AND service_name = 'orders'", tenant); n != rawErrors {
			t.Errorf("errors_1m %v, raw %v", n, rawErrors)
		}
		var svcs float64
		var lang string
		if err := boot.QueryRow(ctx, `SELECT toFloat64(count()), any(h) FROM (SELECT service_name, argMaxMerge(sdk_language) AS l, groupArray(1)(service_namespace)[1] AS h
			FROM openlog.apm_services WHERE tenant_id = ? GROUP BY service_name)`, tenant).Scan(&svcs, &lang); err != nil {
			t.Fatal(err)
		}
		if svcs != 3 || lang != "shop" {
			t.Errorf("services %v namespace %q", svcs, lang)
		}
		if n := queryFloat(t, boot, "SELECT toFloat64(count()) FROM (SELECT host_id FROM openlog.apm_service_hosts WHERE tenant_id = ? GROUP BY host_id, service_name)", tenant); n != 3 {
			t.Errorf("service hosts %v", n)
		}
	})

	t.Run("edge linking per shard is complete and idempotent", func(t *testing.T) {
		linker := apm.NewLinker(boot, apm.LinkerOptions{
			Database: "openlog", Cluster: "openlog", Conn: clickhouse.Options{User: "openlog", Password: "openlog"},
			ResolveAddr: func(r clickhouse.Replica) string { return shardAddrs[r.Addr()] },
		}, log, nil)
		start, end := base, base.Add(5*time.Minute)
		links := func() map[string]float64 {
			syncReplicas(t, boot, s1r2)
			rows, err := boot.Query(ctx, `SELECT service_name, target_service, via, sum(calls), sum(errors) FROM openlog.apm_service_links_1m FINAL
				WHERE tenant_id = ? GROUP BY service_name, target_service, via ORDER BY service_name, target_service`, tenant)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[string]float64{}
			for rows.Next() {
				var src, tgt, via string
				var calls, errs float64
				if err := rows.Scan(&src, &tgt, &via, &calls, &errs); err != nil {
					t.Fatal(err)
				}
				out[src+"->"+tgt+" via "+via] = calls
			}
			return out
		}
		for i := range 2 {
			if err := linker.RunWindow(ctx, start, end); err != nil {
				t.Fatal(err)
			}
			got := links()
			want := map[string]float64{"frontend->orders via orders:8080": 750, "frontend->catalog via catalog:8080": 250}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("run %d: links %v, want %v", i+1, got, want)
			}
		}
		perShard := queryFloat(t, boot, "SELECT toFloat64(count(DISTINCT _shard_num)) FROM openlog.apm_service_links_1m WHERE tenant_id = ?", tenant)
		if perShard != 2 {
			t.Errorf("links written on %v shards, want 2", perShard)
		}
		// A late client/server pair arrives: the re-run picks it up and replaces the minute.
		late := processor.NewRows()
		for _, req := range genTraces(1, base.Add(10*time.Second)) {
			for _, rs := range req.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, sp := range ss.Spans {
						sp.TraceId = []byte("late-trace-00001")
						sp.TraceState = ""
					}
				}
			}
			late.AddTraces(tenant, base, req)
		}
		if err := (processor.DirectWriter{W: sw}).Write(ctx, processor.TableSpans, token+":late", processor.Columns[processor.TableSpans], late.Values(processor.TableSpans)); err != nil {
			t.Fatal(err)
		}
		// The job reads one replica per shard; spans inserted on the other replica are linked once
		// replicated (in production by the next run over the lookback window).
		syncReplicas(t, boot, s1r2)
		if err := linker.RunWindow(ctx, start, end); err != nil {
			t.Fatal(err)
		}
		if got := links()["frontend->orders via orders:8080"]; got != 751 {
			t.Errorf("after late span: %v, want 751", got)
		}
	})
}
