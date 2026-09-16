// Package query is the API's only path to ClickHouse. It is a security
// boundary: every query is built from a tenant-bound Scope and always carries
// "tenant_id = {tenant_id:String}" as a server-side bound parameter on every
// table it reads. Handlers never see the connection, cannot name tables
// except through the predefined Table values, and cannot reference tenant_id,
// other tables or sub-selects in their SQL fragments.
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/config"
)

// Table is a readable table. Its field is unexported so handlers can only use
// the values declared below.
type Table struct{ name string }

// Readable tables (Distributed tables).
var (
	Hosts              = Table{"hosts"}
	Metrics            = Table{"metrics"}
	Metrics1m          = Table{"metrics_1m"}
	Logs               = Table{"logs"}
	Spans              = Table{"spans"}
	TraceIndex         = Table{"trace_index"}
	InventoryItems     = Table{"inventory_items"}
	InventorySnapshots = Table{"inventory_snapshots"}

	// APM aggregates (docs/contracts/apm.md §8). Always re-aggregate with GROUP BY;
	// ApmServiceLinks1m must be read with Final().
	ApmTransactions1m = Table{"apm_transactions_1m"}
	ApmServiceEdges1m = Table{"apm_service_edges_1m"}
	ApmServiceLinks1m = Table{"apm_service_links_1m"}
	ApmDBQueries1m    = Table{"apm_db_queries_1m"}
	ApmErrors1m       = Table{"apm_errors_1m"}
	ApmErrorGroups    = Table{"apm_error_groups"}
	ApmServices       = Table{"apm_services"}
	ApmServiceHosts   = Table{"apm_service_hosts"}

	// APM GA (schema 0035_apm_ga, apm.md §3.3, §12): error group occurrences per dimension and spans per
	// service.version per minute; aggregating tables, always re-aggregate with GROUP BY.
	ApmErrorGroupDims    = Table{"apm_error_group_dims"}
	ApmServiceVersions1m = Table{"apm_service_versions_1m"}

	// Language agent identity per service per hour (schema 0090_apm_agent_versions, D-124); aggregating table,
	// always re-aggregate with GROUP BY.
	ApmAgentVersions1h = Table{"apm_agent_versions_1h"}

	// Containers and service <-> container links (semantic-conventions §2, apm.md §1); aggregating
	// tables, always re-aggregate with GROUP BY.
	Containers           = Table{"containers"}
	ApmServiceContainers = Table{"apm_service_containers"}

	// Kubernetes entities (schema 0040–0042, semantic-conventions §7.6); aggregating tables, always
	// re-aggregate with GROUP BY.
	K8sClusters  = Table{"k8s_clusters"}
	K8sNodes     = Table{"k8s_nodes"}
	K8sWorkloads = Table{"k8s_workloads"}
	K8sPods      = Table{"k8s_pods"}

	// AlertEvaluations holds alert evaluation summaries (docs/contracts/alerting.md §3.6).
	AlertEvaluations = Table{"alert_evaluations"}

	// AttributeKeys is the hourly attribute key index of the query builders (schema 0080_attribute_keys, D-118);
	// aggregating table, always re-aggregate with GROUP BY.
	AttributeKeys = Table{"attribute_keys"}

	// LogPatterns1h is the hourly log pattern rollup (schema 0091_log_patterns, D-128); aggregating table,
	// always re-aggregate with GROUP BY.
	LogPatterns1h = Table{"log_patterns_1h"}
)

const tenantParam = "tenant_id"

// ErrInvalid reports a query rejected by the builder.
var ErrInvalid = errors.New("invalid query")

// DB wraps a ClickHouse connection.
type DB struct {
	conn        driver.Conn
	database    string
	timeoutSecs int
	component   string
	limits      func(tenant string) config.QueryLimits
}

// New creates a DB. queryTimeout sets max_execution_time for every query.
func New(conn driver.Conn, database string, queryTimeout time.Duration) *DB {
	secs := int(queryTimeout / time.Second)
	if secs < 1 {
		secs = 1
	}
	return &DB{conn: conn, database: database, timeoutSecs: secs}
}

// SetLimits applies per-tenant limits (OPENLOG_QUERY_*, D-047) to every query and names the component
// ("api", "alert") in log_comment. Call before serving.
func (db *DB) SetLimits(component string, q config.Query) {
	db.component = component
	db.limits = q.Limits
}

// SetLimitsFunc is SetLimits with a custom per-tenant lookup (plan query limits, internal/app usage.go).
func (db *DB) SetLimitsFunc(component string, limits func(tenant string) config.QueryLimits) {
	db.component = component
	db.limits = limits
}

// Settings returns the ClickHouse settings of every query of tenant: max_execution_time, the tenant's
// limits (only those > 0) and log_comment {"component","tenant_id"} for system.query_log.
func (db *DB) Settings(tenant string) ch.Settings {
	s := ch.Settings{"max_execution_time": db.timeoutSecs}
	if db.limits != nil {
		l := db.limits(tenant)
		for name, v := range map[string]int64{"max_memory_usage": l.MaxMemoryUsage, "max_rows_to_read": l.MaxRowsToRead, "max_bytes_to_read": l.MaxBytesToRead} {
			if v > 0 {
				s[name] = v
			}
		}
	}
	comment, _ := json.Marshal(map[string]string{"component": db.component, "tenant_id": tenant})
	s["log_comment"] = string(comment)
	return s
}

// LimitError reports a query stopped by a ClickHouse limit or quota.
type LimitError struct {
	// Limit names what was exceeded: max_memory_usage, max_rows_to_read, max_bytes_to_read, quota,
	// max_concurrent_queries or server_memory.
	Limit string
	// Retryable is true for limits that depend on load (quota, concurrency, server memory), false when
	// the query itself is too expensive.
	Retryable bool
	Err       error
}

func (e *LimitError) Error() string {
	return "query limit exceeded (" + e.Limit + "): " + e.Err.Error()
}
func (e *LimitError) Unwrap() error { return e.Err }

// limitCodes maps ClickHouse error codes to limits.
var limitCodes = map[int32]LimitError{
	158: {Limit: "max_rows_to_read"},                        // TOO_MANY_ROWS
	201: {Limit: "quota", Retryable: true},                  // QUOTA_EXCEEDED
	202: {Limit: "max_concurrent_queries", Retryable: true}, // TOO_MANY_SIMULTANEOUS_QUERIES
	241: {Limit: "max_memory_usage"},                        // MEMORY_LIMIT_EXCEEDED
	307: {Limit: "max_bytes_to_read"},                       // TOO_MANY_BYTES
	396: {Limit: "max_rows_to_read"},                        // TOO_MANY_ROWS_OR_BYTES
}

// AsLimitError reports whether err is (or wraps) a ClickHouse limit error.
func AsLimitError(err error) (*LimitError, bool) {
	var le *LimitError
	if errors.As(err, &le) {
		return le, true
	}
	var ex *ch.Exception
	if !errors.As(err, &ex) {
		return nil, false
	}
	l, ok := limitCodes[ex.Code]
	if !ok {
		return nil, false
	}
	if ex.Code == 241 && strings.Contains(ex.Message, "(total)") {
		// The server as a whole is out of memory, not this query.
		l = LimitError{Limit: "server_memory", Retryable: true}
	}
	l.Err = err
	return &l, true
}

func classify(err error) error {
	if le, ok := AsLimitError(err); ok {
		return le
	}
	if se, ok := AsStorageError(err); ok { // storage.go
		return se
	}
	return err
}

// limitRows classifies errors that arrive while streaming a result.
type limitRows struct{ driver.Rows }

func (r limitRows) Err() error { return classify(r.Rows.Err()) }

// Ping checks connectivity.
func (db *DB) Ping(ctx context.Context) error { return db.conn.Ping(ctx) }

// Scope returns a tenant-bound query scope.
func (db *DB) Scope(tenantID string) (*Scope, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("%w: empty tenant", ErrInvalid)
	}
	return &Scope{db: db, tenant: tenantID}, nil
}

// Scope builds and runs queries for exactly one tenant.
type Scope struct {
	db     *DB
	tenant string
}

// Tenant returns the scope's tenant id.
func (s *Scope) Tenant() string { return s.tenant }

// From starts a SELECT on a table. The tenant filter is always applied.
func (s *Scope) From(t Table) *Select {
	return &Select{scope: s, table: t, params: map[string]string{}}
}

// FromSub starts a SELECT over a sub-select of the same scope.
func (s *Scope) FromSub(sub *Select) *Select {
	q := &Select{scope: s, sub: sub, params: map[string]string{}}
	if sub == nil || sub.scope != s {
		q.fail("sub-select belongs to a different scope")
	}
	return q
}

// Select is a query under construction.
type Select struct {
	scope   *Scope
	table   Table
	sub     *Select
	final   bool
	cols    []string
	where   []string
	subs    []*Select // sub-selects used in WhereIn, rendered inline
	groupBy []string
	orderBy []string
	limitBy string
	limit   int
	params  map[string]string
	err     error
}

func (q *Select) fail(format string, args ...any) {
	if q.err == nil {
		q.err = fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
}

// forbidden matches fragment content that could escape the tenant boundary:
// references to tenant_id, other tables/databases, sub-queries, statement
// terminators and comments.
var forbidden = regexp.MustCompile(`(?i)(tenant_id|;|--|/\*|\bfrom\b|\bjoin\b|\bunion\b|\binto\b|\bsettings\b|\bformat\b|\bselect\b|\bsystem\b|\bopenlog\b|\bdefault\s*\.|\bremote|\bcluster(allreplicas)?\s*\(|\bjoinget\b|\bdictget|\bgetsetting\b|\b(hosts|metrics|metrics_1m|logs|spans|trace_index|inventory_items|inventory_snapshots|schema_migrations|apm_transactions_1m|apm_service_edges_1m|apm_service_links_1m|apm_db_queries_1m|apm_errors_1m|apm_error_groups|apm_error_group_dims|apm_service_versions_1m|apm_agent_versions_1h|apm_services|apm_service_hosts|containers|apm_service_containers|k8s_clusters|k8s_nodes|k8s_workloads|k8s_pods|alert_evaluations|attribute_keys|attribute_keys_logs|attribute_keys_spans|attribute_keys_metrics|log_patterns_1h)(_local|_mv)?\b)`)

func (q *Select) check(frags ...string) bool {
	for _, f := range frags {
		if m := forbidden.FindString(f); m != "" {
			q.fail("forbidden token %q in fragment %q", m, f)
			return false
		}
	}
	return true
}

// Columns sets the select list.
func (q *Select) Columns(cols ...string) *Select {
	if q.check(cols...) {
		q.cols = append(q.cols, cols...)
	}
	return q
}

// Final adds FINAL (ReplacingMergeTree reads). Only valid on tables.
func (q *Select) Final() *Select {
	if q.sub != nil {
		q.fail("FINAL on sub-select")
	}
	q.final = true
	return q
}

// Where adds an AND-ed condition. Values must be passed with Param and
// referenced as {name:Type}.
func (q *Select) Where(expr string) *Select {
	if q.check(expr) {
		q.where = append(q.where, expr)
	}
	return q
}

// WhereIn adds "expr GLOBAL IN (sub)". sub must belong to the same scope and
// is itself tenant-filtered.
func (q *Select) WhereIn(expr string, sub *Select) *Select {
	if sub == nil || sub.scope != q.scope {
		q.fail("sub-select belongs to a different scope")
		return q
	}
	if q.check(expr) {
		q.where = append(q.where, expr+" GLOBAL IN (\x00sub"+strconv.Itoa(len(q.subs))+"\x00)")
		q.subs = append(q.subs, sub)
	}
	return q
}

// GroupBy sets GROUP BY expressions.
func (q *Select) GroupBy(exprs ...string) *Select {
	if q.check(exprs...) {
		q.groupBy = append(q.groupBy, exprs...)
	}
	return q
}

// OrderBy sets ORDER BY expressions.
func (q *Select) OrderBy(exprs ...string) *Select {
	if q.check(exprs...) {
		q.orderBy = append(q.orderBy, exprs...)
	}
	return q
}

// LimitBy adds LIMIT n BY exprs.
func (q *Select) LimitBy(n int, exprs ...string) *Select {
	if q.check(exprs...) {
		q.limitBy = strconv.Itoa(n) + " BY " + strings.Join(exprs, ", ")
	}
	return q
}

// Limit sets LIMIT.
func (q *Select) Limit(n int) *Select {
	q.limit = n
	return q
}

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Param binds a server-side query parameter. Supported values: string,
// integers, float64, bool, time.Time (as unix nanoseconds, use Int64) and
// []string (as Array(String)). The name tenant_id is reserved.
func (q *Select) Param(name string, value any) *Select {
	if !paramName.MatchString(name) || name == tenantParam {
		q.fail("invalid or reserved parameter name %q", name)
		return q
	}
	var v string
	switch x := value.(type) {
	case string:
		v = x
	case int:
		v = strconv.Itoa(x)
	case int64:
		v = strconv.FormatInt(x, 10)
	case uint32:
		v = strconv.FormatUint(uint64(x), 10)
	case uint64:
		v = strconv.FormatUint(x, 10)
	case float64:
		v = strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		v = strconv.FormatBool(x)
	case time.Time:
		v = strconv.FormatInt(x.UnixNano(), 10)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
		}
		v = "[" + strings.Join(parts, ",") + "]"
	default:
		q.fail("unsupported parameter type %T", value)
		return q
	}
	if old, ok := q.params[name]; ok && old != v {
		q.fail("parameter %q bound twice", name)
	}
	q.params[name] = v
	return q
}

// Build renders the SQL and the full parameter set (including tenant_id).
func (q *Select) Build() (string, map[string]string, error) {
	params := map[string]string{}
	sql, err := q.render(params)
	if err != nil {
		return "", nil, err
	}
	params[tenantParam] = q.scope.tenant
	return sql, params, nil
}

func (q *Select) render(params map[string]string) (string, error) {
	if q.err != nil {
		return "", q.err
	}
	if len(q.cols) == 0 {
		return "", fmt.Errorf("%w: no columns", ErrInvalid)
	}
	for k, v := range q.params {
		if old, ok := params[k]; ok && old != v {
			return "", fmt.Errorf("%w: parameter %q bound with different values", ErrInvalid, k)
		}
		params[k] = v
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(q.cols, ", "))
	b.WriteString(" FROM ")
	where := q.where
	if q.sub != nil {
		inner, err := q.sub.render(params)
		if err != nil {
			return "", err
		}
		b.WriteString("(" + inner + ")")
	} else {
		if q.table.name == "" {
			return "", fmt.Errorf("%w: no table", ErrInvalid)
		}
		b.WriteString(quoteIdent(q.scope.db.database) + "." + q.table.name)
		if q.final {
			b.WriteString(" FINAL")
		}
		// The tenant predicate is always first and cannot be removed.
		where = append([]string{"tenant_id = {" + tenantParam + ":String}"}, where...)
	}
	if len(where) > 0 {
		rendered := make([]string, len(where))
		for i, w := range where {
			rendered[i] = "(" + w + ")"
		}
		joined := strings.Join(rendered, " AND ")
		for i, sub := range q.subs {
			inner, err := sub.render(params)
			if err != nil {
				return "", err
			}
			joined = strings.Replace(joined, "\x00sub"+strconv.Itoa(i)+"\x00", inner, 1)
		}
		b.WriteString(" WHERE " + joined)
	}
	if len(q.groupBy) > 0 {
		b.WriteString(" GROUP BY " + strings.Join(q.groupBy, ", "))
	}
	if len(q.orderBy) > 0 {
		b.WriteString(" ORDER BY " + strings.Join(q.orderBy, ", "))
	}
	if q.limitBy != "" {
		b.WriteString(" LIMIT " + q.limitBy)
	}
	if q.limit > 0 {
		b.WriteString(" LIMIT " + strconv.Itoa(q.limit))
	}
	return b.String(), nil
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// Rows is the result set type.
type Rows = driver.Rows

// Query runs q, which must have been created from this scope.
func (s *Scope) Query(ctx context.Context, q *Select) (Rows, error) {
	if q.scope != s {
		return nil, fmt.Errorf("%w: query belongs to a different scope", ErrInvalid)
	}
	sql, params, err := q.Build()
	if err != nil {
		return nil, err
	}
	// quota_key = tenant: a ClickHouse quota keyed by client key counts each tenant separately.
	ctx = ch.Context(ctx, ch.WithParameters(ch.Parameters(params)), ch.WithSettings(s.db.Settings(s.tenant)), ch.WithQuotaKey(s.tenant))
	rows, err := s.db.conn.Query(ctx, sql)
	if err != nil {
		return nil, classify(err)
	}
	return limitRows{rows}, nil
}

// ParamNames returns the sorted parameter names of a built query (for tests/logging).
func ParamNames(params map[string]string) []string {
	out := make([]string, 0, len(params))
	for k := range params {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
