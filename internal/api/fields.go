package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Field discovery of the query builders (docs/contracts/api.md "Fields", D-118): attribute keys from the hourly key
// index (schema 0080_attribute_keys) with a raw-table sample as fallback, and top values from a bounded sample.

const (
	defaultKeyLimit   = 200
	maxKeyLimit       = 1000
	defaultValueLimit = 50
	maxValueLimit     = 1000
	// keySampleRows bounds the raw records read when the key index has no rows for the range.
	keySampleRows = 20000
	keySampleSpan = time.Hour
	// bodySampleRows / bodySampleSpan bound the JSON body key sample of logs.
	bodySampleRows = 2000
	bodySampleSpan = 15 * time.Minute
	maxBodyKeys    = 100
	// valueSampleRows bounds the records read for top values.
	valueSampleRows = 100000
	maxMetricName   = 512
)

// explorerRoutes registers the field discovery, structured log query and metrics explorer endpoints (D-118, D-119).
func (s *Server) explorerRoutes(mux *http.ServeMux) {
	for pattern, h := range map[string]handlerFunc{
		"GET /api/v1/fields/keys":        s.fieldKeys,
		"GET /api/v1/fields/values":      s.fieldValues,
		"POST /api/v1/logs/query":        s.queryLogs,
		"POST /api/v1/logs/aggregate":    s.aggregateLogs,
		"POST /api/v1/logs/patterns":     s.logPatterns,     // logspatterns.go (D-128)
		"POST /api/v1/traces/query":      s.queryTraces,     // tracesquery.go (D-122)
		"POST /api/v1/traces/aggregate":  s.aggregateTraces, // tracesquery.go (D-122)
		"GET /api/v1/metrics":            s.listMetrics,
		"POST /api/v1/metrics/query":     s.queryMetric,
		"POST /api/v1/metrics/exemplars": s.metricExemplars, // metricsexemplars.go (D-130)
		"GET /api/v1/metrics/{name...}":  s.getMetric,
	} {
		mux.Handle(pattern, s.wrap(pattern, h))
	}
	s.savedViewRoutes(mux) // savedviews.go
}

// signalTable is the raw table of a signal.
func signalTable(sig qb.Signal) query.Table {
	switch sig {
	case qb.Metrics:
		return query.Metrics
	case qb.Traces:
		return query.Spans
	}
	return query.Logs
}

// timeWhere restricts q to [from, to] on the raw table's timestamp column.
func timeWhere(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano())
}

// later returns the later of two times.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// queryBuilderError maps validation errors of the query builder to 400.
func queryBuilderError(err error) error {
	if e, ok := qb.AsError(err); ok {
		return badRequest("%s", e.Msg)
	}
	return err
}

func intParam(r *http.Request, name string, def, maxV int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, badRequest("%s must be a positive integer", name)
	}
	return min(n, maxV), nil
}

func boundedParam(r *http.Request, name string, maxBytes int) (string, error) {
	v := r.URL.Query().Get(name)
	if len(v) > maxBytes {
		return "", badRequest("%s must be at most %d bytes", name, maxBytes)
	}
	return v, nil
}

type fieldKeyJSON struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Source      qb.Source `json:"source"`
	Type        qb.Type   `json:"type"`
	Count       *uint64   `json:"count"`
	Cardinality *uint64   `json:"cardinality"`
}

// keyStats are the aggregated counts of one map key.
type keyStats struct {
	source, key       string
	n, numeric, bools uint64
	card              uint64
}

func (k keyStats) json() fieldKeyJSON {
	typ := qb.TString
	switch {
	case k.n > 0 && k.numeric == k.n:
		typ = qb.TNumber
	case k.n > 0 && k.bools == k.n:
		typ = qb.TBool
	}
	n, card := k.n, k.card
	prefix := map[string]string{"attribute": "attributes.", "resource": "resource.", "body": "body."}[k.source]
	return fieldKeyJSON{Key: prefix + k.key, Name: k.key, Source: qb.Source(k.source), Type: typ, Count: &n, Cardinality: &card}
}

func scanKeyStats(rows query.Rows) ([]keyStats, error) {
	defer rows.Close()
	var out []keyStats
	for rows.Next() {
		var k keyStats
		if err := rows.Scan(&k.source, &k.key, &k.n, &k.numeric, &k.bools, &k.card); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// indexedKeys reads map keys of the range from the key index.
func indexedKeys(r *http.Request, sc *query.Scope, sig qb.Signal, metric, search string, from, to time.Time, limit int) ([]keyStats, error) {
	q := sc.From(query.AttributeKeys).
		Columns("source", "key", "sum(events) AS n", "sum(numeric_events) AS nn", "sum(bool_events) AS nb", "uniqCombinedMerge(12)(value_uniq) AS card").
		Where("signal = {signal:String}").Param("signal", string(sig)).
		Where("hour >= fromUnixTimestamp({h_from:Int64}) AND hour <= fromUnixTimestamp({h_to:Int64})").
		Param("h_from", from.Truncate(time.Hour).Unix()).Param("h_to", to.Unix()).
		GroupBy("source", "key").OrderBy("n DESC", "key").Limit(limit)
	if metric != "" {
		q.Where("metric_name = {metric:String}").Param("metric", metric)
	}
	if search != "" {
		q.Where("positionCaseInsensitiveUTF8(key, {q:String}) > 0").Param("q", search)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	return scanKeyStats(rows)
}

// sampledKeys reads map keys from at most keySampleRows raw records of the last hour of the range.
func sampledKeys(r *http.Request, sc *query.Scope, sig qb.Signal, metric, search string, from, to time.Time, limit int) ([]keyStats, error) {
	records := timeWhere(sc.From(signalTable(sig)).Columns("attributes", "resource_attributes"), later(from, to.Add(-keySampleSpan)), to).
		Limit(keySampleRows)
	if metric != "" {
		records.Where("metric_name = {metric:String}").Param("metric", metric)
	}
	pairs := sc.FromSub(records).Columns("arrayJoin(arrayConcat(" +
		"arrayMap((k, v) -> ('attribute', k, v), mapKeys(CAST(attributes, 'Map(String, String)')), mapValues(CAST(attributes, 'Map(String, String)'))), " +
		"arrayMap((k, v) -> ('resource', k, v), mapKeys(CAST(resource_attributes, 'Map(String, String)')), mapValues(CAST(resource_attributes, 'Map(String, String)'))))) AS kv")
	q := sc.FromSub(pairs).Columns("tupleElement(kv, 1) AS source", "tupleElement(kv, 2) AS k", "count() AS n",
		"countIf(isNotNull(toFloat64OrNull(tupleElement(kv, 3)))) AS nn", "countIf(tupleElement(kv, 3) IN ('true', 'false')) AS nb",
		"uniqCombined(12)(tupleElement(kv, 3)) AS card").
		Where("length(tupleElement(kv, 2)) BETWEEN 1 AND 256").
		GroupBy("source", "k").OrderBy("n DESC", "k").Limit(limit)
	if search != "" {
		q.Where("positionCaseInsensitiveUTF8(tupleElement(kv, 2), {q:String}) > 0").Param("q", search)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	return scanKeyStats(rows)
}

// bodyKeys reads top-level keys of JSON log bodies from a small sample of recent records.
func bodyKeys(r *http.Request, sc *query.Scope, search string, from, to time.Time) ([]keyStats, error) {
	records := timeWhere(sc.From(query.Logs).Columns("body"), later(from, to.Add(-bodySampleSpan)), to).
		Where("startsWith(trimLeft(body), '{')").Limit(bodySampleRows)
	pairs := sc.FromSub(records).Columns("arrayJoin(JSONExtractKeysAndValuesRaw(body)) AS kv")
	q := sc.FromSub(pairs).Columns("'body' AS source", "tupleElement(kv, 1) AS k", "count() AS n",
		"countIf(isNotNull(toFloat64OrNull(tupleElement(kv, 2)))) AS nn", "countIf(tupleElement(kv, 2) IN ('true', 'false')) AS nb",
		"uniqCombined(12)(tupleElement(kv, 2)) AS card").
		Where("match(tupleElement(kv, 1), '^[A-Za-z0-9_@$:-]{1,128}$')").
		GroupBy("k").OrderBy("n DESC", "k").Limit(maxBodyKeys)
	if search != "" {
		q.Where("positionCaseInsensitiveUTF8(tupleElement(kv, 1), {q:String}) > 0").Param("q", search)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	return scanKeyStats(rows)
}

// topLevelKeys lists the signal's top-level fields matching search.
func topLevelKeys(sig qb.Signal, search string) []fieldKeyJSON {
	out := []fieldKeyJSON{}
	for _, d := range qb.Fields[sig] {
		if search == "" || strings.Contains(strings.ToLower(d.Key), strings.ToLower(search)) {
			out = append(out, fieldKeyJSON{Key: d.Key, Name: d.Key, Source: qb.SourceField, Type: d.Type})
		}
	}
	return out
}

// mapKeys returns attribute and resource keys of the range: the key index, else a raw sample.
func mapKeys(r *http.Request, sc *query.Scope, sig qb.Signal, metric, search string, from, to time.Time, limit int) ([]keyStats, bool, error) {
	keys, err := indexedKeys(r, sc, sig, metric, search, from, to, limit)
	if err != nil || len(keys) > 0 {
		return keys, false, err
	}
	keys, err = sampledKeys(r, sc, sig, metric, search, from, to, limit)
	return keys, true, err
}

func (s *Server) fieldKeys(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	qp := r.URL.Query()
	sig, err := qb.ParseSignal(qp.Get("signal"))
	if err != nil {
		return queryBuilderError(err)
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	search, err := boundedParam(r, "q", qb.MaxKeyBytes)
	if err != nil {
		return err
	}
	metric, err := boundedParam(r, "metric", maxMetricName)
	if err != nil {
		return err
	}
	if metric != "" && sig != qb.Metrics {
		return badRequest("metric is only valid with signal=metrics")
	}
	limit, err := intParam(r, "limit", defaultKeyLimit, maxKeyLimit)
	if err != nil {
		return err
	}
	keys := topLevelKeys(sig, search)
	stats, sampled, err := mapKeys(r, sc, sig, metric, search, from, to, limit)
	if err != nil {
		return err
	}
	if sig == qb.Logs {
		body, err := bodyKeys(r, sc, search, from, to)
		if err != nil {
			return err
		}
		stats = append(stats, body...)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k.Key] = true
	}
	for _, st := range stats {
		k := st.json()
		if !seen[k.Key] {
			seen[k.Key] = true
			keys = append(keys, k)
		}
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "sampled": sampled || sig == qb.Logs && len(stats) > 0 && hasBodyKeys(stats)})
	return nil
}

func hasBodyKeys(stats []keyStats) bool {
	for _, k := range stats {
		if k.source == "body" {
			return true
		}
	}
	return false
}

func (s *Server) fieldValues(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	qp := r.URL.Query()
	sig, err := qb.ParseSignal(qp.Get("signal"))
	if err != nil {
		return queryBuilderError(err)
	}
	key := qp.Get("key")
	if key == "" {
		return badRequest("key is required")
	}
	field, err := qb.Resolve(sig, key)
	if err != nil {
		return queryBuilderError(err)
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	search, err := boundedParam(r, "q", qb.MaxValueBytes)
	if err != nil {
		return err
	}
	metric, err := boundedParam(r, "metric", maxMetricName)
	if err != nil {
		return err
	}
	if metric != "" && sig != qb.Metrics {
		return badRequest("metric is only valid with signal=metrics")
	}
	rawFilters, err := boundedParam(r, "filters", 16384)
	if err != nil {
		return err
	}
	filters, err := qb.ParseFilters(rawFilters)
	if err != nil {
		return queryBuilderError(err)
	}
	rawGroups, err := boundedParam(r, "groups", 16384)
	if err != nil {
		return err
	}
	groups, err := qb.ParseGroups(rawGroups)
	if err != nil {
		return queryBuilderError(err)
	}
	// Conditions of the explorer listing the values are computed for ("top values"): logs body search and
	// transaction, traces root_only.
	bodyQ, err := boundedParam(r, "body_q", maxLogQueryBytes)
	if err != nil {
		return err
	}
	txn, txnSvc := qp.Get("transaction"), qp.Get("transaction_service")
	rootOnly := false
	switch qp.Get("root_only") {
	case "", "false", "0":
	case "true", "1":
		rootOnly = true
	default:
		return badRequest("root_only must be true or false")
	}
	if sig != qb.Logs && (bodyQ != "" || txn != "" || txnSvc != "") {
		return badRequest("body_q, transaction and transaction_service are only valid with signal=logs")
	}
	if sig != qb.Traces && rootOnly {
		return badRequest("root_only is only valid with signal=traces")
	}
	limit, err := intParam(r, "limit", defaultValueLimit, maxValueLimit)
	if err != nil {
		return err
	}

	b := qb.NewBuilder(sig, "fv")
	records := sc.From(signalTable(sig)).Columns(b.Expr(field) + " AS v").Limit(valueSampleRows)
	if field.Def() == nil {
		records.Where(b.Has(field))
	}
	if search != "" {
		records.Where("positionCaseInsensitiveUTF8("+b.Expr(field)+", {vq:String}) > 0").Param("vq", search)
	}
	switch sig {
	case qb.Logs:
		c := logConditions{filters: filters, groups: groups, q: bodyQ, txn: txn, txnSvc: txnSvc}
		if err := applyLogConditions(sc, records, b, c, field.Key, from, to); err != nil {
			return err
		}
	case qb.Traces:
		if err := applySpanConditions(records, b, filters, groups, rootOnly, field.Key, from, to); err != nil {
			return err
		}
	default:
		cond, err := b.Where(filters, groups, field.Key)
		if err != nil {
			return queryBuilderError(err)
		}
		timeWhere(records, from, to)
		if cond != "" {
			records.Where(cond)
		}
		if metric != "" {
			records.Where("metric_name = {metric:String}").Param("metric", metric)
		}
		b.Bind(records)
	}
	q := sc.FromSub(records).Columns("v", "count() AS n", "sum(count()) OVER () AS total").
		GroupBy("v").OrderBy("n DESC", "v").Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type valueJSON struct {
		Value string `json:"value"`
		Count uint64 `json:"count"`
	}
	values := []valueJSON{}
	var total uint64
	numeric := true
	for rows.Next() {
		var v valueJSON
		if err := rows.Scan(&v.Value, &v.Count, &total); err != nil {
			return err
		}
		if _, err := strconv.ParseFloat(v.Value, 64); err != nil {
			numeric = false
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	typ := field.Type
	if field.Def() == nil && numeric && len(values) > 0 {
		typ = qb.TNumber
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].Count > values[j].Count })
	writeJSON(w, http.StatusOK, map[string]any{"key": field.Key, "type": typ, "values": values, "total": total, "sampled": total >= valueSampleRows})
	return nil
}
