package postgresql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/dbmon"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"

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

func eventsNamed(b *integrations.Batch, name string) []*logspb.LogRecord {
	var out []*logspb.LogRecord
	for _, e := range b.Events() {
		if evAttr(e, "event.name").GetStringValue() == name {
			out = append(out, e)
		}
	}
	return out
}

// recordingConn is a fakeConn that also records every statement and switches the pg_stat_statements answer.
type recordingConn struct {
	fakeConn
	sql *[]string
}

func (r *recordingConn) Query(ctx context.Context, sql string) ([][]any, error) {
	*r.sql = append(*r.sql, sql)
	return r.fakeConn.Query(ctx, sql)
}

func stmtRow(id int64, db string, calls int64, ms float64, text string) []any {
	return []any{id, db, "app", calls, calls, ms, calls * 10, int64(1), text}
}

func TestQueryMonitoringEvents(t *testing.T) {
	inst := testutil.Instance()
	on := true
	inst.Settings = config.InstanceSettings{Username: "openlog", Password: "pw", QueryStats: &config.QueryStatsConfig{Enabled: true, TopN: 5, Explain: &on}}
	c := &collector{inst: inst, ep: integrations.TCP("127.0.0.1", 5432), conns: map[string]Conn{}}
	var sqls []string
	stmts := [][]any{
		stmtRow(11, "app", 100, 1000, "SELECT * FROM orders WHERE id = $1"),
		stmtRow(12, "app", 5, 50, "CREATE INDEX x ON orders(y)"),
	}
	plan := `[{"Plan":{"Node Type":"Index Scan","Total Cost":8.3,"Plan Rows":1}}]`
	c.connect = func(_ context.Context, db string) (Conn, error) {
		extra := []answer{
			{"FROM pg_extension", [][]any{{"1.10"}}, nil},
			{"FROM pg_stat_statements", nil, nil}, // replaced below per collection
			{"BEGIN TRANSACTION READ ONLY", nil, nil},
			{"SET LOCAL statement_timeout", nil, nil},
			{"EXPLAIN (GENERIC_PLAN, FORMAT JSON) SELECT * FROM orders WHERE id = $1", [][]any{{plan}}, nil},
			{"ROLLBACK", nil, nil},
		}
		return &recordingConn{fakeConn: fakeConn{db: db, answers: append(extra, answers(db, nil)...)}, sql: &sqls}, nil
	}
	collect := func(now time.Time, rows [][]any) *integrations.Batch {
		t.Helper()
		for _, cn := range c.conns {
			cn.(*recordingConn).answers[1].rows = rows
		}
		if len(c.conns) == 0 {
			// first collection: the connection does not exist yet; set the answer through connect
			orig := c.connect
			c.connect = func(ctx context.Context, db string) (Conn, error) {
				cn, err := orig(ctx, db)
				cn.(*recordingConn).answers[1].rows = rows
				return cn, err
			}
			defer func() { c.connect = orig }()
		}
		b := integrations.NewBatch(now, 0)
		if err := c.Collect(context.Background(), b); err != nil {
			t.Fatalf("collect: %v", err)
		}
		return b
	}

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	b := collect(t0, stmts)
	if n := len(eventsNamed(b, dbmon.EventQueryStats)); n != 0 {
		t.Fatalf("first collection must only record the baseline, got %d events", n)
	}

	stmts2 := [][]any{
		stmtRow(11, "app", 160, 1600, "SELECT * FROM orders WHERE id = $1"),
		stmtRow(12, "app", 6, 70, "CREATE INDEX x ON orders(y)"),
	}
	b = collect(t0.Add(30*time.Second), stmts2)
	ev := eventsNamed(b, dbmon.EventQueryStats)
	if len(ev) != 2 {
		t.Fatalf("stats events = %d", len(ev))
	}
	if evAttr(ev[0], "openlog.db.calls").GetIntValue() != 60 || evAttr(ev[0], "openlog.db.total_time_ms").GetDoubleValue() != 600 ||
		evAttr(ev[0], "openlog.db.interval_seconds").GetIntValue() != 30 || evAttr(ev[0], "openlog.db.query.id").GetStringValue() != "11" ||
		evAttr(ev[0], "db.namespace").GetStringValue() != "app" || evAttr(ev[0], "db.system.name").GetStringValue() != "postgresql" {
		t.Errorf("stats = %v", ev[0].Attributes)
	}
	// Plan of the heaviest statement; the utility statement is never passed to EXPLAIN.
	plans := eventsNamed(b, dbmon.EventQueryPlan)
	if len(plans) != 1 || plans[0].Body.GetStringValue() != plan || evAttr(plans[0], "openlog.db.plan.cost").GetDoubleValue() != 8.3 ||
		evAttr(plans[0], "openlog.db.plan.format").GetStringValue() != "json" || evAttr(plans[0], "openlog.db.plan.hash").GetStringValue() == "" {
		t.Fatalf("plans = %v", plans)
	}
	joined := strings.Join(sqls, "\n")
	if strings.Contains(joined, "EXPLAIN (GENERIC_PLAN, FORMAT JSON) CREATE") {
		t.Error("utility statement explained")
	}
	iBegin, iExplain, iRollback := strings.Index(joined, "BEGIN TRANSACTION READ ONLY"), strings.Index(joined, "EXPLAIN ("), strings.Index(joined, "ROLLBACK")
	if iBegin < 0 || iExplain < iBegin || iRollback < iExplain {
		t.Errorf("EXPLAIN not inside a rolled back read-only transaction:\n%s", joined)
	}
	// Within the explain interval the statement is not explained again.
	b = collect(t0.Add(60*time.Second), stmts2)
	if n := len(eventsNamed(b, dbmon.EventQueryPlan)); n != 0 {
		t.Errorf("explained again within the interval: %d", n)
	}
}

func TestSessionSample(t *testing.T) {
	c := queryStatsCollector(&config.QueryStatsConfig{Enabled: true},
		answer{"FROM pg_extension", [][]any{{"1.10"}}, nil},
		answer{"FROM pg_stat_statements", nil, nil},
		answer{"pg_blocking_pids", [][]any{
			{"101", "app", "api", "api-server", "10.0.0.7", "active", "Lock", "transactionid", 1520.5, "100", "UPDATE t SET a = 'x' WHERE id = 1", "-77"},
			{"100", "app", "api", "api-server", "10.0.0.7", "idle in transaction", "Client", "ClientRead", 30000.0, "", "UPDATE t SET a = 'y' WHERE id = 1", ""},
		}, nil})
	if c.SampleInterval() != 10*time.Second {
		t.Fatalf("interval = %v", c.SampleInterval())
	}
	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Sample(context.Background(), b); err != nil || len(b.Events()) != 0 {
		t.Fatalf("sample before the first collection: %v %d", err, len(b.Events()))
	}
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Fatalf("collect: %v", err)
	}
	b = integrations.NewBatch(time.Now(), 0)
	if err := c.Sample(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ev := eventsNamed(b, dbmon.EventSessionSample)
	if len(ev) != 2 {
		t.Fatalf("sessions = %d", len(ev))
	}
	if evAttr(ev[0], "openlog.db.session.id").GetStringValue() != "101" || evAttr(ev[0], "openlog.db.wait.event").GetStringValue() != "transactionid" ||
		evAttr(ev[0], "db.query.text").GetStringValue() != "UPDATE t SET a = ? WHERE id = ?" || evAttr(ev[0], "openlog.db.query.id").GetStringValue() != "-77" ||
		evAttr(ev[0], "openlog.db.blocking_session_ids").GetArrayValue().GetValues()[0].GetStringValue() != "100" {
		t.Errorf("session = %v", ev[0].Attributes)
	}
	if !strings.Contains(SessionsQuery(160000), "query_id") || strings.Contains(SessionsQuery(130000), "query_id") {
		t.Error("query_id must only be read from PostgreSQL 14")
	}
	off := false
	c.inst.Settings.QueryStats.Sessions = &off
	if c.SampleInterval() != 0 {
		t.Error("sessions: false still samples")
	}
}

func TestOwnStatementsExcluded(t *testing.T) {
	for _, s := range []string{
		"SELECT s.queryid, d.datname FROM pg_stat_statements s LEFT JOIN pg_database d ON d.oid = s.dbid",
		"SELECT pid::text FROM pg_stat_activity WHERE backend_type = $1",
		"SHOW server_version_num", "EXPLAIN (GENERIC_PLAN, FORMAT JSON) SELECT 1", "BEGIN TRANSACTION READ ONLY",
		"SELECT count(*) FROM pg_catalog.pg_class",
	} {
		if !ownStatement.MatchString(s) {
			t.Errorf("not recognized as a monitoring statement: %s", s)
		}
	}
	for _, s := range []string{"SELECT * FROM orders WHERE id = $1", "UPDATE stats SET database = $1", "SELECT * FROM pg_stat_app_events"} {
		if ownStatement.MatchString(s) && s != "SELECT * FROM pg_stat_app_events" {
			t.Errorf("application statement excluded: %s", s)
		}
	}
}
