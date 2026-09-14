package oql

import (
	"strconv"
	"strings"
)

// MaxFilters bounds the dashboard cross-widget filters of one query (oql.md §7 "Dashboard filters").
const MaxFilters = 10

// Filter is a dashboard cross-widget filter: attribute = value, ANDed with the query's WHERE clause when the query's
// event type has the attribute. Like variable values, the value is always a bound parameter.
type Filter struct {
	// Attribute as written in OQL: a known attribute (host.name), attributes['k'], resource['k'] / resource.k or a
	// backtick-quoted name.
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	// EventType is the event type of the widget the filter was taken from. Attribute names that are not known
	// attributes (implicit attributes['name'] lookups) are applied only to queries of this event type.
	EventType string `json:"event_type,omitempty"`
}

// ParseAttribute parses one attribute reference as written in OQL.
func ParseAttribute(src string) (*Attr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	a, err := p.attr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, unexpected(t, "the end of the attribute")
	}
	return a, nil
}

// applyFilters appends the applicable filters to the WHERE clause of the plan's query; the others are recorded in
// Plan.IgnoredFilters. Errors only for malformed filters (unparsable attribute, too long value, too many filters).
func (c *compiler) applyFilters(fs []Filter) error {
	if len(fs) == 0 {
		return nil
	}
	if len(fs) > MaxFilters {
		return errAt(0, 0, "at most %d dashboard filters", MaxFilters)
	}
	p := c.plan
	for i, f := range fs {
		a, err := ParseAttribute(f.Attribute)
		if err != nil {
			return errAt(0, 0, "filters[%d].attribute: %s", i, errMessage(err))
		}
		if len(f.Value) > maxStringBytes || strings.ContainsFunc(f.Value, isControl) {
			return errAt(0, 0, "filters[%d].value must be at most %d bytes without control characters", i, maxStringBytes)
		}
		pr, ok := c.filterPredicate(a, f)
		if !ok {
			p.IgnoredFilters = append(p.IgnoredFilters, a.String())
			continue
		}
		if p.Query.Where == nil {
			p.Query.Where = pr
		} else {
			p.Query.Where = &Logical{Op: "and", L: p.Query.Where, R: pr}
		}
	}
	return nil
}

// filterPredicate resolves a filter against the plan's event type; ok=false when the event type does not have the
// attribute or the value does not fit its type.
func (c *compiler) filterPredicate(a *Attr, f Filter) (*Predicate, bool) {
	et := c.plan.Event
	switch {
	case a.Map == "attributes" && et.attrMap != "", a.Map == "resource" && et.resource != "":
		a.res = &resolved{mapKind: a.Map, key: a.Key}
	case a.Map != "":
		return nil, false
	default:
		if def, ok := et.byName[a.Name]; ok {
			a.res = &resolved{def: def}
			break
		}
		if a.Name == "tenant_id" || strings.HasPrefix(a.Name, "_") || et.attrMap == "" || f.EventType != et.name {
			return nil, false
		}
		a.res = &resolved{mapKind: "attributes", key: a.Name, implicit: true}
	}
	v := Value{Kind: ValString, Str: f.Value}
	switch a.res.typ() {
	case TNumber:
		if a.res.isMap() {
			break
		}
		n, err := strconv.ParseFloat(f.Value, 64)
		if err != nil {
			return nil, false
		}
		v = Value{Kind: ValNumber, Num: n, Str: f.Value}
	case TBool:
		b, ok := boolOf(v)
		if !ok {
			return nil, false
		}
		v = Value{Kind: ValBool, Bool: b, Str: f.Value}
	}
	pr := &Predicate{Attr: a, Op: "=", Values: []Value{v}}
	if checkValue(pr, a.res, v) != nil {
		return nil, false
	}
	return pr, true
}

func errMessage(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Msg
	}
	return err.Error()
}
