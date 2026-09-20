package oql

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Parse limits (oql.md §5).
const (
	maxSelectItems = 20
	maxFacets      = 5
	maxInValues    = 500
	maxPredicates  = 100
	maxAliasBytes  = 128
	maxMapKeyBytes = 256
)

// reserved keywords cannot be used as bare attribute names (backticks allow them).
var reserved = map[string]bool{
	"select": true, "from": true, "where": true, "facet": true, "since": true, "until": true, "timeseries": true,
	"limit": true, "compare": true, "with": true, "as": true, "and": true, "or": true, "not": true, "in": true,
	"like": true, "is": true, "null": true,
}

// Keywords lists the OQL keywords (for editors).
var Keywords = []string{"SELECT", "FROM", "WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "AUTO", "LIMIT", "MAX",
	"COMPARE", "WITH", "AGO", "AS", "AND", "OR", "NOT", "IN", "LIKE", "CONTAINS", "IS", "NULL", "NOW", "TRUE", "FALSE"}

var durationUnits = map[string]time.Duration{
	"second": time.Second, "seconds": time.Second, "sec": time.Second, "secs": time.Second, "s": time.Second,
	"minute": time.Minute, "minutes": time.Minute, "min": time.Minute, "mins": time.Minute, "m": time.Minute,
	"hour": time.Hour, "hours": time.Hour, "hr": time.Hour, "hrs": time.Hour, "h": time.Hour,
	"day": 24 * time.Hour, "days": 24 * time.Hour, "d": 24 * time.Hour,
	"week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour, "w": 7 * 24 * time.Hour,
	"year": 365 * 24 * time.Hour, "years": 365 * 24 * time.Hour,
}

var funcNames = map[string]string{
	"count": fnCount, "sum": fnSum, "average": fnAverage, "avg": fnAverage, "min": fnMin, "max": fnMax,
	"uniquecount": fnUniqueCount, "percentile": fnPercentile, "median": fnMedian, "latest": fnLatest,
	"earliest": fnEarliest, "rate": fnRate, "filter": fnFilter, "histogram": fnHistogram,
}

type parser struct {
	src   string
	toks  []token
	i     int
	depth int
	preds int
}

// Parse parses an OQL query. Errors are *Error with the position of the offending token.
func Parse(src string) (*Query, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	return p.query()
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) peekAt(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func isKeyword(t token, kw string) bool { return t.kind == tokIdent && strings.EqualFold(t.text, kw) }

func (p *parser) accept(kind tokenKind) (token, bool) {
	if t := p.peek(); t.kind == kind {
		return p.next(), true
	}
	return token{}, false
}

func (p *parser) acceptKeyword(kw string) (token, bool) {
	if t := p.peek(); isKeyword(t, kw) {
		return p.next(), true
	}
	return token{}, false
}

func unexpected(t token, want string) *Error {
	return errAt(t.pos, t.end, "expected %s, found %s", want, t.describe())
}

func (p *parser) expect(kind tokenKind) (token, error) {
	t := p.next()
	if t.kind != kind {
		return t, unexpected(t, tokenNames[kind])
	}
	return t, nil
}

func (p *parser) expectKeyword(kw string) (token, error) {
	t := p.next()
	if !isKeyword(t, kw) {
		return t, unexpected(t, kw)
	}
	return t, nil
}

func (p *parser) enter(t token) error {
	p.depth++
	if p.depth > maxDepth {
		return errAt(t.pos, t.end, "query is nested more than %d levels deep", maxDepth)
	}
	return nil
}

func (p *parser) query() (*Query, error) {
	if _, err := p.expectKeyword("SELECT"); err != nil {
		return nil, err
	}
	q := &Query{}
	for {
		it, err := p.item()
		if err != nil {
			return nil, err
		}
		if len(q.Select) == maxSelectItems {
			return nil, it.Span.err("at most %d select items", maxSelectItems)
		}
		q.Select = append(q.Select, it)
		if _, ok := p.accept(tokComma); !ok {
			break
		}
	}
	if _, err := p.expectKeyword("FROM"); err != nil {
		return nil, err
	}
	et := p.next()
	if et.kind != tokIdent && et.kind != tokQuotedIdent {
		return nil, unexpected(et, "an event type (Log, Span, Transaction, Metric, Host or Container)")
	}
	def := lookupEventType(et.text)
	if def == nil {
		return nil, errAt(et.pos, et.end, "unknown event type %q (expected Log, Span, Transaction, Metric, Host, Container or Profile)", truncate(et.text, 64))
	}
	q.EventType, q.EventSpan = def.name, Span{et.pos, et.end}

	seen := map[string]bool{}
	for {
		t := p.peek()
		if t.kind == tokEOF {
			return q, nil
		}
		kw := ""
		if t.kind == tokIdent {
			kw = strings.ToUpper(t.text)
		}
		switch kw {
		case "WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "LIMIT", "COMPARE":
		default:
			return nil, unexpected(t, "WHERE, FACET, SINCE, UNTIL, TIMESERIES, LIMIT or COMPARE WITH")
		}
		if seen[kw] {
			return nil, errAt(t.pos, t.end, "duplicate %s clause", kw)
		}
		seen[kw] = true
		p.next()
		var err error
		switch kw {
		case "WHERE":
			q.Where, err = p.cond()
		case "FACET":
			for {
				var a *Attr
				if a, err = p.attr(); err != nil {
					break
				}
				if len(q.Facets) == maxFacets {
					return nil, a.Span.err("at most %d FACET attributes", maxFacets)
				}
				q.Facets = append(q.Facets, a)
				if _, ok := p.accept(tokComma); !ok {
					break
				}
			}
		case "SINCE":
			q.Since, err = p.timeSpec()
		case "UNTIL":
			q.Until, err = p.timeSpec()
		case "TIMESERIES":
			q.Timeseries, err = p.timeseries(t)
		case "LIMIT":
			q.Limit, err = p.limit(t)
		case "COMPARE":
			if _, err = p.expectKeyword("WITH"); err == nil {
				if q.Compare, err = p.duration(); err == nil {
					_, err = p.expectKeyword("AGO")
					q.Compare.Span.Pos = t.pos
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) item() (*SelectItem, error) {
	a, err := p.agg()
	if err != nil {
		return nil, err
	}
	it := &SelectItem{Agg: a, Span: a.Span}
	if _, ok := p.acceptKeyword("AS"); ok {
		t := p.next()
		if (t.kind != tokIdent && t.kind != tokString && t.kind != tokQuotedIdent) || (t.kind == tokIdent && reserved[strings.ToLower(t.text)]) {
			return nil, unexpected(t, "an alias")
		}
		if t.text == "" || len(t.text) > maxAliasBytes || strings.ContainsFunc(t.text, isControl) {
			return nil, errAt(t.pos, t.end, "alias must be 1-%d bytes without control characters", maxAliasBytes)
		}
		it.Alias = t.text
		it.Span.End = t.end
	}
	return it, nil
}

func (p *parser) agg() (*Agg, error) {
	t := p.next()
	if err := p.enter(t); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	if t.kind != tokIdent {
		return nil, unexpected(t, "an aggregate function such as count(*)")
	}
	fn, ok := funcNames[strings.ToLower(t.text)]
	if !ok {
		if p.peek().kind == tokLParen {
			return nil, errAt(t.pos, t.end, "unknown function %q", truncate(t.text, 64))
		}
		return nil, errAt(t.pos, t.end, "expected an aggregate function such as count(*), found %q (select items must be aggregates)", truncate(t.text, 64))
	}
	lp, err := p.expect(tokLParen)
	if err != nil {
		return nil, err
	}
	a := &Agg{Func: fn, Name: t.text}
	switch fn {
	case fnCount:
		if _, ok := p.accept(tokStar); ok {
			a.Star = true
		} else if a.Arg, err = p.attr(); err != nil {
			return nil, err
		}
	case fnSum, fnAverage, fnMin, fnMax, fnUniqueCount, fnMedian, fnLatest, fnEarliest:
		if a.Arg, err = p.attr(); err != nil {
			return nil, err
		}
		if fn == fnMedian {
			a.Levels = []float64{50}
		}
	case fnPercentile:
		if a.Arg, err = p.attr(); err != nil {
			return nil, err
		}
		if _, err = p.expect(tokComma); err != nil {
			return nil, err
		}
		for {
			v, _, err := p.number()
			if err != nil {
				return nil, err
			}
			a.Levels = append(a.Levels, v)
			if _, ok := p.accept(tokComma); !ok {
				break
			}
		}
	case fnRate:
		if a.Inner, err = p.agg(); err != nil {
			return nil, err
		}
		if _, err = p.expect(tokComma); err != nil {
			return nil, err
		}
		if a.Per, err = p.duration(); err != nil {
			return nil, err
		}
	case fnFilter:
		if a.Inner, err = p.agg(); err != nil {
			return nil, err
		}
		if _, err = p.expect(tokComma); err != nil {
			return nil, err
		}
		if _, err = p.expectKeyword("WHERE"); err != nil {
			return nil, err
		}
		if a.Cond, err = p.cond(); err != nil {
			return nil, err
		}
	case fnHistogram:
		if a.Arg, err = p.attr(); err != nil {
			return nil, err
		}
		if _, err = p.expect(tokComma); err != nil {
			return nil, err
		}
		if a.Ceiling, _, err = p.number(); err != nil {
			return nil, err
		}
		a.Buckets = 40
		if _, ok := p.accept(tokComma); ok {
			v, sp, err := p.number()
			if err != nil {
				return nil, err
			}
			if v != math.Trunc(v) || v < 1 || v > 200 {
				return nil, sp.err("histogram buckets must be an integer between 1 and 200")
			}
			a.Buckets = int(v)
		}
	}
	rp, err := p.expect(tokRParen)
	if err != nil {
		return nil, err
	}
	a.Span = Span{t.pos, rp.end}
	a.ArgsText = strings.Join(strings.Fields(p.src[lp.end:rp.pos]), " ")
	return a, nil
}

func (p *parser) number() (float64, Span, error) {
	t := p.next()
	neg := false
	start := t.pos
	if t.kind == tokMinus {
		neg = true
		t = p.next()
	}
	if t.kind != tokNumber {
		return 0, Span{}, unexpected(t, "a number")
	}
	v, err := strconv.ParseFloat(t.text, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, Span{}, errAt(start, t.end, "invalid number")
	}
	if neg {
		v = -v
	}
	return v, Span{start, t.end}, nil
}

func (p *parser) attr() (*Attr, error) {
	t := p.next()
	switch t.kind {
	case tokQuotedIdent:
		return &Attr{Name: t.text, Quoted: true, Span: Span{t.pos, t.end}}, nil
	case tokIdent:
	default:
		return nil, unexpected(t, "an attribute")
	}
	if reserved[strings.ToLower(t.text)] {
		return nil, errAt(t.pos, t.end, "expected an attribute, found keyword %s (quote attribute names that are keywords with backticks)", strings.ToUpper(t.text))
	}
	switch t.text {
	case "attributes", "resource", "resource_attributes":
		if _, ok := p.accept(tokLBracket); ok {
			k, err := p.expect(tokString)
			if err != nil {
				return nil, err
			}
			rb, err := p.expect(tokRBracket)
			if err != nil {
				return nil, err
			}
			if err := checkMapKey(k.text, Span{k.pos, k.end}); err != nil {
				return nil, err
			}
			m := "attributes"
			if t.text != "attributes" {
				m = "resource"
			}
			return &Attr{Map: m, Key: k.text, Span: Span{t.pos, rb.end}}, nil
		}
	}
	if strings.HasSuffix(t.text, ".") || strings.Contains(t.text, "..") {
		return nil, errAt(t.pos, t.end, "invalid attribute name %q", truncate(t.text, 64))
	}
	if key, ok := strings.CutPrefix(t.text, "resource."); ok {
		if err := checkMapKey(key, Span{t.pos, t.end}); err != nil {
			return nil, err
		}
		return &Attr{Map: "resource", Key: key, Span: Span{t.pos, t.end}}, nil
	}
	if len(t.text) > maxMapKeyBytes {
		return nil, errAt(t.pos, t.end, "attribute name is longer than %d bytes", maxMapKeyBytes)
	}
	return &Attr{Name: t.text, Span: Span{t.pos, t.end}}, nil
}

func checkMapKey(k string, sp Span) error {
	if k == "" || len(k) > maxMapKeyBytes || strings.ContainsFunc(k, isControl) {
		return sp.err("attribute key must be 1-%d bytes without control characters", maxMapKeyBytes)
	}
	return nil
}

// cond := and (OR and)*
func (p *parser) cond() (Expr, error) {
	if err := p.enter(p.peek()); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.acceptKeyword("OR"); !ok {
			return l, nil
		}
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = &Logical{Op: "or", L: l, R: r}
	}
}

func (p *parser) and() (Expr, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.acceptKeyword("AND"); !ok {
			return l, nil
		}
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		l = &Logical{Op: "and", L: l, R: r}
	}
}

func (p *parser) not() (Expr, error) {
	t := p.peek()
	if isKeyword(t, "NOT") {
		p.next()
		if err := p.enter(t); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return &Not{X: x, Span: Span{t.pos, x.exprSpan().End}}, nil
	}
	if t.kind == tokLParen {
		p.next()
		e, err := p.cond()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, err
		}
		return e, nil
	}
	return p.predicate()
}

func (p *parser) predicate() (Expr, error) {
	a, err := p.attr()
	if err != nil {
		return nil, err
	}
	p.preds++
	if p.preds > maxPredicates {
		return nil, a.Span.err("at most %d conditions", maxPredicates)
	}
	pr := &Predicate{Attr: a}
	t := p.next()
	switch t.kind {
	case tokEq, tokNeq, tokLt, tokLte, tokGt, tokGte:
		pr.Op = map[tokenKind]string{tokEq: "=", tokNeq: "!=", tokLt: "<", tokLte: "<=", tokGt: ">", tokGte: ">="}[t.kind]
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		pr.Values = []Value{v}
	case tokIdent:
		switch strings.ToUpper(t.text) {
		case "IN":
			pr.Op = "in"
			err = p.list(pr)
		case "LIKE", "CONTAINS":
			// CONTAINS is not reserved: an attribute named contains still parses (D-122).
			pr.Op = strings.ToLower(t.text)
			var v Value
			if v, err = p.value(); err == nil {
				pr.Values = []Value{v}
			}
		case "NOT":
			n := p.next()
			switch {
			case isKeyword(n, "IN"):
				pr.Op = "not in"
				err = p.list(pr)
			case isKeyword(n, "LIKE"), isKeyword(n, "CONTAINS"):
				pr.Op = "not " + strings.ToLower(n.text)
				var v Value
				if v, err = p.value(); err == nil {
					pr.Values = []Value{v}
				}
			default:
				return nil, unexpected(n, "IN, LIKE or CONTAINS after NOT")
			}
		case "IS":
			pr.Op = "is null"
			n := p.next()
			if isKeyword(n, "NOT") {
				pr.Op = "is not null"
				n = p.next()
			}
			if !isKeyword(n, "NULL") {
				return nil, unexpected(n, "NULL")
			}
		default:
			return nil, unexpected(t, "a comparison operator (=, !=, <, <=, >, >=, IN, LIKE, CONTAINS, IS NULL)")
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, unexpected(t, "a comparison operator (=, !=, <, <=, >, >=, IN, LIKE, CONTAINS, IS NULL)")
	}
	end := p.toks[p.i-1].end
	pr.Span = Span{a.Span.Pos, end}
	return pr, nil
}

func (p *parser) list(pr *Predicate) error {
	if _, err := p.expect(tokLParen); err != nil {
		return err
	}
	for {
		v, err := p.value()
		if err != nil {
			return err
		}
		if len(pr.Values) == maxInValues {
			return v.Span.err("at most %d values in IN", maxInValues)
		}
		pr.Values = append(pr.Values, v)
		if _, ok := p.accept(tokComma); !ok {
			break
		}
	}
	_, err := p.expect(tokRParen)
	return err
}

func (p *parser) value() (Value, error) {
	t := p.peek()
	switch {
	case t.kind == tokString:
		p.next()
		return Value{Kind: ValString, Str: t.text, Span: Span{t.pos, t.end}}, nil
	case t.kind == tokNumber || t.kind == tokMinus:
		v, sp, err := p.number()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: ValNumber, Num: v, Str: strconv.FormatFloat(v, 'f', -1, 64), Span: sp}, nil
	case isKeyword(t, "TRUE") || isKeyword(t, "FALSE"):
		p.next()
		b := strings.EqualFold(t.text, "true")
		return Value{Kind: ValBool, Bool: b, Str: strconv.FormatBool(b), Span: Span{t.pos, t.end}}, nil
	case t.kind == tokVariable:
		p.next()
		return Value{Kind: ValVariable, Var: t.text, Span: Span{t.pos, t.end}}, nil
	}
	p.next()
	return Value{}, unexpected(t, "a value (string, number, true, false or {{variable}})")
}

func (p *parser) duration() (*DurationLit, error) {
	n, sp, err := p.number()
	if err != nil {
		return nil, err
	}
	u := p.next()
	unit, ok := durationUnits[strings.ToLower(u.text)]
	if u.kind != tokIdent || !ok {
		return nil, unexpected(u, "a time unit (seconds, minutes, hours, days or weeks)")
	}
	sp.End = u.end
	d := n * float64(unit)
	if n <= 0 || d > float64(10*365*24*time.Hour) || d < float64(time.Second) {
		return nil, sp.err("duration must be between 1 second and 10 years")
	}
	return &DurationLit{D: time.Duration(d), Span: sp}, nil
}

var dateLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"}

func (p *parser) timeSpec() (*TimeSpec, error) {
	t := p.peek()
	switch {
	case isKeyword(t, "NOW"):
		p.next()
		return &TimeSpec{Kind: TimeNow, Span: Span{t.pos, t.end}}, nil
	case t.kind == tokString:
		p.next()
		for _, l := range dateLayouts {
			if at, err := time.Parse(l, t.text); err == nil {
				return &TimeSpec{Kind: TimeAbsolute, At: at.UTC(), Span: Span{t.pos, t.end}}, nil
			}
		}
		return nil, errAt(t.pos, t.end, "invalid time %q (use RFC3339 or 'YYYY-MM-DD HH:MM:SS' in UTC)", truncate(t.text, 64))
	case t.kind == tokNumber:
		if u := p.peekAt(1); u.kind == tokIdent && !isKeyword(u, "AGO") {
			if _, ok := durationUnits[strings.ToLower(u.text)]; ok {
				d, err := p.duration()
				if err != nil {
					return nil, err
				}
				ago, err := p.expectKeyword("AGO")
				if err != nil {
					return nil, err
				}
				return &TimeSpec{Kind: TimeAgo, Ago: d.D, Span: Span{d.Span.Pos, ago.end}}, nil
			}
		}
		p.next()
		ms, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil || ms < 0 || ms > 1e15 {
			return nil, errAt(t.pos, t.end, "expected unix milliseconds or '<n> <unit> AGO'")
		}
		return &TimeSpec{Kind: TimeAbsolute, At: time.UnixMilli(ms).UTC(), Span: Span{t.pos, t.end}}, nil
	}
	p.next()
	return nil, unexpected(t, "a time ('<n> <unit> AGO', a date string, unix milliseconds or NOW)")
}

func (p *parser) timeseries(kw token) (*TimeseriesClause, error) {
	ts := &TimeseriesClause{Auto: true, Span: Span{kw.pos, kw.end}}
	t := p.peek()
	switch {
	case isKeyword(t, "AUTO") || isKeyword(t, "MAX"):
		p.next()
		ts.Span.End = t.end
	case t.kind == tokNumber:
		d, err := p.duration()
		if err != nil {
			return nil, err
		}
		ts.Auto, ts.Bucket, ts.Span.End = false, d.D, d.Span.End
	}
	return ts, nil
}

func (p *parser) limit(kw token) (*LimitClause, error) {
	t := p.next()
	if isKeyword(t, "MAX") {
		return &LimitClause{Max: true, Span: Span{kw.pos, t.end}}, nil
	}
	if t.kind != tokNumber {
		return nil, unexpected(t, "a number or MAX")
	}
	n, err := strconv.Atoi(t.text)
	if err != nil || n < 1 || n > 1_000_000 {
		return nil, errAt(t.pos, t.end, "LIMIT must be a positive integer")
	}
	return &LimitClause{N: n, Span: Span{kw.pos, t.end}}, nil
}
