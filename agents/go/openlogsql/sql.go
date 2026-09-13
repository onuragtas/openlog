// Package openlogsql instruments database/sql: every Exec/Query (direct, prepared or in a
// transaction) becomes a client span with db.system.name, db.namespace, db.operation.name,
// db.query.text (sanitized by default) and server.address/server.port.
//
//	db, err := openlogsql.Open("postgres", dsn)
//
// Spans are only created inside an existing span (a request, a job) unless WithRootSpans is
// used, so connection-pool housekeeping and background polling do not create stray traces.
package openlogsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of database spans.
const ScopeName = "github.com/onuragtas/openlog/agents/go/openlogsql"

// QueryTextMode controls db.query.text.
type QueryTextMode int

const (
	// QuerySanitized replaces literals with ? and collapses IN lists (default).
	QuerySanitized QueryTextMode = iota
	// QueryRaw records the statement as sent (parameterized statements carry no values).
	QueryRaw
	// QueryOff omits db.query.text.
	QueryOff
)

// MaxQueryTextBytes bounds db.query.text.
const MaxQueryTextBytes = 4096

type config struct {
	system     string
	namespace  string
	address    string
	port       int
	queryText  QueryTextMode
	rootSpans  bool
	tp         trace.TracerProvider
	tracerOnce sync.Once
	tracer     trace.Tracer
	baseAttrs  []attribute.KeyValue
}

// Option configures the instrumentation.
type Option func(*config)

// WithDBSystem sets db.system.name (default: derived from the driver name).
func WithDBSystem(system string) Option { return func(c *config) { c.system = system } }

// WithDBNamespace sets db.namespace (default: database name parsed from the DSN).
func WithDBNamespace(ns string) Option { return func(c *config) { c.namespace = ns } }

// WithServerAddress sets server.address and server.port (default: parsed from the DSN).
func WithServerAddress(host string, port int) Option {
	return func(c *config) { c.address, c.port = host, port }
}

// WithQueryText selects how db.query.text is recorded. The default is QuerySanitized, or
// OPENLOG_DB_QUERY_TEXT=sanitized|raw|off.
func WithQueryText(m QueryTextMode) Option { return func(c *config) { c.queryText = m } }

// WithRootSpans creates spans for statements executed without a parent span.
func WithRootSpans() Option { return func(c *config) { c.rootSpans = true } }

// WithTracerProvider uses tp instead of the global provider.
func WithTracerProvider(tp trace.TracerProvider) Option { return func(c *config) { c.tp = tp } }

func newConfig(driverName, dsn string, opts []Option) *config {
	c := &config{queryText: queryTextFromEnv()}
	c.system = systemFromDriver(driverName)
	info := parseDSN(driverName, dsn)
	c.namespace, c.address, c.port = info.database, info.host, info.port
	for _, o := range opts {
		o(c)
	}
	c.baseAttrs = []attribute.KeyValue{attribute.String("db.system.name", c.system), attribute.String("db.system", c.system)}
	if c.namespace != "" {
		c.baseAttrs = append(c.baseAttrs, attribute.String("db.namespace", c.namespace))
	}
	if c.address != "" {
		c.baseAttrs = append(c.baseAttrs, attribute.String("server.address", c.address))
		if c.port > 0 {
			c.baseAttrs = append(c.baseAttrs, attribute.Int("server.port", c.port))
		}
	}
	return c
}

func queryTextFromEnv() QueryTextMode {
	switch strings.ToLower(os.Getenv("OPENLOG_DB_QUERY_TEXT")) {
	case "raw":
		return QueryRaw
	case "off", "none", "false":
		return QueryOff
	}
	return QuerySanitized
}

func (c *config) getTracer() trace.Tracer {
	c.tracerOnce.Do(func() {
		tp := c.tp
		if tp == nil {
			tp = otel.GetTracerProvider()
		}
		c.tracer = tp.Tracer(ScopeName)
	})
	return c.tracer
}

// Open is sql.Open with instrumentation. The driver must already be registered under
// driverName (import it as usual).
func Open(driverName, dsn string, opts ...Option) (*sql.DB, error) {
	name, err := Register(driverName, opts...)
	if err != nil {
		return nil, err
	}
	// The DSN is parsed per registration name; keep attributes DSN-specific.
	db, err := sql.Open(name, dsn)
	if err != nil {
		return nil, err
	}
	return db, nil
}

var (
	regMu      sync.Mutex
	registered = map[string]bool{}
)

// Register registers an instrumented copy of driverName as "<driverName>-openlog" (the
// name is returned) for code that calls sql.Open itself. Calling it again is a no-op.
func Register(driverName string, opts ...Option) (string, error) {
	name := driverName + "-openlog"
	regMu.Lock()
	defer regMu.Unlock()
	if registered[name] {
		return name, nil
	}
	db, err := sql.Open(driverName, "")
	if err != nil {
		return "", fmt.Errorf("openlogsql: %w", err)
	}
	d := db.Driver()
	_ = db.Close()
	sql.Register(name, &wrappedDriver{parent: d, driverName: driverName, opts: opts})
	registered[name] = true
	return name, nil
}

// OpenDB is sql.OpenDB with instrumentation for a driver.Connector (e.g. pgx stdlib, mysql).
func OpenDB(c driver.Connector, driverName string, opts ...Option) *sql.DB {
	cfg := newConfig(driverName, "", opts)
	return sql.OpenDB(&wrappedConnector{parent: c, cfg: cfg, driver: &wrappedDriver{parent: c.Driver(), driverName: driverName, opts: opts}})
}

// WrapDriver returns an instrumented driver; dsn attributes are parsed from the name given
// to Open.
func WrapDriver(d driver.Driver, driverName string, opts ...Option) driver.Driver {
	return &wrappedDriver{parent: d, driverName: driverName, opts: opts}
}

func systemFromDriver(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "postgres"), strings.HasPrefix(n, "pgx"), n == "pq":
		return "postgresql"
	case strings.Contains(n, "mysql"), strings.Contains(n, "mariadb"):
		return "mysql"
	case strings.Contains(n, "sqlite"):
		return "sqlite"
	case strings.Contains(n, "sqlserver"), strings.Contains(n, "mssql"):
		return "microsoft.sql_server"
	case strings.Contains(n, "clickhouse"):
		return "clickhouse"
	case strings.Contains(n, "oracle"), n == "godror", n == "oci8":
		return "oracle.db"
	}
	return "other_sql"
}

type dsnInfo struct {
	host     string
	port     int
	database string
}

// parseDSN understands URL DSNs (postgres://, mysql://, sqlserver://, clickhouse://),
// go-sql-driver/mysql DSNs (user:pass@tcp(host:port)/db) and libpq key=value DSNs.
// Credentials are never recorded.
func parseDSN(driverName, dsn string) dsnInfo {
	var info dsnInfo
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || strings.Contains(strings.ToLower(driverName), "sqlite") {
		return info
	}
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return info
		}
		info.host = u.Hostname()
		info.port, _ = strconv.Atoi(u.Port())
		info.database = strings.TrimPrefix(u.Path, "/")
		if db := u.Query().Get("database"); db != "" {
			info.database = db
		}
		return info
	}
	if at := strings.LastIndex(dsn, "@"); at >= 0 && strings.Contains(dsn[at:], "(") {
		rest := dsn[at+1:]
		if open, cl := strings.IndexByte(rest, '('), strings.IndexByte(rest, ')'); open >= 0 && cl > open {
			addr := rest[open+1 : cl]
			if h, p, err := net.SplitHostPort(addr); err == nil {
				info.host = h
				info.port, _ = strconv.Atoi(p)
			} else if !strings.HasPrefix(addr, "/") {
				info.host = addr
			}
			db := rest[cl+1:]
			db = strings.TrimPrefix(db, "/")
			if q := strings.IndexByte(db, '?'); q >= 0 {
				db = db[:q]
			}
			info.database = db
		}
		return info
	}
	if strings.Contains(dsn, "=") {
		for _, f := range strings.Fields(dsn) {
			k, v, _ := strings.Cut(f, "=")
			v = strings.Trim(v, "'")
			switch k {
			case "host":
				if !strings.HasPrefix(v, "/") {
					info.host = strings.Split(v, ",")[0]
				}
			case "port":
				info.port, _ = strconv.Atoi(strings.Split(v, ",")[0])
			case "dbname":
				info.database = v
			}
		}
	}
	return info
}

// ---- span helpers ----

func (c *config) start(ctx context.Context, query string) (context.Context, trace.Span, bool) {
	if !c.rootSpans && !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, nil, false
	}
	op := operationName(query)
	name := op
	if name == "" {
		name = c.system
	} else if c.namespace != "" {
		name = op + " " + c.namespace
	}
	attrs := make([]attribute.KeyValue, 0, len(c.baseAttrs)+2)
	attrs = append(attrs, c.baseAttrs...)
	if op != "" {
		attrs = append(attrs, attribute.String("db.operation.name", op))
	}
	switch c.queryText {
	case QuerySanitized:
		attrs = append(attrs, attribute.String("db.query.text", truncate(Sanitize(query, c.system))))
	case QueryRaw:
		attrs = append(attrs, attribute.String("db.query.text", truncate(query)))
	}
	ctx, span := c.getTracer().Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	return ctx, span, true
}

func end(span trace.Span, err error) {
	if err != nil && !errors.Is(err, driver.ErrSkip) && !errors.Is(err, sql.ErrNoRows) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
	}
	span.End()
}

func truncate(s string) string {
	if len(s) <= MaxQueryTextBytes {
		return s
	}
	cut := MaxQueryTextBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut]
}

// operationName returns the upper-cased first keyword (SELECT, INSERT, …), skipping comments.
func operationName(q string) string {
	q = strings.TrimSpace(stripLeadingComments(q))
	end := strings.IndexFunc(q, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') })
	if end < 0 {
		end = len(q)
	}
	if end == 0 || end > 16 {
		return ""
	}
	return strings.ToUpper(q[:end])
}

func stripLeadingComments(q string) string {
	for {
		q = strings.TrimSpace(q)
		switch {
		case strings.HasPrefix(q, "--"):
			i := strings.IndexByte(q, '\n')
			if i < 0 {
				return ""
			}
			q = q[i+1:]
		case strings.HasPrefix(q, "/*"):
			i := strings.Index(q, "*/")
			if i < 0 {
				return ""
			}
			q = q[i+2:]
		default:
			return q
		}
	}
}
