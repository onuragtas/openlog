package mysql

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

type result struct {
	cols []string
	rows [][]*string
	err  error
}

// fakeQuerier returns recorded result sets by query prefix.
type fakeQuerier map[string]result

func (f fakeQuerier) Rows(_ context.Context, _ *sql.DB, q string) ([]string, [][]*string, error) {
	for prefix, r := range f {
		if strings.HasPrefix(q, prefix) {
			return r.cols, r.rows, r.err
		}
	}
	return nil, nil, errors.New("unexpected query: " + q)
}

func s(v string) *string { return &v }

func rows(vals ...[]string) [][]*string {
	var out [][]*string
	for _, r := range vals {
		row := make([]*string, len(r))
		for i, v := range r {
			if v != "NULL" {
				row[i] = s(v)
			}
		}
		out = append(out, row)
	}
	return out
}

// Recorded from MySQL 8.4 (subset of SHOW GLOBAL STATUS).
var globalStatus = rows(
	[]string{"Uptime", "7200"},
	[]string{"Innodb_buffer_pool_pages_data", "1000"}, []string{"Innodb_buffer_pool_pages_dirty", "10"},
	[]string{"Innodb_buffer_pool_pages_free", "7000"}, []string{"Innodb_buffer_pool_pages_misc", "18446744073709551615"},
	[]string{"Innodb_buffer_pool_pages_total", "8191"}, []string{"Innodb_buffer_pool_bytes_data", "16384000"},
	[]string{"Innodb_buffer_pool_bytes_dirty", "163840"}, []string{"Innodb_buffer_pool_read_requests", "50000"},
	[]string{"Threads_connected", "3"}, []string{"Threads_running", "2"}, []string{"Threads_created", "5"},
	[]string{"Handler_read_key", "900"}, []string{"Table_locks_waited", "1"}, []string{"Table_locks_immediate", "300"},
	[]string{"Questions", "1234"}, []string{"Queries", "1500"}, []string{"Slow_queries", "2"}, []string{"Connections", "40"},
	[]string{"Aborted_connects", "4"}, []string{"Com_select", "700"}, []string{"Innodb_rows_read", "8000"},
	[]string{"Unrelated_status", "ON"},
)

func collector_(q fakeQuerier) *collector {
	inst := testutil.Instance()
	inst.Settings = config.InstanceSettings{Username: "openlog", Password: "pw"}
	return &collector{inst: inst, ep: integrations.TCP("127.0.0.1", 3306), q: q}
}

func TestCollectRecordedResultSets(t *testing.T) {
	q := fakeQuerier{
		"SHOW GLOBAL STATUS":                        {rows: globalStatus},
		"SELECT @@innodb_buffer_pool_size":          {rows: rows([]string{"134217728"})},
		"SELECT OBJECT_SCHEMA, OBJECT_NAME, COUNT":  {rows: rows([]string{"app", "users", "1", "200", "3", "4", "10", "2000", "30", "40"})},
		"SELECT OBJECT_SCHEMA, OBJECT_NAME, IFNULL": {rows: rows([]string{"app", "users", "PRIMARY", "0", "150", "3", "4", "0", "1500", "30", "40"})},
		"SHOW REPLICA STATUS":                       {err: &driver.MySQLError{Number: 1064, Message: "syntax"}},
		"SHOW SLAVE STATUS":                         {cols: []string{"Slave_IO_State", "Seconds_Behind_Master", "SQL_Delay"}, rows: rows([]string{"Waiting", "5", "0"})},
	}
	now := time.Unix(100_000, 0)
	b := integrations.NewBatch(now, 0)
	if err := collector_(q).collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "mysql.uptime", "s", true, true, 7200, nil)
	testutil.Expect(t, ps, "mysql.buffer_pool.pages", "1", true, false, 1000, map[string]string{"kind": "data"})
	if len(testutil.Find(ps, "mysql.buffer_pool.pages", map[string]string{"kind": "misc"})) != 0 {
		t.Error("out-of-range Innodb_buffer_pool_pages_misc must be skipped")
	}
	testutil.Expect(t, ps, "mysql.buffer_pool.data_pages", "1", true, false, 10, map[string]string{"status": "dirty"})
	testutil.Expect(t, ps, "mysql.buffer_pool.data_pages", "1", true, false, 990, map[string]string{"status": "clean"})
	testutil.Expect(t, ps, "mysql.buffer_pool.usage", "By", true, false, 16220160, map[string]string{"status": "clean"})
	testutil.Expect(t, ps, "mysql.buffer_pool.limit", "By", true, false, 134217728, nil)
	testutil.Expect(t, ps, "mysql.buffer_pool.operations", "1", true, true, 50000, map[string]string{"operation": "read_requests"})
	testutil.Expect(t, ps, "mysql.threads", "1", true, false, 3, map[string]string{"kind": "connected"})
	testutil.Expect(t, ps, "mysql.handlers", "1", true, true, 900, map[string]string{"kind": "read_key"})
	testutil.Expect(t, ps, "mysql.locks", "1", true, true, 1, map[string]string{"kind": "waited"})
	testutil.Expect(t, ps, "mysql.query.client.count", "1", true, true, 1234, nil)
	testutil.Expect(t, ps, "mysql.query.slow.count", "1", true, true, 2, nil)
	testutil.Expect(t, ps, "mysql.connection.count", "1", true, true, 40, nil)
	testutil.Expect(t, ps, "mysql.connection.errors", "1", true, true, 4, map[string]string{"error": "aborted"})
	testutil.Expect(t, ps, "mysql.commands", "1", true, true, 700, map[string]string{"command": "select"})
	testutil.Expect(t, ps, "mysql.row_operations", "1", true, true, 8000, map[string]string{"operation": "read"})
	testutil.Expect(t, ps, "mysql.table.io.wait.count", "1", true, true, 200, map[string]string{"operation": "fetch", "schema": "app", "table": "users"})
	testutil.Expect(t, ps, "mysql.table.io.wait.time", "ns", true, true, 2000, map[string]string{"operation": "fetch", "table": "users"})
	testutil.Expect(t, ps, "mysql.index.io.wait.count", "1", true, true, 150, map[string]string{"operation": "fetch", "index": "PRIMARY"})
	testutil.Expect(t, ps, "mysql.replica.time_behind_source", "s", true, false, 5, nil)
	testutil.Expect(t, ps, "mysql.replica.sql_delay", "s", true, false, 0, nil)
	p := testutil.One(t, ps, "mysql.uptime", nil)
	if p.Resource["mysql.instance.endpoint"] != "127.0.0.1:3306" || p.Resource["service.instance.id"] != ServiceInstanceID("host-1:3306") {
		t.Errorf("resource = %v", p.Resource)
	}
}

func TestPartialAndClassify(t *testing.T) {
	denied := &driver.MySQLError{Number: 1142, Message: "SELECT command denied to user 'openlog'@'localhost' for table 'table_io_waits_summary_by_table'"}
	q := fakeQuerier{
		"SHOW GLOBAL STATUS":               {rows: globalStatus},
		"SELECT @@innodb_buffer_pool_size": {rows: rows([]string{"134217728"})},
		"SELECT OBJECT_SCHEMA":             {err: denied},
		"SHOW REPLICA STATUS":              {cols: []string{"Seconds_Behind_Source"}},
	}
	b := integrations.NewBatch(time.Now(), 0)
	err := collector_(q).collect(context.Background(), b)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "permission denied: SELECT command denied") || b.Points() == 0 {
		t.Errorf("partial = %v (points %d)", err, b.Points())
	}

	c := collector_(nil)
	auth := &driver.MySQLError{Number: 1045, Message: "Access denied for user 'openlog'@'172.18.0.1' (using password: YES)"}
	if err := c.classify(auth); err == nil || !strings.HasPrefix(err.Error(), "authentication failed: Access denied") {
		t.Errorf("wrong password: %v", err)
	}
	c.inst.Settings.Password = ""
	var se *integrations.StatusError
	if err := c.classify(auth); !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration {
		t.Errorf("no password: %v", err)
	}
}

func TestIOWaitsOperationsAndPerformanceSchemaOff(t *testing.T) {
	// Recorded from MySQL 8.4 after inserts, selects, updates and a delete on app.users
	// (columns: COUNT_DELETE, _FETCH, _INSERT, _UPDATE, then FLOOR(SUM_TIMER_*/1000) in the same order).
	b := integrations.NewBatch(time.Now(), 0)
	s := b.Resource()
	RecordIOWaits(s, rows([]string{"app", "users", "1", "37", "16", "4", "22992", "80766", "205011", "61578"}), false)
	RecordIOWaits(s, rows(
		[]string{"app", "users", "PRIMARY", "1", "6", "0", "4", "22992", "21236", "0", "61578"},
		[]string{"app", "users", "idx_name", "0", "31", "0", "0", "0", "59530", "0", "0"},
		[]string{"app", "users", "NONE", "0", "0", "16", "0", "0", "0", "205011", "0"},
		[]string{"app", "users", "short"}), true)
	ps := testutil.Points(b)
	for op, want := range map[string][2]int64{"delete": {1, 22992}, "fetch": {37, 80766}, "insert": {16, 205011}, "update": {4, 61578}} {
		m := map[string]string{"operation": op, "schema": "app", "table": "users"}
		testutil.Expect(t, ps, "mysql.table.io.wait.count", "1", true, true, want[0], m)
		testutil.Expect(t, ps, "mysql.table.io.wait.time", "ns", true, true, want[1], m)
	}
	testutil.Expect(t, ps, "mysql.index.io.wait.count", "1", true, true, 16, map[string]string{"operation": "insert", "index": "NONE"})
	testutil.Expect(t, ps, "mysql.index.io.wait.time", "ns", true, true, 21236, map[string]string{"operation": "fetch", "index": "PRIMARY"})
	testutil.Expect(t, ps, "mysql.index.io.wait.count", "1", true, true, 31, map[string]string{"operation": "fetch", "index": "idx_name", "schema": "app", "table": "users"})
	if n := len(testutil.Find(ps, "mysql.index.io.wait.count", nil)); n != 12 {
		t.Errorf("index points = %d (3 indexes × 4 operations; short rows skipped)", n)
	}
	if !strings.Contains(TableIOWaitsQuery(5), "ORDER BY SUM_TIMER_WAIT DESC, OBJECT_SCHEMA, OBJECT_NAME LIMIT 5") ||
		!strings.Contains(IndexIOWaitsQuery(7), "IFNULL(INDEX_NAME, 'NONE')") {
		t.Error("queries")
	}

	q := fakeQuerier{
		"SHOW GLOBAL STATUS":               {rows: globalStatus},
		"SELECT @@innodb_buffer_pool_size": {rows: rows([]string{"134217728"})},
		"SELECT @@performance_schema":      {rows: rows([]string{"0"})},
		"SELECT OBJECT_SCHEMA":             {},
		"SHOW REPLICA STATUS":              {cols: []string{"Seconds_Behind_Source"}},
	}
	err := collector_(q).collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "performance_schema is disabled") {
		t.Errorf("performance_schema off: %v", err)
	}
	q["SELECT @@performance_schema"] = result{rows: rows([]string{"1"})}
	if err := collector_(q).collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Errorf("performance_schema on, no tables yet: %v", err)
	}
}

const refUUID = "36230c90-44e6-5815-8415-e76dde42e8c9"

func TestServiceInstanceID(t *testing.T) {
	// python3: uuid.uuid5(uuid.UUID('4d63009a-8d0f-11ee-aad7-4c796ed8e320'), 'db-1:3306')
	got := ServiceInstanceID("db-1:3306")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(got) {
		t.Errorf("not a UUIDv5: %s", got)
	}
	if want := refUUID; want != "" && got != want {
		t.Errorf("uuid = %s, want %s", got, want)
	}
}
