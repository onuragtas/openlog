package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Structured log search of the Logs Explorer (docs/contracts/api.md "Logs": POST /api/v1/logs/query and
// POST /api/v1/logs/aggregate, D-118). Conditions come from internal/querybuilder; paging reuses the GET /api/v1/logs
// cursor (logs_cursor.go).

const (
	maxLogColumns    = 50
	maxLogQueryBytes = 1024
	// histogramBuckets is the target bucket count of an automatic log aggregate step.
	histogramBuckets = 120
	defaultAggSeries = 10
	maxAggSeries     = 50
	maxJSONQueryBody = 256 << 10
)

// niceSteps are the automatic aggregate steps.
var niceSteps = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// logFilter is the filter part shared by the log query and aggregate requests.
type logFilter struct {
	From    json.RawMessage `json:"from"`
	To      json.RawMessage `json:"to"`
	Filters []qb.Filter     `json:"filters"`
	Groups  [][]qb.Filter   `json:"groups"`
	Q       string          `json:"q"`
}

// bodyTime parses a JSON time (RFC3339 string or unix milliseconds as number or string); ok=false when absent.
func bodyTime(raw json.RawMessage, name string) (time.Time, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, false, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return time.Time{}, false, badRequest("%s: invalid time", name)
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strings.TrimSpace(string(raw))
	default:
		return time.Time{}, false, badRequest("%s: invalid time (use RFC3339 or unix milliseconds)", name)
	}
	t, err := parseTime(s)
	if err != nil {
		return time.Time{}, false, badRequest("%s: %v", name, err)
	}
	return t, true, nil
}

// bodyRange parses from/to of a request body with the defaults of timeRange (now-1h..now).
func (s *Server) bodyRange(fromRaw, toRaw json.RawMessage) (time.Time, time.Time, error) {
	now := s.now().UTC()
	from, to := now.Add(-time.Hour), now
	if t, ok, err := bodyTime(fromRaw, "from"); err != nil {
		return from, to, err
	} else if ok {
		from = t
	}
	if t, ok, err := bodyTime(toRaw, "to"); err != nil {
		return from, to, err
	} else if ok {
		to = t
	}
	if !from.Before(to) {
		return from, to, badRequest("from must be before to")
	}
	return from, to, nil
}

// decodeQueryBody reads a JSON request body of a telemetry query (at most 256 KiB, unknown fields rejected).
func decodeQueryBody(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		return badRequest("Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONQueryBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

// apply adds the range, the inventory exclusion, q and the builder conditions to a logs query and binds b.
func (f *logFilter) apply(q *query.Select, b *qb.Builder, from, to time.Time) error {
	if len(f.Q) > maxLogQueryBytes {
		return badRequest("q must be at most %d bytes", maxLogQueryBytes)
	}
	timeWhere(q, from, to).
		Where("NOT startsWith(event_name, {inventory_prefix:String})").Param("inventory_prefix", "openlog.inventory.")
	if f.Q != "" {
		q.Where("positionCaseInsensitiveUTF8(body, {q:String}) > 0").Param("q", f.Q)
	}
	cond, err := b.Where(f.Filters, f.Groups, "")
	if err != nil {
		return queryBuilderError(err)
	}
	if cond != "" {
		q.Where(cond)
	}
	b.Bind(q)
	return nil
}

type logsQueryRequest struct {
	logFilter
	Order         string   `json:"order"`
	Limit         int      `json:"limit"`
	Cursor        string   `json:"cursor"`
	Columns       []string `json:"columns"`
	IncludeRecord bool     `json:"include_record"`
}

type logQueryRowJSON struct {
	ID                string            `json:"id"`
	Timestamp         string            `json:"timestamp"`
	ObservedTimestamp string            `json:"observed_timestamp"`
	SeverityText      string            `json:"severity_text"`
	SeverityNumber    uint8             `json:"severity_number"`
	Body              string            `json:"body"`
	ServiceName       string            `json:"service_name"`
	HostID            string            `json:"host_id"`
	HostName          string            `json:"host_name"`
	TraceID           string            `json:"trace_id"`
	SpanID            string            `json:"span_id"`
	Fields            map[string]string `json:"fields"`
	// Only with include_record.
	Attributes         *map[string]string `json:"attributes,omitempty"`
	ResourceAttributes *map[string]string `json:"resource_attributes,omitempty"`
}

func (s *Server) queryLogs(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req logsQueryRequest
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

	b := qb.NewBuilder(qb.Logs, "lq")
	cols := []string{"timestamp", "observed_timestamp", "severity_text", "severity_number", "body", "service_name", "host_id",
		"host_name", "trace_id", "span_id", logRowKey}
	// Requested columns: values and presence as two arrays (timestamps come from the core columns).
	var colKeys []string
	var vals, has []string
	seen := map[string]bool{}
	for _, k := range req.Columns {
		f, err := qb.Resolve(qb.Logs, k)
		if err != nil {
			return queryBuilderError(err)
		}
		if seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		colKeys = append(colKeys, f.Key)
		if f.Key == "timestamp" || f.Key == "observed_timestamp" {
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
	q := sc.From(query.Logs).Columns(cols...)
	if err := req.apply(q, b, from, to); err != nil {
		return err
	}
	if asc {
		q.OrderBy("timestamp ASC", "l_key ASC")
		if cursor != nil {
			q.Where("timestamp >= fromUnixTimestamp64Nano({c_ts:Int64})").
				Where("(timestamp > fromUnixTimestamp64Nano({c_ts:Int64}) OR l_key >= {c_key:UInt64})")
		}
	} else {
		q.OrderBy("timestamp DESC", "l_key DESC")
		if cursor != nil {
			q.Where("timestamp <= fromUnixTimestamp64Nano({c_ts:Int64})").
				Where("(timestamp < fromUnixTimestamp64Nano({c_ts:Int64}) OR l_key <= {c_key:UInt64})")
		}
	}
	if cursor != nil {
		q.Param("c_ts", cursor.ts).Param("c_key", cursor.key)
	}
	q.Limit(limit + skip + 1)

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []logQueryRowJSON{}
	positions := []logPos{}
	ids := map[string]int{}
	for rows.Next() {
		var l logQueryRowJSON
		var ts, observed time.Time
		var key uint64
		var cvals []string
		var attrs, resAttrs map[string]string
		var chas []uint8
		dest := []any{&ts, &observed, &l.SeverityText, &l.SeverityNumber, &l.Body, &l.ServiceName, &l.HostID, &l.HostName, &l.TraceID, &l.SpanID, &key}
		if len(colKeys) > 0 {
			dest = append(dest, &cvals, &chas)
		}
		if req.IncludeRecord {
			dest = append(dest, &attrs, &resAttrs)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		l.Timestamp, l.ObservedTimestamp = formatTime(ts), formatTime(observed)
		l.Fields = map[string]string{}
		for i, k := range colKeys {
			switch {
			case k == "timestamp":
				l.Fields[k] = l.Timestamp
			case k == "observed_timestamp":
				l.Fields[k] = l.ObservedTimestamp
			case i < len(chas) && chas[i] == 1 && i < len(cvals):
				l.Fields[k] = cvals[i]
			}
		}
		if req.IncludeRecord {
			attrs, resAttrs = nonNilMap(attrs), nonNilMap(resAttrs)
			l.Attributes, l.ResourceAttributes = &attrs, &resAttrs
		}
		l.ID = strconv.FormatInt(ts.UnixNano(), 10) + "-" + strconv.FormatUint(key, 16)
		if n := ids[l.ID]; n > 0 {
			ids[l.ID] = n + 1
			l.ID += "-" + strconv.Itoa(n)
		} else {
			ids[l.ID] = 1
		}
		out = append(out, l)
		positions = append(positions, logPos{ts: ts.UnixNano(), key: key})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	first, end, next := logPageOrder(positions, cursor, skip, limit, asc)
	var nextCursor *string
	if next != "" {
		nextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out[first:end], "next_cursor": nextCursor})
	return nil
}

type logsAggregateRequest struct {
	logFilter
	Step    string `json:"step"`
	GroupBy string `json:"group_by"`
	Limit   int    `json:"limit"`
}

// aggregateStep returns an explicit step (≥ 1s) or the smallest nice step giving at most histogramBuckets buckets.
func aggregateStep(explicit string, from, to time.Time) (time.Duration, error) {
	if explicit != "" {
		d, err := time.ParseDuration(explicit)
		if err != nil || d < time.Second {
			return 0, badRequest("step must be a duration of at least 1s")
		}
		d = d.Truncate(time.Second)
		if to.Sub(from)/d > 10_000 {
			return 0, badRequest("step gives more than 10000 buckets; use a larger step")
		}
		return d, nil
	}
	span := to.Sub(from)
	for _, st := range niceSteps {
		if span/st <= histogramBuckets {
			return st, nil
		}
	}
	return niceSteps[len(niceSteps)-1], nil
}

type aggSeriesJSON struct {
	Group  string   `json:"group"`
	Other  bool     `json:"other"`
	Total  uint64   `json:"total"`
	Points [][2]any `json:"points"`
}

func (s *Server) aggregateLogs(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req logsAggregateRequest
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
	limit := defaultAggSeries
	if req.Limit < 0 || req.Limit > maxAggSeries {
		return badRequest("limit must be between 1 and %d", maxAggSeries)
	} else if req.Limit > 0 {
		limit = req.Limit
	}
	var group *qb.Field
	if req.GroupBy != "" {
		if group, err = qb.Resolve(qb.Logs, req.GroupBy); err != nil {
			return queryBuilderError(err)
		}
		if d := group.Def(); d != nil && !d.Filterable {
			return badRequest("group_by: %q cannot be used to group", group.Key)
		}
	}
	bucket := "toInt64(toUnixTimestamp(toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})))) * 1000 AS t"

	// Top group values (values outside them form the "other" series).
	var top []string
	if group != nil {
		b := qb.NewBuilder(qb.Logs, "la")
		q := sc.From(query.Logs).Columns(b.Expr(group) + " AS g")
		if err := req.apply(q, b, from, to); err != nil {
			return err
		}
		q.GroupBy("g").OrderBy("count() DESC", "g").Limit(limit)
		rows, err := sc.Query(r.Context(), q)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				return err
			}
			top = append(top, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	b := qb.NewBuilder(qb.Logs, "la")
	var q *query.Select
	if group == nil {
		q = sc.From(query.Logs).Columns("'' AS grp", "toUInt8(1) AS in_top", bucket, "count() AS n")
	} else {
		expr := b.Expr(group)
		q = sc.From(query.Logs).Columns("if(has({top:Array(String)}, "+expr+"), "+expr+", '') AS grp",
			"toUInt8(has({top:Array(String)}, "+expr+")) AS in_top", bucket, "count() AS n").Param("top", top)
	}
	if err := req.apply(q, b, from, to); err != nil {
		return err
	}
	q.Param("step", uint32(step/time.Second)).GroupBy("grp", "in_top", "t").OrderBy("t", "grp").Limit((limit + 1) * 100_000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	series := []*aggSeriesJSON{}
	index := map[string]*aggSeriesJSON{}
	var total uint64
	for rows.Next() {
		var grp string
		var inTop uint8
		var t int64
		var n uint64
		if err := rows.Scan(&grp, &inTop, &t, &n); err != nil {
			return err
		}
		k := "\x00other"
		if inTop == 1 {
			k = grp
		}
		sr, ok := index[k]
		if !ok {
			sr = &aggSeriesJSON{Group: grp, Other: inTop == 0, Points: [][2]any{}}
			index[k] = sr
			series = append(series, sr)
		}
		sr.Points = append(sr.Points, [2]any{t, n})
		sr.Total += n
		total += n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rank := map[string]int{}
	for i, g := range top {
		rank[g] = i
	}
	sort.SliceStable(series, func(i, j int) bool {
		a, c := series[i], series[j]
		if a.Other != c.Other {
			return !a.Other
		}
		return rank[a.Group] < rank[c.Group]
	})
	writeJSON(w, http.StatusOK, map[string]any{"step": formatStep(step), "total": total, "series": series})
	return nil
}
