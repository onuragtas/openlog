// Package mssql implements the Microsoft SQL Server integration (TDS via github.com/microsoft/go-mssqldb, BSD-3-Clause)
// emitting metrics of the OpenTelemetry Collector sqlserverreceiver where it defines them (semantic-conventions §6.7).
// It works on every OS and can monitor a remote server through integrations.mssql.endpoint.
package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	driver "github.com/microsoft/go-mssqldb"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// DefaultTopWaits bounds the wait types of sqlserver.os.wait.duration (integrations.mssql.top_n_tables overrides it).
const DefaultTopWaits = 10

// Integration is the SQL Server integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationMSSQL }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: 1433, SkipPort: func(p int) bool { return p == 1434 }} // 1434: DAC / browser
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	ep := "127.0.0.1:1433"
	if len(inst.Endpoints) > 0 {
		ep = inst.Endpoints[0].Display
	}
	return `# SQL Server needs a monitoring login with SQL Server authentication (mixed mode); the agent never creates logins.
# Run as sysadmin (sqlcmd or SSMS):
#   CREATE LOGIN openlog WITH PASSWORD = '<password>', CHECK_POLICY = ON;
#   GRANT VIEW SERVER STATE TO openlog;        -- SQL Server 2022+: VIEW SERVER PERFORMANCE STATE
#   GRANT VIEW ANY DEFINITION TO openlog;      -- database sizes (sys.master_files)
# Then enter the login and password in openlog (host → Integrations → SQL Server), or in config.yaml:
integrations:
  mssql:
    username: openlog
    password: env:OPENLOG_MSSQL_PASSWORD   # or file:C:\ProgramData\openlog\infra-agent\mssql.password
    instances:
      - match: { endpoint: "` + ep + `" }`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	if ep.Network != "tcp" {
		return nil, fmt.Errorf("unsupported endpoint %s (SQL Server needs host:port): %w", ep.Display, integrations.ErrTryNext)
	}
	return &collector{inst: inst, ep: ep, q: sqlQuerier{}, open: openDB}, nil
}

// Querier abstracts the queries so that tests can use recorded result sets.
type Querier interface {
	// Rows runs a query and returns all rows as strings (NULL → nil).
	Rows(ctx context.Context, db *sql.DB, query string) ([][]*string, error)
}

type sqlQuerier struct{}

func (sqlQuerier) Rows(ctx context.Context, db *sql.DB, query string) ([][]*string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]*string
	for rows.Next() {
		raw := make([]sql.RawBytes, len(cols))
		dest := make([]any, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]*string, len(cols))
		for i, r := range raw {
			if r != nil {
				s := string(r)
				row[i] = &s
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type collector struct {
	inst *integrations.Instance
	ep   integrations.Endpoint
	db   *sql.DB
	q    Querier
	open func(ctx context.Context, dsn string) (*sql.DB, error)

	// Previous cumulative "/sec" counters for the .rate gauges.
	prev   map[string]int64
	prevAt time.Time
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := driver.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (c *collector) Close() {
	if c.db != nil {
		c.db.Close()
		c.db = nil
	}
}

// DSN builds the connection URL. It contains the password: never log it.
func DSN(s config.InstanceSettings, password string, ep integrations.Endpoint, timeout time.Duration) (string, error) {
	q := url.Values{}
	q.Set("database", "master")
	q.Set("app name", "openlog-infra-agent")
	secs := strconv.Itoa(max(1, int(timeout.Seconds())))
	q.Set("dial timeout", secs)
	q.Set("connection timeout", secs)
	q.Set("encrypt", "false") // SQL Server default: only the login packet is encrypted
	if t := s.TLS; t != nil && t.Enabled {
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", strconv.FormatBool(t.InsecureSkipVerify))
		if t.CAFile != "" {
			if _, err := os.Stat(t.CAFile); err != nil {
				return "", integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			q.Set("certificate", t.CAFile)
		}
		if t.ServerName != "" {
			q.Set("hostNameInCertificate", t.ServerName)
		}
	}
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(s.Username, password), Host: ep.Address, RawQuery: q.Encode()}
	return u.String(), nil
}

func (c *collector) connect(ctx context.Context) error {
	password, err := c.inst.Password()
	if err != nil {
		return integrations.NeedsConfiguration("password: "+err.Error(), false)
	}
	if c.inst.Settings.Username == "" {
		return integrations.NeedsConfiguration("integrations.mssql.username is not set (SQL Server authentication login)", true)
	}
	dsn, err := DSN(c.inst.Settings, password, c.ep, c.inst.Timeout)
	if err != nil {
		return err
	}
	db, err := c.open(ctx, dsn)
	if err != nil {
		return c.classify(err)
	}
	c.db = db
	return nil
}

func (c *collector) classify(err error) error {
	var me driver.Error
	if errors.As(err, &me) {
		switch me.Number {
		case 18456: // login failed
			if c.inst.Settings.Password == "" {
				return integrations.NeedsConfiguration("login failed without a password: configure integrations.mssql.username and password", false)
			}
			return fmt.Errorf("authentication failed: %s", me.Message)
		case 297, 300, 229: // permission denied (VIEW SERVER STATE)
			return fmt.Errorf("permission denied: %s (GRANT VIEW SERVER STATE TO <login>)", me.Message)
		}
		return err
	}
	if integrations.IsUnreachable(err) {
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	return err
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.db == nil {
		if err := c.connect(ctx); err != nil {
			return err
		}
	}
	return c.collect(ctx, b)
}

// Queries (exported for documentation and tests).
const (
	QueryServer   = "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), CAST(@@SERVICENAME AS nvarchar(128)), DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) FROM sys.dm_os_sys_info"
	QueryCounters = "SELECT RTRIM(object_name), RTRIM(counter_name), RTRIM(instance_name), cntr_value FROM sys.dm_os_performance_counters WHERE counter_name IN (" +
		"'User Connections','Processes blocked','Batch Requests/sec','SQL Compilations/sec','SQL Re-Compilations/sec','Number of Deadlocks/sec'," +
		"'Lock Waits/sec','Buffer cache hit ratio','Buffer cache hit ratio base','Page life expectancy','Transactions/sec','Page Splits/sec')"
	QueryDatabaseSizes = "SELECT DB_NAME(database_id), type_desc, SUM(CAST(size AS bigint)) * 8192 FROM sys.master_files GROUP BY database_id, type_desc"
)

// benignWaits are idle/background wait types excluded from the top waits (subset of the list used by Microsoft's and
// Paul Randal's wait scripts).
var benignWaits = []string{
	"BROKER_EVENTHANDLER", "BROKER_RECEIVE_WAITFOR", "BROKER_TASK_STOP", "BROKER_TO_FLUSH", "BROKER_TRANSMITTER", "CHECKPOINT_QUEUE",
	"CHKPT", "CLR_AUTO_EVENT", "CLR_MANUAL_EVENT", "CLR_SEMAPHORE", "DBMIRROR_DBM_EVENT", "DBMIRROR_EVENTS_QUEUE", "DBMIRROR_WORKER_QUEUE",
	"DBMIRRORING_CMD", "DIRTY_PAGE_POLL", "DISPATCHER_QUEUE_SEMAPHORE", "EXECSYNC", "FSAGENT", "FT_IFTS_SCHEDULER_IDLE_WAIT", "FT_IFTSHC_MUTEX",
	"HADR_CLUSAPI_CALL", "HADR_FILESTREAM_IOMGR_IOCOMPLETION", "HADR_LOGCAPTURE_WAIT", "HADR_NOTIFICATION_DEQUEUE", "HADR_TIMER_TASK",
	"HADR_WORK_QUEUE", "KSOURCE_WAKEUP", "LAZYWRITER_SLEEP", "LOGMGR_QUEUE", "MEMORY_ALLOCATION_EXT", "ONDEMAND_TASK_QUEUE",
	"PARALLEL_REDO_DRAIN_WORKER", "PARALLEL_REDO_LOG_CACHE", "PARALLEL_REDO_TRAN_LIST", "PARALLEL_REDO_WORKER_SYNC", "PARALLEL_REDO_WORKER_WAIT_WORK",
	"PREEMPTIVE_OS_FLUSHFILEBUFFERS", "PREEMPTIVE_XE_GETTARGETSTATE", "PVS_PREALLOCATE", "PWAIT_ALL_COMPONENTS_INITIALIZED",
	"PWAIT_DIRECTLOGCONSUMER_GETNEXT", "PWAIT_EXTENSIBILITY_CLEANUP_TASK", "QDS_PERSIST_TASK_MAIN_LOOP_SLEEP", "QDS_ASYNC_QUEUE",
	"QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP", "QDS_SHUTDOWN_QUEUE", "REDO_THREAD_PENDING_WORK", "REQUEST_FOR_DEADLOCK_SEARCH",
	"RESOURCE_QUEUE", "SERVER_IDLE_CHECK", "SLEEP_BPOOL_FLUSH", "SLEEP_DBSTARTUP", "SLEEP_DCOMSTARTUP", "SLEEP_MASTERDBREADY",
	"SLEEP_MASTERMDREADY", "SLEEP_MASTERUPGRADED", "SLEEP_MSDBSTARTUP", "SLEEP_SYSTEMTASK", "SLEEP_TASK", "SLEEP_TEMPDBSTARTUP",
	"SNI_HTTP_ACCEPT", "SOS_WORK_DISPATCHER", "SP_SERVER_DIAGNOSTICS_SLEEP", "SQLTRACE_BUFFER_FLUSH", "SQLTRACE_INCREMENTAL_FLUSH_SLEEP",
	"SQLTRACE_WAIT_ENTRIES", "UCS_SESSION_REGISTRATION", "VDI_CLIENT_OTHER", "WAIT_FOR_RESULTS", "WAITFOR", "WAITFOR_TASKSHUTDOWN",
	"WAIT_XTP_RECOVERY", "WAIT_XTP_HOST_WAIT", "WAIT_XTP_OFFLINE_CKPT_NEW_LOG", "WAIT_XTP_CKPT_CLOSE", "XE_DISPATCHER_JOIN",
	"XE_DISPATCHER_WAIT", "XE_TIMER_EVENT", "XE_LIVE_TARGET_TVF",
}

// WaitStatsQuery returns the top n wait types by total wait time.
func WaitStatsQuery(n int) string {
	return fmt.Sprintf("SELECT TOP (%d) wait_type, wait_time_ms FROM sys.dm_os_wait_stats WHERE wait_time_ms > 0 AND wait_type NOT IN ('%s') ORDER BY wait_time_ms DESC",
		n, strings.Join(benignWaits, "','"))
}

// WaitCategory maps a wait type to the category names of Query Store (sys.query_store_wait_stats).
func WaitCategory(waitType string) string {
	w := strings.ToUpper(waitType)
	switch {
	case strings.HasPrefix(w, "LCK_M_"):
		return "Lock"
	case strings.HasPrefix(w, "PAGEIOLATCH_"):
		return "Buffer IO"
	case strings.HasPrefix(w, "PAGELATCH_"):
		return "Buffer Latch"
	case strings.HasPrefix(w, "LATCH_"):
		return "Latch"
	case w == "WRITELOG" || w == "LOGBUFFER":
		return "Tran Log IO"
	case w == "ASYNC_NETWORK_IO" || strings.HasPrefix(w, "NET_WAITFOR_PACKET"):
		return "Network IO"
	case w == "CXPACKET" || w == "CXCONSUMER" || w == "EXCHANGE":
		return "Parallelism"
	case w == "SOS_SCHEDULER_YIELD":
		return "CPU"
	case strings.HasPrefix(w, "RESOURCE_SEMAPHORE") || w == "CMEMTHREAD":
		return "Memory"
	case strings.HasPrefix(w, "PREEMPTIVE_"):
		return "Preemptive"
	case strings.HasPrefix(w, "IO_COMPLETION") || w == "ASYNC_IO_COMPLETION" || w == "BACKUPIO":
		return "Other Disk IO"
	case strings.HasPrefix(w, "HADR_"):
		return "Replication"
	}
	return "Other"
}

func (c *collector) collect(ctx context.Context, b *integrations.Batch) error {
	server, err := c.q.Rows(ctx, c.db, QueryServer)
	if err != nil {
		return c.classify(err)
	}
	if len(server) == 1 && len(server[0]) == 3 {
		if v := server[0][0]; v != nil {
			b.SetResourceAttr(otlputil.Str("sqlserver.version", *v))
		}
		if v := server[0][1]; v != nil {
			b.SetResourceAttr(otlputil.Str("sqlserver.instance.name", *v))
		}
		if v := server[0][2]; v != nil {
			if up, err := strconv.ParseInt(*v, 10, 64); err == nil && up >= 0 {
				b.SetStartTime(b.Now().Add(-time.Duration(up) * time.Second))
			}
		}
	}
	rows, err := c.q.Rows(ctx, c.db, QueryCounters)
	if err != nil {
		return c.classify(err)
	}
	s := b.Resource()
	c.recordCounters(s, ParseCounters(rows), b.Now())

	var partial []string
	if rows, err := c.q.Rows(ctx, c.db, QueryDatabaseSizes); err != nil {
		partial = append(partial, "database sizes (sys.master_files, needs VIEW ANY DEFINITION): "+c.classify(err).Error())
	} else {
		RecordDatabaseSizes(s, rows)
	}
	topN := c.inst.Settings.TopNTables
	if topN <= 0 {
		topN = DefaultTopWaits
	}
	if rows, err := c.q.Rows(ctx, c.db, WaitStatsQuery(topN)); err != nil {
		partial = append(partial, "wait stats: "+c.classify(err).Error())
	} else {
		RecordWaits(s, rows)
	}
	if len(partial) > 0 {
		return integrations.Partial(errors.New(strings.Join(partial, "; ")))
	}
	return nil
}

// ParseCounters maps "<object>|<counter>" (object without the SQLServer:/MSSQL$<instance>: prefix) to cntr_value.
// Per-instance counters keep only the _Total instance (Locks, Databases) or the empty instance.
func ParseCounters(rows [][]*string) map[string]int64 {
	out := map[string]int64{}
	for _, r := range rows {
		if len(r) != 4 || r[0] == nil || r[1] == nil || r[3] == nil {
			continue
		}
		obj := *r[0]
		if i := strings.IndexByte(obj, ':'); i >= 0 {
			obj = obj[i+1:]
		}
		inst := ""
		if r[2] != nil {
			inst = strings.TrimSpace(*r[2])
		}
		if inst != "" && inst != "_Total" {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(*r[3]), 10, 64)
		if err != nil {
			continue
		}
		out[obj+"|"+*r[1]] = v
	}
	return out
}

// rateCounters are cumulative "/sec" counters reported as OTel .rate gauges (per second since the previous collection).
var rateCounters = []struct{ key, metric, unit string }{
	{"SQL Statistics|Batch Requests/sec", "sqlserver.batch.request.rate", "{requests}/s"},
	{"SQL Statistics|SQL Compilations/sec", "sqlserver.batch.sql_compilation.rate", "{compilations}/s"},
	{"SQL Statistics|SQL Re-Compilations/sec", "sqlserver.batch.sql_recompilation.rate", "{compilations}/s"},
	{"Locks|Number of Deadlocks/sec", "sqlserver.deadlock.rate", "{deadlocks}/s"},
	{"Locks|Lock Waits/sec", "sqlserver.lock.wait.rate", "{requests}/s"},
	{"Databases|Transactions/sec", "sqlserver.transaction.rate", "{transactions}/s"},
	{"Access Methods|Page Splits/sec", "sqlserver.page.split.rate", "{pages}/s"},
}

func (c *collector) recordCounters(s *integrations.Scope, v map[string]int64, now time.Time) {
	if n, ok := v["General Statistics|User Connections"]; ok {
		s.GaugeInt("sqlserver.user.connection.count", "{connections}", n)
	}
	if n, ok := v["General Statistics|Processes blocked"]; ok {
		s.GaugeInt("sqlserver.processes.blocked", "{processes}", n)
	}
	if n, ok := v["Buffer Manager|Page life expectancy"]; ok {
		s.GaugeInt("sqlserver.page.life_expectancy", "s", n, otlputil.Str("performance_counter.object_name", "Buffer Manager"))
	}
	if ratio, ok := v["Buffer Manager|Buffer cache hit ratio"]; ok {
		if base := v["Buffer Manager|Buffer cache hit ratio base"]; base > 0 {
			s.GaugeDouble("sqlserver.page.buffer_cache.hit_ratio", "%", float64(ratio)*100/float64(base))
		}
	}
	// Deadlocks are also sent as a cumulative count: a deadlock between two collections is not lost in a rate.
	if n, ok := v["Locks|Number of Deadlocks/sec"]; ok {
		s.SumInt("sqlserver.deadlock.count", "{deadlocks}", true, n)
	}
	if !c.prevAt.IsZero() {
		if secs := now.Sub(c.prevAt).Seconds(); secs > 0 {
			for _, rc := range rateCounters {
				cur, ok1 := v[rc.key]
				prev, ok2 := c.prev[rc.key]
				if ok1 && ok2 && cur >= prev {
					s.GaugeDouble(rc.metric, rc.unit, float64(cur-prev)/secs)
				}
			}
		}
	}
	c.prev, c.prevAt = v, now
}

// RecordDatabaseSizes emits sqlserver.database.size (openlog, not in OTel) per database and file type (ROWS, LOG, …).
func RecordDatabaseSizes(s *integrations.Scope, rows [][]*string) {
	for _, r := range rows {
		if len(r) != 3 || r[0] == nil || r[1] == nil || r[2] == nil {
			continue
		}
		n, err := strconv.ParseInt(*r[2], 10, 64)
		if err != nil {
			continue
		}
		s.SumInt("sqlserver.database.size", "By", false, n,
			otlputil.Str("sqlserver.database.name", *r[0]), otlputil.Str("file_type", strings.ToLower(*r[1])))
	}
}

// RecordWaits emits sqlserver.os.wait.duration (seconds since server start) for the top wait types.
func RecordWaits(s *integrations.Scope, rows [][]*string) {
	for _, r := range rows {
		if len(r) != 2 || r[0] == nil || r[1] == nil {
			continue
		}
		ms, err := strconv.ParseInt(*r[1], 10, 64)
		if err != nil {
			continue
		}
		s.SumDouble("sqlserver.os.wait.duration", "s", true, float64(ms)/1000,
			otlputil.Str("wait.category", WaitCategory(*r[0])), otlputil.Str("wait.type", *r[0]))
	}
}
