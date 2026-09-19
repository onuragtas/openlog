package processor

import (
	"strings"
	"testing"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/onuragtas/openlog/internal/apm"
)

func f64(k string, v float64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}}
}

func strs(k string, vs ...string) *commonpb.KeyValue {
	arr := &commonpb.ArrayValue{}
	for _, v := range vs {
		arr.Values = append(arr.Values, &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}})
	}
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: arr}}}
}

// dbResource is the resource of an integration instance: the host resource plus the instance attributes.
func dbResource() *resourcepb.Resource {
	r := hostResource()
	r.Attributes = append(r.Attributes, s("service.instance.id", "db1.internal:5432"), s("server.address", "db1.internal"),
		i64("server.port", 5432), s("openlog.integration.id", "postgresql"))
	return r
}

func dbLogs(recs ...*logspb.LogRecord) *collogs.ExportLogsServiceRequest {
	return &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: dbResource(), ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
	}}}
}

func TestAddLogsDBQueryStats(t *testing.T) {
	r := NewRows()
	r.AddLogs("t1", recv, dbLogs(
		&logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBQueryStats, Attributes: []*commonpb.KeyValue{
			s("db.system.name", "postgresql"), s("db.namespace", "shop"), s("openlog.db.user", "app"), s("openlog.db.query.id", "-4242"),
			s("db.query.text", "SELECT * FROM orders WHERE id = $1 AND status IN ($2, $3, $4)"),
			i64("openlog.db.interval_seconds", 30), i64("openlog.db.calls", 120), f64("openlog.db.total_time_ms", 845.5),
			i64("openlog.db.rows", 120), i64("openlog.db.blocks_hit", 900), i64("openlog.db.blocks_read", 4), f64("openlog.db.errors", -3),
		}},
		// No statement text: dropped (a statement is the key of everything here).
		&logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBQueryStats, Attributes: []*commonpb.KeyValue{
			s("db.system.name", "postgresql"), i64("openlog.db.interval_seconds", 30)}},
		// No interval: dropped.
		&logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBQueryStats, Attributes: []*commonpb.KeyValue{
			s("db.system", "mysql"), s("db.query.text", "SELECT 1")}},
	))
	if len(r.Logs) != 0 {
		t.Fatalf("db events reached logs: %d", len(r.Logs))
	}
	if len(r.DBQueryStats) != 1 || r.Dropped["invalid_db_query_stats"] != 2 {
		t.Fatalf("stats = %d dropped = %v", len(r.DBQueryStats), r.Dropped)
	}
	row := r.DBQueryStats[0]
	want := "SELECT * FROM orders WHERE id = ? AND status IN (?)"
	if row.QueryText != want || row.Fingerprint != DBFingerprint(want) {
		t.Fatalf("text = %q fp = %d", row.QueryText, row.Fingerprint)
	}
	if row.Instance != "db1.internal:5432" || row.ServerAddress != "db1.internal" || row.ServerPort != 5432 || row.HostID != "h-1" ||
		row.DBSystem != "postgresql" || row.DBName != "shop" || row.DBUser != "app" || row.QueryID != "-4242" || row.IntervalSeconds != 30 {
		t.Errorf("identity = %+v", row)
	}
	if row.Calls != 120 || row.TotalTimeMs != 845.5 || row.Rows != 120 || row.BlocksHit != 900 || row.BlocksRead != 4 || row.Errors != 0 {
		t.Errorf("numbers = %+v", row)
	}
	if got := r.Values(TableDBQueryStats)[0]; len(got) != len(Columns[TableDBQueryStats]) {
		t.Errorf("values %d != columns %d", len(got), len(Columns[TableDBQueryStats]))
	}
}

// The statement key must be the one APM computes for the same query sent by an application with literals.
func TestDBFingerprintMatchesAPM(t *testing.T) {
	agent := "SELECT * FROM orders WHERE id = $1 AND status IN ($2, $3, $4)"
	app := "SELECT *  FROM orders WHERE id = 42 AND status IN ('new', 'paid', 'sent')"
	if NormalizeDBQuery("postgresql", agent) != apm.NormalizeStatement("postgresql", app) {
		t.Fatalf("%q != %q", NormalizeDBQuery("postgresql", agent), apm.NormalizeStatement("postgresql", app))
	}
	// The agent's MySQL/SQL Server redaction leaves '?' for strings and ? for numbers: same canonical form.
	if NormalizeDBQuery("mysql", "UPDATE t SET a = '?' WHERE id = ?") != apm.NormalizeStatement("mysql", "UPDATE t SET a = 'x' WHERE id = 7") {
		t.Fatal("redacted MySQL text does not normalize like the application's")
	}
}

func TestAddLogsDBSessionSample(t *testing.T) {
	r := NewRows()
	r.AddLogs("t1", recv, dbLogs(
		&logspb.LogRecord{TimeUnixNano: ts1, Attributes: []*commonpb.KeyValue{
			s("event.name", EventDBSessionSample), s("db.system.name", "postgresql"), s("db.namespace", "shop"),
			s("openlog.db.session.id", "4711"), s("openlog.db.session.state", "active"), s("openlog.db.wait.type", "Lock"),
			s("openlog.db.wait.event", "transactionid"), f64("openlog.db.duration_ms", 1520), strs("openlog.db.blocking_session_ids", "4700", ""),
			s("db.query.text", "UPDATE orders SET status = $1 WHERE id = $2"), s("openlog.db.application", "api"), s("client.address", "10.0.0.7"),
		}},
		// On the CPU, between statements: no text, still a sample.
		&logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBSessionSample, Attributes: []*commonpb.KeyValue{
			s("db.system.name", "postgresql"), s("openlog.db.session.id", "4700"), s("openlog.db.session.state", "idle in transaction")}},
		// No session id: dropped.
		&logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBSessionSample, Attributes: []*commonpb.KeyValue{s("db.system.name", "postgresql")}},
	))
	if len(r.DBSessionSamples) != 2 || r.Dropped["invalid_db_session_sample"] != 1 {
		t.Fatalf("samples = %d dropped = %v", len(r.DBSessionSamples), r.Dropped)
	}
	a := r.DBSessionSamples[0]
	if a.SessionID != "4711" || a.WaitEventType != "Lock" || a.WaitEvent != "transactionid" || a.DurationMs != 1520 ||
		len(a.BlockingSessionIDs) != 1 || a.BlockingSessionIDs[0] != "4700" || a.QueryText != "UPDATE orders SET status = ? WHERE id = ?" ||
		a.Application != "api" || a.ClientAddress != "10.0.0.7" || a.Instance != "db1.internal:5432" {
		t.Errorf("sample = %+v", a)
	}
	if b := r.DBSessionSamples[1]; b.QueryText != "" || b.Fingerprint != 0 || b.BlockingSessionIDs == nil {
		t.Errorf("idle sample = %+v (blocking ids must be an empty array, not nil)", b)
	}
}

func TestAddLogsDBQueryPlan(t *testing.T) {
	plan := `[{"Plan":{"Node Type":"Index Scan","Total Cost":8.3}}]`
	base := []*commonpb.KeyValue{s("db.system.name", "postgresql"), s("db.query.text", "SELECT * FROM orders WHERE id = $1"),
		s("openlog.db.plan.hash", "9f86d081"), f64("openlog.db.plan.cost", 8.3)}
	rec := func(format, body string) *logspb.LogRecord {
		return &logspb.LogRecord{TimeUnixNano: ts1, EventName: EventDBQueryPlan, Attributes: append(append([]*commonpb.KeyValue{}, base...), s("openlog.db.plan.format", format)),
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}}
	}
	r := NewRows()
	r.AddLogs("t1", recv, dbLogs(rec("json", plan), rec("yaml", plan), rec("json", ""), rec("xml", strings.Repeat("x", MaxDBPlanBytes+1))))
	if len(r.DBQueryPlans) != 1 || r.Dropped["invalid_db_query_plan"] != 2 || r.Dropped["db_query_plan_too_large"] != 1 {
		t.Fatalf("plans = %d dropped = %v", len(r.DBQueryPlans), r.Dropped)
	}
	p := r.DBQueryPlans[0]
	if p.Plan != plan || p.PlanFormat != "json" || p.PlanHash != "9f86d081" || p.TotalCost != 8.3 || p.CapturedAt.IsZero() ||
		p.Fingerprint != DBFingerprint("SELECT * FROM orders WHERE id = ?") {
		t.Errorf("plan = %+v", p)
	}
}
