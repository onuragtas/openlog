package openlogsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func attrs(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

func open(t testing.TB, driverName string, opts ...Option) (*sql.DB, *tracetest.SpanRecorder, trace.Tracer) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	// A unique registration per test: Register caches by driver name, so use WrapDriver.
	db := sql.OpenDB(mustConnector(t, WrapDriver(&fakeDriver{legacy: driverName == "fakedb-legacy"}, "postgres",
		append([]Option{WithTracerProvider(tp)}, opts...)...), "postgres://app:secret@db.example:5433/shop?sslmode=disable"))
	t.Cleanup(func() { _ = db.Close() })
	return db, rec, tp.Tracer("parent")
}

func mustConnector(t testing.TB, d driver.Driver, dsn string) driver.Connector {
	c, err := d.(*wrappedDriver).OpenConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSpansAndAttributes(t *testing.T) {
	for _, driverName := range []string{"fakedb", "fakedb-legacy"} {
		t.Run(driverName, func(t *testing.T) {
			db, rec, tr := open(t, driverName)
			ctx, parent := tr.Start(context.Background(), "request")

			if _, err := db.ExecContext(ctx, "INSERT INTO orders (note, total) VALUES ('secret note', 42.5)"); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRowContext(ctx, "select n from t where id = $1", 7).Scan(&n); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE t SET n = n + 1 WHERE id IN (1, 2, 3)"); err != nil {
				t.Fatal(err)
			}
			_ = tx.Commit()
			if _, err := db.ExecContext(ctx, "FAIL"); err == nil {
				t.Fatal("expected error")
			}
			// Without a parent span nothing is recorded.
			_, _ = db.ExecContext(context.Background(), "DELETE FROM t")
			parent.End()

			var db0 []sdktrace.ReadOnlySpan
			for _, s := range rec.Ended() {
				if s.Name() != "request" {
					db0 = append(db0, s)
				}
			}
			if len(db0) != 4 {
				names := []string{}
				for _, s := range db0 {
					names = append(names, s.Name())
				}
				t.Fatalf("db spans = %v", names)
			}
			ins := db0[0]
			a := attrs(ins.Attributes())
			want := map[attribute.Key]string{
				"db.system.name": "postgresql", "db.system": "postgresql", "db.namespace": "shop", "db.operation.name": "INSERT",
				"db.query.text": "INSERT INTO orders (note, total) VALUES (?)", "server.address": "db.example",
			}
			for k, v := range want {
				if a[k].AsString() != v {
					t.Errorf("%s = %q, want %q", k, a[k].AsString(), v)
				}
			}
			if a["server.port"].AsInt64() != 5433 || ins.Name() != "INSERT shop" || ins.SpanKind() != trace.SpanKindClient {
				t.Errorf("port %v name %q kind %v", a["server.port"], ins.Name(), ins.SpanKind())
			}
			if ins.Parent().SpanID() != parent.SpanContext().SpanID() {
				t.Error("db span not a child of the request")
			}
			eqs(t, attrs(db0[1].Attributes())["db.query.text"].AsString(), "select n from t where id = $1")
			eqs(t, db0[1].Name(), "SELECT shop")
			eqs(t, attrs(db0[2].Attributes())["db.query.text"].AsString(), "UPDATE t SET n = n + ? WHERE id IN (?)")
			if db0[3].Status().Code != codes.Error || len(db0[3].Events()) == 0 {
				t.Errorf("error span status %v", db0[3].Status())
			}
		})
	}
}

func TestRootSpansAndQueryTextModes(t *testing.T) {
	db, rec, _ := open(t, "fakedb", WithRootSpans(), WithQueryText(QueryOff))
	if _, err := db.ExecContext(context.Background(), "DELETE FROM t WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Ended()) != 1 {
		t.Fatalf("spans = %d", len(rec.Ended()))
	}
	if _, ok := attrs(rec.Ended()[0].Attributes())["db.query.text"]; ok {
		t.Error("QueryOff recorded db.query.text")
	}

	db, rec, tr := open(t, "fakedb", WithQueryText(QueryRaw), WithDBSystem("mysql"), WithServerAddress("mysql-1", 3306))
	ctx, p := tr.Start(context.Background(), "p")
	_, _ = db.ExecContext(ctx, "DELETE FROM t WHERE name = 'x'")
	p.End()
	a := attrs(rec.Ended()[0].Attributes())
	eqs(t, a["db.query.text"].AsString(), "DELETE FROM t WHERE name = 'x'")
	eqs(t, a["db.system.name"].AsString(), "mysql")
	eqs(t, a["server.address"].AsString(), "mysql-1")
}

func TestOpenAndRegister(t *testing.T) {
	name, err := Register("fakedb")
	if err != nil || name != "fakedb-openlog" {
		t.Fatal(name, err)
	}
	if again, _ := Register("fakedb"); again != name {
		t.Error("second Register differs")
	}
	db, err := Open("fakedb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := Register("no-such-driver"); err == nil {
		t.Error("unknown driver accepted")
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, system, want string }{
		{"SELECT * FROM users WHERE email = 'a@b.c' AND age > 30", "postgresql", "SELECT * FROM users WHERE email = ? AND age > ?"},
		{"select  *\n from t1 where col_2 = 3.14e-2 -- trailing comment", "postgresql", "select * from t1 where col_2 = ?"},
		{"/* app=shop */ UPDATE t SET a = 'it''s', b = E'x\\'y', c = 0xFF WHERE id IN (1,2, 3)", "postgresql", "UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (?)"},
		{`INSERT INTO t ("from", note) VALUES (1, 'a'), (2, 'b'), (3, 'c')`, "postgresql", `INSERT INTO t ("from", note) VALUES (?)`},
		{`SELECT * FROM t WHERE name = "bob" # mysql comment`, "mysql", `SELECT * FROM t WHERE name = ?`},
		{"SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2", "mysql", "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2"},
		{"SELECT a::text, $$secret$$ FROM t", "postgresql", "SELECT a::text, ? FROM t"},
		{"SELECT -5, x-1 FROM t", "postgresql", "SELECT -?, x-? FROM t"},
		{"SELECT 'unterminated", "postgresql", "SELECT ?"},
		{"SELECT 'ünïcödé' AS naïve FROM tåble", "postgresql", "SELECT ? AS naïve FROM tåble"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in, c.system); got != c.want {
			t.Errorf("Sanitize(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestParseDSN(t *testing.T) {
	cases := []struct {
		driver, dsn string
		want        dsnInfo
	}{
		{"pgx", "postgres://u:p@pg.internal:5432/shop?sslmode=require", dsnInfo{"pg.internal", 5432, "shop"}},
		{"postgres", "host=10.0.0.5 port=6432 dbname=orders user=app password='x y'", dsnInfo{"10.0.0.5", 6432, "orders"}},
		{"mysql", "app:secret@tcp(mysql-1:3306)/shop?parseTime=true", dsnInfo{"mysql-1", 3306, "shop"}},
		{"mysql", "app@unix(/var/run/mysqld.sock)/shop", dsnInfo{"", 0, "shop"}},
		{"sqlserver", "sqlserver://sa:pw@mssql:1433?database=crm", dsnInfo{"mssql", 1433, "crm"}},
		{"sqlite", "file:test.db?cache=shared", dsnInfo{}},
	}
	for _, c := range cases {
		if got := parseDSN(c.driver, c.dsn); got != c.want {
			t.Errorf("parseDSN(%q, %q) = %+v, want %+v", c.driver, c.dsn, got, c.want)
		}
	}
	for d, want := range map[string]string{"pgx": "postgresql", "postgres": "postgresql", "mysql": "mysql", "sqlite3": "sqlite",
		"sqlserver": "microsoft.sql_server", "foo": "other_sql"} {
		eqs(t, systemFromDriver(d), want)
	}
}

func eqs(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
