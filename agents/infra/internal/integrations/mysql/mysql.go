// Package mysql implements the MySQL/MariaDB integration (native protocol via
// github.com/go-sql-driver/mysql, MPL-2.0) emitting the metrics of the
// OpenTelemetry Collector mysqlreceiver (semantic-conventions §6.5).
package mysql

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// DefaultTopN bounds the tables and indexes with io-wait series.
const DefaultTopN = 50

// Integration is the MySQL/MariaDB integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationMySQL }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{
		DefaultPort: 3306,
		SkipPort:    func(p int) bool { return p == 33060 || p == 33062 }, // X protocol, admin port
		UnixSockets: []string{"/run/mysqld/mysqld.sock", "/var/run/mysqld/mysqld.sock", "/var/lib/mysql/mysql.sock", "/tmp/mysql.sock"},
	}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	ep := "127.0.0.1:3306"
	if len(inst.Endpoints) > 0 {
		ep = inst.Endpoints[0].Display
	}
	return `# MySQL needs a read-only monitoring user; the agent never creates users itself. Run as an
# administrator (for a container: docker exec -it <container> mysql -uroot -p):
#   CREATE USER 'openlog'@'%' IDENTIFIED BY '<password>';   -- '%': the agent connects through Docker
#                                                          -- networks too; use 'localhost' for socket-only servers
#   GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'openlog'@'%';
#   GRANT SELECT ON performance_schema.* TO 'openlog'@'%';
# Then enter the user and password in openlog (host → Integrations → MySQL), or in config.yaml:
integrations:
  mysql:
    username: openlog
    password: env:OPENLOG_MYSQL_PASSWORD   # or file:/etc/openlog-infra-agent/mysql.password
    instances:
      - match: { endpoint: "` + ep + `" }`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	if ep.Network != "tcp" && ep.Network != "unix" {
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	return &collector{inst: inst, ep: ep, q: sqlQuerier{}}, nil
}

// Querier abstracts the queries so that tests can use recorded result sets.
type Querier interface {
	// Rows runs a query and returns all rows as strings (NULL → nil).
	Rows(ctx context.Context, db *sql.DB, query string) ([]string, [][]*string, error)
}

type sqlQuerier struct{}

func (sqlQuerier) Rows(ctx context.Context, db *sql.DB, query string) ([]string, [][]*string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]*string
	for rows.Next() {
		raw := make([]sql.RawBytes, len(cols))
		dest := make([]any, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
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
	return cols, out, rows.Err()
}

type collector struct {
	inst *integrations.Instance
	ep   integrations.Endpoint
	db   *sql.DB
	q    Querier
}

func (c *collector) Close() {
	if c.db != nil {
		c.db.Close()
		c.db = nil
	}
}

func (c *collector) open(ctx context.Context) error {
	password, err := c.inst.Password()
	if err != nil {
		return integrations.NeedsConfiguration("password: "+err.Error(), false)
	}
	cfg := driver.NewConfig()
	cfg.User, cfg.Passwd = c.inst.Settings.Username, password
	cfg.Net, cfg.Addr = c.ep.Network, c.ep.Address
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = c.inst.Timeout, c.inst.Timeout, c.inst.Timeout
	// Connection attribute (performance_schema.session_connect_attrs), not a session variable.
	cfg.ConnectionAttributes = "program_name:openlog-infra-agent"
	if t := c.inst.Settings.TLS; t != nil && t.Enabled {
		tc := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName}
		if tc.ServerName == "" && c.ep.Network == "tcp" {
			tc.ServerName = c.ep.Host()
		}
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			tc.RootCAs = pool
		}
		cfg.TLS = tc
	}
	// Without TLS, caching_sha2_password full authentication requests the
	// server's RSA public key (the driver's default when ServerPubKey is unset).
	conn, err := driver.NewConnector(cfg)
	if err != nil {
		return err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return c.classify(err)
	}
	c.db = db
	return nil
}

func (c *collector) classify(err error) error {
	var me *driver.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1045, 1698: // access denied
			if c.inst.Settings.Password == "" {
				return integrations.NeedsConfiguration("access denied without a password: configure integrations.mysql.username and password", false)
			}
			return fmt.Errorf("authentication failed: %s", me.Message)
		case 1227, 1142, 1044:
			return fmt.Errorf("permission denied: %s", me.Message)
		}
		return err
	}
	if integrations.IsUnreachable(err) || errors.Is(err, driver.ErrInvalidConn) {
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	return err
}

func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if c.db == nil {
		if err := c.open(ctx); err != nil {
			return err
		}
	}
	return c.collect(ctx, b)
}

func (c *collector) collect(ctx context.Context, b *integrations.Batch) error {
	status, err := c.keyValues(ctx, "SHOW GLOBAL STATUS")
	if err != nil {
		return c.classify(err)
	}
	b.SetResourceAttr(otlputil.Str("mysql.instance.endpoint", c.ep.Display))
	b.SetResourceAttr(otlputil.Str(integrations.AttrServiceInstanceID, ServiceInstanceID(integrations.ServiceInstanceID(c.ep, c.inst.HostName))))
	if up, err := strconv.ParseInt(status["Uptime"], 10, 64); err == nil {
		b.SetStartTime(b.Now().Add(-time.Duration(up) * time.Second))
	}
	s := b.Resource()
	RecordGlobalStatus(s, status)

	var partial []string
	if _, rows, err := c.q.Rows(ctx, c.db, "SELECT @@innodb_buffer_pool_size"); err == nil && len(rows) == 1 && rows[0][0] != nil {
		if v, err := strconv.ParseInt(*rows[0][0], 10, 64); err == nil {
			s.SumInt("mysql.buffer_pool.limit", "By", false, v)
		}
	}
	topN := c.inst.Settings.TopNTables
	if topN <= 0 {
		topN = DefaultTopN
	}
	if _, rows, err := c.q.Rows(ctx, c.db, TableIOWaitsQuery(topN)); err != nil {
		partial = append(partial, "performance_schema table io waits: "+c.classify(err).Error())
	} else {
		RecordIOWaits(s, rows, false)
		if len(rows) == 0 {
			// The summary tables exist but stay empty while performance_schema is
			// OFF (the MariaDB default).
			if _, r, err := c.q.Rows(ctx, c.db, "SELECT @@performance_schema"); err == nil && len(r) == 1 && len(r[0]) == 1 && r[0][0] != nil && *r[0][0] == "0" {
				partial = append(partial, "performance_schema is disabled: set performance_schema=ON in the server configuration and restart (mysql.table.io.wait.*, mysql.index.io.wait.*)")
			}
		}
	}
	if _, rows, err := c.q.Rows(ctx, c.db, IndexIOWaitsQuery(topN)); err != nil {
		partial = append(partial, "performance_schema index io waits: "+c.classify(err).Error())
	} else {
		RecordIOWaits(s, rows, true)
	}
	cols, rows, err := c.q.Rows(ctx, c.db, "SHOW REPLICA STATUS")
	if err != nil {
		var me *driver.MySQLError
		if errors.As(err, &me) && me.Number == 1064 { // syntax: MySQL < 8.0.22 / MariaDB < 10.5.1
			cols, rows, err = c.q.Rows(ctx, c.db, "SHOW SLAVE STATUS")
		}
	}
	if err != nil {
		partial = append(partial, "replica status: "+c.classify(err).Error())
	} else {
		RecordReplicaStatus(s, cols, rows)
	}
	if len(partial) > 0 {
		return integrations.Partial(errors.New(strings.Join(partial, "; ")))
	}
	return nil
}

func (c *collector) keyValues(ctx context.Context, q string) (map[string]string, error) {
	_, rows, err := c.q.Rows(ctx, c.db, q)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if len(r) >= 2 && r[0] != nil && r[1] != nil {
			out[*r[0]] = *r[1]
		}
	}
	return out, nil
}

// ServiceInstanceID derives service.instance.id like mysqlreceiver: a UUIDv5
// (OTel namespace 4d63009a-8d0f-11ee-aad7-4c796ed8e320) of host:port, with
// loopback hosts replaced by the host name.
func ServiceInstanceID(seed string) string {
	ns := [16]byte{0x4d, 0x63, 0x00, 0x9a, 0x8d, 0x0f, 0x11, 0xee, 0xaa, 0xd7, 0x4c, 0x79, 0x6e, 0xd8, 0xe3, 0x20}
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(seed))
	u := h.Sum(nil)[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

type statusDef struct {
	metric, unit string
	monotonic    bool
	attrKey      string
	attrVal      string
}

// statusMetrics maps SHOW GLOBAL STATUS variables to mysqlreceiver metrics.
var statusMetrics = map[string]statusDef{}

func def(metric, unit string, mono bool, key string, vars map[string]string) {
	for v, val := range vars {
		statusMetrics[v] = statusDef{metric, unit, mono, key, val}
	}
}

func init() {
	def("mysql.buffer_pool.pages", "1", false, "kind", map[string]string{
		"Innodb_buffer_pool_pages_data": "data", "Innodb_buffer_pool_pages_free": "free",
		"Innodb_buffer_pool_pages_misc": "misc", "Innodb_buffer_pool_pages_total": "total"})
	def("mysql.buffer_pool.page_flushes", "1", true, "", map[string]string{"Innodb_buffer_pool_pages_flushed": ""})
	def("mysql.buffer_pool.operations", "1", true, "operation", map[string]string{
		"Innodb_buffer_pool_read_ahead_rnd": "read_ahead_rnd", "Innodb_buffer_pool_read_ahead": "read_ahead",
		"Innodb_buffer_pool_read_ahead_evicted": "read_ahead_evicted", "Innodb_buffer_pool_read_requests": "read_requests",
		"Innodb_buffer_pool_reads": "reads", "Innodb_buffer_pool_wait_free": "wait_free", "Innodb_buffer_pool_write_requests": "write_requests"})
	def("mysql.connection.errors", "1", true, "error", map[string]string{
		"Connection_errors_accept": "accept", "Connection_errors_internal": "internal", "Connection_errors_max_connections": "max_connections",
		"Connection_errors_peer_address": "peer_address", "Connection_errors_select": "select", "Connection_errors_tcpwrap": "tcpwrap",
		"Aborted_clients": "aborted_clients", "Aborted_connects": "aborted", "Locked_connects": "locked"})
	def("mysql.connection.count", "1", true, "", map[string]string{"Connections": ""})
	def("mysql.max_used_connections", "1", false, "", map[string]string{"Max_used_connections": ""})
	def("mysql.prepared_statements", "1", true, "command", map[string]string{
		"Com_stmt_execute": "execute", "Com_stmt_close": "close", "Com_stmt_fetch": "fetch", "Com_stmt_prepare": "prepare",
		"Com_stmt_reset": "reset", "Com_stmt_send_long_data": "send_long_data"})
	def("mysql.commands", "1", true, "command", map[string]string{
		"Com_alter_table": "alter_table", "Com_create_index": "create_index", "Com_create_table": "create_table", "Com_delete": "delete",
		"Com_delete_multi": "delete_multi", "Com_insert": "insert", "Com_optimize": "optimize", "Com_select": "select",
		"Com_update": "update", "Com_update_multi": "update_multi"})
	def("mysql.tmp_resources", "1", true, "resource", map[string]string{
		"Created_tmp_disk_tables": "disk_tables", "Created_tmp_files": "files", "Created_tmp_tables": "tables"})
	def("mysql.handlers", "1", true, "kind", map[string]string{
		"Handler_commit": "commit", "Handler_delete": "delete", "Handler_discover": "discover", "Handler_external_lock": "external_lock",
		"Handler_mrr_init": "mrr_init", "Handler_prepare": "prepare", "Handler_read_first": "read_first", "Handler_read_key": "read_key",
		"Handler_read_last": "read_last", "Handler_read_next": "read_next", "Handler_read_prev": "read_prev", "Handler_read_rnd": "read_rnd",
		"Handler_read_rnd_next": "read_rnd_next", "Handler_rollback": "rollback", "Handler_savepoint": "savepoint",
		"Handler_savepoint_rollback": "savepoint_rollback", "Handler_update": "update", "Handler_write": "write"})
	def("mysql.double_writes", "1", true, "kind", map[string]string{"Innodb_dblwr_pages_written": "pages_written", "Innodb_dblwr_writes": "writes"})
	def("mysql.log_operations", "1", true, "operation", map[string]string{
		"Innodb_log_waits": "waits", "Innodb_log_write_requests": "write_requests", "Innodb_log_writes": "writes", "Innodb_os_log_fsyncs": "fsyncs"})
	def("mysql.operations", "1", true, "operation", map[string]string{"Innodb_data_fsyncs": "fsyncs", "Innodb_data_reads": "reads", "Innodb_data_writes": "writes"})
	def("mysql.page_operations", "1", true, "operation", map[string]string{"Innodb_pages_created": "created", "Innodb_pages_read": "read", "Innodb_pages_written": "written"})
	def("mysql.row_locks", "1", true, "kind", map[string]string{"Innodb_row_lock_waits": "waits", "Innodb_row_lock_time": "time"})
	def("mysql.row_operations", "1", true, "operation", map[string]string{
		"Innodb_rows_deleted": "deleted", "Innodb_rows_inserted": "inserted", "Innodb_rows_read": "read", "Innodb_rows_updated": "updated"})
	def("mysql.locks", "1", true, "kind", map[string]string{"Table_locks_immediate": "immediate", "Table_locks_waited": "waited"})
	def("mysql.query.count", "1", true, "", map[string]string{"Queries": ""})
	def("mysql.query.client.count", "1", true, "", map[string]string{"Questions": ""})
	def("mysql.query.slow.count", "1", true, "", map[string]string{"Slow_queries": ""})
	def("mysql.sorts", "1", true, "kind", map[string]string{"Sort_merge_passes": "merge_passes", "Sort_range": "range", "Sort_rows": "rows", "Sort_scan": "scan"})
	def("mysql.threads", "1", false, "kind", map[string]string{"Threads_cached": "cached", "Threads_connected": "connected", "Threads_created": "created", "Threads_running": "running"})
	def("mysql.opened_resources", "1", true, "kind", map[string]string{"Opened_files": "file", "Opened_tables": "table", "Opened_table_definitions": "table_definition"})
	def("mysql.mysqlx_connections", "1", true, "status", map[string]string{
		"Mysqlx_connections_accepted": "accepted", "Mysqlx_connections_closed": "closed", "Mysqlx_connections_rejected": "rejected"})
	def("mysql.uptime", "s", true, "", map[string]string{"Uptime": ""})
}

// RecordGlobalStatus emits metrics derived from SHOW GLOBAL STATUS.
func RecordGlobalStatus(s *integrations.Scope, status map[string]string) {
	for _, k := range integrations.SortedKeys(status) {
		d, ok := statusMetrics[k]
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(status[k], 10, 64)
		if err != nil {
			continue // e.g. Innodb_buffer_pool_pages_misc out of range (bug #59550)
		}
		if d.attrKey == "" {
			s.SumInt(d.metric, d.unit, d.monotonic, v)
		} else {
			s.SumInt(d.metric, d.unit, d.monotonic, v, otlputil.Str(d.attrKey, d.attrVal))
		}
	}
	pair := func(metric, unit, dirtyKey, dataKey string) {
		dirty, err1 := strconv.ParseInt(status[dirtyKey], 10, 64)
		data, err2 := strconv.ParseInt(status[dataKey], 10, 64)
		if err1 != nil {
			return
		}
		s.SumInt(metric, unit, false, dirty, otlputil.Str("status", "dirty"))
		if err2 == nil {
			s.SumInt(metric, unit, false, data-dirty, otlputil.Str("status", "clean"))
		}
	}
	pair("mysql.buffer_pool.data_pages", "1", "Innodb_buffer_pool_pages_dirty", "Innodb_buffer_pool_pages_data")
	pair("mysql.buffer_pool.usage", "By", "Innodb_buffer_pool_bytes_dirty", "Innodb_buffer_pool_bytes_data")
}

const ioWaitsExclude = "OBJECT_SCHEMA NOT IN ('mysql', 'performance_schema', 'information_schema', 'sys')"

// TableIOWaitsQuery returns the top-N tables by total io wait time.
func TableIOWaitsQuery(n int) string {
	return "SELECT OBJECT_SCHEMA, OBJECT_NAME, " +
		"COUNT_DELETE, COUNT_FETCH, COUNT_INSERT, COUNT_UPDATE, " +
		"FLOOR(SUM_TIMER_DELETE/1000), FLOOR(SUM_TIMER_FETCH/1000), FLOOR(SUM_TIMER_INSERT/1000), FLOOR(SUM_TIMER_UPDATE/1000) " +
		"FROM performance_schema.table_io_waits_summary_by_table WHERE " + ioWaitsExclude +
		" ORDER BY SUM_TIMER_WAIT DESC, OBJECT_SCHEMA, OBJECT_NAME LIMIT " + strconv.Itoa(n)
}

// IndexIOWaitsQuery returns the top-N indexes by total io wait time.
func IndexIOWaitsQuery(n int) string {
	return "SELECT OBJECT_SCHEMA, OBJECT_NAME, IFNULL(INDEX_NAME, 'NONE'), " +
		"COUNT_DELETE, COUNT_FETCH, COUNT_INSERT, COUNT_UPDATE, " +
		"FLOOR(SUM_TIMER_DELETE/1000), FLOOR(SUM_TIMER_FETCH/1000), FLOOR(SUM_TIMER_INSERT/1000), FLOOR(SUM_TIMER_UPDATE/1000) " +
		"FROM performance_schema.table_io_waits_summary_by_index_usage WHERE " + ioWaitsExclude +
		" ORDER BY SUM_TIMER_WAIT DESC, OBJECT_SCHEMA, OBJECT_NAME, INDEX_NAME LIMIT " + strconv.Itoa(n)
}

// RecordIOWaits emits mysql.{table,index}.io.wait.{count,time}.
func RecordIOWaits(s *integrations.Scope, rows [][]*string, index bool) {
	prefix, off := "mysql.table.io.wait.", 2
	if index {
		prefix, off = "mysql.index.io.wait.", 3
	}
	ops := []string{"delete", "fetch", "insert", "update"}
	for _, r := range rows {
		if len(r) < off+8 || r[0] == nil || r[1] == nil {
			continue
		}
		attrs := []string{"schema", *r[0], "table", *r[1]}
		if index && r[2] != nil {
			attrs = append(attrs, "index", *r[2])
		}
		for i, op := range ops {
			kv := append([]string{"operation", op}, attrs...)
			if v, ok := intOf(r[off+i]); ok {
				s.SumInt(prefix+"count", "1", true, v, strAttrs(kv)...)
			}
			if v, ok := intOf(r[off+4+i]); ok {
				s.SumInt(prefix+"time", "ns", true, v, strAttrs(kv)...)
			}
		}
	}
}

// RecordReplicaStatus emits replica lag metrics when the server is a replica.
func RecordReplicaStatus(s *integrations.Scope, cols []string, rows [][]*string) {
	for _, r := range rows {
		for i, c := range cols {
			if i >= len(r) {
				break
			}
			switch strings.ToLower(c) {
			case "seconds_behind_source", "seconds_behind_master":
				if v, ok := intOf(r[i]); ok {
					s.SumInt("mysql.replica.time_behind_source", "s", false, v)
				}
			case "sql_delay":
				if v, ok := intOf(r[i]); ok {
					s.SumInt("mysql.replica.sql_delay", "s", false, v)
				}
			}
		}
	}
}

func intOf(p *string) (int64, bool) {
	if p == nil {
		return 0, false
	}
	v, err := strconv.ParseInt(*p, 10, 64)
	return v, err == nil
}

func strAttrs(kv []string) []*commonKV {
	out := make([]*commonKV, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, otlputil.Str(kv[i], kv[i+1]))
	}
	return out
}
