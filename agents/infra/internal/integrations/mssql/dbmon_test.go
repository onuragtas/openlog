package mssql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"

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

func named(b *integrations.Batch, name string) []*logspb.LogRecord {
	var out []*logspb.LogRecord
	for _, e := range b.Events() {
		if evAttr(e, "event.name").GetStringValue() == name {
			out = append(out, e)
		}
	}
	return out
}

const showplan = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements>` +
	`<StmtSimple StatementText="SELECT * FROM orders WHERE email = N&apos;a@b.c&apos;" StatementSubTreeCost="0.0032831" StatementEstRows="1">` +
	`<QueryPlan CachedPlanSize="16"><RelOp PhysicalOp="Index Seek" EstimateRows="1" EstimateIO="0.003125">` +
	`<ScalarOperator ScalarString="[shop].[dbo].[orders].[email]=N&apos;a@b.c&apos;"/>` +
	`<ParameterList><ColumnReference Column="@p1" ParameterCompiledValue="N&apos;a@b.c&apos;"/></ParameterList>` +
	`</RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

func TestMSSQLQueryStatsAndPlans(t *testing.T) {
	stats := func(calls, us string) [][]*string {
		return [][]*string{{sp("0x1A2B3C4D5E6F7788"), sp("shop"), sp(calls), sp(us), sp(calls), sp("1000"), sp("10"),
			sp("SELECT * FROM orders WHERE email = @p1"), sp("0x06000500AABBCCDD")}}
	}
	q := &fakeQuerier{answers: map[string][][]*string{
		"SERVERPROPERTY":                             {{sp("16.0.4135.4"), sp("MSSQLSERVER"), sp("120")}},
		"dm_os_performance":                          counters(1000, 2),
		"sys.master_files":                           nil,
		"sys.dm_os_wait_stats":                       nil,
		"sys.dm_exec_query_stats":                    stats("100", "2000000"),
		"sys.dm_exec_query_plan(0x06000500AABBCCDD)": {{sp(showplan)}},
	}}
	c := newCollector(q, config.InstanceSettings{Username: "u", Password: "p", QueryStats: &config.QueryStatsConfig{Enabled: true}})
	t0 := time.Unix(100_000, 0)
	b := integrations.NewBatch(t0, 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(b.Events()) != 0 {
		t.Fatalf("baseline produced events: %d", len(b.Events()))
	}
	q.answers["sys.dm_exec_query_stats"] = stats("160", "2600000")
	b = integrations.NewBatch(t0.Add(30*time.Second), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	st := named(b, "openlog.db.query_stats")
	if len(st) != 1 || evAttr(st[0], "openlog.db.calls").GetIntValue() != 60 || evAttr(st[0], "openlog.db.total_time_ms").GetDoubleValue() != 600 ||
		evAttr(st[0], "db.system.name").GetStringValue() != "mssql" || evAttr(st[0], "openlog.db.query.id").GetStringValue() != "0x1A2B3C4D5E6F7788" ||
		evAttr(st[0], "db.query.text").GetStringValue() != "SELECT * FROM orders WHERE email = @p1" {
		t.Fatalf("stats = %v", st)
	}
	pl := named(b, "openlog.db.query_plan")
	if len(pl) != 1 {
		t.Fatalf("plans = %d", len(pl))
	}
	body := pl[0].Body.GetStringValue()
	if strings.Contains(body, "a@b.c") || !strings.Contains(body, `ParameterCompiledValue="?"`) || !strings.Contains(body, "[email]=?") ||
		evAttr(pl[0], "openlog.db.plan.format").GetStringValue() != "xml" || evAttr(pl[0], "openlog.db.plan.cost").GetDoubleValue() != 0.0032831 {
		t.Fatalf("plan = %s", body)
	}
}

func TestShowplanShapeIgnoresEstimates(t *testing.T) {
	a, _ := ShowplanShape(RedactShowplan(showplan))
	b, _ := ShowplanShape(RedactShowplan(strings.ReplaceAll(showplan, `EstimateRows="1"`, `EstimateRows="9000"`)))
	c, _ := ShowplanShape(RedactShowplan(strings.ReplaceAll(showplan, "Index Seek", "Clustered Index Scan")))
	if a != b || a == c {
		t.Fatalf("hashes %s %s %s", a, b, c)
	}
}

func TestMSSQLSessions(t *testing.T) {
	requests := [][]*string{{sp("55"), sp("shop"), sp("app"), sp("api"), sp("10.0.0.7"), sp("suspended"), sp("LCK_M_X"), sp("4200"), sp("52"),
		sp("UPDATE orders SET status = 'paid' WHERE id = 7"), sp("0x1A2B")}}
	sleeping := [][]*string{{sp("52"), sp("shop"), sp("app"), sp("api"), sp("web-1"), sp("61000")}}
	b := integrations.NewBatch(time.Now(), 0)
	RecordSessions(b, requests, sleeping)
	ev := b.Events()
	if len(ev) != 2 {
		t.Fatalf("sessions = %d", len(ev))
	}
	if evAttr(ev[0], "openlog.db.wait.type").GetStringValue() != "Lock" || evAttr(ev[0], "openlog.db.wait.event").GetStringValue() != "LCK_M_X" ||
		evAttr(ev[0], "openlog.db.blocking_session_ids").GetArrayValue().GetValues()[0].GetStringValue() != "52" ||
		evAttr(ev[0], "db.query.text").GetStringValue() != "UPDATE orders SET status = ? WHERE id = ?" {
		t.Errorf("request = %v", ev[0].Attributes)
	}
	if evAttr(ev[1], "openlog.db.session.state").GetStringValue() != "idle in transaction" || evAttr(ev[1], "openlog.db.duration_ms").GetDoubleValue() != 61000 {
		t.Errorf("sleeping blocker = %v", ev[1].Attributes)
	}
}

func TestQueryStatsQueryExcludesMonitoringStatements(t *testing.T) {
	q := QueryStatsQuery(5)
	if !strings.Contains(q, "TOP (5)") || !strings.Contains(q, "NOT LIKE '%sys.dm[_]%'") || strings.Contains(q, "%!") {
		t.Fatalf("query = %s", q)
	}
}
