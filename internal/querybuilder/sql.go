package querybuilder

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Builder renders expressions and conditions of one query. Every key and value is bound as a parameter named
// <prefix>_<n>; Bind adds them to a query.
type Builder struct {
	Signal Signal
	// AttrMap and ResourceMap are the map columns (default attributes, resource_attributes).
	AttrMap     string
	ResourceMap string
	// BodyColumn is the log body column (default body).
	BodyColumn string
	prefix     string
	names      []string
	values     []any
}

// NewBuilder creates a builder; prefix must be a valid parameter name prefix unique within the query.
func NewBuilder(sig Signal, prefix string) *Builder {
	return &Builder{Signal: sig, AttrMap: "attributes", ResourceMap: "resource_attributes", BodyColumn: "body", prefix: prefix}
}

// param binds v and returns its placeholder.
func (b *Builder) param(v any, typ string) string {
	name := b.prefix + "_" + strconv.Itoa(len(b.names))
	b.names = append(b.names, name)
	b.values = append(b.values, v)
	return "{" + name + ":" + typ + "}"
}

// Bind adds the builder's parameters to q.
func (b *Builder) Bind(q *query.Select) *query.Select {
	for i, n := range b.names {
		q.Param(n, b.values[i])
	}
	return q
}

// Expr returns a String expression reading f ("" for missing map keys and body paths).
func (b *Builder) Expr(f *Field) string {
	switch f.Source {
	case SourceField:
		if f.def.Type != TString {
			return "toString(" + f.def.SQL + ")"
		}
		return f.def.SQL
	case SourceAttribute:
		return b.AttrMap + "[" + b.param(f.Name, "String") + "]"
	case SourceResource:
		return b.ResourceMap + "[" + b.param(f.Name, "String") + "]"
	case SourceAny:
		k := b.param(f.Name, "String")
		return "if(mapContains(" + b.AttrMap + ", " + k + "), " + b.AttrMap + "[" + k + "], " + b.ResourceMap + "[" + k + "])"
	case SourceBody:
		path := b.bodyPath(f)
		// Strings unquoted, other JSON values (numbers, booleans, objects) as their raw text.
		return "if(JSONType(" + b.BodyColumn + path + ") = 'String', JSONExtractString(" + b.BodyColumn + path + "), JSONExtractRaw(" + b.BodyColumn + path + "))"
	}
	return "''"
}

// Has returns a condition that is true when the record has f (non-empty for top-level string fields).
func (b *Builder) Has(f *Field) string {
	switch f.Source {
	case SourceField:
		if f.def.Type == TString {
			return f.def.SQL + " != ''"
		}
		return "1"
	case SourceAttribute:
		return "mapContains(" + b.AttrMap + ", " + b.param(f.Name, "String") + ")"
	case SourceResource:
		return "mapContains(" + b.ResourceMap + ", " + b.param(f.Name, "String") + ")"
	case SourceAny:
		k := b.param(f.Name, "String")
		return "(mapContains(" + b.AttrMap + ", " + k + ") OR mapContains(" + b.ResourceMap + ", " + k + "))"
	case SourceBody:
		return "JSONHas(" + b.BodyColumn + b.bodyPath(f) + ")"
	}
	return "0"
}

func (b *Builder) bodyPath(f *Field) string {
	var sb strings.Builder
	for _, p := range f.path {
		sb.WriteString(", ")
		sb.WriteString(b.param(p, "String"))
	}
	return sb.String()
}

// numberExpr returns a Nullable(Float64) (map/body values) or numeric expression of f.
func (b *Builder) numberExpr(f *Field) string {
	if f.Source == SourceField && f.def.Type == TNumber {
		return f.def.SQL
	}
	return "toFloat64OrNull(" + b.Expr(f) + ")"
}

// Condition renders one filter. A filter on a top-level field that is display-only is rejected.
func (b *Builder) Condition(flt Filter) (string, error) {
	f, err := Resolve(b.Signal, flt.Key)
	if err != nil {
		return "", err
	}
	if f.def != nil && !f.def.Filterable {
		return "", invalid("key %q cannot be filtered (use the time range)", f.Key)
	}
	op := strings.ToLower(strings.TrimSpace(flt.Op))
	if !opSet[op] {
		return "", invalid("filter on %q: unknown op %q (one of %s)", clip(f.Key), clip(flt.Op), strings.Join(Ops, ", "))
	}
	isNum := f.def != nil && f.def.Type == TNumber
	switch op {
	case "exists", "not_exists":
		if len(flt.Value) > 0 || len(flt.Values) > 0 {
			return "", invalid("filter on %q: %s takes no value", clip(f.Key), op)
		}
		c := b.Has(f)
		if op == "not_exists" {
			c = "NOT (" + c + ")"
		}
		return c, nil
	case "in", "not_in":
		if len(flt.Value) > 0 || len(flt.Values) == 0 || len(flt.Values) > MaxValues {
			return "", invalid("filter on %q: %s takes 1 to %d values", clip(f.Key), op, MaxValues)
		}
		vals := make([]string, 0, len(flt.Values))
		for _, raw := range flt.Values {
			v, err := b.value(f, raw)
			if err != nil {
				return "", err
			}
			vals = append(vals, v)
		}
		var c string
		if isNum {
			ph := make([]string, len(vals))
			for i, v := range vals {
				n, err := number(f, v)
				if err != nil {
					return "", err
				}
				ph[i] = b.param(n, "Float64")
			}
			c = f.def.SQL + " IN (" + strings.Join(ph, ", ") + ")"
		} else {
			// The expression is rendered after the values so a map key parameter follows the value parameters.
			arr := b.param(vals, "Array(String)")
			c = "has(" + arr + ", " + b.Expr(f) + ")"
		}
		if op == "not_in" {
			c = "NOT (" + c + ")"
		}
		return c, nil
	}
	if len(flt.Values) > 0 || len(flt.Value) == 0 {
		return "", invalid("filter on %q: %s takes one value", clip(f.Key), op)
	}
	v, err := b.value(f, flt.Value)
	if err != nil {
		return "", err
	}
	switch op {
	case "=", "!=":
		sqlOp := map[string]string{"=": " = ", "!=": " != "}[op]
		if isNum {
			n, err := number(f, v)
			if err != nil {
				return "", err
			}
			return f.def.SQL + sqlOp + b.param(n, "Float64"), nil
		}
		return b.Expr(f) + sqlOp + b.param(v, "String"), nil
	case ">", ">=", "<", "<=":
		n, err := number(f, v)
		if err != nil {
			return "", err
		}
		return b.numberExpr(f) + " " + op + " " + b.param(n, "Float64"), nil
	case "contains", "not_contains":
		c := "positionCaseInsensitiveUTF8(" + b.Expr(f) + ", " + b.param(v, "String") + ") > 0"
		if op == "not_contains" {
			c = "NOT (" + c + ")"
		}
		return c, nil
	case "like", "not_like":
		c := b.Expr(f) + " LIKE " + b.param(v, "String")
		if op == "not_like" {
			c = "NOT (" + c + ")"
		}
		return c, nil
	case "regex", "not_regex":
		if len(v) > MaxRegexBytes {
			return "", invalid("filter on %q: regular expressions are at most %d bytes", clip(f.Key), MaxRegexBytes)
		}
		if _, err := regexp.Compile(v); err != nil {
			return "", invalid("filter on %q: invalid regular expression: %v", clip(f.Key), err)
		}
		c := "match(" + b.Expr(f) + ", " + b.param(v, "String") + ")"
		if op == "not_regex" {
			c = "NOT (" + c + ")"
		}
		return c, nil
	}
	return "", invalid("filter on %q: unsupported op %q", clip(f.Key), op)
}

// value decodes and bounds one filter value.
func (b *Builder) value(f *Field, raw []byte) (string, error) {
	v, ok := scalar(raw)
	if !ok {
		return "", invalid("filter on %q: values must be strings, numbers or booleans", clip(f.Key))
	}
	if len(v) > MaxValueBytes || strings.ContainsRune(v, 0) {
		return "", invalid("filter on %q: values are at most %d bytes", clip(f.Key), MaxValueBytes)
	}
	if f.def != nil && f.def.Lower {
		v = strings.ToLower(v)
	}
	return v, nil
}

func number(f *Field, v string) (float64, error) {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, invalid("filter on %q: %q is not a number", clip(f.Key), clip(v))
	}
	return n, nil
}

// Where renders filters (AND-ed) and groups (OR of AND-groups) as one condition ("" when there are none).
// Filters whose key resolves to skipKey (canonical form) are left out (fields/values of that key).
func (b *Builder) Where(filters []Filter, groups [][]Filter, skipKey string) (string, error) {
	total := len(filters)
	for _, g := range groups {
		total += len(g)
	}
	if total > MaxConditions {
		return "", invalid("at most %d filter conditions", MaxConditions)
	}
	if len(groups) > MaxGroups {
		return "", invalid("at most %d filter groups", MaxGroups)
	}
	skip := func(flt Filter) bool {
		if skipKey == "" {
			return false
		}
		f, err := Resolve(b.Signal, flt.Key)
		return err == nil && f.Key == skipKey
	}
	and := func(fs []Filter) ([]string, error) {
		var out []string
		for _, flt := range fs {
			if skip(flt) {
				continue
			}
			c, err := b.Condition(flt)
			if err != nil {
				return nil, err
			}
			out = append(out, "("+c+")")
		}
		return out, nil
	}
	parts, err := and(filters)
	if err != nil {
		return "", err
	}
	var ors []string
	for _, g := range groups {
		conds, err := and(g)
		if err != nil {
			return "", err
		}
		if len(conds) == 0 {
			// An empty group matches everything, so the whole OR does.
			ors = nil
			break
		}
		ors = append(ors, "("+strings.Join(conds, " AND ")+")")
	}
	if len(ors) > 0 {
		parts = append(parts, "("+strings.Join(ors, " OR ")+")")
	}
	return strings.Join(parts, " AND "), nil
}
