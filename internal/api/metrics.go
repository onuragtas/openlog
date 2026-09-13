package api

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

const (
	targetPoints   = 300
	minStep        = 10 * time.Second
	rollupMinRange = 6 * time.Hour
)

var validAggs = map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "last": true, "rate": true}

// chooseStep returns the bucket width: explicit (>= 10s) or ≈ 300 points,
// rounded up to 10s (or to whole minutes when reading the 1m rollup).
func chooseStep(explicit string, from, to time.Time, rollup bool) (time.Duration, error) {
	var step time.Duration
	if explicit != "" {
		d, err := time.ParseDuration(explicit)
		if err != nil {
			return 0, badRequest("step: invalid duration %q", explicit)
		}
		if d < minStep {
			return 0, badRequest("step must be at least 10s")
		}
		step = d
	} else {
		step = to.Sub(from) / targetPoints
	}
	unit := minStep
	if rollup {
		unit = time.Minute
	}
	step = max(step, unit)
	if rem := step % unit; rem != 0 {
		step += unit - rem
	}
	return step, nil
}

type metricMeta struct {
	Type        string
	Temporality string
	Monotonic   bool
	Unit        string
}

func defaultAgg(m metricMeta) string {
	switch {
	case m.Type == "sum" && m.Monotonic:
		return "rate"
	case m.Type == "sum":
		return "last"
	default:
		return "avg"
	}
}

func (s *Server) hostMetrics(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	qp := r.URL.Query()
	hostID := r.PathValue("host_id")
	name := qp.Get("name")
	if name == "" {
		return badRequest("name is required")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	agg := qp.Get("agg")
	if agg != "" && !validAggs[agg] {
		return badRequest("agg must be one of avg, min, max, sum, last, rate")
	}
	var groupBy, resGroup []string
	for _, k := range strings.Split(qp.Get("group_by"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			groupBy = append(groupBy, k)
			if rk, ok := strings.CutPrefix(k, "resource."); ok {
				if !metricResourceKeys[rk] {
					return badRequest("group_by: resource.%s: unsupported resource attribute (supported: %s)", truncate(rk, 64), strings.Join(MetricResourceKeys(), ", "))
				}
				resGroup = append(resGroup, rk)
			}
		}
	}
	resFilters, err := parseMetricResourceFilters(qp)
	if err != nil {
		return err
	}
	if err := requireHost(r, sc, hostID); err != nil {
		return err
	}
	// The 1-minute rollup has no resource attributes: resource filters and groupings read raw points.
	rollup := to.Sub(from) > rollupMinRange && len(resFilters) == 0 && len(resGroup) == 0
	step, err := chooseStep(qp.Get("step"), from, to, rollup)
	if err != nil {
		return err
	}

	// Metric metadata from raw data points.
	metaQ := sc.From(query.Metrics).
		Columns("toString(any(metric_type))", "toString(any(temporality))", "any(is_monotonic)", "any(unit)", "count()").
		Where("metric_name = {name:String}").Param("name", name).
		Where("host_id = {host_id:String}").Param("host_id", hostID).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano())
	addMetricResourceFilters(metaQ, resFilters)
	mrows, err := sc.Query(r.Context(), metaQ)
	if err != nil {
		return err
	}
	var meta metricMeta
	var n uint64
	if mrows.Next() {
		if err := mrows.Scan(&meta.Type, &meta.Temporality, &meta.Monotonic, &meta.Unit, &n); err != nil {
			mrows.Close()
			return err
		}
	}
	mrows.Close()

	type seriesJSON struct {
		Attributes map[string]string `json:"attributes"`
		Points     [][2]any          `json:"points"`
	}
	resp := map[string]any{
		"metric": map[string]string{"name": name, "type": meta.Type, "unit": meta.Unit},
		"step":   formatStep(step),
		"series": []seriesJSON{},
	}
	if n == 0 {
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
	if agg == "" {
		agg = defaultAgg(meta)
	}
	// Histograms/summaries are not rolled up; always read them raw.
	if meta.Type != "gauge" && meta.Type != "sum" {
		rollup = false
	}

	// Level 1: per series, per step bucket.
	var inner *query.Select
	if rollup {
		inner = sc.From(query.Metrics1m).Columns(
			"series_id", "any(attributes) AS attrs",
			"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t",
			"min(value_min) AS vmin", "max(value_max) AS vmax", "sum(value_sum) AS vsum",
			"toUInt64(sum(value_count)) AS vcnt", "argMaxMerge(value_last) AS vlast")
	} else {
		attrsExpr := "any(attributes) AS attrs"
		if len(resGroup) > 0 {
			// Grouped resource attributes join the data point attributes as "resource.<key>".
			attrsExpr = "any(mapUpdate(CAST(attributes, 'Map(String, String)'), mapApply((k, v) -> (concat('resource.', k), v), " +
				"mapFilter((k, v) -> has({res_group:Array(String)}, k), CAST(resource_attributes, 'Map(String, String)'))))) AS attrs"
		}
		inner = sc.From(query.Metrics).Columns(
			"series_id", attrsExpr,
			"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t",
			"min(value) AS vmin", "max(value) AS vmax", "sum(value) AS vsum",
			"toUInt64(count()) AS vcnt", "argMax(value, timestamp) AS vlast")
	}
	inner.Where("metric_name = {name:String}").Param("name", name).
		Where("host_id = {host_id:String}").Param("host_id", hostID).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		Param("step", uint32(step/time.Second)).
		GroupBy("series_id", "t")
	addMetricResourceFilters(inner, resFilters)
	if len(resGroup) > 0 {
		inner.Param("res_group", resGroup)
	}

	source := inner
	var aggExpr string
	var extraWhere string
	switch agg {
	case "avg":
		aggExpr = "sum(vsum) / sum(vcnt)"
	case "min":
		aggExpr = "min(vmin)"
	case "max":
		aggExpr = "max(vmax)"
	case "sum":
		aggExpr = "sum(vsum)"
	case "last":
		aggExpr = "sum(vlast)"
	case "rate":
		if meta.Temporality == "delta" && !rollup {
			// Delta sums: the increase within a bucket is the sum of its points.
			aggExpr = "sum(vsum) / {step:UInt32}"
			break
		}
		// Level 2: per-series increase between consecutive buckets; counter resets clamp to 0.
		win := " OVER (PARTITION BY series_id ORDER BY t ASC ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)"
		source = sc.FromSub(inner).Columns("series_id", "attrs", "t",
			"vlast - lagInFrame(vlast)"+win+" AS dv",
			"toInt64(toUnixTimestamp(t)) - toInt64(toUnixTimestamp(lagInFrame(t)"+win+")) AS dt",
			"row_number() OVER (PARTITION BY series_id ORDER BY t ASC) AS rn")
		aggExpr = "sum(greatest(dv, 0) / dt)"
		extraWhere = "rn > 1 AND dt > 0"
	}

	groupExpr := "attrs"
	outer := sc.FromSub(source)
	if len(groupBy) > 0 {
		groupExpr = "mapFilter((k, v) -> has({group_by:Array(String)}, k), attrs)"
		outer.Param("group_by", groupBy)
	}
	outer.Columns(groupExpr+" AS g", "toInt64(toUnixTimestamp(t)) * 1000 AS ts_ms", aggExpr+" AS v").
		GroupBy("g", "ts_ms").OrderBy("g", "ts_ms").Limit(s.cfg.MaxRows * 100)
	if extraWhere != "" {
		outer.Where(extraWhere)
	}
	if agg == "rate" && meta.Temporality == "delta" && !rollup {
		outer.Param("step", uint32(step/time.Second))
	}

	rows, err := sc.Query(r.Context(), outer)
	if err != nil {
		return err
	}
	defer rows.Close()
	series := []seriesJSON{}
	index := map[string]int{}
	for rows.Next() {
		var g map[string]string
		var ts int64
		var v float64
		if err := rows.Scan(&g, &ts, &v); err != nil {
			return err
		}
		if !finite(v) {
			continue
		}
		key := string(canonical(g))
		i, ok := index[key]
		if !ok {
			i = len(series)
			index[key] = i
			series = append(series, seriesJSON{Attributes: nonNilMap(g), Points: [][2]any{}})
		}
		series[i].Points = append(series[i].Points, [2]any{ts, v})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// ClickHouse does not guarantee a useful ORDER BY on Map keys, so order
	// each series' points by timestamp here.
	for i := range series {
		pts := series[i].Points
		sort.Slice(pts, func(a, b int) bool { return pts[a][0].(int64) < pts[b][0].(int64) })
	}
	resp["series"] = series
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// metricResourceKeys are the resource attributes accepted as `resource.<key>=<value>` filters and
// `group_by=resource.<key>` on GET /hosts/{host_id}/metrics: integration instance identity and
// PostgreSQL entities (semantic-conventions §6.1, §6.5). The allowlist keeps queries on attributes
// the infra agent sets.
var metricResourceKeys = map[string]bool{
	"openlog.discovery.id":       true,
	"openlog.discovery.instance": true,
	"openlog.integration.id":     true,
	"service.instance.id":        true,
	"server.address":             true,
	"server.port":                true,
	"postgresql.database.name":   true,
	"postgresql.table.name":      true,
	"postgresql.index.name":      true,
}

const maxMetricResourceFilters = 4

// MetricResourceKeys returns the supported metric resource attribute keys, sorted.
func MetricResourceKeys() []string {
	keys := make([]string, 0, len(metricResourceKeys))
	for k := range metricResourceKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type attrFilter struct{ key, value string }

// parseMetricResourceFilters validates resource.<key>=<value> parameters (sorted by key).
func parseMetricResourceFilters(qp url.Values) ([]attrFilter, error) {
	var keys []string
	for p := range qp {
		if k, ok := strings.CutPrefix(p, "resource."); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxMetricResourceFilters {
		return nil, badRequest("at most %d resource.* filters", maxMetricResourceFilters)
	}
	out := make([]attrFilter, 0, len(keys))
	for _, k := range keys {
		if !metricResourceKeys[k] {
			return nil, badRequest("resource.%s: unsupported resource attribute filter (supported: %s)", truncate(k, 64), strings.Join(MetricResourceKeys(), ", "))
		}
		vals := qp["resource."+k]
		if len(vals) != 1 || vals[0] == "" || len(vals[0]) > maxAttrFilterValueBytes {
			return nil, badRequest("resource.%s: exactly one non-empty value of at most %d bytes is required", k, maxAttrFilterValueBytes)
		}
		out = append(out, attrFilter{k, vals[0]})
	}
	return out, nil
}

// addMetricResourceFilters adds one bound `resource_attributes[key] = value` condition per filter.
func addMetricResourceFilters(q *query.Select, fs []attrFilter) {
	for i, f := range fs {
		n := strconv.Itoa(i)
		q.Where("resource_attributes[{res_key_"+n+":String}] = {res_value_"+n+":String}").
			Param("res_key_"+n, f.key).Param("res_value_"+n, f.value)
	}
}

// formatStep renders the step in whole seconds, e.g. "60s".
func formatStep(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// canonical encodes attributes deterministically to group points into series.
func canonical(m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for _, k := range keys {
		b = append(b, k...)
		b = append(b, 0)
		b = append(b, m[k]...)
		b = append(b, 1)
	}
	return b
}
