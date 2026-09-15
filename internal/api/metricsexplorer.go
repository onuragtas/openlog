package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Metrics Explorer (docs/contracts/api.md "Metrics", D-119): every metric of the organization regardless of its
// resource, its metadata and keys, and time series with the query builder filter model and type-aware aggregations.

const (
	defaultMetricListLimit = 1000
	maxMetricListLimit     = 5000
	defaultMetricSeries    = 50
	maxMetricSeries        = 200
	maxMetricGroupBy       = 5
	maxMetricPoints        = 11000
	// maxDistributionRows bounds the (series, bucket) rows of histogram and summary queries.
	maxDistributionRows = 200_000
	maxMetricKeys       = 500
)

type metricInfoJSON struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Unit        string   `json:"unit"`
	Description string   `json:"description"`
	Temporality string   `json:"temporality"`
	Monotonic   bool     `json:"monotonic"`
	LastSeen    string   `json:"last_seen"`
	Series      uint64   `json:"series"`
	Services    []string `json:"services"`
}

// Raw metadata columns: name, type, unit, monotonic, last seen, series, services, description, temporality.
var rawMetricMetaColumns = []string{"metric_name", "toString(any(metric_type))", "any(unit)", "any(is_monotonic)", "max(timestamp)",
	"uniq(series_id)", "topKIf(5)(service_name, service_name != '')", "anyIf(description, description != '')", "toString(any(temporality))"}

// Rollup metadata columns (metrics_1m has neither description nor temporality).
var rollupMetricMetaColumns = []string{"metric_name", "toString(any(metric_type))", "any(unit)", "any(is_monotonic)", "max(timestamp)",
	"uniq(series_id)", "topKIf(5)(service_name, service_name != '')", "''", "'unspecified'"}

// readMetricInfos runs a metadata query and merges its rows into byName: later reads fill descriptive fields and
// extend last_seen/series.
func readMetricInfos(r *http.Request, sc *query.Scope, q *query.Select, byName map[string]*metricInfoJSON, raw bool) (int, error) {
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var m metricInfoJSON
		var last time.Time
		if err := rows.Scan(&m.Name, &m.Type, &m.Unit, &m.Monotonic, &last, &m.Series, &m.Services, &m.Description, &m.Temporality); err != nil {
			return n, err
		}
		n++
		m.LastSeen = formatTime(last)
		if m.Services == nil {
			m.Services = []string{}
		}
		old, ok := byName[m.Name]
		if !ok {
			byName[m.Name] = &m
			continue
		}
		if raw {
			old.Type, old.Unit, old.Monotonic, old.Description, old.Temporality = m.Type, m.Unit, m.Monotonic, m.Description, m.Temporality
			if len(m.Services) > 0 {
				old.Services = m.Services
			}
		}
		old.Series = max(old.Series, m.Series)
		if m.LastSeen > old.LastSeen {
			old.LastSeen = m.LastSeen
		}
	}
	return n, rows.Err()
}

// metricInfos reads the metadata of the range: raw data points of at most the last 6h, preceded for longer ranges by
// the 1-minute rollup (gauges and sums). name restricts to one metric; search to names containing it.
func metricInfos(r *http.Request, sc *query.Scope, name, search string, from, to time.Time, limit int) (map[string]*metricInfoJSON, bool, error) {
	byName := map[string]*metricInfoJSON{}
	truncated := false
	restrict := func(q *query.Select) *query.Select {
		if name != "" {
			q.Where("metric_name = {name:String}").Param("name", name)
		}
		if search != "" {
			q.Where("positionCaseInsensitiveUTF8(metric_name, {q:String}) > 0").Param("q", search)
		}
		return q.GroupBy("metric_name").OrderBy("metric_name").Limit(limit + 1)
	}
	if to.Sub(from) > rollupMinRange {
		q := restrict(timeWhere(sc.From(query.Metrics1m).Columns(rollupMetricMetaColumns...), from, to))
		n, err := readMetricInfos(r, sc, q, byName, false)
		if err != nil {
			return nil, false, err
		}
		truncated = n > limit
	}
	q := restrict(timeWhere(sc.From(query.Metrics).Columns(rawMetricMetaColumns...), later(from, to.Add(-rollupMinRange)), to))
	n, err := readMetricInfos(r, sc, q, byName, true)
	if err != nil {
		return nil, false, err
	}
	return byName, truncated || n > limit, nil
}

func (s *Server) listMetrics(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	search, err := boundedParam(r, "q", qb.MaxKeyBytes)
	if err != nil {
		return err
	}
	limit, err := intParam(r, "limit", defaultMetricListLimit, maxMetricListLimit)
	if err != nil {
		return err
	}
	byName, truncated, err := metricInfos(r, sc, "", search, from, to, limit)
	if err != nil {
		return err
	}
	out := make([]metricInfoJSON, 0, len(byName))
	for _, m := range byName {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > limit {
		out, truncated = out[:limit], true
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": out, "truncated": truncated})
	return nil
}

// metricAggregations lists the aggregations of a metric type and the default one.
func metricAggregations(m *metricInfoJSON) ([]string, string) {
	switch {
	case m.Type == "sum" && m.Monotonic:
		return []string{"rate", "increase", "sum", "last"}, "rate"
	case m.Type == "sum":
		return []string{"last", "avg", "min", "max", "sum", "count"}, "last"
	case m.Type == "histogram" || m.Type == "exponential_histogram":
		return []string{"p50", "p75", "p90", "p95", "p99", "avg", "count", "sum", "rate"}, "p95"
	case m.Type == "summary":
		return []string{"p50", "p75", "p90", "p95", "p99", "avg", "count", "sum"}, "avg"
	}
	return []string{"avg", "min", "max", "sum", "last", "count"}, "avg"
}

func validMetricName(name string) error {
	if name == "" || len(name) > maxMetricName {
		return badRequest("metric name must be 1 to %d bytes", maxMetricName)
	}
	return nil
}

func (s *Server) getMetric(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	name := r.PathValue("name")
	if err := validMetricName(name); err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	byName, _, err := metricInfos(r, sc, name, "", from, to, 1)
	if err != nil {
		return err
	}
	m, ok := byName[name]
	if !ok {
		return notFound("metric not found in the time range")
	}
	stats, _, err := mapKeys(r, sc, qb.Metrics, name, "", from, to, maxMetricKeys)
	if err != nil {
		return err
	}
	attrs, res := []fieldKeyJSON{}, []fieldKeyJSON{}
	for _, st := range stats {
		if st.source == "resource" {
			res = append(res, st.json())
		} else {
			attrs = append(attrs, st.json())
		}
	}
	aggs, def := metricAggregations(m)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": m.Name, "type": m.Type, "unit": m.Unit, "description": m.Description, "temporality": m.Temporality,
		"monotonic": m.Monotonic, "last_seen": m.LastSeen, "series": m.Series, "services": m.Services,
		"attribute_keys": attrs, "resource_keys": res, "aggregations": aggs, "default_aggregation": def,
	})
	return nil
}

type metricQueryRequest struct {
	Metric      string          `json:"metric"`
	From        json.RawMessage `json:"from"`
	To          json.RawMessage `json:"to"`
	Filters     []qb.Filter     `json:"filters"`
	Groups      [][]qb.Filter   `json:"groups"`
	Aggregation string          `json:"aggregation"`
	GroupBy     []string        `json:"group_by"`
	Step        string          `json:"step"`
	Limit       int             `json:"limit"`
}

// rollupField reports whether the 1-minute rollup (metrics_1m: host_id, service_name, attributes) can read f.
func rollupField(f *qb.Field) bool {
	switch f.Source {
	case qb.SourceAttribute:
		return true
	case qb.SourceField:
		switch f.Key {
		case "metric.name", "metric.type", "unit", "service.name", "host.id":
			return true
		}
	}
	return false
}

type metricSeriesJSON struct {
	Attributes map[string]string `json:"attributes"`
	Points     [][2]any          `json:"points"`
}

// seriesSet collects points per group, keeping at most limit groups.
type seriesSet struct {
	keys      []string
	limit     int
	series    []*metricSeriesJSON
	index     map[string]*metricSeriesJSON
	truncated bool
}

func newSeriesSet(keys []string, limit int) *seriesSet {
	return &seriesSet{keys: keys, limit: limit, index: map[string]*metricSeriesJSON{}}
}

func (ss *seriesSet) add(g []string, ts int64, v float64) {
	if !finite(v) {
		return
	}
	k := strings.Join(g, "\x00")
	sr, ok := ss.index[k]
	if !ok {
		if len(ss.series) >= ss.limit {
			ss.truncated = true
			return
		}
		attrs := map[string]string{}
		for i, key := range ss.keys {
			if i < len(g) {
				attrs[key] = g[i]
			}
		}
		sr = &metricSeriesJSON{Attributes: attrs, Points: [][2]any{}}
		ss.index[k] = sr
		ss.series = append(ss.series, sr)
	}
	sr.Points = append(sr.Points, [2]any{ts, v})
}

func (ss *seriesSet) result() []*metricSeriesJSON {
	for _, sr := range ss.series {
		pts := sr.Points
		sort.Slice(pts, func(a, b int) bool { return pts[a][0].(int64) < pts[b][0].(int64) })
	}
	sort.SliceStable(ss.series, func(i, j int) bool {
		return seriesLabel(ss.keys, ss.series[i].Attributes) < seriesLabel(ss.keys, ss.series[j].Attributes)
	})
	return ss.series
}

func seriesLabel(keys []string, attrs map[string]string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = attrs[k]
	}
	return strings.Join(parts, "\x00")
}

func (s *Server) queryMetric(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req metricQueryRequest
	if err := decodeQueryBody(r, &req); err != nil {
		return err
	}
	if err := validMetricName(req.Metric); err != nil {
		return err
	}
	from, to, err := s.bodyRange(req.From, req.To)
	if err != nil {
		return err
	}
	limit := defaultMetricSeries
	if req.Limit < 0 || req.Limit > maxMetricSeries {
		return badRequest("limit must be between 1 and %d", maxMetricSeries)
	} else if req.Limit > 0 {
		limit = req.Limit
	}
	if len(req.GroupBy) > maxMetricGroupBy {
		return badRequest("at most %d group_by keys", maxMetricGroupBy)
	}
	rollupOK := true
	var groupFields []*qb.Field
	var groupKeys []string
	for _, k := range req.GroupBy {
		f, err := qb.Resolve(qb.Metrics, k)
		if err != nil {
			return queryBuilderError(err)
		}
		if f.Key == "value" {
			return badRequest("group_by: value cannot be used to group")
		}
		groupFields, groupKeys = append(groupFields, f), append(groupKeys, f.Key)
		rollupOK = rollupOK && rollupField(f)
	}
	for _, fs := range append([][]qb.Filter{req.Filters}, req.Groups...) {
		for _, flt := range fs {
			f, err := qb.Resolve(qb.Metrics, flt.Key)
			if err != nil {
				return queryBuilderError(err)
			}
			rollupOK = rollupOK && rollupField(f)
		}
	}
	// Conditions are validated before any statement runs.
	b := qb.NewBuilder(qb.Metrics, "mq")
	gExprs := make([]string, len(groupFields))
	for i, f := range groupFields {
		gExprs[i] = b.Expr(f)
	}
	group := "emptyArrayString() AS g"
	if len(gExprs) > 0 {
		group = "any([" + strings.Join(gExprs, ", ") + "]) AS g"
	}
	cond, err := b.Where(req.Filters, req.Groups, "")
	if err != nil {
		return queryBuilderError(err)
	}

	byName, _, err := metricInfos(r, sc, req.Metric, "", from, to, 1)
	if err != nil {
		return err
	}
	meta := byName[req.Metric]
	resp := map[string]any{"series": []*metricSeriesJSON{}, "truncated": false}
	if meta == nil {
		meta = &metricInfoJSON{Name: req.Metric, Type: "", Temporality: "unspecified"}
	}
	aggs, def := metricAggregations(meta)
	agg := req.Aggregation
	if agg == "" {
		agg = def
	}
	valid := false
	for _, a := range aggs {
		valid = valid || a == agg
	}
	if meta.Type != "" && !valid {
		return badRequest("aggregation %q is not available for %s metrics (one of %s)", agg, meta.Type, strings.Join(aggs, ", "))
	}
	distribution := meta.Type == "histogram" || meta.Type == "exponential_histogram" || meta.Type == "summary"
	rollup := rollupOK && !distribution && to.Sub(from) > rollupMinRange
	step, err := chooseStep(req.Step, from, to, rollup)
	if err != nil {
		return err
	}
	if to.Sub(from)/step > maxMetricPoints {
		return badRequest("step gives more than %d points; use a larger step", maxMetricPoints)
	}
	resp["metric"] = map[string]any{"name": meta.Name, "type": meta.Type, "unit": meta.Unit, "temporality": meta.Temporality, "monotonic": meta.Monotonic}
	resp["aggregation"] = agg
	resp["step"] = formatStep(step)
	if meta.Type == "" {
		writeJSON(w, http.StatusOK, resp)
		return nil
	}

	bucket := "toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t"
	stepSecs := uint32(step / time.Second)
	restrict := func(q *query.Select) *query.Select {
		timeWhere(q, from, to).Where("metric_name = {name:String}").Param("name", req.Metric).Param("step", stepSecs)
		if cond != "" {
			q.Where(cond)
		}
		return b.Bind(q)
	}
	ss := newSeriesSet(groupKeys, limit)

	if distribution {
		if err := s.queryDistribution(r, sc, meta, agg, step, group, restrict, ss); err != nil {
			return err
		}
		resp["series"], resp["truncated"] = ss.result(), ss.truncated
		writeJSON(w, http.StatusOK, resp)
		return nil
	}

	// Gauges and sums, level 1: per series, per step bucket.
	var inner *query.Select
	if rollup {
		inner = sc.From(query.Metrics1m).Columns("series_id", group, bucket,
			"min(value_min) AS vmin", "max(value_max) AS vmax", "sum(value_sum) AS vsum",
			"toUInt64(sum(value_count)) AS vcnt", "argMaxMerge(value_last) AS vlast")
	} else {
		inner = sc.From(query.Metrics).Columns("series_id", group, bucket,
			"min(value) AS vmin", "max(value) AS vmax", "sum(value) AS vsum", "toUInt64(count()) AS vcnt", "argMax(value, timestamp) AS vlast")
	}
	restrict(inner).GroupBy("series_id", "t")

	source := inner
	var aggExpr, extraWhere string
	delta := meta.Temporality == "delta"
	switch agg {
	case "avg":
		aggExpr = "sum(vsum) / sum(vcnt)"
	case "min":
		aggExpr = "min(vmin)"
	case "max":
		aggExpr = "max(vmax)"
	case "count":
		aggExpr = "toFloat64(sum(vcnt))"
	case "sum":
		aggExpr = "sum(vsum)"
		if meta.Type == "sum" && meta.Monotonic && !delta {
			aggExpr = "sum(vlast)"
		}
	case "last":
		aggExpr = "sum(vlast)"
	case "rate", "increase":
		if delta {
			aggExpr = "sum(vsum)"
			if agg == "rate" {
				aggExpr = "sum(vsum) / {step:UInt32}"
			}
			break
		}
		// Level 2: per-series increase between consecutive buckets; after a counter reset the new value is the increase.
		win := " OVER (PARTITION BY series_id ORDER BY t ASC ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)"
		source = sc.FromSub(inner).Columns("series_id", "g", "t", "vlast",
			"vlast - lagInFrame(vlast)"+win+" AS dv",
			"toInt64(toUnixTimestamp(t)) - toInt64(toUnixTimestamp(lagInFrame(t)"+win+")) AS dt",
			"row_number() OVER (PARTITION BY series_id ORDER BY t ASC) AS rn")
		aggExpr = "sum(if(dv < 0, vlast, dv))"
		if agg == "rate" {
			aggExpr = "sum(if(dv < 0, vlast, dv) / dt)"
		}
		extraWhere = "rn > 1 AND dt > 0"
	default:
		return badRequest("aggregation %q is not available for %s metrics", agg, meta.Type)
	}
	points := int(to.Sub(from)/step) + 2
	outer := sc.FromSub(source).Columns("g", "toInt64(toUnixTimestamp(t)) * 1000 AS ts_ms", aggExpr+" AS v").
		GroupBy("g", "ts_ms").OrderBy("g", "ts_ms").Limit((limit + 1) * points)
	if extraWhere != "" {
		outer.Where(extraWhere)
	}
	if strings.Contains(aggExpr, "{step:UInt32}") {
		outer.Param("step", stepSecs)
	}
	rows, err := sc.Query(r.Context(), outer)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g []string
		var ts int64
		var v float64
		if err := rows.Scan(&g, &ts, &v); err != nil {
			return err
		}
		ss.add(g, ts, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	resp["series"], resp["truncated"] = ss.result(), ss.truncated
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// queryDistribution computes histogram and summary aggregations: ClickHouse returns per series and step bucket the
// latest cumulative (or summed delta) counts; the increases, merging per group and quantile interpolation happen in
// Go (metricsexplorer_hist.go).
func (s *Server) queryDistribution(r *http.Request, sc *query.Scope, meta *metricInfoJSON, agg string, step time.Duration,
	group string, restrict func(*query.Select) *query.Select, ss *seriesSet) error {
	bucket := "toInt64(toUnixTimestamp(toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})))) * 1000 AS t"
	summary := meta.Type == "summary"
	cols := []string{"series_id", group, bucket, "argMax(count, timestamp) AS clast", "sum(count) AS csum",
		"argMax(sum, timestamp) AS slast", "sum(sum) AS ssum"}
	if summary {
		cols = append(cols, "argMax(quantiles, timestamp) AS qs", "argMax(quantile_values, timestamp) AS qv")
	} else {
		cols = append(cols, "argMax(bucket_counts, timestamp) AS blast", "sumForEach(bucket_counts) AS bsum", "argMax(explicit_bounds, timestamp) AS bounds")
	}
	q := restrict(sc.From(query.Metrics).Columns(cols...)).GroupBy("series_id", "t").OrderBy("series_id", "t").Limit(maxDistributionRows + 1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	acc := newDistAccumulator(meta.Temporality == "delta" && !summary)
	n := 0
	for rows.Next() {
		var row distRow
		dest := []any{&row.series, &row.g, &row.t, &row.clast, &row.csum, &row.slast, &row.ssum}
		if summary {
			dest = append(dest, &row.quantiles, &row.qvalues)
		} else {
			dest = append(dest, &row.blast, &row.bsum, &row.bounds)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		if n++; n > maxDistributionRows {
			ss.truncated = true
			break
		}
		acc.add(&row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range acc.buckets() {
		if v, ok := b.value(agg, step); ok {
			ss.add(b.g, b.t, v)
		}
	}
	return nil
}
