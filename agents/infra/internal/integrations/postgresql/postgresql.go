// Package postgresql implements the PostgreSQL integration (pgx, MIT) emitting
// the metrics of the OpenTelemetry Collector postgresqlreceiver in its default
// (legacy resource attribute) mode (semantic-conventions §6.6).
package postgresql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// Cardinality guards.
const (
	DefaultTopN  = 20 // tables and indexes per database
	MaxDatabases = 32
	MaxLockRows  = 100 // postgresql.database.locks rows per database
)

// Integration is the PostgreSQL integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationPostgreSQL }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{
		DefaultPort: 5432,
		UnixSockets: []string{"/var/run/postgresql/.s.PGSQL.5432", "/run/postgresql/.s.PGSQL.5432", "/tmp/.s.PGSQL.5432"},
	}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	ep := "127.0.0.1:5432"
	if len(inst.Endpoints) > 0 {
		ep = inst.Endpoints[0].Display
	}
	return `# PostgreSQL needs a read-only monitoring role; the agent never creates roles itself. Run as a
# superuser (for a container: docker exec -it <container> psql -U postgres):
#   CREATE ROLE openlog WITH LOGIN PASSWORD '<password>';
#   GRANT pg_monitor TO openlog;
# pg_hba.conf must allow the role from the agent's address (127.0.0.1, or the Docker network for
# a container). Then enter the user and password in openlog (host → Integrations → PostgreSQL),
# or in config.yaml:
integrations:
  postgresql:
    username: openlog
    password: env:OPENLOG_POSTGRESQL_PASSWORD   # or file:/etc/openlog-infra-agent/postgresql.password
    instances:
      - match: { endpoint: "` + ep + `" }`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	if ep.Network != "tcp" && ep.Network != "unix" {
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	return &collector{inst: inst, ep: ep, conns: map[string]Conn{}}, nil
}

// Conn is the query interface used by the collector (a *pgx.Conn or a test fake).
type Conn interface {
	Query(ctx context.Context, sql string) ([][]any, error)
	Close(ctx context.Context) error
}

type pgxConn struct{ c *pgx.Conn }

func (p pgxConn) Query(ctx context.Context, sql string) ([][]any, error) {
	rows, err := p.c.Query(ctx, sql, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p pgxConn) Close(ctx context.Context) error { return p.c.Close(ctx) }

type collector struct {
	inst  *integrations.Instance
	ep    integrations.Endpoint
	conns map[string]Conn
	// Connect opens a connection to a database (replaceable in tests).
	connect func(ctx context.Context, database string) (Conn, error)
}

func (c *collector) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for k, cn := range c.conns {
		_ = cn.Close(ctx)
		delete(c.conns, k)
	}
}

func (c *collector) mainDB() string {
	if d := c.inst.Settings.Database; d != "" {
		return d
	}
	return "postgres"
}

func (c *collector) dial(ctx context.Context, database string) (Conn, error) {
	if c.connect != nil {
		return c.connect(ctx, database)
	}
	password, err := c.inst.Password()
	if err != nil {
		return nil, integrations.NeedsConfiguration("password: "+err.Error(), false)
	}
	cfg, err := pgx.ParseConfig("sslmode=prefer connect_timeout=" + strconv.Itoa(max(1, int(c.inst.Timeout.Seconds()))))
	if err != nil {
		return nil, err
	}
	switch c.ep.Network {
	case "unix": // .../.s.PGSQL.5432 → host = directory, port from the name
		cfg.Host = path.Dir(c.ep.Address)
		if p, err := strconv.Atoi(strings.TrimPrefix(path.Base(c.ep.Address), ".s.PGSQL.")); err == nil {
			cfg.Port = uint16(p)
		}
		cfg.TLSConfig, cfg.Fallbacks = nil, nil
	default:
		cfg.Host = c.ep.Host()
		cfg.Port = uint16(c.ep.Port())
		for _, fb := range cfg.Fallbacks {
			fb.Host, fb.Port = cfg.Host, cfg.Port
		}
	}
	if t := c.inst.Settings.TLS; t != nil && t.Enabled && c.ep.Network == "tcp" {
		tc := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName}
		if tc.ServerName == "" {
			tc.ServerName = cfg.Host
		}
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return nil, integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			tc.RootCAs = pool
		}
		cfg.TLSConfig, cfg.Fallbacks = tc, nil
	}
	cfg.User, cfg.Password, cfg.Database = c.inst.Settings.Username, password, database
	cfg.RuntimeParams["application_name"] = "openlog-infra-agent"
	cfg.ConnectTimeout = c.inst.Timeout
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, c.classify(err, database)
	}
	return pgxConn{conn}, nil
}

func (c *collector) classify(err error, database string) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "28P01", "28000": // invalid_password, invalid_authorization_specification (pg_hba)
			if c.inst.Settings.Password == "" {
				return integrations.NeedsConfiguration("authentication required: configure integrations.postgresql.username and password ("+pe.Message+")", false)
			}
			return fmt.Errorf("authentication failed: %s (SQLSTATE %s)", pe.Message, pe.Code)
		case "3D000":
			return integrations.NeedsConfiguration(fmt.Sprintf("database %q does not exist: set integrations.postgresql.database", database), false)
		case "42501":
			return fmt.Errorf("permission denied: %s (grant pg_monitor)", pe.Message)
		}
		return fmt.Errorf("%s (SQLSTATE %s)", pe.Message, pe.Code)
	}
	if integrations.IsUnreachable(err) {
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	return err
}

func (c *collector) conn(ctx context.Context, database string) (Conn, error) {
	if cn, ok := c.conns[database]; ok {
		return cn, nil
	}
	cn, err := c.dial(ctx, database)
	if err != nil {
		return nil, err
	}
	c.conns[database] = cn
	return cn, nil
}

func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	main, err := c.conn(ctx, c.mainDB())
	if err != nil {
		return err
	}
	var partial []string
	fail := func(what string, err error) {
		partial = append(partial, what+": "+c.classify(err, "").Error())
	}
	q := func(cn Conn, sql string) [][]any {
		rows, err := cn.Query(ctx, sql)
		if err != nil {
			fail(firstWords(sql), err)
			return nil
		}
		return rows
	}

	verRows, err := main.Query(ctx, "SHOW server_version_num")
	if err != nil {
		c.Close() // a broken connection is re-opened next time
		return c.classify(err, c.mainDB())
	}
	version := 0
	if len(verRows) == 1 {
		version, _ = strconv.Atoi(str(verRows[0][0]))
	}

	dbs := c.databases(q(main, "SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn ORDER BY datname"))
	inst := b.Resource()
	inst.SumInt("postgresql.database.count", "{databases}", false, int64(len(dbs)))
	for _, r := range q(main, "SHOW max_connections") {
		inst.GaugeInt("postgresql.connection.max", "{connections}", i64(r[0]))
	}
	RecordBGWriter(inst, q(main, BGWriterQuery(version)), version)
	RecordReplication(inst, q(main, ReplicationQuery))
	for _, r := range q(main, WalAgeQuery) {
		if r[0] != nil {
			inst.GaugeInt("postgresql.wal.age", "s", i64(r[0]))
		}
	}

	want := map[string]bool{}
	for _, d := range dbs {
		want[d] = true
	}
	perDB := map[string]*integrations.Scope{}
	scope := func(db string) *integrations.Scope {
		if s, ok := perDB[db]; ok {
			return s
		}
		s := b.Resource(otlputil.Str("postgresql.database.name", db))
		perDB[db] = s
		return s
	}
	for _, r := range q(main, "SELECT datname, count(*) FROM pg_stat_activity WHERE datname IS NOT NULL GROUP BY datname") {
		if db := str(r[0]); want[db] {
			scope(db).SumInt("postgresql.backends", "1", false, i64(r[1]))
		}
	}
	for _, r := range q(main, "SELECT datname, xact_commit, xact_rollback, deadlocks FROM pg_stat_database WHERE datname IS NOT NULL") {
		if db := str(r[0]); want[db] {
			s := scope(db)
			s.SumInt("postgresql.commits", "1", true, i64(r[1]))
			s.SumInt("postgresql.rollbacks", "1", true, i64(r[2]))
			s.SumInt("postgresql.deadlocks", "{deadlock}", true, i64(r[3]))
		}
	}
	for _, r := range q(main, "SELECT datname, pg_database_size(datname) FROM pg_database WHERE datistemplate = false AND has_database_privilege(datname, 'CONNECT')") {
		if db := str(r[0]); want[db] {
			scope(db).SumInt("postgresql.db_size", "By", false, i64(r[1]))
		}
	}

	topN := c.inst.Settings.TopNTables
	if topN <= 0 {
		topN = DefaultTopN
	}
	for _, db := range dbs {
		cn, err := c.conn(ctx, db)
		if err != nil {
			fail("database "+db, err)
			continue
		}
		s := scope(db)
		for _, r := range q(cn, TableCountQuery) {
			s.SumInt("postgresql.table.count", "{table}", false, i64(r[0]))
		}
		RecordLocks(s, q(cn, LocksQuery))
		RecordTables(b, db, q(cn, TablesQuery(topN)), q(cn, TableIOQuery(topN)))
		RecordIndexes(b, db, q(cn, IndexesQuery(topN)))
		if db != c.mainDB() {
			// Per-database connections are not kept between collections.
			_ = cn.Close(ctx)
			delete(c.conns, db)
		}
	}
	if len(partial) > 0 {
		return integrations.Partial(errors.New(strings.Join(partial, "; ")))
	}
	return nil
}

func (c *collector) databases(rows [][]any) []string {
	s := c.inst.Settings
	include := map[string]bool{}
	for _, d := range s.Databases {
		include[d] = true
	}
	exclude := map[string]bool{}
	for _, d := range s.ExcludeDatabases {
		exclude[d] = true
	}
	var out []string
	for _, r := range rows {
		d := str(r[0])
		if d == "" || exclude[d] || (len(include) > 0 && !include[d]) {
			continue
		}
		if len(out) >= MaxDatabases {
			break
		}
		out = append(out, d)
	}
	return out
}

func firstWords(sql string) string {
	f := strings.Fields(sql)
	for i, w := range f {
		if strings.EqualFold(w, "FROM") && i+1 < len(f) {
			return strings.Trim(f[i+1], ";")
		}
	}
	if len(f) > 2 {
		f = f[:2]
	}
	return strings.Join(f, " ")
}

// Queries (the same sources as postgresqlreceiver; top-N ordering added as a cardinality guard).
const (
	TableCountQuery  = `SELECT count(*) FROM pg_class WHERE relkind IN ('r', 'm', 'p') AND relnamespace NOT IN (SELECT oid FROM pg_namespace WHERE nspname = 'pg_catalog' OR nspname = 'information_schema' OR nspname ~ '^pg_toast')`
	LocksQuery       = `SELECT COALESCE(relname, '') AS relation, mode, locktype, count(*) AS locks FROM pg_locks LEFT JOIN pg_class ON pg_locks.relation = pg_class.oid WHERE pg_locks.database = (SELECT oid FROM pg_database WHERE datname = current_database()) GROUP BY relname, mode, locktype ORDER BY locks DESC LIMIT ` + "100"
	ReplicationQuery = `SELECT coalesce(cast(client_addr as varchar), 'unix'), coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), -1)::bigint, extract('epoch' from coalesce(write_lag, '-1 seconds'))::integer, extract('epoch' from coalesce(flush_lag, '-1 seconds'))::integer, extract('epoch' from coalesce(replay_lag, '-1 seconds'))::integer FROM pg_stat_replication`
	WalAgeQuery      = `SELECT extract('epoch' from (CURRENT_TIMESTAMP - last_archived_time))::bigint FROM pg_stat_archiver WHERE last_archived_time IS NOT NULL`
	exclusiveLocks   = `SELECT DISTINCT relation FROM pg_locks WHERE locktype = 'relation' AND mode = 'AccessExclusiveLock' AND granted = true`
)

// TablesQuery returns the top-N tables by size (tables holding an exclusive lock are skipped).
func TablesQuery(n int) string {
	return `SELECT s.schemaname, s.relname, s.n_live_tup, s.n_dead_tup, s.n_tup_ins, s.n_tup_upd, s.n_tup_del, s.n_tup_hot_upd, pg_relation_size(s.relid), s.vacuum_count
FROM pg_stat_user_tables s LEFT JOIN (` + exclusiveLocks + `) l ON s.relid = l.relation
WHERE l.relation IS NULL ORDER BY pg_relation_size(s.relid) DESC, s.schemaname, s.relname LIMIT ` + strconv.Itoa(n)
}

// TableIOQuery returns block statistics of the same top-N tables.
func TableIOQuery(n int) string {
	return `SELECT io.schemaname, io.relname, coalesce(heap_blks_read, 0), coalesce(heap_blks_hit, 0), coalesce(idx_blks_read, 0), coalesce(idx_blks_hit, 0), coalesce(toast_blks_read, 0), coalesce(toast_blks_hit, 0), coalesce(tidx_blks_read, 0), coalesce(tidx_blks_hit, 0)
FROM pg_statio_user_tables io LEFT JOIN (` + exclusiveLocks + `) l ON io.relid = l.relation
WHERE l.relation IS NULL ORDER BY pg_relation_size(io.relid) DESC, io.schemaname, io.relname LIMIT ` + strconv.Itoa(n)
}

// IndexesQuery returns the top-N indexes by size.
func IndexesQuery(n int) string {
	return `SELECT s.schemaname, s.relname, s.indexrelname, pg_relation_size(s.indexrelid), s.idx_scan
FROM pg_stat_user_indexes s LEFT JOIN (` + exclusiveLocks + `) l ON s.indexrelid = l.relation
WHERE l.relation IS NULL ORDER BY pg_relation_size(s.indexrelid) DESC, s.schemaname, s.indexrelname LIMIT ` + strconv.Itoa(n)
}

// BGWriterQuery selects checkpoint/bgwriter statistics (pg_stat_checkpointer from 17).
func BGWriterQuery(version int) string {
	if version >= 170000 {
		return `SELECT cp.num_requested, cp.num_timed, cp.write_time, cp.sync_time, cp.buffers_written, bg.buffers_clean, bg.buffers_alloc, bg.maxwritten_clean, -1::bigint, -1::bigint FROM pg_stat_bgwriter bg, pg_stat_checkpointer cp`
	}
	return `SELECT checkpoints_req, checkpoints_timed, checkpoint_write_time, checkpoint_sync_time, buffers_checkpoint, buffers_clean, buffers_alloc, maxwritten_clean, buffers_backend, buffers_backend_fsync FROM pg_stat_bgwriter`
}

// RecordBGWriter emits postgresql.bgwriter.*.
func RecordBGWriter(s *integrations.Scope, rows [][]any, version int) {
	if len(rows) != 1 || len(rows[0]) < 10 {
		return
	}
	r := rows[0]
	t := func(v string) *commonKV { return otlputil.Str("type", v) }
	src := func(v string) *commonKV { return otlputil.Str("source", v) }
	s.SumInt("postgresql.bgwriter.checkpoint.count", "{checkpoints}", true, i64(r[0]), t("requested"))
	s.SumInt("postgresql.bgwriter.checkpoint.count", "{checkpoints}", true, i64(r[1]), t("scheduled"))
	s.SumDouble("postgresql.bgwriter.duration", "ms", true, f64(r[2]), t("write"))
	s.SumDouble("postgresql.bgwriter.duration", "ms", true, f64(r[3]), t("sync"))
	s.SumInt("postgresql.bgwriter.buffers.writes", "{buffers}", true, i64(r[4]), src("checkpoints"))
	s.SumInt("postgresql.bgwriter.buffers.writes", "{buffers}", true, i64(r[5]), src("bgwriter"))
	if v := i64(r[8]); v >= 0 {
		s.SumInt("postgresql.bgwriter.buffers.writes", "{buffers}", true, v, src("backend"))
	}
	if v := i64(r[9]); v >= 0 {
		s.SumInt("postgresql.bgwriter.buffers.writes", "{buffers}", true, v, src("backend_fsync"))
	}
	s.SumInt("postgresql.bgwriter.buffers.allocated", "{buffers}", true, i64(r[6]))
	s.SumInt("postgresql.bgwriter.maxwritten", "1", true, i64(r[7]))
}

// RecordReplication emits postgresql.replication.data_delay and postgresql.wal.lag.
func RecordReplication(s *integrations.Scope, rows [][]any) {
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		client := otlputil.Str("replication_client", str(r[0]))
		if v := i64(r[1]); v >= 0 {
			s.GaugeInt("postgresql.replication.data_delay", "By", v, client)
		}
		for i, op := range []string{"write", "flush", "replay"} {
			if v := i64(r[2+i]); v >= 0 {
				s.GaugeInt("postgresql.wal.lag", "s", v, otlputil.Str("operation", op), client)
			}
		}
	}
}

// RecordLocks emits postgresql.database.locks.
func RecordLocks(s *integrations.Scope, rows [][]any) {
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		s.GaugeInt("postgresql.database.locks", "{lock}", i64(r[3]),
			otlputil.Str("relation", str(r[0])), otlputil.Str("mode", str(r[1])), otlputil.Str("lock_type", str(r[2])))
	}
}

// RecordTables emits per-table metrics into resources with postgresql.table.name = "<schema>.<table>".
func RecordTables(b *integrations.Batch, db string, tables, io [][]any) {
	for _, r := range tables {
		if len(r) < 10 {
			continue
		}
		s := b.Resource(otlputil.Str("postgresql.database.name", db), otlputil.Str("postgresql.table.name", str(r[0])+"."+str(r[1])))
		s.SumInt("postgresql.rows", "1", false, i64(r[2]), otlputil.Str("state", "live"))
		s.SumInt("postgresql.rows", "1", false, i64(r[3]), otlputil.Str("state", "dead"))
		for i, op := range []string{"ins", "upd", "del", "hot_upd"} {
			s.SumInt("postgresql.operations", "1", true, i64(r[4+i]), otlputil.Str("operation", op))
		}
		s.SumInt("postgresql.table.size", "By", false, i64(r[8]))
		s.SumInt("postgresql.table.vacuum.count", "{vacuum}", true, i64(r[9]))
	}
	for _, r := range io {
		if len(r) < 10 {
			continue
		}
		s := b.Resource(otlputil.Str("postgresql.database.name", db), otlputil.Str("postgresql.table.name", str(r[0])+"."+str(r[1])))
		for i, src := range []string{"heap_read", "heap_hit", "idx_read", "idx_hit", "toast_read", "toast_hit", "tidx_read", "tidx_hit"} {
			s.SumInt("postgresql.blocks_read", "1", true, i64(r[2+i]), otlputil.Str("source", src))
		}
	}
}

// RecordIndexes emits per-index metrics. Like postgresqlreceiver (without the
// separateSchemaAttr gate), postgresql.table.name carries the table without schema here.
func RecordIndexes(b *integrations.Batch, db string, rows [][]any) {
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		s := b.Resource(otlputil.Str("postgresql.database.name", db), otlputil.Str("postgresql.table.name", str(r[1])),
			otlputil.Str("postgresql.index.name", str(r[2])))
		s.GaugeInt("postgresql.index.size", "By", i64(r[3]))
		s.SumInt("postgresql.index.scans", "{scans}", true, i64(r[4]))
	}
}
