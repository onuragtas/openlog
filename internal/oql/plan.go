package oql

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is the result shape.
type Kind string

// Result kinds.
const (
	KindSingle     Kind = "single"
	KindFacets     Kind = "facets"
	KindTimeseries Kind = "timeseries"
	KindHistogram  Kind = "histogram"
)

// Plan limits (oql.md §5).
const (
	DefaultLimit        = 10
	MaxTableLimit       = 2000
	MaxTimeseriesFacets = 50
	maxColumns          = 30
	maxSeries           = 500
	maxPoints           = 100_000
	maxBuckets          = 1000
	autoBuckets         = 300
	maxLevels           = 10
	maxCompare          = 400 * 24 * time.Hour
)

var niceBuckets = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 10 * time.Minute,
	15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// Options control Compile.
type Options struct {
	// Now is the reference time of relative ranges (default time.Now()).
	Now time.Time
	// From and To, when both set, override SINCE/UNTIL.
	From, To time.Time
	// Variables are dashboard variable values ({{name}}).
	Variables map[string][]string
	// DefaultLimit is the facet limit without LIMIT (default 10); MaxLimit caps LIMIT without TIMESERIES (default 2000).
	DefaultLimit, MaxLimit int
	// NoRollup forces raw Metric data points.
	NoRollup bool
	// Filters are dashboard cross-widget filters (filters.go), ANDed with WHERE where the event type has the attribute.
	Filters []Filter
}

// resolved is the attribute an AST Attr refers to.
type resolved struct {
	def      *attrDef // nil for map lookups
	mapKind  string   // "attributes" or "resource" for map lookups
	key      string
	implicit bool
}

func (r *resolved) typ() AttrType {
	if r.def != nil {
		return r.def.typ
	}
	return TString
}

func (r *resolved) isMap() bool { return r.def == nil }

// Column is one result column.
type Column struct {
	Name     string
	Function string
	Type     AttrType // TNumber or TString
	agg      *Agg
	level    float64
	zeroFill bool
}

// Plan is a validated query.
type Plan struct {
	Source    string
	Query     *Query
	Event     *eventType
	Kind      Kind
	From, To  time.Time
	Bucket    time.Duration // timeseries
	Rollup    bool
	Columns   []Column
	Facets    []*Attr
	Limit     int
	Compare   time.Duration
	Warnings  []*Error
	Variables []string // referenced variable names, sorted
	// IgnoredFilters are dashboard filters (Options.Filters) that do not apply to this event type.
	IgnoredFilters []string

	vars map[string][]string
	hist *Agg
}

// EventTypeName returns the canonical event type.
func (p *Plan) EventTypeName() string { return p.Event.name }

// Table returns the ClickHouse table the plan reads.
func (p *Plan) Table() string {
	if p.Rollup {
		return "metrics_1m"
	}
	return map[string]string{"Log": "logs", "Span": "spans", "Transaction": "spans", "Metric": "metrics", "Host": "hosts", "Container": "containers", "Profile": "profiles"}[p.Event.name]
}

// FacetNames returns the facet attribute names as written.
func (p *Plan) FacetNames() []string {
	out := make([]string, len(p.Facets))
	for i, f := range p.Facets {
		out[i] = f.String()
	}
	return out
}

// Compile parses and validates src.
func Compile(src string, opt Options) (*Plan, error) {
	q, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return compile(src, q, opt)
}

type compiler struct {
	plan *Plan
	vars map[string]bool
}

func compile(src string, q *Query, opt Options) (*Plan, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	opt.Now = opt.Now.UTC()
	if opt.DefaultLimit <= 0 {
		opt.DefaultLimit = DefaultLimit
	}
	if opt.MaxLimit <= 0 {
		opt.MaxLimit = MaxTableLimit
	}
	et := lookupEventType(q.EventType)
	p := &Plan{Source: src, Query: q, Event: et, Facets: q.Facets, vars: opt.Variables}
	c := &compiler{plan: p, vars: map[string]bool{}}

	// select list
	for _, it := range q.Select {
		if err := c.selectItem(it, len(q.Select)); err != nil {
			return nil, err
		}
	}
	if len(p.Columns) > maxColumns {
		return nil, q.Select[len(q.Select)-1].Span.err("at most %d result columns (percentile levels count separately)", maxColumns)
	}
	for _, f := range q.Facets {
		if err := c.resolve(f); err != nil {
			return nil, err
		}
	}
	if err := c.applyFilters(opt.Filters); err != nil {
		return nil, err
	}
	if q.Where != nil {
		if err := c.cond(q.Where); err != nil {
			return nil, err
		}
	}
	for v := range c.vars {
		p.Variables = append(p.Variables, v)
	}
	sort.Strings(p.Variables)

	// kind
	switch {
	case p.hist != nil:
		p.Kind = KindHistogram
		for _, bad := range []struct {
			present bool
			sp      Span
			what    string
		}{{len(q.Facets) > 0, spanOfAttrs(q.Facets), "FACET"}, {q.Timeseries != nil, spanOfTS(q.Timeseries), "TIMESERIES"}, {q.Compare != nil, spanOfDur(q.Compare), "COMPARE WITH"}} {
			if bad.present {
				return nil, bad.sp.err("histogram cannot be combined with %s", bad.what)
			}
		}
	case q.Timeseries != nil:
		p.Kind = KindTimeseries
	case len(q.Facets) > 0:
		p.Kind = KindFacets
	default:
		p.Kind = KindSingle
	}

	// time range
	now := opt.Now
	anchor := q.EventSpan
	if !opt.From.IsZero() && !opt.To.IsZero() {
		p.From, p.To = opt.From.UTC(), opt.To.UTC()
	} else {
		p.From, p.To = now.Add(-time.Hour), now
		if q.Since != nil {
			p.From, anchor = resolveTime(q.Since, now), q.Since.Span
		}
		if q.Until != nil {
			p.To = resolveTime(q.Until, now)
			if q.Since == nil {
				anchor = q.Until.Span
			}
		}
	}
	if !p.From.Before(p.To) {
		return nil, anchor.err("the start of the time range must be before its end")
	}
	if p.From.Before(now.Add(-maxLookback)) {
		return nil, anchor.err("the time range starts more than %d days ago", int(maxLookback/(24*time.Hour)))
	}
	rng := p.To.Sub(p.From)
	if rng > et.maxRange {
		return nil, anchor.err("the time range of %s queries is at most %d days", et.name, int(et.maxRange/(24*time.Hour)))
	}

	// limit
	limitCap := opt.MaxLimit
	if p.Kind == KindTimeseries {
		limitCap = min(limitCap, MaxTimeseriesFacets)
	}
	p.Limit = min(opt.DefaultLimit, limitCap)
	if l := q.Limit; l != nil {
		switch {
		case l.Max:
			p.Limit = limitCap
		case l.N > limitCap:
			return nil, l.Span.err("LIMIT must be at most %d here", limitCap)
		default:
			p.Limit = l.N
		}
	}

	// rollup (Metric) and buckets
	p.Rollup = et.rollup && !opt.NoRollup && rng > rollupMinRange && c.rollupEligible()
	if p.Kind == KindTimeseries {
		if err := c.buckets(rng); err != nil {
			return nil, err
		}
	}
	if et.rollup && !p.Rollup && rng > maxRawRange {
		return nil, anchor.err("Metric queries longer than %d days must be answerable from the 1-minute rollup (oql.md §4)", int(maxRawRange/(24*time.Hour)))
	}

	if p.Kind == KindTimeseries {
		groups := 1
		if len(q.Facets) > 0 {
			groups = p.Limit
		}
		series := groups * len(p.Columns)
		if series > maxSeries {
			return nil, spanOfTS(q.Timeseries).err("too many series (%d facets x %d columns > %d); lower LIMIT or remove columns", groups, len(p.Columns), maxSeries)
		}
		if nb := bucketCount(p.From, p.To, p.Bucket); series*nb > maxPoints {
			return nil, spanOfTS(q.Timeseries).err("too many points (%d series x %d buckets > %d); use a larger bucket or lower LIMIT", series, nb, maxPoints)
		}
	}

	if q.Compare != nil {
		if q.Compare.D < time.Minute || q.Compare.D > maxCompare {
			return nil, q.Compare.Span.err("COMPARE WITH must be between 1 minute and %d days", int(maxCompare/(24*time.Hour)))
		}
		p.Compare = q.Compare.D
	}
	return p, nil
}

func spanOfAttrs(as []*Attr) Span {
	if len(as) == 0 {
		return Span{}
	}
	return Span{as[0].Span.Pos, as[len(as)-1].Span.End}
}

func spanOfTS(t *TimeseriesClause) Span {
	if t == nil {
		return Span{}
	}
	return t.Span
}

func spanOfDur(d *DurationLit) Span {
	if d == nil {
		return Span{}
	}
	return d.Span
}

func resolveTime(t *TimeSpec, now time.Time) time.Time {
	switch t.Kind {
	case TimeAgo:
		return now.Add(-t.Ago)
	case TimeAbsolute:
		return t.At
	}
	return now
}

func bucketCount(from, to time.Time, bucket time.Duration) int {
	if bucket <= 0 {
		return 0
	}
	start := alignDown(from, bucket)
	return int((to.Sub(start) + bucket - 1) / bucket)
}

func alignDown(t time.Time, d time.Duration) time.Time {
	ms := t.UnixMilli()
	step := d.Milliseconds()
	return time.UnixMilli(ms - ((ms%step)+step)%step).UTC()
}

func (c *compiler) buckets(rng time.Duration) error {
	p := c.plan
	ts := p.Query.Timeseries
	minBucket := 10 * time.Second
	if ts.Auto {
		if p.Rollup {
			minBucket = time.Minute
		}
		want := max(rng/autoBuckets, minBucket)
		p.Bucket = niceBuckets[len(niceBuckets)-1]
		for _, nb := range niceBuckets {
			if nb >= want {
				p.Bucket = nb
				break
			}
		}
	} else {
		if ts.Bucket < minBucket {
			return ts.Span.err("the TIMESERIES bucket must be at least 10 seconds")
		}
		if ts.Bucket%time.Second != 0 {
			return ts.Span.err("the TIMESERIES bucket must be a whole number of seconds")
		}
		p.Bucket = ts.Bucket
		if p.Rollup && p.Bucket%time.Minute != 0 {
			p.Rollup = false // the rollup has minute resolution
		}
	}
	if nb := bucketCount(p.From, p.To, p.Bucket); nb > maxBuckets {
		return ts.Span.err("TIMESERIES would produce %d buckets (at most %d); use a larger bucket", nb, maxBuckets)
	}
	return nil
}

// ---- attributes ----

func (c *compiler) resolve(a *Attr) error {
	et := c.plan.Event
	if a.res != nil {
		return nil
	}
	if a.Map != "" {
		switch {
		case a.Map == "attributes" && et.attrMap == "":
			return a.Span.err("%s has no attributes map", et.name)
		case a.Map == "resource" && et.resource == "":
			return a.Span.err("%s has no resource attributes", et.name)
		}
		a.res = &resolved{mapKind: a.Map, key: a.Key}
		return nil
	}
	if def, ok := et.byName[a.Name]; ok {
		a.res = &resolved{def: def}
		return nil
	}
	if a.Name == "tenant_id" || strings.HasPrefix(a.Name, "_") {
		return a.Span.err("unknown attribute %q for %s", truncate(a.Name, 64), et.name)
	}
	if et.attrMap == "" {
		return a.Span.err("unknown attribute %q for %s", truncate(a.Name, 64), et.name)
	}
	a.res = &resolved{mapKind: "attributes", key: a.Name, implicit: true}
	c.plan.Warnings = append(c.plan.Warnings, a.Span.err("%q is not a known attribute of %s; it is read from attributes['%s']", truncate(a.Name, 64), et.name, truncate(a.Name, 64)))
	return nil
}

// numeric reports whether a can be used as a number (number or bool attributes, map values converted).
func numeric(a *Attr) bool { return a.res.isMap() || a.res.typ() != TString }

// ---- select items ----

var zeroFillFuncs = map[string]bool{fnCount: true, fnSum: true, fnUniqueCount: true}

func (c *compiler) selectItem(it *SelectItem, items int) error {
	a := it.Agg
	if a.Func == fnHistogram {
		if items != 1 {
			return a.Span.err("histogram must be the only select item")
		}
		if err := c.resolve(a.Arg); err != nil {
			return err
		}
		if !numeric(a.Arg) {
			return a.Arg.Span.err("histogram needs a number attribute; %s is a string", a.Arg)
		}
		if a.Ceiling <= 0 || a.Ceiling > 1e15 {
			return a.Span.err("histogram ceiling must be a positive number")
		}
		c.plan.hist = a
		c.plan.Columns = append(c.plan.Columns, Column{Name: "histogram(" + a.ArgsText + ")", Function: fnHistogram, Type: TNumber, agg: a, zeroFill: true})
		return nil
	}
	typ, err := c.agg(a, 0)
	if err != nil {
		return err
	}
	base := a
	for base.Func == fnRate || base.Func == fnFilter {
		base = base.Inner
	}
	zero := zeroFillFuncs[base.Func] || a.Func == fnRate
	fname := a.Func
	if fname == fnUniqueCount {
		fname = "uniqueCount"
	}
	name := it.Alias
	if name == "" {
		name = a.Name + "(" + a.ArgsText + ")"
	}
	if len(base.Levels) > 0 {
		multi := len(base.Levels) > 1
		for _, lv := range base.Levels {
			cn := name
			switch {
			case it.Alias == "" && base == a:
				cn = columnWithLevel(a, lv)
			case multi:
				cn = name + " (" + formatNum(lv) + ")"
			}
			c.plan.Columns = append(c.plan.Columns, Column{Name: cn, Function: fname, Type: TNumber, agg: a, level: lv, zeroFill: zero})
		}
		return nil
	}
	c.plan.Columns = append(c.plan.Columns, Column{Name: name, Function: fname, Type: typ, agg: a, zeroFill: zero && typ == TNumber})
	return nil
}

// columnWithLevel names one level of a percentile column: percentile(duration.ms, 95).
func columnWithLevel(a *Agg, lv float64) string {
	switch a.Func {
	case fnPercentile:
		return "percentile(" + a.Arg.String() + ", " + formatNum(lv) + ")"
	case fnMedian:
		return "median(" + a.Arg.String() + ")"
	}
	return a.Name + "(" + a.ArgsText + ") (" + formatNum(lv) + ")"
}

func formatNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// agg validates an aggregate and returns its result type. depth counts rate/filter wrappers.
func (c *compiler) agg(a *Agg, depth int) (AttrType, error) {
	if a.Arg != nil {
		if err := c.resolve(a.Arg); err != nil {
			return 0, err
		}
	}
	switch a.Func {
	case fnCount, fnUniqueCount:
		return TNumber, nil
	case fnSum, fnAverage, fnMin, fnMax:
		if !numeric(a.Arg) {
			return 0, a.Arg.Span.err("%s needs a number attribute; %s is a string", a.Name, a.Arg)
		}
		return TNumber, nil
	case fnPercentile, fnMedian:
		if !numeric(a.Arg) {
			return 0, a.Arg.Span.err("%s needs a number attribute; %s is a string", a.Name, a.Arg)
		}
		if len(a.Levels) > maxLevels {
			return 0, a.Span.err("at most %d percentile levels", maxLevels)
		}
		for _, lv := range a.Levels {
			if lv <= 0 || lv >= 100 {
				return 0, a.Span.err("percentile levels must be between 0 and 100 (exclusive)")
			}
		}
		return TNumber, nil
	case fnLatest, fnEarliest:
		if a.Arg.res.typ() == TString {
			return TString, nil
		}
		return TNumber, nil
	case fnRate:
		inner := a.Inner
		if inner.Func == fnFilter {
			inner = inner.Inner
		}
		switch inner.Func {
		case fnCount, fnSum, fnUniqueCount:
		default:
			return 0, a.Inner.Span.err("rate takes count, sum or uniqueCount (optionally inside filter)")
		}
		if a.Per.D < time.Second {
			return 0, a.Per.Span.err("rate duration must be at least 1 second")
		}
		return c.agg(a.Inner, depth+1)
	case fnFilter:
		switch a.Inner.Func {
		case fnRate, fnFilter, fnHistogram:
			return 0, a.Inner.Span.err("filter cannot contain %s", a.Inner.Name)
		}
		if err := c.cond(a.Cond); err != nil {
			return 0, err
		}
		return c.agg(a.Inner, depth+1)
	case fnHistogram:
		return 0, a.Span.err("histogram must be the only select item")
	}
	return 0, a.Span.err("unknown function %q", a.Name)
}

// ---- conditions ----

func (c *compiler) cond(e Expr) error {
	switch x := e.(type) {
	case *Logical:
		if err := c.cond(x.L); err != nil {
			return err
		}
		return c.cond(x.R)
	case *Not:
		return c.cond(x.X)
	case *Predicate:
		return c.predicate(x)
	}
	return fmt.Errorf("unexpected condition %T", e)
}

func (c *compiler) predicate(pr *Predicate) error {
	if err := c.resolve(pr.Attr); err != nil {
		return err
	}
	r := pr.Attr.res
	for _, v := range pr.Values {
		if v.Kind == ValVariable {
			c.vars[v.Var] = true
			continue
		}
		if err := checkValue(pr, r, v); err != nil {
			return err
		}
	}
	return nil
}

// checkValue validates a literal against the attribute type and operator.
func checkValue(pr *Predicate, r *resolved, v Value) error {
	name := pr.Attr.String()
	switch pr.Op {
	case "like", "not like", "contains", "not contains":
		kw := strings.ToUpper(strings.TrimPrefix(pr.Op, "not "))
		if !r.isMap() && r.typ() != TString {
			return pr.Span.err("%s needs a string attribute; %s is a %s", kw, name, r.typ())
		}
		if v.Kind != ValString {
			return v.Span.err("%s needs a string value", kw)
		}
		return nil
	}
	switch {
	case r.isMap():
		if v.Kind == ValBool && pr.Op != "=" && pr.Op != "!=" && pr.Op != "in" && pr.Op != "not in" {
			return v.Span.err("cannot order booleans")
		}
	case r.typ() == TString:
		if v.Kind == ValBool {
			return v.Span.err("%s is a string; compare it with a string", name)
		}
	case r.typ() == TNumber:
		switch v.Kind {
		case ValBool:
			return v.Span.err("%s is a number; compare it with a number", name)
		case ValString:
			if _, err := strconv.ParseFloat(v.Str, 64); err != nil {
				return v.Span.err("%s is a number; compare it with a number", name)
			}
		}
	case r.typ() == TBool:
		if pr.Op != "=" && pr.Op != "!=" && pr.Op != "in" && pr.Op != "not in" {
			return pr.Span.err("%s is a boolean; use = or !=", name)
		}
		if _, ok := boolOf(v); !ok {
			return v.Span.err("%s is a boolean; compare it with true or false", name)
		}
	}
	return nil
}

func boolOf(v Value) (bool, bool) {
	switch v.Kind {
	case ValBool:
		return v.Bool, true
	case ValNumber:
		if v.Num == 0 || v.Num == 1 {
			return v.Num == 1, true
		}
	case ValString:
		switch strings.ToLower(v.Str) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	}
	return false, false
}

// ---- rollup eligibility (oql.md §4) ----

func (c *compiler) rollupEligible() bool {
	p := c.plan
	if p.hist != nil {
		return false
	}
	okAttr := func(a *Attr) bool {
		r := a.res
		if r.isMap() {
			return r.mapKind == "attributes"
		}
		return r.def.rollup != ""
	}
	var okCond func(e Expr) bool
	okCond = func(e Expr) bool {
		switch x := e.(type) {
		case *Logical:
			return okCond(x.L) && okCond(x.R)
		case *Not:
			return okCond(x.X)
		case *Predicate:
			return okAttr(x.Attr)
		}
		return false
	}
	var okAgg func(a *Agg, inFilter bool) bool
	okAgg = func(a *Agg, inFilter bool) bool {
		isValue := a.Arg != nil && a.Arg.res.def != nil && a.Arg.res.def.value
		switch a.Func {
		case fnCount:
			return a.Star || isValue || okAttr(a.Arg)
		case fnSum, fnAverage, fnMin, fnMax:
			return isValue
		case fnLatest:
			return isValue && !inFilter
		case fnUniqueCount:
			return !isValue && okAttr(a.Arg)
		case fnRate:
			return okAgg(a.Inner, inFilter)
		case fnFilter:
			return okCond(a.Cond) && okAgg(a.Inner, true)
		}
		return false
	}
	for _, it := range p.Query.Select {
		if !okAgg(it.Agg, false) {
			return false
		}
	}
	for _, f := range p.Facets {
		if !okAttr(f) {
			return false
		}
	}
	if w := p.Query.Where; w != nil && !okCond(w) {
		return false
	}
	return true
}

// ---- validation for editors ----

// Validation is the result of Validate.
type Validation struct {
	Valid     bool         `json:"valid"`
	EventType *string      `json:"event_type"`
	Kind      *string      `json:"kind"`
	Variables []string     `json:"variables"`
	Errors    []Diagnostic `json:"errors"`
	Warnings  []Diagnostic `json:"warnings"`
}

// Validate compiles src and reports errors and warnings with positions.
func Validate(src string, opt Options) Validation {
	v := Validation{Variables: []string{}, Errors: []Diagnostic{}, Warnings: []Diagnostic{}}
	q, err := Parse(src)
	if err == nil {
		et := q.EventType
		v.EventType = &et
		var p *Plan
		if p, err = compile(src, q, opt); err == nil {
			v.Valid = true
			k := string(p.Kind)
			v.Kind = &k
			v.Variables = append(v.Variables, p.Variables...)
			for _, w := range p.Warnings {
				v.Warnings = append(v.Warnings, Diagnose(src, w))
			}
		}
	}
	if err != nil {
		v.Errors = append(v.Errors, Diagnose(src, err))
	}
	return v
}

// finiteOrNil converts NaN/Inf to nil.
func finiteOrNil(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}
