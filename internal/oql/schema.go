package oql

import (
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// AttrType is the type of an attribute.
type AttrType int

// Attribute types.
const (
	TString AttrType = iota
	TNumber
	TBool
)

func (t AttrType) String() string {
	switch t {
	case TNumber:
		return "number"
	case TBool:
		return "bool"
	}
	return "string"
}

// attrDef is a whitelisted attribute. sql and rollup are fixed SQL expressions (never built from query text).
type attrDef struct {
	name    string
	aliases []string
	typ     AttrType
	sql     string // on the raw source
	rollup  string // on metrics_1m; "" = not available there
	value   bool   // Metric value (rollup: only inside rollup-compatible aggregates)
}

// eventType describes one FROM target.
type eventType struct {
	name, desc string
	table      query.Table
	final      bool
	container  bool     // merged sub-select over containers (containerSource)
	base       []string // mandatory predicates (fixed SQL)
	timeCol    string   // DateTime64 column
	attrs      []attrDef
	attrMap    string // attributes map column ("" = none)
	resource   string // resource attributes map column ("" = none)
	rollup     bool   // Metric: metrics_1m may be used
	maxRange   time.Duration
	byName     map[string]*attrDef
}

const (
	maxRawRange    = 31 * 24 * time.Hour
	maxRollupRange = 400 * 24 * time.Hour
	maxLookback    = 400 * 24 * time.Hour
	rollupMinRange = 6 * time.Hour
)

func s(name, sql string, aliases ...string) attrDef {
	return attrDef{name: name, sql: sql, aliases: aliases, typ: TString}
}

func n(name, sql string, aliases ...string) attrDef {
	return attrDef{name: name, sql: sql, aliases: aliases, typ: TNumber}
}

func b(name, sql string, aliases ...string) attrDef {
	return attrDef{name: name, sql: sql, aliases: aliases, typ: TBool}
}

func spanAttrs(tx bool) []attrDef {
	name := s("name", "name")
	extra := []attrDef{}
	if tx {
		name = s("name", "transaction_name")
		extra = append(extra, s("span.name", "name"))
	}
	return append(append([]attrDef{name}, extra...),
		s("kind", "toString(kind)"),
		s("status.code", "toString(status_code)"),
		s("status.message", "status_message"),
		s("trace.id", "trace_id"),
		s("span.id", "span_id"),
		s("parent.id", "parent_span_id"),
		s("service.name", "service_name", "appName"),
		s("service.namespace", "service_namespace"),
		s("deployment.environment", "deployment_environment"),
		s("host.id", "host_id"),
		n("duration", "duration_ns / 1e9"),
		n("duration.ms", "duration_ns / 1e6"),
		s("transaction.name", "transaction_name"),
		s("transaction.type", "transaction_type"),
		b("entry", "is_entry"),
		b("error", "is_error"),
		n("http.status_code", "http_status_code"),
		s("db.system", "db_system"),
		s("db.name", "db_name"),
		s("db.operation", "db_operation"),
		s("db.statement", "db_statement_normalized"),
		s("peer.type", "peer_type"),
		s("peer.name", "peer_name"),
		s("error.type", "error_type"),
		s("error.message", "error_message"),
		n("sample.weight", "sample_weight"),
		s("scope.name", "scope_name"),
	)
}

func rolled(a attrDef, expr string) attrDef {
	a.rollup = expr
	return a
}

var eventTypes = []*eventType{
	{
		name: "Log", desc: "Log records (logs)", table: query.Logs, timeCol: "timestamp",
		attrMap: "attributes", resource: "resource_attributes", maxRange: maxRawRange,
		attrs: []attrDef{
			s("service.name", "service_name"),
			s("host.id", "host_id"),
			s("host.name", "host_name"),
			s("severity", "severity_text", "severity.text"),
			n("severity.number", "severity_number"),
			s("message", "body", "body"),
			s("trace.id", "trace_id"),
			s("span.id", "span_id"),
			s("event.name", "event_name"),
			s("scope.name", "scope_name"),
			// Log pattern (D-128): the id as text (UInt64), "" for records stored before 0091_log_patterns.
			s("pattern.id", "if(pattern_id = 0, '', toString(pattern_id))"),
			s("pattern.template", "pattern_template", "pattern"),
		},
	},
	{
		name: "Span", desc: "Trace spans (spans)", table: query.Spans, timeCol: "timestamp",
		attrMap: "attributes", resource: "resource_attributes", maxRange: maxRawRange, attrs: spanAttrs(false),
	},
	{
		name: "Transaction", desc: "Entry spans of services (spans with is_entry)", table: query.Spans, timeCol: "timestamp",
		base: []string{"is_entry"}, attrMap: "attributes", resource: "resource_attributes", maxRange: maxRawRange, attrs: spanAttrs(true),
	},
	{
		name: "Metric", desc: "Metric data points (metrics, metrics_1m rollup)", table: query.Metrics, timeCol: "timestamp",
		attrMap: "attributes", resource: "resource_attributes", rollup: true, maxRange: maxRollupRange,
		attrs: []attrDef{
			rolled(s("metricName", "metric_name", "metric.name"), "metric_name"),
			rolled(s("metric.type", "toString(metric_type)"), "toString(metric_type)"),
			rolled(s("unit", "unit"), "unit"),
			rolled(s("service.name", "service_name"), "service_name"),
			rolled(s("host.id", "host_id"), "host_id"),
			s("host.name", "host_name"),
			{name: "value", sql: "value", typ: TNumber, value: true},
			n("count", "toFloat64(count)"),
			n("sum", "sum"),
			s("scope.name", "scope_name"),
		},
	},
	{
		name: "Host", desc: "Hosts (hosts, by last_seen)", table: query.Hosts, final: true, timeCol: "last_seen",
		resource: "resource_attributes", maxRange: maxRawRange,
		attrs: []attrDef{
			s("host.id", "host_id"),
			s("host.name", "host_name"),
			s("os.type", "os_type"),
			s("os.description", "os_description"),
			s("arch", "arch"),
			s("agent.name", "agent_name"),
			s("agent.version", "agent_version"),
		},
	},
	{
		name: "Container", desc: "Containers (containers, by last_seen)", table: query.Containers, container: true, timeCol: "c_last_seen",
		attrMap: "c_attributes", maxRange: maxRawRange,
		attrs: []attrDef{
			s("container.id", "c_container_id"),
			s("container.name", "c_name"),
			s("host.id", "c_host_id"),
			s("host.name", "c_host_name"),
			s("image.name", "c_image_name"),
			s("image.tags", "c_image_tags"),
			s("runtime", "c_runtime"),
			s("compose.project", "c_compose_project"),
			s("compose.service", "c_compose_service"),
			s("k8s.pod.name", "c_k8s_pod_name"),
			s("k8s.namespace.name", "c_k8s_namespace_name"),
			s("k8s.container.name", "c_k8s_container_name"),
			s("state", "c_state"),
			s("health", "c_health"),
			n("restarts", "c_restarts"),
		},
	},
	{
		// Continuous profiling samples (0095_profiles). One row per sample, so every query aggregates —
		// `SELECT sum(value) FROM Profile FACET function` is the self-time table the API serves, written by hand.
		name: "Profile", desc: "Profiling samples (profiles)", table: query.Profiles, timeCol: "timestamp",
		attrMap: "attributes", resource: "resource_attributes", maxRange: maxRawRange,
		attrs: []attrDef{
			s("service.name", "service_name"),
			s("service.namespace", "service_namespace"),
			s("deployment.environment", "deployment_environment"),
			s("host.id", "host_id"),
			// The profile's own sample type and unit; never invented, so a chart can say what its numbers mean.
			s("profile.type", "profile_type"),
			s("unit", "unit"),
			// The innermost frame, which is what self time is attributed to (a stored column, not the last
			// element of the stack array, because that is the question a profile is asked first).
			s("function", "leaf", "leaf"),
			// The whole stack as folded text (`main;handleRequest;db.Query`), the classic flame graph format:
			// it makes a stack groupable without exposing an array type to the query language.
			s("stack", "arrayStringConcat(stack, ';')"),
			n("stack.depth", "length(stack)"),
			n("value", "value"),
			n("profile.duration", "duration_ns / 1e9"),
		},
	},
	// Real user monitoring rollups (0094_rum). Their aggregate columns are SimpleAggregateFunction, so they
	// are read with plain sum()/max() rather than the merge machinery the containers table needs. The
	// histogram columns are tuples of arrays and are deliberately left out: OQL has no array type, and the
	// percentiles they carry are served by the RUM API, which knows the bucket scale.
	//
	// The weighted sums are exposed as they are stored rather than pre-divided, because the average of a
	// rollup is sum/sum — `sum(duration.sum.ms) / sum(views)` — and a column that pretended to be an average
	// would be wrong the moment it was faceted.
	{
		name: "RumPageView", desc: "Browser page views per minute (rum_page_views_1m)", table: query.RumPageViews1m,
		timeCol: "timestamp", maxRange: maxRawRange,
		attrs: []attrDef{
			s("app", "app"),
			s("deployment.environment", "deployment_environment"),
			s("route", "route"),
			// 'load' (a document navigation) or 'route_change' (an SPA history change).
			s("page.kind", "page_view_kind"),
			s("device.type", "device_type"),
			s("browser.name", "browser_name"),
			n("views", "views"),
			n("samples", "toFloat64(samples)"),
			n("duration.sum.ms", "duration_sum_ms"),
			n("duration.max.ms", "duration_max_ms"),
			n("ttfb.sum.ms", "ttfb_sum_ms"),
			n("dns.sum.ms", "dns_sum_ms"),
			n("connect.sum.ms", "connect_sum_ms"),
			n("tls.sum.ms", "tls_sum_ms"),
			n("response.sum.ms", "response_sum_ms"),
			n("dom.interactive.sum.ms", "dom_interactive_sum_ms"),
			n("dom.content_loaded.sum.ms", "dom_content_loaded_sum_ms"),
			n("load.event.sum.ms", "load_event_sum_ms"),
		},
	},
	{
		name: "RumVital", desc: "Core Web Vitals per minute (rum_vitals_1m)", table: query.RumVitals1m,
		timeCol: "timestamp", maxRange: maxRawRange,
		attrs: []attrDef{
			s("app", "app"),
			s("deployment.environment", "deployment_environment"),
			// lcp, inp, cls, fcp, ttfb (rum.md §2).
			s("vital", "vital"),
			s("route", "route"),
			s("device.type", "device_type"),
			n("count", "count"),
			n("samples", "toFloat64(samples)"),
			n("value.sum", "value_sum"),
			n("value.max", "value_max"),
			// The Core Web Vitals rating counters; their share of count is the good/poor split.
			n("good", "good"),
			n("needs_improvement", "needs_improvement"),
			n("poor", "poor"),
		},
	},
	{
		// A session is an entity with a lifetime, like a host: the time column is last_seen, not a bucket.
		// entry_route, exit_route and last_trace_id are real AggregateFunction columns (argMin/argMax) and
		// are left out — they need a merge that only makes sense grouped by session.
		name: "RumSession", desc: "Browser sessions (rum_sessions, by last_seen)", table: query.RumSessions,
		timeCol: "last_seen", maxRange: maxRawRange,
		attrs: []attrDef{
			s("app", "app"),
			s("deployment.environment", "deployment_environment"),
			s("session.id", "session_id"),
			n("page.views", "page_views"),
			n("errors", "errors"),
			n("spans", "toFloat64(spans)"),
			s("device.type", "device_type"),
			s("browser.name", "browser_name"),
			s("browser.version", "browser_version"),
			s("os.name", "os_name"),
		},
	},
	{
		// Database statement statistics (0096_db_monitoring): deltas per collection interval, plain columns.
		name: "DbQuery", desc: "Database statement statistics per interval (db_query_stats)", table: query.DBQueryStats,
		timeCol: "timestamp", maxRange: maxRawRange,
		attrs: []attrDef{
			s("db.system", "db_system"),
			s("instance", "instance"),
			s("db.name", "db_name"),
			s("db.user", "db_user"),
			s("host.id", "host_id"),
			s("host.name", "host_name"),
			s("server.address", "server_address"),
			n("server.port", "server_port"),
			s("query.id", "query_id"),
			s("query.text", "query_text"),
			// UInt64 as text: a float64 cannot hold it without losing digits, and it is an identifier anyway.
			s("fingerprint", "toString(fingerprint)"),
			n("calls", "toFloat64(calls)"),
			n("total_time.ms", "total_time_ms"),
			n("rows", "toFloat64(rows)"),
			n("rows_examined", "toFloat64(rows_examined)"),
			n("errors", "toFloat64(errors)"),
			n("no_index_used", "toFloat64(no_index_used)"),
			n("blocks.hit", "toFloat64(blocks_hit)"),
			n("blocks.read", "toFloat64(blocks_read)"),
			n("interval.seconds", "interval_seconds"),
		},
	},
}

// containerColumns merge the aggregating containers table to one row per container (0009_containers.sql).
var containerColumns = []string{
	"host_id AS c_host_id", "container_id AS c_container_id", "min(first_seen) AS c_first_seen", "max(last_seen) AS c_last_seen",
	"argMaxMerge(host_name) AS c_host_name", "argMaxMerge(name) AS c_name", "argMaxMerge(image_name) AS c_image_name",
	"argMaxMerge(image_tags) AS c_image_tags", "argMaxMerge(runtime) AS c_runtime", "argMaxMerge(compose_project) AS c_compose_project",
	"argMaxMerge(compose_service) AS c_compose_service", "argMaxMerge(k8s_pod_name) AS c_k8s_pod_name",
	"argMaxMerge(k8s_namespace_name) AS c_k8s_namespace_name", "argMaxMerge(k8s_container_name) AS c_k8s_container_name",
	"argMaxMerge(state) AS c_state", "argMaxMerge(health) AS c_health", "argMaxMerge(restarts) AS c_restarts",
	"argMaxMerge(attributes) AS c_attributes",
}

var eventTypeByLower = map[string]*eventType{}

func init() {
	for _, et := range eventTypes {
		et.byName = map[string]*attrDef{}
		for i := range et.attrs {
			a := &et.attrs[i]
			et.byName[a.name] = a
			for _, al := range a.aliases {
				et.byName[al] = a
			}
		}
		eventTypeByLower[strings.ToLower(et.name)] = et
	}
}

func lookupEventType(name string) *eventType { return eventTypeByLower[strings.ToLower(name)] }

// ---- schema for editors (GET /api/v1/query/schema) ----

// SchemaAttribute is one attribute of an event type.
type SchemaAttribute struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Aliases []string `json:"aliases"`
	Rollup  bool     `json:"rollup"`
}

// SchemaEventType is one event type.
type SchemaEventType struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Attributes      []SchemaAttribute `json:"attributes"`
	Maps            []string          `json:"maps"`
	MaxRangeSeconds int64             `json:"max_range_seconds"`
}

// SchemaFunction is one aggregate function.
type SchemaFunction struct {
	Name        string `json:"name"`
	Signature   string `json:"signature"`
	Description string `json:"description"`
}

// EventTypeNames lists the event types in documentation order.
func EventTypeNames() []string {
	out := make([]string, len(eventTypes))
	for i, et := range eventTypes {
		out[i] = et.name
	}
	return out
}

// EventTypes returns the static schema of every event type.
func EventTypes() []SchemaEventType {
	out := make([]SchemaEventType, 0, len(eventTypes))
	for _, et := range eventTypes {
		se := SchemaEventType{Name: et.name, Description: et.desc, Maps: []string{}, MaxRangeSeconds: int64(et.maxRange / time.Second)}
		for _, a := range et.attrs {
			al := a.aliases
			if al == nil {
				al = []string{}
			}
			se.Attributes = append(se.Attributes, SchemaAttribute{Name: a.name, Type: a.typ.String(), Aliases: al, Rollup: a.rollup != "" || a.value})
		}
		if et.attrMap != "" {
			se.Maps = append(se.Maps, "attributes")
		}
		if et.resource != "" {
			se.Maps = append(se.Maps, "resource")
		}
		out = append(out, se)
	}
	return out
}

// Functions lists the aggregate functions.
var Functions = []SchemaFunction{
	{"count", "count(*) | count(attr)", "Number of events (with a map attribute: events that have the key)"},
	{"sum", "sum(attr)", "Sum of a number attribute"},
	{"average", "average(attr)", "Average of a number attribute (alias avg)"},
	{"min", "min(attr)", "Minimum"},
	{"max", "max(attr)", "Maximum"},
	{"uniqueCount", "uniqueCount(attr)", "Number of distinct values"},
	{"percentile", "percentile(attr, p, ...)", "Percentiles (0 < p < 100), one column per level"},
	{"median", "median(attr)", "50th percentile"},
	{"latest", "latest(attr)", "Value of the newest event"},
	{"earliest", "earliest(attr)", "Value of the oldest event"},
	{"rate", "rate(agg, 1 minute)", "Aggregate per time unit"},
	{"filter", "filter(agg, WHERE cond)", "Aggregate over matching events"},
	{"histogram", "histogram(attr, ceiling, buckets)", "Event counts in equal buckets over [0, ceiling)"},
}
