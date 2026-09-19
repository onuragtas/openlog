package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/dbmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

func evAttr(r *logspb.LogRecord, k string) *commonpb.AnyValue {
	for _, kv := range r.Attributes {
		if kv.Key == k {
			return kv.Value
		}
	}
	return nil
}

func baseQuerier() fakeQuerier {
	return fakeQuerier{
		"SHOW GLOBAL STATUS":                              {rows: globalStatus},
		"SELECT @@innodb_buffer_pool_size":                {rows: rows([]string{"134217728"})},
		"SELECT OBJECT_SCHEMA, OBJECT_NAME, COUNT":        {},
		"SELECT OBJECT_SCHEMA, OBJECT_NAME, IFNULL":       {},
		"SELECT @@performance_schema":                     {rows: rows([]string{"1"})},
		"SHOW REPLICA STATUS":                             {},
		"SELECT COUNT(*) FROM information_schema.COLUMNS": {rows: rows([]string{"1"})},
	}
}

func TestDigestDeltas(t *testing.T) {
	off := false
	q := baseQuerier()
	c := collector_(q)
	c.inst.Settings.QueryStats = &config.QueryStatsConfig{Enabled: true, Explain: &off}
	digest := func(calls, pico string) result {
		return result{rows: rows(
			[]string{"shop", "d1", "SELECT * FROM `orders` WHERE `id` = ?", "SELECT * FROM orders WHERE id = 42", calls, pico, calls, "900", "0", "0"},
			[]string{"shop", "d2", "SELECT * FROM `big` WHERE `x` LIKE ?", "SELECT * FROM big WHERE x LIKE 'secret%' AND a IN (1, 2 ...", "10", "5000000000000", "1", "100000", "1", "10"},
		)}
	}
	collect := func(now time.Time, r result) *integrations.Batch {
		q["SELECT COALESCE(SCHEMA_NAME"] = r
		b := integrations.NewBatch(now, 0)
		if err := c.collect(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	t0 := time.Unix(100_000, 0)
	if n := len(collect(t0, digest("100", "2000000000000")).Events()); n != 0 {
		t.Fatalf("baseline produced %d events", n)
	}
	b := collect(t0.Add(30*time.Second), digest("160", "2600000000000"))
	ev := b.Events()
	if len(ev) != 1 {
		t.Fatalf("events = %d", len(ev))
	}
	e := ev[0]
	// The sample (not the backticked digest) is sent, redacted; picoseconds become milliseconds.
	if evAttr(e, "db.query.text").GetStringValue() != "SELECT * FROM orders WHERE id = ?" || evAttr(e, "openlog.db.calls").GetIntValue() != 60 ||
		evAttr(e, "openlog.db.total_time_ms").GetDoubleValue() != 600 || evAttr(e, "db.system.name").GetStringValue() != "mysql" ||
		evAttr(e, "openlog.db.query.id").GetStringValue() != "d1" || evAttr(e, "db.namespace").GetStringValue() != "shop" {
		t.Errorf("event = %v", e.Attributes)
	}
	if !strings.Contains(DigestQuery(10, true), "QUERY_SAMPLE_TEXT") || strings.Contains(DigestQuery(10, false), "QUERY_SAMPLE_TEXT") {
		t.Error("sample column must depend on the server")
	}
}

func TestMySQLSessions(t *testing.T) {
	q := baseQuerier()
	q["SELECT t.PROCESSLIST_ID"] = result{rows: rows(
		[]string{"12", "app", "10.0.0.7:51234", "shop", "Query", "updating", "4", "UPDATE orders SET status = 'paid' WHERE id = 7", "wait/lock/table/sql/handler", "50"},
		[]string{"13", "app", "10.0.0.8:4000", "shop", "Query", "executing", "0", "SELECT 1", "", "51"},
	)}
	q["SELECT r.PROCESSLIST_ID"] = result{rows: rows([]string{"13", "12"})}
	c := collector_(q)
	c.inst.Settings.QueryStats = &config.QueryStatsConfig{Enabled: true}
	if c.SampleInterval() != 10*time.Second {
		t.Fatalf("interval = %v", c.SampleInterval())
	}
	b := integrations.NewBatch(time.Now(), 0)
	RecordSessions(b, q["SELECT t.PROCESSLIST_ID"].rows, map[string][]string{"13": {"12"}})
	ev := b.Events()
	if len(ev) != 2 || evAttr(ev[0], "openlog.db.wait.type").GetStringValue() != "lock" ||
		evAttr(ev[0], "openlog.db.wait.event").GetStringValue() != "table/sql/handler" || evAttr(ev[0], "client.address").GetStringValue() != "10.0.0.7" ||
		evAttr(ev[0], "db.query.text").GetStringValue() != "UPDATE orders SET status = ? WHERE id = ?" || evAttr(ev[0], "openlog.db.duration_ms").GetDoubleValue() != 4000 {
		t.Fatalf("session 12 = %v", ev)
	}
	// Blocked without an instrumented wait: reported as an InnoDB row lock wait.
	if evAttr(ev[1], "openlog.db.wait.type").GetStringValue() != "lock" || evAttr(ev[1], "openlog.db.blocking_session_ids") == nil {
		t.Errorf("session 13 = %v", ev[1].Attributes)
	}
	for _, e := range ev {
		if evAttr(e, "event.name").GetStringValue() != dbmon.EventSessionSample {
			t.Errorf("event name %v", evAttr(e, "event.name"))
		}
	}
}

func TestRedactMySQLPlan(t *testing.T) {
	plan := `{"query_block":{"select_id":1,"cost_info":{"query_cost":"12.50"},"table":{"table_name":"orders","access_type":"ref",
"attached_condition":"(` + "`shop`.`orders`.`email` = 'a@b.c'" + `)","rows_examined_per_scan":3}}}`
	out, cost := redactMySQLPlan(plan)
	if cost != 12.5 || strings.Contains(out, "a@b.c") || !strings.Contains(out, "`shop`.`orders`.`email` = ?") || !strings.Contains(out, `"table_name":"orders"`) {
		t.Fatalf("plan = %s cost %v", out, cost)
	}
}
