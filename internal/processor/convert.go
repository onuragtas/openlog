package processor

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/cespare/xxhash/v2"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/otlputil"
)

// Inventory event names and attributes (semantic-conventions.md §3).
const (
	EventInventoryItem     = "openlog.inventory.item"
	EventInventorySnapshot = "openlog.inventory.snapshot"

	attrEventName          = "event.name"
	attrEntityType         = "openlog.entity.type"
	entityTypeHost         = "host"
	attrInventorySnapshot  = "openlog.inventory.snapshot_id"
	attrInventoryCategory  = "openlog.inventory.category"
	attrInventoryKey       = "openlog.inventory.key"
	attrInventoryItemCount = "openlog.inventory.item_count"
)

// Rows accumulates table rows derived from OTLP requests. Hosts are
// deduplicated per (tenant, host) and keep the most recent resource.
type Rows struct {
	Metrics            []MetricRow
	Logs               []LogRow
	Spans              []SpanRow
	InventoryItems     []InventoryItemRow
	InventorySnapshots []InventorySnapshotRow
	hosts              map[[2]string]*HostRow
	// Dropped counts individual items that could not be stored, by reason.
	Dropped map[string]int
}

// NewRows returns an empty accumulator.
func NewRows() *Rows {
	return &Rows{hosts: map[[2]string]*HostRow{}, Dropped: map[string]int{}}
}

// Len returns the number of rows for table.
func (r *Rows) Len(table string) int {
	switch table {
	case TableMetrics:
		return len(r.Metrics)
	case TableLogs:
		return len(r.Logs)
	case TableSpans:
		return len(r.Spans)
	case TableInventoryItems:
		return len(r.InventoryItems)
	case TableInventorySnapshots:
		return len(r.InventorySnapshots)
	case TableHosts:
		return len(r.hosts)
	}
	return 0
}

// Total returns the number of rows over all tables.
func (r *Rows) Total() int {
	n := 0
	for _, t := range Tables {
		n += r.Len(t)
	}
	return n
}

// Hosts returns host rows sorted by (tenant, host).
func (r *Rows) Hosts() []HostRow {
	out := make([]HostRow, 0, len(r.hosts))
	for _, h := range r.hosts {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].HostID < out[j].HostID
	})
	return out
}

// Values returns the insert values for table.
func (r *Rows) Values(table string) [][]any {
	var out [][]any
	switch table {
	case TableMetrics:
		out = make([][]any, len(r.Metrics))
		for i := range r.Metrics {
			out[i] = r.Metrics[i].Values()
		}
	case TableLogs:
		out = make([][]any, len(r.Logs))
		for i := range r.Logs {
			out[i] = r.Logs[i].Values()
		}
	case TableSpans:
		out = make([][]any, len(r.Spans))
		for i := range r.Spans {
			out[i] = r.Spans[i].Values()
		}
	case TableInventoryItems:
		out = make([][]any, len(r.InventoryItems))
		for i := range r.InventoryItems {
			out[i] = r.InventoryItems[i].Values()
		}
	case TableInventorySnapshots:
		out = make([][]any, len(r.InventorySnapshots))
		for i := range r.InventorySnapshots {
			out[i] = r.InventorySnapshots[i].Values()
		}
	case TableHosts:
		hosts := r.Hosts()
		out = make([][]any, len(hosts))
		for i := range hosts {
			out[i] = hosts[i].Values()
		}
	}
	return out
}

// resourceInfo holds columns extracted from resource attributes.
type resourceInfo struct {
	attrs       map[string]string
	hostID      string
	hostName    string
	serviceName string
	// sortedAttrs is the canonical encoding used for series ids.
	seriesPrefix []byte
}

func newResourceInfo(res *resourcepb.Resource) resourceInfo {
	attrs := otlputil.AttrsToMap(res.GetAttributes())
	return resourceInfo{
		attrs:        attrs,
		hostID:       attrs[otlputil.AttrHostID],
		hostName:     attrs[otlputil.AttrHostName],
		serviceName:  attrs[otlputil.AttrServiceName],
		seriesPrefix: canonicalMap(nil, attrs),
	}
}

// upsertHost records the host of a resource once per (tenant, host). Only host entity
// resources (openlog.entity.type=host, the infra agent) write host rows: application
// resources (OTel SDKs, APM) also carry host.id but lack the agent/OS attributes and
// would overwrite them in the ReplacingMergeTree. Their host <-> service link is kept in
// apm_service_hosts (docs/contracts/apm.md §1).
func (r *Rows) upsertHost(tenant string, ri resourceInfo, seen time.Time) {
	if ri.hostID == "" || ri.attrs[attrEntityType] != entityTypeHost {
		return
	}
	k := [2]string{tenant, ri.hostID}
	if h, ok := r.hosts[k]; ok && !seen.After(h.LastSeen) {
		return
	}
	r.hosts[k] = &HostRow{
		TenantID:           tenant,
		HostID:             ri.hostID,
		HostName:           ri.hostName,
		OSType:             ri.attrs[otlputil.AttrOSType],
		OSDescription:      ri.attrs[otlputil.AttrOSDescription],
		Arch:               ri.attrs[otlputil.AttrHostArch],
		AgentName:          ri.attrs[otlputil.AttrAgentName],
		AgentVersion:       ri.attrs[otlputil.AttrAgentVersion],
		ResourceAttributes: ri.attrs,
		LastSeen:           seen.UTC().Truncate(time.Millisecond),
	}
}

func tsOr(nanos uint64, fallback time.Time) time.Time {
	if nanos == 0 {
		return fallback.UTC()
	}
	if nanos > math.MaxInt64 {
		nanos = math.MaxInt64
	}
	return time.Unix(0, int64(nanos)).UTC()
}

// canonicalMap appends a deterministic encoding of m (sorted keys) to b.
func canonicalMap(b []byte, m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b = append(b, k...)
		b = append(b, 0xfe)
		b = append(b, m[k]...)
		b = append(b, 0xff)
	}
	return b
}

// SeriesID is a stable 64-bit hash of (tenant, metric name, resource attributes, attributes).
func SeriesID(tenant, metric string, resourceAttrs, attrs map[string]string) uint64 {
	return seriesID(tenant, metric, canonicalMap(nil, resourceAttrs), attrs)
}

func seriesID(tenant, metric string, resEncoded []byte, attrs map[string]string) uint64 {
	b := make([]byte, 0, 256)
	b = append(b, tenant...)
	b = append(b, 0)
	b = append(b, metric...)
	b = append(b, 0)
	b = append(b, resEncoded...)
	b = append(b, 0)
	b = canonicalMap(b, attrs)
	return xxhash.Sum64(b)
}

var temporalityNames = map[metricspb.AggregationTemporality]string{
	metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_UNSPECIFIED: "unspecified",
	metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA:       "delta",
	metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE:  "cumulative",
}

func temporality(t metricspb.AggregationTemporality) string {
	if s, ok := temporalityNames[t]; ok {
		return s
	}
	return "unspecified"
}

func numberValue(dp *metricspb.NumberDataPoint) float64 {
	switch v := dp.Value.(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

func mean(sum float64, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// AddMetrics converts a metrics export request.
func (r *Rows) AddMetrics(tenant string, receivedAt time.Time, req *colmetrics.ExportMetricsServiceRequest) {
	for _, rm := range req.GetResourceMetrics() {
		ri := newResourceInfo(rm.GetResource())
		r.upsertHost(tenant, ri, receivedAt)
		for _, sm := range rm.GetScopeMetrics() {
			scope := sm.GetScope().GetName()
			for _, m := range sm.GetMetrics() {
				if m.GetName() == "" {
					r.Dropped["empty_metric_name"]++
					continue
				}
				base := MetricRow{
					TenantID: tenant, MetricName: m.GetName(), Unit: m.GetUnit(), Description: m.GetDescription(),
					ServiceName: ri.serviceName, HostID: ri.hostID, HostName: ri.hostName,
					ResourceAttributes: ri.attrs, ScopeName: scope, Temporality: "unspecified",
				}
				add := func(row MetricRow, attrs []*commonpb.KeyValue, start, ts uint64, flags uint32) {
					row.Attributes = otlputil.AttrsToMap(attrs)
					row.SeriesID = seriesID(tenant, row.MetricName, ri.seriesPrefix, row.Attributes)
					row.StartTimestamp = tsOr(start, time.Unix(0, 0))
					row.Timestamp = tsOr(ts, receivedAt)
					row.Flags = flags
					r.Metrics = append(r.Metrics, row)
				}
				switch d := m.Data.(type) {
				case *metricspb.Metric_Gauge:
					for _, dp := range d.Gauge.GetDataPoints() {
						row := base
						row.MetricType = "gauge"
						row.Value = numberValue(dp)
						add(row, dp.GetAttributes(), dp.GetStartTimeUnixNano(), dp.GetTimeUnixNano(), dp.GetFlags())
					}
				case *metricspb.Metric_Sum:
					for _, dp := range d.Sum.GetDataPoints() {
						row := base
						row.MetricType = "sum"
						row.Temporality = temporality(d.Sum.GetAggregationTemporality())
						row.IsMonotonic = d.Sum.GetIsMonotonic()
						row.Value = numberValue(dp)
						add(row, dp.GetAttributes(), dp.GetStartTimeUnixNano(), dp.GetTimeUnixNano(), dp.GetFlags())
					}
				case *metricspb.Metric_Histogram:
					for _, dp := range d.Histogram.GetDataPoints() {
						row := base
						row.MetricType = "histogram"
						row.Temporality = temporality(d.Histogram.GetAggregationTemporality())
						row.Count = dp.GetCount()
						row.Sum = dp.GetSum()
						// value holds the mean so generic aggregations are meaningful.
						row.Value = mean(row.Sum, row.Count)
						row.BucketCounts = dp.GetBucketCounts()
						row.ExplicitBounds = dp.GetExplicitBounds()
						add(row, dp.GetAttributes(), dp.GetStartTimeUnixNano(), dp.GetTimeUnixNano(), dp.GetFlags())
					}
				case *metricspb.Metric_ExponentialHistogram:
					// M0 keeps count/sum/mean and the positive bucket counts; scale and offset are not stored.
					for _, dp := range d.ExponentialHistogram.GetDataPoints() {
						row := base
						row.MetricType = "exponential_histogram"
						row.Temporality = temporality(d.ExponentialHistogram.GetAggregationTemporality())
						row.Count = dp.GetCount()
						row.Sum = dp.GetSum()
						row.Value = mean(row.Sum, row.Count)
						row.BucketCounts = dp.GetPositive().GetBucketCounts()
						add(row, dp.GetAttributes(), dp.GetStartTimeUnixNano(), dp.GetTimeUnixNano(), dp.GetFlags())
					}
				case *metricspb.Metric_Summary:
					for _, dp := range d.Summary.GetDataPoints() {
						row := base
						row.MetricType = "summary"
						row.Count = dp.GetCount()
						row.Sum = dp.GetSum()
						row.Value = mean(row.Sum, row.Count)
						add(row, dp.GetAttributes(), dp.GetStartTimeUnixNano(), dp.GetTimeUnixNano(), dp.GetFlags())
					}
				default:
					r.Dropped["unsupported_metric_type"]++
				}
			}
		}
	}
}

// AddLogs converts a logs export request, routing inventory events to the
// inventory tables instead of logs.
func (r *Rows) AddLogs(tenant string, receivedAt time.Time, req *collogs.ExportLogsServiceRequest) {
	for _, rl := range req.GetResourceLogs() {
		ri := newResourceInfo(rl.GetResource())
		r.upsertHost(tenant, ri, receivedAt)
		for _, sl := range rl.GetScopeLogs() {
			scope := sl.GetScope().GetName()
			for _, lr := range sl.GetLogRecords() {
				event := otlputil.AttrString(lr.GetAttributes(), attrEventName)
				if event == "" {
					event = lr.GetEventName()
				}
				ts := tsOr(lr.GetTimeUnixNano(), tsOr(lr.GetObservedTimeUnixNano(), receivedAt))
				switch event {
				case EventInventoryItem:
					r.addInventoryItem(tenant, ri, ts, lr.GetAttributes(), otlputil.AnyValueString(lr.GetBody()))
					continue
				case EventInventorySnapshot:
					r.addInventorySnapshot(tenant, ri, ts, lr.GetAttributes())
					continue
				}
				sev := lr.GetSeverityNumber()
				if sev < 0 || sev > 255 {
					sev = 0
				}
				r.Logs = append(r.Logs, LogRow{
					TenantID:           tenant,
					Timestamp:          ts,
					ObservedTimestamp:  tsOr(lr.GetObservedTimeUnixNano(), receivedAt),
					ServiceName:        ri.serviceName,
					HostID:             ri.hostID,
					HostName:           ri.hostName,
					SeverityText:       lr.GetSeverityText(),
					SeverityNumber:     uint8(sev),
					TraceID:            otlputil.HexID(lr.GetTraceId()),
					SpanID:             otlputil.HexID(lr.GetSpanId()),
					TraceFlags:         uint8(lr.GetFlags() & 0xff),
					EventName:          event,
					Body:               otlputil.AnyValueString(lr.GetBody()),
					ResourceAttributes: ri.attrs,
					ScopeName:          scope,
					Attributes:         otlputil.AttrsToMap(lr.GetAttributes()),
				})
			}
		}
	}
}

func (r *Rows) addInventoryItem(tenant string, ri resourceInfo, ts time.Time, attrs []*commonpb.KeyValue, body string) {
	snap := otlputil.AttrString(attrs, attrInventorySnapshot)
	cat := otlputil.AttrString(attrs, attrInventoryCategory)
	key := otlputil.AttrString(attrs, attrInventoryKey)
	if ri.hostID == "" || snap == "" || cat == "" || key == "" {
		r.Dropped["invalid_inventory_item"]++
		return
	}
	r.InventoryItems = append(r.InventoryItems, InventoryItemRow{
		TenantID: tenant, HostID: ri.hostID, SnapshotID: snap, SnapshotTime: ts, Category: cat, ItemKey: key, Data: body,
	})
}

func (r *Rows) addInventorySnapshot(tenant string, ri resourceInfo, ts time.Time, attrs []*commonpb.KeyValue) {
	snap := otlputil.AttrString(attrs, attrInventorySnapshot)
	if ri.hostID == "" || snap == "" {
		r.Dropped["invalid_inventory_snapshot"]++
		return
	}
	var count uint32
	if v := otlputil.FindAttr(attrs, attrInventoryItemCount); v != nil {
		switch x := v.Value.(type) {
		case *commonpb.AnyValue_IntValue:
			if x.IntValue > 0 && x.IntValue <= math.MaxUint32 {
				count = uint32(x.IntValue)
			}
		case *commonpb.AnyValue_StringValue:
			if n, err := strconv.ParseUint(x.StringValue, 10, 32); err == nil {
				count = uint32(n)
			}
		}
	}
	r.InventorySnapshots = append(r.InventorySnapshots, InventorySnapshotRow{
		TenantID: tenant, HostID: ri.hostID, SnapshotID: snap, SnapshotTime: ts, ItemCount: count,
	})
}

var spanKinds = map[tracepb.Span_SpanKind]string{
	tracepb.Span_SPAN_KIND_UNSPECIFIED: "unspecified",
	tracepb.Span_SPAN_KIND_INTERNAL:    "internal",
	tracepb.Span_SPAN_KIND_SERVER:      "server",
	tracepb.Span_SPAN_KIND_CLIENT:      "client",
	tracepb.Span_SPAN_KIND_PRODUCER:    "producer",
	tracepb.Span_SPAN_KIND_CONSUMER:    "consumer",
}

var statusCodes = map[tracepb.Status_StatusCode]string{
	tracepb.Status_STATUS_CODE_UNSET: "unset",
	tracepb.Status_STATUS_CODE_OK:    "ok",
	tracepb.Status_STATUS_CODE_ERROR: "error",
}

// localSpanKey identifies a span within one export request.
type localSpanKey struct{ trace, span string }

// AddTraces converts a trace export request. APM columns are derived per span
// (docs/contracts/apm.md); the parent lookup for entry-span detection is limited
// to this request, so conversion stays a function of the Kafka record.
func (r *Rows) AddTraces(tenant string, receivedAt time.Time, req *coltrace.ExportTraceServiceRequest) {
	services := map[localSpanKey]string{}
	for _, rs := range req.GetResourceSpans() {
		svc := otlputil.AttrString(rs.GetResource().GetAttributes(), otlputil.AttrServiceName)
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				services[localSpanKey{string(sp.GetTraceId()), string(sp.GetSpanId())}] = svc
			}
		}
	}
	for _, rs := range req.GetResourceSpans() {
		ri := newResourceInfo(rs.GetResource())
		r.upsertHost(tenant, ri, receivedAt)
		for _, ss := range rs.GetScopeSpans() {
			scope := ss.GetScope().GetName()
			for _, sp := range ss.GetSpans() {
				traceID := otlputil.HexID(sp.GetTraceId())
				spanID := otlputil.HexID(sp.GetSpanId())
				if traceID == "" || spanID == "" {
					r.Dropped["invalid_span_id"]++
					continue
				}
				start := tsOr(sp.GetStartTimeUnixNano(), receivedAt)
				var dur uint64
				if e, s := sp.GetEndTimeUnixNano(), sp.GetStartTimeUnixNano(); s > 0 && e > s {
					dur = e - s
				}
				kind, ok := spanKinds[sp.GetKind()]
				if !ok {
					kind = "unspecified"
				}
				code, ok := statusCodes[sp.GetStatus().GetCode()]
				if !ok {
					code = "unset"
				}
				row := SpanRow{
					TenantID: tenant, Timestamp: start, DurationNs: dur, TraceID: traceID, SpanID: spanID,
					ParentSpanID: otlputil.HexID(sp.GetParentSpanId()), TraceState: sp.GetTraceState(),
					Name: sp.GetName(), Kind: kind, StatusCode: code, StatusMessage: sp.GetStatus().GetMessage(),
					ServiceName: ri.serviceName, HostID: ri.hostID, ResourceAttributes: ri.attrs, ScopeName: scope,
					Attributes: otlputil.AttrsToMap(sp.GetAttributes()),
				}
				for _, ev := range sp.GetEvents() {
					row.EventsTimestamp = append(row.EventsTimestamp, tsOr(ev.GetTimeUnixNano(), start))
					row.EventsName = append(row.EventsName, ev.GetName())
					row.EventsAttributes = append(row.EventsAttributes, otlputil.AttrsToMap(ev.GetAttributes()))
				}
				for _, l := range sp.GetLinks() {
					row.LinksTraceID = append(row.LinksTraceID, otlputil.HexID(l.GetTraceId()))
					row.LinksSpanID = append(row.LinksSpanID, otlputil.HexID(l.GetSpanId()))
				}
				parentSvc, parentLocal := services[localSpanKey{string(sp.GetTraceId()), string(sp.GetParentSpanId())}]
				row.APM = apm.Derive(&apm.Input{
					Resource: ri.attrs, Attributes: row.Attributes, Kind: kind, StatusCode: code,
					StatusMessage: row.StatusMessage, Name: row.Name, TraceState: row.TraceState, Flags: sp.GetFlags(),
					ParentSpanID:           row.ParentSpanID,
					ParentLocalSameService: parentLocal && len(sp.GetParentSpanId()) > 0 && parentSvc == ri.serviceName,
					EventsName:             row.EventsName, EventsAttributes: row.EventsAttributes,
				})
				r.Spans = append(r.Spans, row)
			}
		}
	}
}
