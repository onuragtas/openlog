package alert

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Filter restricts the telemetry a condition reads (docs/contracts/alerting.md §2.1). Values and attribute keys
// are always bound as query parameters; only fixed SQL fragments are built here.
type Filter struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// columns maps filter fields and group-by dimensions to the columns of one table ("" = not available).
type columns struct {
	hostID, hostName, service, attrs, resource string
}

var (
	telemetryCols = columns{hostID: "host_id", hostName: "host_name", service: "service_name", attrs: "attributes", resource: "resource_attributes"}
	hostTableCols = columns{hostID: "host_id", hostName: "host_name", resource: "resource_attributes"}
	attrKeyRe     = regexp.MustCompile(`^[A-Za-z0-9_.\-/]{1,128}$`)
)

const (
	maxFilters     = 20
	maxFilterVals  = 100
	maxValueBytes  = 1024
	maxGroupBy     = 5
	defaultWindow  = 300
	minWindow      = 10
	maxMetricWin   = 21600
	maxMetricBytes = 256
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// splitField returns the column kind ("host.id", "host.name", "service.name", "attr", "resource") and map key.
func splitField(f string) (kind, key string) {
	switch f {
	case "host.id", "host.name", "service.name":
		return f, ""
	}
	if k, ok := strings.CutPrefix(f, "attr."); ok {
		return "attr", k
	}
	if k, ok := strings.CutPrefix(f, "resource."); ok {
		return "resource", k
	}
	return "", ""
}

func (c columns) column(kind string) string {
	switch kind {
	case "host.id":
		return c.hostID
	case "host.name":
		return c.hostName
	case "service.name":
		return c.service
	case "attr":
		return c.attrs
	case "resource":
		return c.resource
	}
	return ""
}

func validateFilters(fs []Filter, cols columns) ([]Filter, error) {
	if len(fs) > maxFilters {
		return nil, invalid("filters", "at most %d filters", maxFilters)
	}
	out := make([]Filter, 0, len(fs))
	for i, f := range fs {
		field := fmt.Sprintf("filters[%d]", i)
		kind, key := splitField(f.Field)
		if kind == "" || cols.column(kind) == "" {
			return nil, invalid(field+".field", "unsupported field %q for this rule type", f.Field)
		}
		if (kind == "attr" || kind == "resource") && !attrKeyRe.MatchString(key) {
			return nil, invalid(field+".field", "invalid attribute key %q", key)
		}
		switch f.Op {
		case "eq", "neq", "contains":
			if len(f.Values) != 1 {
				return nil, invalid(field+".values", "op %s takes exactly one value", f.Op)
			}
		case "in", "not_in":
			if len(f.Values) < 1 || len(f.Values) > maxFilterVals {
				return nil, invalid(field+".values", "op %s takes 1-%d values", f.Op, maxFilterVals)
			}
		default:
			return nil, invalid(field+".op", "must be eq, neq, in, not_in or contains")
		}
		for _, v := range f.Values {
			if len(v) > maxValueBytes {
				return nil, invalid(field+".values", "value longer than %d bytes", maxValueBytes)
			}
		}
		out = append(out, Filter{Field: f.Field, Op: f.Op, Values: append([]string(nil), f.Values...)})
	}
	return out, nil
}

// applyFilters adds one WHERE condition per filter. Parameter names use prefix to avoid collisions when the same
// filters are applied to several sub-selects of one query.
func applyFilters(q *query.Select, fs []Filter, cols columns, prefix string) {
	for i, f := range fs {
		kind, key := splitField(f.Field)
		col := cols.column(kind)
		vp := prefix + "f" + strconv.Itoa(i)
		expr := col
		if kind == "attr" || kind == "resource" {
			kp := prefix + "fk" + strconv.Itoa(i)
			expr = col + "[{" + kp + ":String}]"
			q.Param(kp, key)
		} else {
			expr = "toString(" + col + ")"
		}
		switch f.Op {
		case "eq":
			q.Where(expr+" = {"+vp+":String}").Param(vp, f.Values[0])
		case "neq":
			q.Where(expr+" != {"+vp+":String}").Param(vp, f.Values[0])
		case "in":
			q.Where("has({"+vp+":Array(String)}, "+expr+")").Param(vp, f.Values)
		case "not_in":
			q.Where("NOT has({"+vp+":Array(String)}, "+expr+")").Param(vp, f.Values)
		case "contains":
			q.Where("positionCaseInsensitiveUTF8("+expr+", {"+vp+":String}) > 0").Param(vp, f.Values[0])
		}
	}
}

// dim is one group-by dimension.
type dim struct {
	token string // host, service, attr.<k>, resource.<k>
	label string // label name in series labels
	col   string // column (map column for attr/resource)
	key   string // map key for attr/resource
}

func validateGroupBy(gb []string, cols columns, allowed map[string]bool) ([]string, error) {
	if len(gb) > maxGroupBy {
		return nil, invalid("group_by", "at most %d dimensions", maxGroupBy)
	}
	seen := map[string]bool{}
	out := []string{}
	for i, g := range gb {
		field := fmt.Sprintf("group_by[%d]", i)
		ok := false
		switch {
		case g == "host":
			ok = cols.hostID != ""
		case g == "service":
			ok = cols.service != ""
		case strings.HasPrefix(g, "attr."):
			ok = cols.attrs != "" && attrKeyRe.MatchString(strings.TrimPrefix(g, "attr."))
		case strings.HasPrefix(g, "resource."):
			ok = cols.resource != "" && attrKeyRe.MatchString(strings.TrimPrefix(g, "resource."))
		}
		if allowed != nil && !allowed[g] {
			ok = false
		}
		if !ok {
			return nil, invalid(field, "unsupported dimension %q", g)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out, nil
}

func buildDims(gb []string, cols columns) []dim {
	ds := make([]dim, 0, len(gb))
	used := map[string]bool{}
	for _, g := range gb {
		d := dim{token: g}
		switch {
		case g == "host":
			d.label, d.col = "host.id", cols.hostID
		case g == "service":
			d.label, d.col = "service.name", cols.service
		case strings.HasPrefix(g, "attr."):
			d.key = strings.TrimPrefix(g, "attr.")
			d.label, d.col = d.key, cols.attrs
		default:
			d.key = strings.TrimPrefix(g, "resource.")
			d.label, d.col = d.key, cols.resource
		}
		if used[d.label] {
			d.label = g
		}
		used[d.label] = true
		ds = append(ds, d)
	}
	return ds
}

func hasHostDim(ds []dim) bool {
	for _, d := range ds {
		if d.token == "host" {
			return true
		}
	}
	return false
}

// dimExprs returns the key expression of each dimension (bound map keys use prefix "gk<i>").
func dimExprs(q *query.Select, ds []dim) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		if d.key != "" {
			kp := "gk" + strconv.Itoa(i)
			q.Param(kp, d.key)
			out[i] = d.col + "[{" + kp + ":String}]"
		} else {
			out[i] = "toString(" + d.col + ")"
		}
	}
	return out
}

// groupLabels builds series labels from dimension values (and the host name for the host dimension).
func groupLabels(ds []dim, vals []string, hostName string) map[string]string {
	labels := make(map[string]string, len(ds)+1)
	for i, d := range ds {
		labels[d.label] = vals[i]
		if d.token == "host" {
			labels["host.name"] = hostName
		}
	}
	return labels
}

// seriesKey is the stable identity of a series: dimension labels in dimension order (host.name excluded).
func seriesKey(ds []dim, vals []string) string {
	if len(ds) == 0 {
		return "*"
	}
	var b strings.Builder
	for i, d := range ds {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(d.label)
		b.WriteByte('=')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `|`, `\|`).Replace(vals[i]))
	}
	return b.String()
}

// validateThreshold checks operator, threshold and recovery threshold.
func validateThreshold(op string, th, rec *float64) (Judge, error) {
	switch op {
	case "gt", "gte", "lt", "lte":
	case "":
		return Judge{}, invalid("operator", "required")
	default:
		return Judge{}, invalid("operator", "must be gt, gte, lt or lte")
	}
	if th == nil {
		return Judge{}, invalid("threshold", "required")
	}
	if !finite(*th) {
		return Judge{}, invalid("threshold", "must be a finite number")
	}
	j := Judge{Operator: op, Threshold: *th, Recovery: *th}
	if rec != nil {
		if !finite(*rec) {
			return Judge{}, invalid("recovery_threshold", "must be a finite number")
		}
		if (op == "gt" || op == "gte") && *rec > *th {
			return Judge{}, invalid("recovery_threshold", "must be <= threshold for operator %s", op)
		}
		if (op == "lt" || op == "lte") && *rec < *th {
			return Judge{}, invalid("recovery_threshold", "must be >= threshold for operator %s", op)
		}
		j.Recovery = *rec
	}
	return j, nil
}

func validateWindow(field string, v *int, def, lo, hi int) error {
	if *v == 0 {
		*v = def
	}
	if *v < lo || *v > hi {
		return invalid(field, "must be between %d and %d", lo, hi)
	}
	return nil
}

// LimitError reports a query that matched more series or rows than allowed.
type LimitError struct{ Msg string }

func (e *LimitError) Error() string { return e.Msg }

// rangeEnds returns the step ends in (from, to] aligned to from.
func rangeEnds(from, to time.Time, step time.Duration) []time.Time {
	var out []time.Time
	for e := from.Add(step); !e.After(to); e = e.Add(step) {
		out = append(out, e)
	}
	return out
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

func opSymbol(op string) string {
	switch op {
	case "gt":
		return ">"
	case "gte":
		return "≥"
	case "lt":
		return "<"
	case "lte":
		return "≤"
	}
	return op
}

// humanDuration renders whole seconds compactly (90s → 1m30s, 300s → 5m, 3600s → 1h).
func humanDuration(d time.Duration) string {
	s := int64(d / time.Second)
	if s <= 0 {
		return "0s"
	}
	var b strings.Builder
	if h := s / 3600; h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m := s % 3600 / 60; m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if r := s % 60; r > 0 {
		fmt.Fprintf(&b, "%ds", r)
	}
	return b.String()
}

func formatValue(v float64) string {
	if math.IsNaN(v) {
		return "no data"
	}
	return strconv.FormatFloat(roundSig(v), 'g', -1, 64)
}

// labelSuffix renders " on web-1" (host name, else the labels) for summaries.
func labelSuffix(labels map[string]string) string {
	if n := labels["host.name"]; n != "" {
		return " on " + n
	}
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// sortSeries orders range series by key for deterministic output.
func sortSeries(ss []RangeSeries) {
	sort.Slice(ss, func(i, j int) bool { return ss[i].Key < ss[j].Key })
}

func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// tsRange adds "col in [from, to)" on a DateTime64(9) column.
func tsRange(q *query.Select, col string, from, to time.Time) {
	q.Where(col+" >= fromUnixTimestamp64Nano({t_start:Int64}) AND "+col+" < fromUnixTimestamp64Nano({t_end:Int64})").
		Param("t_start", from.UnixNano()).Param("t_end", to.UnixNano())
}

// bucketExpr is the bucket index of a DateTime64 column relative to origin with width step.
func bucketExpr(q *query.Select, col string, origin time.Time, step time.Duration) string {
	q.Param("b_origin", origin.UnixNano()).Param("b_step", int64(step))
	return "intDiv(toUnixTimestamp64Nano(" + col + ") - {b_origin:Int64}, {b_step:Int64})"
}
