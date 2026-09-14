package oql

import "time"

// Span is a byte range of the query text.
type Span struct{ Pos, End int }

func (s Span) err(format string, args ...any) *Error { return errAt(s.Pos, s.End, format, args...) }

// Query is a parsed OQL query.
type Query struct {
	Select     []*SelectItem
	EventType  string // canonical name (Log, Span, ...)
	EventSpan  Span
	Where      Expr
	Facets     []*Attr
	Since      *TimeSpec
	Until      *TimeSpec
	Timeseries *TimeseriesClause
	Limit      *LimitClause
	Compare    *DurationLit
}

// SelectItem is one aggregate of the select list.
type SelectItem struct {
	Agg   *Agg
	Alias string
	Span  Span
}

// Aggregate function names (normalized, lower case).
const (
	fnCount       = "count"
	fnSum         = "sum"
	fnAverage     = "average"
	fnMin         = "min"
	fnMax         = "max"
	fnUniqueCount = "uniquecount"
	fnPercentile  = "percentile"
	fnMedian      = "median"
	fnLatest      = "latest"
	fnEarliest    = "earliest"
	fnRate        = "rate"
	fnFilter      = "filter"
	fnHistogram   = "histogram"
)

// Agg is an aggregate function call.
type Agg struct {
	Func  string // normalized name (fn* constants)
	Name  string // name as written (for column names)
	Star  bool   // count(*)
	Arg   *Attr
	Inner *Agg // rate, filter
	// Levels are percentile levels (0 < p < 100); median is percentile 50.
	Levels   []float64
	Per      *DurationLit // rate
	Cond     Expr         // filter
	Ceiling  float64      // histogram
	Buckets  int          // histogram
	Span     Span
	ArgsText string // source text between the parentheses (column names)
}

// Attr is an attribute reference: a named attribute, or a map lookup (attributes['k'], resource['k'], resource.k).
type Attr struct {
	Name   string // identifier as written ("" for explicit map lookups)
	Map    string // "", "attributes" or "resource"
	Key    string // map key
	Quoted bool   // backtick identifier
	Span   Span

	res *resolved // set by Compile
}

// String renders the attribute as OQL.
func (a *Attr) String() string {
	switch {
	case a.Name != "":
		if a.Quoted {
			return "`" + a.Name + "`"
		}
		return a.Name
	case a.Map != "":
		return a.Map + "['" + a.Key + "']"
	}
	return ""
}

// Expr is a WHERE condition.
type Expr interface{ exprSpan() Span }

// Logical is AND/OR of two conditions.
type Logical struct {
	Op   string // "and" or "or"
	L, R Expr
}

// Not negates a condition.
type Not struct {
	X    Expr
	Span Span
}

// Predicate compares an attribute.
type Predicate struct {
	Attr   *Attr
	Op     string // = != < <= > >= in "not in" like "not like" "is null" "is not null"
	Values []Value
	Span   Span
}

func (l *Logical) exprSpan() Span   { return Span{l.L.exprSpan().Pos, l.R.exprSpan().End} }
func (n *Not) exprSpan() Span       { return n.Span }
func (p *Predicate) exprSpan() Span { return p.Span }

// Value kinds.
const (
	ValString = iota
	ValNumber
	ValBool
	ValVariable
)

// Value is a literal or variable.
type Value struct {
	Kind int
	Str  string // string value; number text for numbers
	Num  float64
	Bool bool
	Var  string
	Span Span
}

// DurationLit is "<n> <unit>".
type DurationLit struct {
	D    time.Duration
	Span Span
}

// Time spec kinds.
const (
	TimeAgo = iota
	TimeAbsolute
	TimeNow
)

// TimeSpec is the argument of SINCE/UNTIL.
type TimeSpec struct {
	Kind int
	Ago  time.Duration
	At   time.Time
	Span Span
}

// TimeseriesClause is TIMESERIES [bucket|AUTO].
type TimeseriesClause struct {
	Auto   bool
	Bucket time.Duration
	Span   Span
}

// LimitClause is LIMIT n | LIMIT MAX.
type LimitClause struct {
	N    int
	Max  bool
	Span Span
}
