package openlogpgx_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onuragtas/openlog/agents/go/instrumentation/pgx"
	"github.com/onuragtas/openlog/agents/go/openlogsql"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setup(opts ...openlogpgx.Option) (*openlogpgx.Tracer, *tracetest.SpanRecorder, context.Context, func()) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t := openlogpgx.NewTracer(append(opts, openlogpgx.WithTracerProvider(tp))...)
	ctx, parent := tp.Tracer("test").Start(context.Background(), "request")
	return t, rec, ctx, func() { parent.End() }
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, a := range s.Attributes() {
		out[a.Key] = a.Value.Emit()
	}
	return out
}

func TestAStatementBecomesASpanWithItsValuesRemoved(t *testing.T) {
	tr, rec, ctx, done := setup()
	defer done()
	sql := "SELECT * FROM users WHERE name = 'ayşe' AND id IN (1, 2, 3)"
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	a := attrs(spans[0])
	if a["db.system.name"] != "postgresql" || a["db.operation.name"] != "SELECT" {
		t.Errorf("%v", a)
	}
	if strings.Contains(a["db.query.text"], "ayşe") || !strings.Contains(a["db.query.text"], "IN (?)") {
		t.Errorf("%q", a["db.query.text"])
	}
}

func TestNoSpanWithoutAParentAndTheRequestIsLeftAlone(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	tr := openlogpgx.NewTracer(openlogpgx.WithTracerProvider(tp))
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("%d spans with no parent", n)
	}

	root := openlogpgx.NewTracer(openlogpgx.WithTracerProvider(tp), openlogpgx.WithRootSpans())
	ctx = root.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	root.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if n := len(rec.Ended()); n != 1 {
		t.Errorf("WithRootSpans: %d spans", n)
	}
}

// Ping starts no span, so its end hook must not take the request's span for its own and end it.
func TestAPingDoesNotEndTheRequest(t *testing.T) {
	tr, rec, ctx, done := setup()
	next := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "-- ping"})
	tr.TraceQueryEnd(next, nil, pgx.TraceQueryEndData{})
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("%d spans ended", n)
	}
	done()
	if n := len(rec.Ended()); n != 1 {
		t.Errorf("the request itself: %d", n)
	}
}

func TestAFailureIsAnErrorAndNoRowsIsNot(t *testing.T) {
	tr, rec, ctx, done := setup()
	defer done()
	c1 := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(c1, nil, pgx.TraceQueryEndData{Err: errors.New("boom")})
	c2 := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 2"})
	tr.TraceQueryEnd(c2, nil, pgx.TraceQueryEndData{Err: pgx.ErrNoRows})
	spans := rec.Ended()
	if spans[0].Status().Code != codes.Error || spans[1].Status().Code == codes.Error {
		t.Errorf("%v %v", spans[0].Status(), spans[1].Status())
	}
}

func TestQueryTextCanBeTurnedOff(t *testing.T) {
	tr, rec, ctx, done := setup(openlogpgx.WithQueryText(openlogsql.QueryOff))
	defer done()
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if _, ok := attrs(rec.Ended()[0])["db.query.text"]; ok {
		t.Error("query text recorded")
	}
}

func TestExplainIsOffUnlessAskedFor(t *testing.T) {
	tr, rec, ctx, done := setup()
	defer done()
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	time.Sleep(20 * time.Millisecond)
	for _, s := range rec.Ended() {
		if s.Name() == "db.explain" {
			t.Error("explained with nobody asking")
		}
	}
}

func explainPool(t *testing.T, ctx context.Context, tr *openlogpgx.Tracer) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tr.ExplainWith(pool)
	return pool
}

func plans(rec *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "db.explain" {
			out = append(out, s)
		}
	}
	return out
}

func waitForPlans(rec *tracetest.SpanRecorder, n int) []sdktrace.ReadOnlySpan {
	for i := 0; i < 300 && len(plans(rec)) < n; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return plans(rec)
}

func TestASlowSelectGetsAPlanWithNoValuesInIt(t *testing.T) {
	tr, rec, ctx, done := setup(openlogpgx.WithExplain(time.Nanosecond))
	defer done()
	pool := explainPool(t, ctx, tr)
	const secret = "ayşe-secret"
	mk := "CREATE TABLE IF NOT EXISTS openlogpgx_explain (id int, name text)"
	if _, err := pool.Exec(ctx, mk); err != nil {
		t.Fatal(err)
	}
	var n int
	sql := "SELECT count(*) FROM openlogpgx_explain WHERE name = $1 AND id > 12345"
	if err := pool.QueryRow(ctx, sql, secret).Scan(&n); err != nil {
		t.Fatal(err)
	}
	got := waitForPlans(rec, 1)
	if len(got) != 1 {
		t.Fatalf("%d plans", len(got))
	}
	a := attrs(got[0])
	plan := a["openlog.db.plan"]
	if !strings.Contains(plan, "openlogpgx_explain") || !strings.Contains(plan, "cost=") {
		t.Errorf("not a plan: %s", plan)
	}
	if strings.Contains(plan, secret) || strings.Contains(plan, "12345") {
		t.Errorf("a value is in the plan: %s", plan)
	}
	if a["openlog.db.plan.generic"] != "true" || a["openlog.db.plan.system"] != "postgresql" {
		t.Errorf("%v", a)
	}
	if _, ok := a["db.system.name"]; ok {
		t.Error("a plan must not count as a database call")
	}
	// The EXPLAIN itself is no statement of the application's.
	for _, s := range rec.Ended() {
		if strings.Contains(attrs(s)["db.query.text"], "EXPLAIN") {
			t.Errorf("the EXPLAIN became a span: %v", attrs(s))
		}
	}

	// Once an hour: the same statement again asks for nothing.
	if err := pool.QueryRow(ctx, sql, "other").Scan(&n); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := plans(rec); len(got) != 1 {
		t.Errorf("explained again: %d", len(got))
	}
}

func TestOnlySelectsAreExplained(t *testing.T) {
	tr, rec, ctx, done := setup(openlogpgx.WithExplain(time.Nanosecond))
	defer done()
	pool := explainPool(t, ctx, tr)
	for _, sql := range []string{
		"CREATE TABLE IF NOT EXISTS openlogpgx_explain2 (id int)",
		"INSERT INTO openlogpgx_explain2 VALUES (1)",
		"DELETE FROM openlogpgx_explain2 WHERE id = 1",
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := plans(rec); len(got) != 0 {
		t.Errorf("%d plans", len(got))
	}
}

// Against a real server, since what is being tested is which hooks pgx calls and with what (skipped without a database).
func TestPoolAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	tr, rec, ctx, done := setup()
	defer done()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec("CREATE TEMP TABLE IF NOT EXISTS openlogpgx_t (id int)")
	before := len(rec.Ended())

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var got int
	if err := conn.QueryRow(ctx, "SELECT $1::int + 1", 41).Scan(&got); err != nil || got != 42 {
		t.Fatalf("%d %v", got, err)
	}
	if _, err := conn.Exec(ctx, "SELECT * FROM does_not_exist"); err == nil {
		t.Fatal("expected an error")
	}
	batch := &pgx.Batch{}
	batch.Queue("SELECT 'one'")
	batch.Queue("SELECT 'two'")
	if err := conn.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE TEMP TABLE IF NOT EXISTS openlogpgx_copy (id int)"); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.CopyFrom(ctx, pgx.Identifier{"openlogpgx_copy"}, []string{"id"}, pgx.CopyFromRows([][]any{{1}, {2}})); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	ops := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended()[before:] {
		ops[attrs(s)["db.operation.name"]] = s
	}
	for _, want := range []string{"SELECT", "BATCH", "COPY", "BEGIN", "COMMIT"} {
		if _, ok := ops[want]; !ok {
			t.Errorf("no %s span in %v", want, ops)
		}
	}
	b := attrs(ops["BATCH"])
	if b["db.operation.batch.size"] != "2" || !strings.Contains(b["db.query.text"], "SELECT ?; SELECT ?") {
		t.Errorf("batch %v", b)
	}
	if a := attrs(ops["COPY"]); a["db.collection.name"] != `"openlogpgx_copy"` {
		t.Errorf("copy %v", a)
	}
	failed := false
	for _, s := range rec.Ended()[before:] {
		if s.Status().Code == codes.Error && strings.Contains(attrs(s)["db.query.text"], "does_not_exist") {
			failed = true
		}
		if a := attrs(s); a["db.namespace"] == "" || a["server.address"] == "" {
			t.Errorf("%s has no connection attributes: %v", s.Name(), a)
		}
		if strings.Contains(attrs(s)["db.query.text"], "ping") {
			t.Errorf("a ping became a span: %s", s.Name())
		}
	}
	if !failed {
		t.Error("the failed statement is not an error span")
	}
}
