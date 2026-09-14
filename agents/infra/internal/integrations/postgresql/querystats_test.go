package postgresql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

func queryStatsCollector(qs *config.QueryStatsConfig, extra ...answer) *collector {
	inst := testutil.Instance()
	inst.Settings = config.InstanceSettings{Username: "openlog", Password: "pw", QueryStats: qs}
	c := &collector{inst: inst, ep: integrations.TCP("127.0.0.1", 5432), conns: map[string]Conn{}}
	c.connect = func(_ context.Context, db string) (Conn, error) {
		return &fakeConn{db: db, answers: append(extra, answers(db, nil)...)}, nil
	}
	return c
}

// Rows recorded from PostgreSQL 16 (pg_stat_statements 1.10); a duplicate
// (track = all) and a row hidden by missing pg_read_all_stats added.
var statementRows = [][]any{
	{int64(3831311678992190039), "app", "openlog", int64(12), int64(1000), 2.234832, int64(3615), int64(1),
		"INSERT INTO t(v)\n    SELECT md5(g::text) FROM generate_series($1,$2) g"},
	{int64(-5683600581812057733), "postgres", "postgres", int64(1), int64(0), 1.589957, int64(8), int64(4), "CREATE ROLE openlog LOGIN PASSWORD 'olpw'"},
	{int64(3831311678992190039), "app", "openlog", int64(3), int64(3), 0.5, int64(1), int64(0), "INSERT INTO t(v) SELECT md5(g::text) FROM generate_series($1,$2) g"},
	{nil, "app", "other", int64(9), int64(9), 0.1, int64(0), int64(0), "<insufficient privilege>"},
}

func TestQueryStats(t *testing.T) {
	c := queryStatsCollector(&config.QueryStatsConfig{Enabled: true},
		answer{"FROM pg_extension", [][]any{{"1.10"}}, nil},
		answer{"FROM pg_stat_statements", statementRows, nil})
	b := integrations.NewBatch(time.Now(), 0)
	err := c.Collect(context.Background(), b)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "pg_stat_statements: 1 statements of other roles are hidden") ||
		!strings.Contains(err.Error(), "pg_read_all_stats") {
		t.Errorf("partial = %v", err)
	}
	ps := testutil.Points(b)
	q := map[string]string{"postgresql.queryid": "3831311678992190039", "postgresql.database.name": "app", "postgresql.rolname": "openlog"}
	testutil.Expect(t, ps, "postgresql.query.calls", "{call}", true, true, 12, q)
	testutil.Expect(t, ps, "postgresql.query.rows", "{row}", true, true, 1000, q)
	testutil.Expect(t, ps, "postgresql.query.shared_blocks", "{block}", true, true, 3615, merge(q, "source", "hit"))
	testutil.Expect(t, ps, "postgresql.query.shared_blocks", "{block}", true, true, 1, merge(q, "source", "read"))
	p := testutil.One(t, ps, "postgresql.query.total_exec_time", q)
	if p.Double != 2.234832 || p.Metric.Unit != "ms" || !p.Monotonic {
		t.Errorf("total_exec_time = %+v", p)
	}
	if got := p.Resource["db.query.text"]; got != "INSERT INTO t(v) SELECT md5(g::text) FROM generate_series($1,$2) g" {
		t.Errorf("text = %q", got)
	}
	role := testutil.One(t, ps, "postgresql.query.calls", map[string]string{"postgresql.queryid": "-5683600581812057733"})
	if got := role.Resource["db.query.text"]; got != "CREATE ROLE openlog LOGIN PASSWORD '?'" {
		t.Errorf("utility statement literal not redacted: %q", got)
	}
	if n := len(testutil.Find(ps, "postgresql.query.calls", nil)); n != 2 {
		t.Errorf("statements = %d", n)
	}
}

func TestQueryStatsMissingExtensionAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []answer
		want  string
	}{
		{"no extension", []answer{{"FROM pg_extension", nil, nil}}, `extension not installed in database "postgres": run CREATE EXTENSION pg_stat_statements`},
		{"not preloaded", []answer{{"FROM pg_extension", [][]any{{"1.10"}}, nil},
			{"FROM pg_stat_statements", nil, &pgconn.PgError{Code: "55000", Message: `pg_stat_statements must be loaded via "shared_preload_libraries"`}}},
			"add pg_stat_statements to shared_preload_libraries"},
		{"denied", []answer{{"FROM pg_extension", [][]any{{"1.10"}}, nil},
			{"FROM pg_stat_statements", nil, &pgconn.PgError{Code: "42501", Message: "permission denied for view pg_stat_statements"}}},
			"grant pg_monitor or pg_read_all_stats"},
	} {
		c := queryStatsCollector(&config.QueryStatsConfig{Enabled: true}, tc.extra...)
		b := integrations.NewBatch(time.Now(), 0)
		err := c.Collect(context.Background(), b)
		var pe *integrations.PartialError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
		if len(testutil.Find(testutil.Points(b), "postgresql.commits", nil)) == 0 {
			t.Errorf("%s: other metrics must still be collected", tc.name)
		}
	}
	// Disabled: pg_stat_statements is never queried (the fake would fail an unexpected query).
	c := queryStatsCollector(&config.QueryStatsConfig{Enabled: false})
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Errorf("disabled: %v", err)
	}
}

func TestQueryStatsQuery(t *testing.T) {
	q := QueryStatsQuery("1.10", 100, 0, []string{"app", "o'brien"})
	for _, want := range []string{"s.total_exec_time", "s.calls >= 1", "d.datname IN ('app', 'o''brien')", "ORDER BY s.total_exec_time DESC, s.queryid LIMIT 100", "left(s.query, 4096)"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q: %s", want, q)
		}
	}
	if q := QueryStatsQuery("1.7", 20, 5, nil); !strings.Contains(q, "s.total_time DESC") || !strings.Contains(q, "s.calls >= 5") || strings.Contains(q, "IN (") {
		t.Errorf("pg12 query: %s", q)
	}
}

func TestNormalizeQueryText(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT  *\n\tFROM t WHERE a = $1":                     "SELECT * FROM t WHERE a = $1",
		"ALTER ROLE x PASSWORD 'it''s secret'":                 "ALTER ROLE x PASSWORD '?'",
		"ALTER USER x WITH PASSWORD E'p\\w'":                   "ALTER USER x WITH PASSWORD E'?'",
		"CREATE FUNCTION f() AS $$ select 'x' $$ LANGUAGE sql": "CREATE FUNCTION f() AS $$?$$ LANGUAGE sql",
		"SET app.token = 'abc":                                 "SET app.token = '?'",
		"  ":                                                   "",
	} {
		if got := NormalizeQueryText(in); got != want {
			t.Errorf("NormalizeQueryText(%q) = %q, want %q", in, got, want)
		}
	}
	long := "SELECT " + strings.Repeat("ş", 1000)
	if got := NormalizeQueryText(long); len(got) > MaxQueryTextBytes || !utf8.ValidString(got) {
		t.Errorf("truncation: %d bytes valid=%t", len(got), utf8.ValidString(got))
	}
}
