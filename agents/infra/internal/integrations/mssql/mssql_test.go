package mssql

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	driver "github.com/microsoft/go-mssqldb"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

func sp(s string) *string { return &s }

type fakeQuerier struct {
	answers map[string][][]*string // by query prefix
	errs    map[string]error
	calls   []string
}

func (f *fakeQuerier) Rows(_ context.Context, _ *sql.DB, q string) ([][]*string, error) {
	f.calls = append(f.calls, q)
	for prefix, err := range f.errs {
		if strings.Contains(q, prefix) {
			return nil, err
		}
	}
	for prefix, rows := range f.answers {
		if strings.Contains(q, prefix) {
			return rows, nil
		}
	}
	return nil, errors.New("unexpected query " + q)
}

func counters(batches, deadlocks int) [][]*string {
	row := func(obj, counter, inst, v string) []*string { return []*string{sp(obj), sp(counter), sp(inst), sp(v)} }
	return [][]*string{
		row("MSSQL$SQLEXPRESS:General Statistics", "User Connections", "", "7"),
		row("MSSQL$SQLEXPRESS:General Statistics", "Processes blocked", "", "1"),
		row("MSSQL$SQLEXPRESS:SQL Statistics", "Batch Requests/sec", "", itoa(batches)),
		row("MSSQL$SQLEXPRESS:Locks", "Number of Deadlocks/sec", "_Total", itoa(deadlocks)),
		row("MSSQL$SQLEXPRESS:Locks", "Number of Deadlocks/sec", "Page", "99"), // per lock type: ignored
		row("MSSQL$SQLEXPRESS:Buffer Manager", "Buffer cache hit ratio", "", "990"),
		row("MSSQL$SQLEXPRESS:Buffer Manager", "Buffer cache hit ratio base", "", "1000"),
		row("MSSQL$SQLEXPRESS:Buffer Manager", "Page life expectancy", "", "3600"),
		row("MSSQL$SQLEXPRESS:Databases", "Transactions/sec", "_Total", "500"),
		row("MSSQL$SQLEXPRESS:Databases", "Transactions/sec", "master", "10"),
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func newCollector(q *fakeQuerier, s config.InstanceSettings) *collector {
	inst := testutil.Instance()
	inst.Settings = s
	return &collector{inst: inst, ep: integrations.TCP("127.0.0.1", 1433), q: q,
		open: func(context.Context, string) (*sql.DB, error) { return &sql.DB{}, nil }}
}

func TestCollectRecordedResultSets(t *testing.T) {
	q := &fakeQuerier{answers: map[string][][]*string{
		"SERVERPROPERTY":       {{sp("16.0.4135.4"), sp("SQLEXPRESS"), sp("120")}},
		"dm_os_performance":    counters(1000, 2),
		"sys.master_files":     {{sp("master"), sp("ROWS"), sp("6291456")}, {sp("app"), sp("LOG"), sp("8388608")}},
		"sys.dm_os_wait_stats": {{sp("WRITELOG"), sp("1500")}, {sp("LCK_M_X"), sp("250")}},
	}}
	c := newCollector(q, config.InstanceSettings{Username: "openlog", Password: "pw", TopNTables: 5})
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := integrations.NewBatch(now, 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ps := testutil.Points(b)
	one := func(name string, match map[string]string) testutil.Point {
		t.Helper()
		f := testutil.Find(ps, name, match)
		if len(f) != 1 {
			t.Fatalf("%s %v: %d points", name, match, len(f))
		}
		return f[0]
	}
	if p := one("sqlserver.user.connection.count", nil); p.Int != 7 || p.IsSum {
		t.Errorf("connections %+v", p)
	}
	if p := one("sqlserver.page.buffer_cache.hit_ratio", nil); math.Abs(p.Double-99) > 1e-9 {
		t.Errorf("hit ratio %v", p.Double)
	}
	if p := one("sqlserver.deadlock.count", nil); p.Int != 2 || !p.Monotonic {
		t.Errorf("deadlocks %+v", p)
	}
	if p := one("sqlserver.database.size", map[string]string{"sqlserver.database.name": "app", "file_type": "log"}); p.Int != 8388608 {
		t.Errorf("db size %+v", p)
	}
	if p := one("sqlserver.os.wait.duration", map[string]string{"wait.type": "WRITELOG", "wait.category": "Tran Log IO"}); p.Double != 1.5 {
		t.Errorf("waits %+v", p)
	}
	if p := one("sqlserver.page.life_expectancy", nil); p.Int != 3600 || p.Resource["sqlserver.version"] != "16.0.4135.4" || p.Resource["sqlserver.instance.name"] != "SQLEXPRESS" {
		t.Errorf("ple %+v", p)
	}
	if len(testutil.Find(ps, "sqlserver.batch.request.rate", nil)) != 0 {
		t.Error("rate emitted on the first collection")
	}
	if !strings.Contains(q.calls[len(q.calls)-1], "TOP (5)") {
		t.Errorf("top_n_tables not applied: %s", q.calls[len(q.calls)-1])
	}

	// Second collection 10 s later: rates from the counter deltas.
	q.answers["dm_os_performance"] = counters(1500, 3)
	b = integrations.NewBatch(now.Add(10*time.Second), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ps = testutil.Points(b)
	if p := one("sqlserver.batch.request.rate", nil); p.Double != 50 {
		t.Errorf("batch rate %v", p.Double)
	}
	if p := one("sqlserver.deadlock.rate", nil); p.Double != 0.1 {
		t.Errorf("deadlock rate %v", p.Double)
	}
	if p := one("sqlserver.transaction.rate", nil); p.Double != 0 {
		t.Errorf("transaction rate %v", p.Double)
	}
}

func TestPartialAndErrors(t *testing.T) {
	denied := driver.Error{Number: 300, Message: "VIEW SERVER STATE permission was denied on object 'server', database 'master'."}
	q := &fakeQuerier{
		answers: map[string][][]*string{"SERVERPROPERTY": {{sp("16.0"), sp("MSSQLSERVER"), sp("1")}}, "dm_os_performance": counters(1, 0),
			"sys.dm_os_wait_stats": nil},
		errs: map[string]error{"sys.master_files": denied},
	}
	c := newCollector(q, config.InstanceSettings{Username: "openlog", Password: "pw"})
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "VIEW ANY DEFINITION") {
		t.Fatalf("want partial error, got %v", err)
	}

	// Login failed without a password: needs_configuration.
	c = newCollector(&fakeQuerier{}, config.InstanceSettings{Username: "openlog"})
	c.open = func(context.Context, string) (*sql.DB, error) {
		return nil, driver.Error{Number: 18456, Message: "Login failed for user 'openlog'."}
	}
	var se *integrations.StatusError
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration {
		t.Fatalf("want needs_configuration, got %v", err)
	}
	// With a password: an authentication error.
	c.inst.Settings.Password = "wrong"
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err == nil || !strings.HasPrefix(err.Error(), "authentication failed") {
		t.Fatalf("got %v", err)
	}
	// Username missing.
	c = newCollector(&fakeQuerier{}, config.InstanceSettings{Password: "pw"})
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration {
		t.Fatalf("want needs_configuration for a missing username, got %v", err)
	}
}

func TestDSN(t *testing.T) {
	dsn, err := DSN(config.InstanceSettings{Username: `dom\user`, TLS: &config.TLSConfig{Enabled: true, InsecureSkipVerify: true, ServerName: "db.example"}},
		"p@ss:w/rd", integrations.TCP("10.0.0.5", 1433), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.Scheme != "sqlserver" || u.Host != "10.0.0.5:1433" || u.User.Username() != `dom\user` || pw != "p@ss:w/rd" {
		t.Errorf("dsn %s", dsn)
	}
	q := u.Query()
	if q.Get("encrypt") != "true" || q.Get("TrustServerCertificate") != "true" || q.Get("hostNameInCertificate") != "db.example" || q.Get("dial timeout") != "10" {
		t.Errorf("query %v", q)
	}
	if _, err := DSN(config.InstanceSettings{TLS: &config.TLSConfig{Enabled: true, CAFile: "/does/not/exist.pem"}}, "", integrations.TCP("h", 1), time.Second); err == nil {
		t.Error("missing CA file accepted")
	}
}

func TestNewRejectsUnixSockets(t *testing.T) {
	_, err := Integration{}.New(testutil.Instance(), integrations.Endpoint{Network: "unix", Address: "/tmp/x", Display: "unix:/tmp/x"})
	if !errors.Is(err, integrations.ErrTryNext) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(WaitStatsQuery(3), "TOP (3)") || !strings.Contains(WaitStatsQuery(3), "'SLEEP_TASK'") {
		t.Error("wait stats query")
	}
	for in, want := range map[string]string{"LCK_M_S": "Lock", "PAGEIOLATCH_SH": "Buffer IO", "CXPACKET": "Parallelism", "FOO": "Other"} {
		if got := WaitCategory(in); got != want {
			t.Errorf("WaitCategory(%s) = %s", in, got)
		}
	}
}
