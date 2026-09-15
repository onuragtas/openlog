package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Structured span search of the Traces Explorer (docs/contracts/api.md "Traces": POST /api/v1/traces/query and
// POST /api/v1/traces/aggregate, D-122). Conditions come from internal/querybuilder (signal traces); paging reuses the
// log cursor (logs_cursor.go) with the span key as tiebreaker.

// spanRowKey is the tiebreaker of spans with the same timestamp (a span is identified by trace and span id).
const spanRowKey = "cityHash64(trace_id, span_id) AS s_key"

// spanFilter is the filter part shared by the span query and aggregate requests.
type spanFilter struct {
	From    json.RawMessage `json:"from"`
	To      json.RawMessage `json:"to"`
	Filters []qb.Filter     `json:"filters"`
	Groups  [][]qb.Filter   `json:"groups"`
	// RootOnly keeps root spans (no parent span id).
	RootOnly bool `json:"root_only"`
}

// applySpanConditions adds the range, root_only and the builder conditions (without those on skipKey) to a spans query
// and binds b. Validation errors are returned before a statement runs.
func applySpanConditions(q *query.Select, b *qb.Builder, filters []qb.Filter, groups [][]qb.Filter, rootOnly bool, skipKey string, from, to time.Time) error {
	cond, err := b.Where(filters, groups, skipKey)
	if err != nil {
		return queryBuilderError(err)
	}
	timeWhere(q, from, to)
	if rootOnly {
		q.Where("parent_span_id = ''")
	}
	if cond != "" {
		q.Where(cond)
	}
	b.Bind(q)
	return nil
}

func (f *spanFilter) apply(q *query.Select, b *qb.Builder, from, to time.Time) error {
	return applySpanConditions(q, b, f.Filters, f.Groups, f.RootOnly, "", from, to)
}

type tracesQueryRequest struct {
	spanFilter
	Order         string   `json:"order"`
	Sort          string   `json:"sort"`
	Limit         int      `json:"limit"`
	Cursor        string   `json:"cursor"`
	Columns       []string `json:"columns"`
	IncludeRecord bool     `json:"include_record"`
}

type spanQueryRowJSON struct {
	ID              string            `json:"id"`
	Timestamp       string            `json:"timestamp"`
	TraceID         string            `json:"trace_id"`
	SpanID          string            `json:"span_id"`
	ParentSpanID    string            `json:"parent_span_id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	StatusCode      string            `json:"status_code"`
	StatusMessage   string            `json:"status_message"`
	ServiceName     string            `json:"service_name"`
	HostID          string            `json:"host_id"`
	DurationNs      uint64            `json:"duration_ns"`
	DurationMs      float64           `json:"duration_ms"`
	IsEntry         bool              `json:"is_entry"`
	IsError         bool              `json:"is_error"`
	HTTPStatusCode  uint16            `json:"http_status_code"`
	TransactionName string            `json:"transaction_name"`
	Fields          map[string]string `json:"fields"`
	// Only with include_record.
	Attributes         *map[string]string `json:"attributes,omitempty"`
	ResourceAttributes *map[string]string `json:"resource_attributes,omitempty"`
}

func (s *Server) queryTraces(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req tracesQueryRequest
	if err := decodeQueryBody(r, &req); err != nil {
		return err
	}
	from, to, err := s.bodyRange(req.From, req.To)
	if err != nil {
		return err
	}
	asc := false
	switch req.Order {
	case "", "desc":
	case "asc":
		asc = true
	default:
		return badRequest("order must be asc or desc")
	}
	byDuration := false
	switch req.Sort {
	case "", "timestamp":
	case "duration":
		byDuration = true
		if req.Cursor != "" || asc {
			return badRequest("sort=duration returns the slowest spans in one page (no cursor, order desc)")
		}
	default:
		return badRequest("sort must be timestamp or duration")
	}
	limit := min(100, s.cfg.MaxRows)
	if req.Limit < 0 {
		return badRequest("limit must be a positive integer")
	} else if req.Limit > 0 {
		limit = min(req.Limit, s.cfg.MaxRows)
	}
	if len(req.Columns) > maxLogColumns {
		return badRequest("at most %d columns", maxLogColumns)
	}
	var cursor *logPos
	skip := 0
	if req.Cursor != "" {
		pos, n, err := decodeLogCursorOrder(req.Cursor, asc)
		if err != nil {
			return err
		}
		cursor, skip = &pos, n
	}

	b := qb.NewBuilder(qb.Traces, "tq")
	cols := []string{"timestamp", "trace_id", "span_id", "parent_span_id", "name", "toString(kind)", "toString(status_code)", "status_message",
		"service_name", "host_id", "duration_ns", "is_entry", "is_error", "http_status_code", "transaction_name", spanRowKey}
	var colKeys []string
	var vals, has []string
	seen := map[string]bool{}
	for _, k := range req.Columns {
		f, err := qb.Resolve(qb.Traces, k)
		if err != nil {
			return queryBuilderError(err)
		}
		if seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		colKeys = append(colKeys, f.Key)
		if f.Key == "timestamp" {
			vals, has = append(vals, "''"), append(has, "toUInt8(1)")
			continue
		}
		vals, has = append(vals, b.Expr(f)), append(has, "toUInt8("+b.Has(f)+")")
	}
	if len(colKeys) > 0 {
		cols = append(cols, "["+strings.Join(vals, ", ")+"] AS c_vals", "["+strings.Join(has, ", ")+"] AS c_has")
	}
	if req.IncludeRecord {
		cols = append(cols, "attributes", "resource_attributes")
	}
	q := sc.From(query.Spans).Columns(cols...)
	if err := req.apply(q, b, from, to); err != nil {
		return err
	}
	switch {
	case byDuration:
		q.OrderBy("duration_ns DESC", "timestamp DESC", "s_key DESC").Limit(limit)
	case asc:
		q.OrderBy("timestamp ASC", "s_key ASC")
		if cursor != nil {
			q.Where("timestamp >= fromUnixTimestamp64Nano({c_ts:Int64})").
				Where("(timestamp > fromUnixTimestamp64Nano({c_ts:Int64}) OR s_key >= {c_key:UInt64})")
		}
	default:
		q.OrderBy("timestamp DESC", "s_key DESC")
		if cursor != nil {
			q.Where("timestamp <= fromUnixTimestamp64Nano({c_ts:Int64})").
				Where("(timestamp < fromUnixTimestamp64Nano({c_ts:Int64}) OR s_key <= {c_key:UInt64})")
		}
	}
	if cursor != nil {
		q.Param("c_ts", cursor.ts).Param("c_key", cursor.key)
	}
	if !byDuration {
		q.Limit(limit + skip + 1)
	}

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []spanQueryRowJSON{}
	positions := []logPos{}
	ids := map[string]int{}
	for rows.Next() {
		var sp spanQueryRowJSON
		var ts time.Time
		var key uint64
		var cvals []string
		var chas []uint8
		var attrs, resAttrs map[string]string
		dest := []any{&ts, &sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind, &sp.StatusCode, &sp.StatusMessage,
			&sp.ServiceName, &sp.HostID, &sp.DurationNs, &sp.IsEntry, &sp.IsError, &sp.HTTPStatusCode, &sp.TransactionName, &key}
		if len(colKeys) > 0 {
			dest = append(dest, &cvals, &chas)
		}
		if req.IncludeRecord {
			dest = append(dest, &attrs, &resAttrs)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		sp.Timestamp = formatTime(ts)
		sp.DurationMs = float64(sp.DurationNs) / 1e6
		sp.Fields = map[string]string{}
		for i, k := range colKeys {
			switch {
			case k == "timestamp":
				sp.Fields[k] = sp.Timestamp
			case i < len(chas) && chas[i] == 1 && i < len(cvals):
				sp.Fields[k] = cvals[i]
			}
		}
		if req.IncludeRecord {
			attrs, resAttrs = nonNilMap(attrs), nonNilMap(resAttrs)
			sp.Attributes, sp.ResourceAttributes = &attrs, &resAttrs
		}
		sp.ID = strconv.FormatInt(ts.UnixNano(), 10) + "-" + strconv.FormatUint(key, 16)
		if n := ids[sp.ID]; n > 0 {
			ids[sp.ID] = n + 1
			sp.ID += "-" + strconv.Itoa(n)
		} else {
			ids[sp.ID] = 1
		}
		out = append(out, sp)
		positions = append(positions, logPos{ts: ts.UnixNano(), key: key})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var nextCursor *string
	if byDuration {
		writeJSON(w, http.StatusOK, map[string]any{"rows": out, "next_cursor": nextCursor})
		return nil
	}
	first, end, next := logPageOrder(positions, cursor, skip, limit, asc)
	if next != "" {
		nextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out[first:end], "next_cursor": nextCursor})
	return nil
}

type tracesAggregateRequest struct {
	spanFilter
	Step    string `json:"step"`
	GroupBy string `json:"group_by"`
	Limit   int    `json:"limit"`
}

// latencyJSON are span duration percentiles (milliseconds) per bucket.
type latencyJSON struct {
	P50 [][2]any `json:"p50"`
	P95 [][2]any `json:"p95"`
	P99 [][2]any `json:"p99"`
}

func (s *Server) aggregateTraces(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req tracesAggregateRequest
	if err := decodeQueryBody(r, &req); err != nil {
		return err
	}
	from, to, err := s.bodyRange(req.From, req.To)
	if err != nil {
		return err
	}
	step, err := aggregateStep(req.Step, from, to)
	if err != nil {
		return err
	}
	limit, err := aggregateLimit(req.Limit)
	if err != nil {
		return err
	}
	group, err := aggregateGroup(qb.Traces, req.GroupBy)
	if err != nil {
		return err
	}
	// Validate the conditions before any statement runs.
	if _, err := qb.NewBuilder(qb.Traces, "tv").Where(req.Filters, req.Groups, ""); err != nil {
		return queryBuilderError(err)
	}
	series, total, err := countSeries(r, sc, query.Spans, qb.Traces, "ta", group, limit, step, func(q *query.Select, b *qb.Builder) error {
		return req.apply(q, b, from, to)
	})
	if err != nil {
		return err
	}

	// Latency percentiles of all matching spans (not split by group_by).
	b := qb.NewBuilder(qb.Traces, "tl")
	q := sc.From(query.Spans).Columns(stepBucket, "quantiles(0.5, 0.95, 0.99)(duration_ns) AS qs")
	if err := req.apply(q, b, from, to); err != nil {
		return err
	}
	q.Param("step", uint32(step/time.Second)).GroupBy("t").OrderBy("t").Limit(100_000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	lat := latencyJSON{P50: [][2]any{}, P95: [][2]any{}, P99: [][2]any{}}
	for rows.Next() {
		var t int64
		var qs []float64
		if err := rows.Scan(&t, &qs); err != nil {
			return err
		}
		if len(qs) != 3 {
			continue
		}
		for i, dst := range []*[][2]any{&lat.P50, &lat.P95, &lat.P99} {
			if !math.IsNaN(qs[i]) && !math.IsInf(qs[i], 0) {
				*dst = append(*dst, [2]any{t, qs[i] / 1e6})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": formatStep(step), "total": total, "series": series, "latency": lat})
	return nil
}
