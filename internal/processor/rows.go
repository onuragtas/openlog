package processor

import (
	"time"

	"github.com/onuragtas/openlog/internal/apm"
)

// Table names (Distributed tables, unqualified).
const (
	TableMetrics            = "metrics"
	TableLogs               = "logs"
	TableSpans              = "spans"
	TableHosts              = "hosts"
	TableInventoryItems     = "inventory_items"
	TableInventorySnapshots = "inventory_snapshots"
)

// Tables lists all tables in insert order. Hosts go last so a host only
// appears once its telemetry has been written.
var Tables = []string{TableMetrics, TableLogs, TableSpans, TableInventoryItems, TableInventorySnapshots, TableHosts}

// Columns per table; Values() of each row type follows the same order.
var Columns = map[string][]string{
	TableMetrics: {"tenant_id", "metric_name", "metric_type", "temporality", "is_monotonic", "unit", "description",
		"service_name", "host_id", "host_name", "series_id", "resource_attributes", "scope_name", "attributes",
		"start_timestamp", "timestamp", "value", "count", "sum", "bucket_counts", "explicit_bounds", "flags"},
	TableLogs: {"tenant_id", "timestamp", "observed_timestamp", "service_name", "host_id", "host_name",
		"severity_text", "severity_number", "trace_id", "span_id", "trace_flags", "event_name", "body",
		"resource_attributes", "scope_name", "attributes"},
	TableSpans: {"tenant_id", "timestamp", "duration_ns", "trace_id", "span_id", "parent_span_id", "trace_state",
		"name", "kind", "status_code", "status_message", "service_name", "host_id", "resource_attributes",
		"scope_name", "attributes", "events_timestamp", "events_name", "events_attributes", "links_trace_id", "links_span_id",
		// APM (docs/contracts/apm.md §8)
		"service_namespace", "deployment_environment", "is_entry", "transaction_type", "transaction_name", "is_error",
		"http_status_code", "sample_weight", "peer_type", "peer_name", "db_system", "db_name", "db_operation",
		"db_statement_normalized", "error_group_id", "error_type", "error_message"},
	TableHosts: {"tenant_id", "host_id", "host_name", "os_type", "os_description", "arch", "agent_name",
		"agent_version", "resource_attributes", "last_seen"},
	TableInventoryItems:     {"tenant_id", "host_id", "snapshot_id", "snapshot_time", "category", "item_key", "data"},
	TableInventorySnapshots: {"tenant_id", "host_id", "snapshot_id", "snapshot_time", "item_count"},
}

// MetricRow is one data point.
type MetricRow struct {
	TenantID           string
	MetricName         string
	MetricType         string // gauge, sum, histogram, exponential_histogram, summary
	Temporality        string // unspecified, delta, cumulative
	IsMonotonic        bool
	Unit               string
	Description        string
	ServiceName        string
	HostID             string
	HostName           string
	SeriesID           uint64
	ResourceAttributes map[string]string
	ScopeName          string
	Attributes         map[string]string
	StartTimestamp     time.Time
	Timestamp          time.Time
	Value              float64
	Count              uint64
	Sum                float64
	BucketCounts       []uint64
	ExplicitBounds     []float64
	Flags              uint32
}

// Values implements row.
func (r *MetricRow) Values() []any {
	return []any{r.TenantID, r.MetricName, r.MetricType, r.Temporality, r.IsMonotonic, r.Unit, r.Description,
		r.ServiceName, r.HostID, r.HostName, r.SeriesID, r.ResourceAttributes, r.ScopeName, r.Attributes,
		r.StartTimestamp, r.Timestamp, r.Value, r.Count, r.Sum, nonNilU64(r.BucketCounts), nonNilF64(r.ExplicitBounds), r.Flags}
}

// LogRow is one log record.
type LogRow struct {
	TenantID           string
	Timestamp          time.Time
	ObservedTimestamp  time.Time
	ServiceName        string
	HostID             string
	HostName           string
	SeverityText       string
	SeverityNumber     uint8
	TraceID            string
	SpanID             string
	TraceFlags         uint8
	EventName          string
	Body               string
	ResourceAttributes map[string]string
	ScopeName          string
	Attributes         map[string]string
}

// Values implements row.
func (r *LogRow) Values() []any {
	return []any{r.TenantID, r.Timestamp, r.ObservedTimestamp, r.ServiceName, r.HostID, r.HostName,
		r.SeverityText, r.SeverityNumber, r.TraceID, r.SpanID, r.TraceFlags, r.EventName, r.Body,
		r.ResourceAttributes, r.ScopeName, r.Attributes}
}

// SpanRow is one span.
type SpanRow struct {
	TenantID           string
	Timestamp          time.Time
	DurationNs         uint64
	TraceID            string
	SpanID             string
	ParentSpanID       string
	TraceState         string
	Name               string
	Kind               string
	StatusCode         string
	StatusMessage      string
	ServiceName        string
	HostID             string
	ResourceAttributes map[string]string
	ScopeName          string
	Attributes         map[string]string
	EventsTimestamp    []time.Time
	EventsName         []string
	EventsAttributes   []map[string]string
	LinksTraceID       []string
	LinksSpanID        []string
	// APM holds the derived APM columns (internal/apm.Derive).
	APM apm.Derived
}

// Values implements row.
func (r *SpanRow) Values() []any {
	a := &r.APM
	return []any{r.TenantID, r.Timestamp, r.DurationNs, r.TraceID, r.SpanID, r.ParentSpanID, r.TraceState,
		r.Name, r.Kind, r.StatusCode, r.StatusMessage, r.ServiceName, r.HostID, r.ResourceAttributes,
		r.ScopeName, r.Attributes, nonNil(r.EventsTimestamp), nonNil(r.EventsName), nonNil(r.EventsAttributes),
		nonNil(r.LinksTraceID), nonNil(r.LinksSpanID),
		a.ServiceNamespace, a.Environment, a.IsEntry, a.TransactionType, a.TransactionName, a.IsError,
		a.HTTPStatusCode, a.SampleWeight, a.PeerType, a.PeerName, a.DBSystem, a.DBName, a.DBOperation,
		a.DBStatementNormalized, a.ErrorGroupID, a.ErrorType, a.ErrorMessage}
}

// HostRow is a host upsert.
type HostRow struct {
	TenantID           string
	HostID             string
	HostName           string
	OSType             string
	OSDescription      string
	Arch               string
	AgentName          string
	AgentVersion       string
	ResourceAttributes map[string]string
	LastSeen           time.Time
}

// Values implements row.
func (r *HostRow) Values() []any {
	return []any{r.TenantID, r.HostID, r.HostName, r.OSType, r.OSDescription, r.Arch, r.AgentName,
		r.AgentVersion, r.ResourceAttributes, r.LastSeen}
}

// InventoryItemRow is one inventory item.
type InventoryItemRow struct {
	TenantID     string
	HostID       string
	SnapshotID   string
	SnapshotTime time.Time
	Category     string
	ItemKey      string
	Data         string
}

// Values implements row.
func (r *InventoryItemRow) Values() []any {
	return []any{r.TenantID, r.HostID, r.SnapshotID, r.SnapshotTime, r.Category, r.ItemKey, r.Data}
}

// InventorySnapshotRow marks a snapshot complete.
type InventorySnapshotRow struct {
	TenantID     string
	HostID       string
	SnapshotID   string
	SnapshotTime time.Time
	ItemCount    uint32
}

// Values implements row.
func (r *InventorySnapshotRow) Values() []any {
	return []any{r.TenantID, r.HostID, r.SnapshotID, r.SnapshotTime, r.ItemCount}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nonNilU64(s []uint64) []uint64   { return nonNil(s) }
func nonNilF64(s []float64) []float64 { return nonNil(s) }
