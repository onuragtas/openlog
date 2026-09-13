package postgresql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

type answer struct {
	match string
	rows  [][]any
	err   error
}

// fakeConn answers queries by the first matching substring (recorded from PostgreSQL 16).
type fakeConn struct {
	db      string
	answers []answer
	closed  bool
}

func (f *fakeConn) Query(_ context.Context, sql string) ([][]any, error) {
	for _, a := range f.answers {
		if strings.Contains(sql, a.match) {
			return a.rows, a.err
		}
	}
	return nil, errors.New("unexpected query: " + sql)
}

func (f *fakeConn) Close(context.Context) error { f.closed = true; return nil }

func answers(db string, lockErr error) []answer {
	return []answer{
		{"server_version_num", [][]any{{"160004"}}, nil},
		{"max_connections", [][]any{{"100"}}, nil},
		{"pg_stat_bgwriter", [][]any{{int64(5), int64(10), 1.5, 0.5, int64(100), int64(7), int64(900), int64(0), int64(3), int64(0)}}, nil},
		{"pg_stat_replication", [][]any{{"10.0.0.2", int64(1024), int32(1), int32(-1), int32(2)}}, nil},
		{"pg_stat_archiver", nil, nil},
		{"pg_stat_activity", [][]any{{"app", int64(3)}, {"postgres", int64(1)}, {"other", int64(9)}}, nil},
		{"FROM pg_stat_database", [][]any{{"app", int64(10), int64(1), int64(0)}, {"postgres", int64(4), int64(0), int64(1)}}, nil},
		{"pg_database_size", [][]any{{"app", int64(8_000_000)}, {"postgres", int64(7_000_000)}}, nil},
		{"SELECT datname FROM pg_database", [][]any{{"app"}, {"other"}, {"postgres"}}, nil},
		{"pg_stat_user_tables", [][]any{{"public", "users", int64(5), int64(1), int64(6), int64(2), int64(1), int64(0), int64(8192), int64(1)}}, nil},
		{"pg_statio_user_tables", [][]any{{"public", "users", int64(1), int64(20), int64(2), int64(30), int64(0), int64(0), int64(0), int64(0)}}, nil},
		{"pg_stat_user_indexes", [][]any{{"public", "users", "users_pkey", int64(16384), int64(12)}}, nil},
		{"FROM pg_class WHERE relkind", [][]any{{int64(2)}}, nil},
		{"FROM pg_locks LEFT JOIN pg_class", [][]any{{"users", "AccessShareLock", "relation", int64(1)}}, lockErr},
	}
}

func newCollector(t *testing.T, s config.InstanceSettings, lockErr map[string]error) (*collector, map[string]*fakeConn) {
	inst := testutil.Instance()
	inst.Settings = s
	c := &collector{inst: inst, ep: integrations.TCP("127.0.0.1", 5432), conns: map[string]Conn{}}
	conns := map[string]*fakeConn{}
	c.connect = func(_ context.Context, db string) (Conn, error) {
		f := &fakeConn{db: db, answers: answers(db, lockErr[db])}
		conns[db] = f
		return f, nil
	}
	return c, conns
}

func TestCollectRecordedResultSets(t *testing.T) {
	c, conns := newCollector(t, config.InstanceSettings{Username: "openlog", Password: "pw", ExcludeDatabases: []string{"other"}}, nil)
	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ps := testutil.Points(b)
	server := func(name string) []testutil.Point {
		var out []testutil.Point
		for _, p := range testutil.Find(ps, name, nil) {
			if p.Resource["postgresql.database.name"] == "" {
				out = append(out, p)
			}
		}
		return out
	}
	if p := server("postgresql.database.count"); len(p) != 1 || p[0].Int != 2 {
		t.Errorf("database.count = %+v", p)
	}
	if p := server("postgresql.connection.max"); len(p) != 1 || p[0].Int != 100 || p[0].IsSum {
		t.Errorf("connection.max = %+v", p)
	}
	testutil.Expect(t, ps, "postgresql.bgwriter.checkpoint.count", "{checkpoints}", true, true, 5, map[string]string{"type": "requested"})
	testutil.Expect(t, ps, "postgresql.bgwriter.buffers.writes", "{buffers}", true, true, 3, map[string]string{"source": "backend"})
	testutil.Expect(t, ps, "postgresql.bgwriter.buffers.allocated", "{buffers}", true, true, 900, nil)
	if p := testutil.One(t, ps, "postgresql.bgwriter.duration", map[string]string{"type": "write"}); p.Double != 1.5 || p.Metric.Unit != "ms" {
		t.Errorf("bgwriter.duration = %+v", p)
	}
	testutil.Expect(t, ps, "postgresql.replication.data_delay", "By", false, false, 1024, map[string]string{"replication_client": "10.0.0.2"})
	testutil.Expect(t, ps, "postgresql.wal.lag", "s", false, false, 2, map[string]string{"operation": "replay"})
	if len(testutil.Find(ps, "postgresql.wal.lag", map[string]string{"operation": "flush"})) != 0 {
		t.Error("unknown lag (-1) must be omitted")
	}

	app := map[string]string{"postgresql.database.name": "app"}
	testutil.Expect(t, ps, "postgresql.backends", "1", true, false, 3, app)
	testutil.Expect(t, ps, "postgresql.commits", "1", true, true, 10, app)
	testutil.Expect(t, ps, "postgresql.rollbacks", "1", true, true, 1, app)
	testutil.Expect(t, ps, "postgresql.db_size", "By", true, false, 8_000_000, app)
	testutil.Expect(t, ps, "postgresql.deadlocks", "{deadlock}", true, true, 1, map[string]string{"postgresql.database.name": "postgres"})
	if len(testutil.Find(ps, "postgresql.backends", map[string]string{"postgresql.database.name": "other"})) != 0 {
		t.Error("excluded database collected")
	}
	for _, p := range testutil.Find(ps, "postgresql.table.count", app) {
		if p.Resource["postgresql.table.name"] != "" || p.Int != 2 {
			t.Errorf("table.count = %+v", p)
		}
	}
	testutil.Expect(t, ps, "postgresql.database.locks", "{lock}", false, false, 1,
		map[string]string{"postgresql.database.name": "app", "relation": "users", "mode": "AccessShareLock", "lock_type": "relation"})

	table := map[string]string{"postgresql.database.name": "app", "postgresql.table.name": "public.users"}
	testutil.Expect(t, ps, "postgresql.rows", "1", true, false, 5, merge(table, "state", "live"))
	testutil.Expect(t, ps, "postgresql.operations", "1", true, true, 6, merge(table, "operation", "ins"))
	testutil.Expect(t, ps, "postgresql.table.size", "By", true, false, 8192, table)
	testutil.Expect(t, ps, "postgresql.table.vacuum.count", "{vacuum}", true, true, 1, table)
	testutil.Expect(t, ps, "postgresql.blocks_read", "1", true, true, 20, merge(table, "source", "heap_hit"))
	index := map[string]string{"postgresql.database.name": "app", "postgresql.table.name": "users", "postgresql.index.name": "users_pkey"}
	testutil.Expect(t, ps, "postgresql.index.size", "By", false, false, 16384, index)
	testutil.Expect(t, ps, "postgresql.index.scans", "{scans}", true, true, 12, index)

	if !conns["app"].closed || conns["postgres"].closed {
		t.Error("per-database connections are closed after collection, the main one is kept")
	}
}

func merge(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for a, b := range m {
		out[a] = b
	}
	return out
}

func TestPartialAndClassify(t *testing.T) {
	denied := &pgconn.PgError{Code: "42501", Message: "permission denied for table pg_locks"}
	c, _ := newCollector(t, config.InstanceSettings{Username: "openlog", Password: "pw"}, map[string]error{"app": denied})
	b := integrations.NewBatch(time.Now(), 0)
	err := c.Collect(context.Background(), b)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "permission denied: permission denied for table pg_locks (grant pg_monitor)") {
		t.Errorf("partial = %v", err)
	}

	auth := &pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "openlog"`}
	if err := c.classify(auth, "postgres"); err == nil || !strings.HasPrefix(err.Error(), "authentication failed: password authentication failed") {
		t.Errorf("wrong password: %v", err)
	}
	c.inst.Settings.Password = ""
	var se *integrations.StatusError
	if err := c.classify(auth, "postgres"); !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration {
		t.Errorf("no password: %v", err)
	}
	if err := c.classify(&pgconn.PgError{Code: "3D000", Message: "database does not exist"}, "nope"); !errors.As(err, &se) {
		t.Errorf("missing database: %v", err)
	}
}
