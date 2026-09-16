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
