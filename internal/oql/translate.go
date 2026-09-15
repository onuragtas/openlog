package oql

import (
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/querybuilder"
)

// sqlBuilder renders a plan as query.Select fragments. Every value from the query (literals, map keys, variable
// values, times) becomes a bound parameter; fragments only contain fixed SQL and whitelisted expressions.
type sqlBuilder struct {
	plan     *Plan
	rollup   bool
	params   map[string]any
	n        int
	rateBase time.Duration // denominator of rate(): bucket, window or range
	err      error
}

func newBuilder(p *Plan, rateBase time.Duration) *sqlBuilder {
	return &sqlBuilder{plan: p, rollup: p.Rollup, params: map[string]any{}, rateBase: rateBase}
}

func (b *sqlBuilder) param(v any, typ string) string {
	name := "p" + strconv.Itoa(b.n)
	b.n++
	b.params[name] = v
	return "{" + name + ":" + typ + "}"
}

func (b *sqlBuilder) fail(err *Error) {
	if b.err == nil {
		b.err = err
	}
}

// apply binds every parameter to q.
func (b *sqlBuilder) apply(q *query.Select) {
	for name, v := range b.params {
		q.Param(name, v)
	}
}

// ---- sources ----

func (b *sqlBuilder) source(sc *query.Scope) *query.Select {
	et := b.plan.Event
	switch {
	case et.container:
		inner := sc.From(query.Containers).Columns(containerColumns...).GroupBy("host_id", "container_id")
		return sc.FromSub(inner)
	case b.rollup:
		return sc.From(query.Metrics1m)
	}
	q := sc.From(et.table)
	if et.final {
		q.Final()
	}
	return q
}

// filters returns the time range, mandatory and user conditions.
func (b *sqlBuilder) filters(from, to time.Time) []string {
	et := b.plan.Event
	col := et.timeCol
	out := []string{col + " >= fromUnixTimestamp64Nano({t_from:Int64}) AND " + col + " < fromUnixTimestamp64Nano({t_to:Int64})"}
	b.params["t_from"] = from.UnixNano()
	b.params["t_to"] = to.UnixNano()
	out = append(out, et.base...)
	if w := b.plan.Query.Where; w != nil {
		if c := b.cond(w); c != "1" {
			out = append(out, c)
		}
	}
	return out
}

// timeMillis is the event time as unix milliseconds (Int64).
func (b *sqlBuilder) timeMillis() string {
	if b.rollup {
		return "toInt64(toUnixTimestamp(timestamp)) * 1000"
	}
	return "toUnixTimestamp64Milli(" + b.plan.Event.timeCol + ")"
}

func (b *sqlBuilder) timeNanos() string {
	if b.rollup {
		return "toInt64(toUnixTimestamp(timestamp)) * 1000000000"
	}
	return "toUnixTimestamp64Nano(" + b.plan.Event.timeCol + ")"
}

// ---- attributes ----

func (b *sqlBuilder) mapColumn(kind string) string {
	if kind == "resource" {
		return b.plan.Event.resource
	}
	return b.plan.Event.attrMap
}

func (b *sqlBuilder) attr(a *Attr) string {
	r := a.res
	if r.isMap() {
		return b.mapColumn(r.mapKind) + "[" + b.param(r.key, "String") + "]"
	}
	if b.rollup {
		return r.def.rollup
	}
	return r.def.sql
}

func (b *sqlBuilder) num(a *Attr) string {
	r := a.res
	switch {
	case r.isMap():
		return "toFloat64OrNull(" + b.attr(a) + ")"
	case r.typ() == TBool:
		return "toFloat64(" + b.attr(a) + ")"
	}
	return b.attr(a)
}

// ---- conditions ----

func (b *sqlBuilder) cond(e Expr) string {
	switch x := e.(type) {
	case *Logical:
		l, r := b.cond(x.L), b.cond(x.R)
		if x.Op == "and" {
			switch {
			case l == "1":
				return r
			case r == "1":
				return l
			}
			return "(" + l + ") AND (" + r + ")"
		}
		if l == "1" || r == "1" {
			return "1"
		}
		return "(" + l + ") OR (" + r + ")"
	case *Not:
		return "NOT (" + b.cond(x.X) + ")"
	case *Predicate:
		return b.predicate(x)
	}
	return "0"
}

// values expands variables; ok=false means a variable is unset (the predicate is true).
func (b *sqlBuilder) values(pr *Predicate) ([]Value, bool) {
	out := make([]Value, 0, len(pr.Values))
	for _, v := range pr.Values {
		if v.Kind != ValVariable {
			out = append(out, v)
			continue
		}
		vals := b.plan.vars[v.Var]
		if len(vals) == 0 {
			return nil, false
		}
		for _, s := range vals {
			if s == "*" {
				return nil, false
			}
			if len(s) > maxStringBytes {
				b.fail(v.Span.err("value of variable {{%s}} is longer than %d bytes", v.Var, maxStringBytes))
				return nil, false
			}
			nv := Value{Kind: ValString, Str: s, Span: v.Span}
			if f, err := strconv.ParseFloat(s, 64); err == nil && pr.Attr.res.typ() == TNumber {
				nv = Value{Kind: ValNumber, Num: f, Str: s, Span: v.Span}
			}
			if err := checkValue(pr, pr.Attr.res, nv); err != nil {
				b.fail(err.(*Error))
				return nil, false
			}
			out = append(out, nv)
		}
	}
	return out, true
}

func (b *sqlBuilder) predicate(pr *Predicate) string {
	r := pr.Attr.res
	switch pr.Op {
	case "is null", "is not null":
		isNull := pr.Op == "is null"
		switch {
		case r.isMap():
			c := "mapContains(" + b.mapColumn(r.mapKind) + ", " + b.param(r.key, "String") + ")"
			if isNull {
				return "NOT " + c
			}
			return c
		case r.typ() == TString:
			if isNull {
				return "empty(" + b.attr(pr.Attr) + ")"
			}
			return "notEmpty(" + b.attr(pr.Attr) + ")"
		}
		if isNull {
			return "0"
		}
		return "1"
	}
	vals, ok := b.values(pr)
	if !ok {
		return "1"
	}
	op := pr.Op
	if len(vals) > 1 {
		switch op {
		case "=":
			op = "in"
		case "!=":
			op = "not in"
		case "in", "not in":
		default:
			b.fail(pr.Span.err("a variable with several values can only be used with =, !=, IN or NOT IN"))
			return "0"
		}
	}
	switch op {
	case "like", "not like":
		c := b.attr(pr.Attr) + " LIKE " + b.param(vals[0].Str, "String")
		if op == "not like" {
			return "NOT (" + c + ")"
		}
		return c
	case "contains", "not contains":
		// Same condition as the explorers' contains (querybuilder.ContainsSQL, D-122): case-insensitive, literal.
		c := querybuilder.ContainsSQL(b.attr(pr.Attr), b.param(vals[0].Str, "String"))
		if op == "not contains" {
			return "NOT (" + c + ")"
		}
		return c
	case "in", "not in":
		var c string
		switch cmp := compareKind(r, vals); cmp {
		case TString:
			strs := make([]string, len(vals))
			for i, v := range vals {
				strs[i] = v.Str
			}
			c = "has(" + b.param(strs, "Array(String)") + ", " + b.attr(pr.Attr) + ")"
		default:
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = b.scalar(pr.Attr, "=", v, cmp)
			}
			c = strings.Join(parts, " OR ")
		}
		if op == "not in" {
			return "NOT (" + c + ")"
		}
		return "(" + c + ")"
	}
	return b.scalar(pr.Attr, op, vals[0], compareKind(r, vals))
}

// compareKind chooses string, number or bool comparison for an attribute and its values.
func compareKind(r *resolved, vals []Value) AttrType {
	if !r.isMap() {
		return r.typ()
	}
	for _, v := range vals {
		if v.Kind != ValNumber {
			return TString
		}
	}
	return TNumber
}

func (b *sqlBuilder) scalar(a *Attr, op string, v Value, cmp AttrType) string {
	switch cmp {
	case TNumber:
		f := v.Num
		if v.Kind != ValNumber {
			f, _ = strconv.ParseFloat(v.Str, 64)
		}
		return b.num(a) + " " + op + " " + b.param(f, "Float64")
	case TBool:
		bv, _ := boolOf(v)
		return b.attr(a) + " " + op + " " + b.param(bv, "Bool")
	}
	return b.attr(a) + " " + op + " " + b.param(v.Str, "String")
}

// ---- aggregates ----

func ifName(fn, cond string) string {
	if cond == "" {
		return fn
	}
	return fn + "If"
}

func withCond(args, cond string) string {
	if cond == "" {
		return "(" + args + ")"
	}
	return "(" + args + ", " + cond + ")"
}

func andCond(a, c string) string {
	switch {
	case a == "" || a == "1":
		return c
	case c == "" || c == "1":
		return a
	}
	return "(" + a + ") AND (" + c + ")"
}

// agg renders an aggregate (level: percentile level of this column; cond: filter condition or "").
func (b *sqlBuilder) agg(a *Agg, level float64, cond string) string {
	if b.rollup {
		return b.rollupAgg(a, cond)
	}
	switch a.Func {
	case fnCount:
		if a.Star || !a.Arg.res.isMap() {
			if cond == "" {
				return "count()"
			}
			return "countIf(" + cond + ")"
		}
		has := "mapContains(" + b.mapColumn(a.Arg.res.mapKind) + ", " + b.param(a.Arg.res.key, "String") + ")"
		return "countIf(" + andCond(has, cond) + ")"
	case fnSum, fnAverage, fnMin, fnMax:
		fn := map[string]string{fnSum: "sum", fnAverage: "avg", fnMin: "min", fnMax: "max"}[a.Func]
		return ifName(fn, cond) + withCond(b.num(a.Arg), cond)
	case fnUniqueCount:
		return ifName("uniqExact", cond) + withCond(b.attr(a.Arg), cond)
	case fnPercentile, fnMedian:
		// The level is a validated float64 (0 < p < 100) formatted as a number literal: ClickHouse does not accept
		// query parameters as aggregate function parameters.
		lv := strconv.FormatFloat(level/100, 'f', -1, 64)
		return ifName("quantile", cond) + "(" + lv + ")" + withCond(b.num(a.Arg), cond)
	case fnLatest, fnEarliest:
		fn := "argMax"
		if a.Func == fnEarliest {
			fn = "argMin"
		}
		x := b.attr(a.Arg)
		if a.Arg.res.typ() == TBool {
			x = "toFloat64(" + x + ")"
		}
		return ifName(fn, cond) + withCond(x+", "+b.plan.Event.timeCol, cond)
	case fnRate:
		return "(" + b.agg(a.Inner, level, cond) + ") * " + b.param(b.rateFactor(a), "Float64")
	case fnFilter:
		return b.agg(a.Inner, level, andCond(cond, b.cond(a.Cond)))
	}
	return "0"
}

func (b *sqlBuilder) rateFactor(a *Agg) float64 {
	if b.rateBase <= 0 {
		return 0
	}
	return a.Per.D.Seconds() / b.rateBase.Seconds()
}

// rollupAgg renders an aggregate on metrics_1m (only plans that passed rollupEligible).
func (b *sqlBuilder) rollupAgg(a *Agg, cond string) string {
	switch a.Func {
	case fnCount:
		if a.Star || !a.Arg.res.isMap() {
			return ifName("sum", cond) + withCond("value_count", cond)
		}
		has := "mapContains(attributes, " + b.param(a.Arg.res.key, "String") + ")"
		return "sumIf(value_count, " + andCond(has, cond) + ")"
	case fnSum:
		return ifName("sum", cond) + withCond("value_sum", cond)
	case fnAverage:
		return ifName("sum", cond) + withCond("value_sum", cond) + " / " + ifName("sum", cond) + withCond("value_count", cond)
	case fnMin:
		return ifName("min", cond) + withCond("value_min", cond)
	case fnMax:
		return ifName("max", cond) + withCond("value_max", cond)
	case fnLatest:
		return "argMaxMerge(value_last)"
	case fnUniqueCount:
		return ifName("uniqExact", cond) + withCond(b.attr(a.Arg), cond)
	case fnRate:
		return "(" + b.rollupAgg(a.Inner, cond) + ") * " + b.param(b.rateFactor(a), "Float64")
	case fnFilter:
		return b.rollupAgg(a.Inner, andCond(cond, b.cond(a.Cond)))
	}
	return "0"
}

func (b *sqlBuilder) column(col Column) string {
	sql := b.agg(col.agg, col.level, "")
	if col.Type == TString {
		return "toString(" + sql + ")"
	}
	return "CAST(" + sql + " AS Nullable(Float64))"
}

func (b *sqlBuilder) facetExprs() []string {
	out := make([]string, len(b.plan.Facets))
	for i, f := range b.plan.Facets {
		out[i] = "toString(" + b.attr(f) + ")"
	}
	return out
}

// ---- statements ----

// build returns the main SELECT of the plan for [from, to).
func (p *Plan) build(sc *query.Scope, from, to time.Time) (*query.Select, error) {
	base := to.Sub(from)
	if p.Kind == KindTimeseries {
		base = p.Bucket
	}
	b := newBuilder(p, base)
	q := b.source(sc)
	where := b.filters(from, to)
	var sub *query.Select
	switch p.Kind {
	case KindHistogram:
		h := p.hist
		x := b.num(h.Arg)
		width := h.Ceiling / float64(h.Buckets)
		cond := x + " >= 0 AND " + x + " < " + b.param(h.Ceiling, "Float64")
		q.Columns("toInt64(assumeNotNull(floor("+x+" / "+b.param(width, "Float64")+"))) AS hb", "CAST(count() AS Nullable(Float64)) AS a0").
			GroupBy("hb").OrderBy("hb")
		where = append(where, cond)
	case KindSingle:
		q.Columns(b.columns()...)
	case KindFacets:
		fe := b.facetExprs()
		cols := make([]string, 0, len(fe)+len(p.Columns))
		groups := make([]string, 0, len(fe))
		order := []string{"a0 DESC"}
		for i, f := range fe {
			alias := "f" + strconv.Itoa(i)
			cols = append(cols, f+" AS "+alias)
			groups = append(groups, alias)
			order = append(order, alias)
		}
		q.Columns(append(cols, b.columns()...)...).GroupBy(groups...).OrderBy(order...).Limit(p.Limit + 1)
	case KindTimeseries:
		fe := b.facetExprs()
		cols := make([]string, 0, len(fe)+len(p.Columns)+1)
		groups := make([]string, 0, len(fe)+1)
		for i, f := range fe {
			alias := "f" + strconv.Itoa(i)
			cols = append(cols, f+" AS "+alias)
			groups = append(groups, alias)
		}
		bk := "toInt64(intDiv(" + b.timeMillis() + ", {b_ms:Int64}) * {b_ms:Int64})"
		b.params["b_ms"] = p.Bucket.Milliseconds()
		cols = append(cols, bk+" AS bk")
		groups = append(groups, "bk")
		q.Columns(append(cols, b.columns()...)...).GroupBy(groups...).OrderBy("bk").Limit(maxPoints + 1)
		if len(fe) > 0 {
			// Top groups over the whole range (first column, descending), then bucketed.
			sub = b.source(sc)
			for _, w := range where {
				sub.Where(w)
			}
			sub.Columns(fe...).GroupBy(fe...).OrderBy(append([]string{b.agg(p.Columns[0].agg, p.Columns[0].level, "") + " DESC"}, fe...)...).Limit(p.Limit)
			tuple := fe[0]
			if len(fe) > 1 {
				tuple = "(" + strings.Join(fe, ", ") + ")"
			}
			defer q.WhereIn(tuple, sub) // after the time/user conditions
		}
	}
	for _, w := range where {
		q.Where(w)
	}
	if b.err != nil {
		return nil, b.err
	}
	b.apply(q)
	if sub != nil {
		b.apply(sub)
	}
	return q, nil
}

func (b *sqlBuilder) columns() []string {
	out := make([]string, len(b.plan.Columns))
	for i, c := range b.plan.Columns {
		out[i] = b.column(c) + " AS a" + strconv.Itoa(i)
	}
	return out
}

// buildWindows returns a SELECT computing single/facets plans for every window [e_j - window, e_j) with
// e_j = origin + j·step, j = 1..n (alert ranges). The column ei is j + k where k = ceil(window/step).
func (p *Plan) buildWindows(sc *query.Scope, origin time.Time, step, window time.Duration, n, maxRows int) (*query.Select, int, error) {
	b := newBuilder(p, window)
	b.rollup = false
	k := int((window + step - 1) / step)
	q := b.source(sc)
	from := origin.Add(step - window)
	to := origin.Add(time.Duration(n) * step)
	where := b.filters(from, to)
	shifted := origin.Add(-time.Duration(k) * step)
	b.params["w_origin"] = shifted.UnixNano()
	b.params["w_step"] = int64(step)
	b.params["w_win"] = int64(window)
	d := b.timeNanos() + " - {w_origin:Int64}"
	ei := "arrayJoin(range(toUInt64(intDiv(" + d + ", {w_step:Int64}) + 1), toUInt64(intDiv(" + d + " + {w_win:Int64}, {w_step:Int64}) + 1)))"
	fe := b.facetExprs()
	cols := make([]string, 0, len(fe)+len(p.Columns)+1)
	groups := make([]string, 0, len(fe)+1)
	for i, f := range fe {
		alias := "f" + strconv.Itoa(i)
		cols = append(cols, f+" AS "+alias)
		groups = append(groups, alias)
	}
	cols = append(cols, ei+" AS ei")
	groups = append(groups, "ei")
	q.Columns(append(cols, b.columns()...)...).GroupBy(groups...).Limit(maxRows + 1)
	for _, w := range where {
		q.Where(w)
	}
	if b.err != nil {
		return nil, 0, b.err
	}
	b.apply(q)
	return q, k, nil
}

// SQL renders the main statement for tests and debugging.
func (p *Plan) SQL(sc *query.Scope) (string, map[string]string, error) {
	q, err := p.build(sc, p.From, p.To)
	if err != nil {
		return "", nil, err
	}
	return q.Build()
}
