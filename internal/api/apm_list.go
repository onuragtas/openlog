package api

import (
	"cmp"
	"sort"
	"strings"
)

// The sorted, paged lists of the APM screens (api.md "Lists").
//
// Two kinds live here. A list ClickHouse can order gets an ORDER BY from its listSpec, like any other. The rest hold values only Go
// knows, since a p95 and an Apdex come out of a histogram and a workflow status out of PostgreSQL: those read up to a cap, sort in Go
// and cut the page from the result, so their order is complete only up to that cap. Each one says which cap is its own.

// apmDatabasesList is the service's statements. A p95 comes out of a histogram in Go, after the LIMIT, so it has no column here.
var apmDatabasesList = listSpec{
	columns: map[string]string{
		"time": "a_dsum", "calls": "a_req", "slowest": "a_avg", "errors": "a_err", "error_rate": "a_err / greatest(a_req, 1e-9)", "max": "a_max",
		"statement": "db_statement_normalized", "database": "db_name",
	},
	sort:     "time",
	asc:      map[string]bool{"statement": true, "database": true},
	tiebreak: []string{"db_system", "db_name", "db_statement_normalized"},
	limit:    100,
	maxLimit: 1000,
}

// apmTracesList is entry spans. There is no cursor on this endpoint, so a span arriving between two pages moves the rest by one.
var apmTracesList = listSpec{
	columns:  map[string]string{"timestamp": "timestamp", "duration": "duration_ns"},
	sort:     "timestamp",
	tiebreak: []string{"trace_id", "span_id"},
	limit:    50,
	maxLimit: 500,
}

// rowKey is what a row holds in the column it is sorted by.
type rowKey struct {
	blank bool
	num   float64
	text  string
}

func numKey(v float64) rowKey { return rowKey{num: v} }

func optKey(v *float64) rowKey {
	if v == nil {
		return rowKey{blank: true}
	}
	return rowKey{num: *v}
}

func textKey(v string) rowKey { return rowKey{blank: v == "", text: strings.ToLower(v)} }

// sortPage sorts every row it was given and cuts one page, for a list whose values are only known in Go. A column missing from keys is
// a bug, which is why columnsOf builds the spec from the same map. What has no value goes last in either direction, because a blank is
// not the smallest of anything. tie makes the order total, so that a row can't swap pages between two requests.
func sortPage[T any](rows []T, l listRequest, keys map[string]func(T) rowKey, tie func(a, b T) int) ([]T, bool) {
	read := keys[l.sort]
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := read(rows[i]), read(rows[j])
		if a.blank != b.blank {
			return b.blank
		}
		if c := cmp.Or(cmp.Compare(a.num, b.num), strings.Compare(a.text, b.text)); c != 0 {
			return (c < 0) == l.asc
		}
		return tie(rows[i], rows[j]) < 0
	})
	if l.offset >= len(rows) {
		return rows[:0], false
	}
	if end := l.offset + l.limit; end < len(rows) {
		return rows[l.offset:end], true
	}
	return rows[l.offset:], false
}

// columnsOf is the columns of a list sorted in Go. They order no statement, so a name only has to exist.
func columnsOf[T any](keys map[string]func(T) rowKey) map[string]string {
	out := make(map[string]string, len(keys))
	for name := range keys {
		out[name] = ""
	}
	return out
}

// ---- transactions ----

var transactionKeys = map[string]func(transactionJSON) rowKey{
	"time":       func(t transactionJSON) rowKey { return numKey(t.TimeConsumedMs) },
	"throughput": func(t transactionJSON) rowKey { return numKey(t.Requests) },
	"avg":        func(t transactionJSON) rowKey { return optKey(t.AvgMs) },
	"slowest":    func(t transactionJSON) rowKey { return optKey(t.P95Ms) },
	"errors":     func(t transactionJSON) rowKey { return numKey(t.Errors) },
	"error_rate": func(t transactionJSON) rowKey { return numKey(t.ErrorRate) },
	"apdex":      func(t transactionJSON) rowKey { return optKey(t.Apdex) },
	"name":       func(t transactionJSON) rowKey { return textKey(t.TransactionName) },
}

var apmTransactionsList = listSpec{
	columns:  columnsOf(transactionKeys),
	sort:     "time",
	asc:      map[string]bool{"name": true},
	limit:    100,
	maxLimit: 1000,
}

func transactionTie(a, b transactionJSON) int {
	return cmp.Or(strings.Compare(a.TransactionName, b.TransactionName), strings.Compare(a.TransactionType, b.TransactionType))
}

// ---- services ----

var serviceKeys = map[string]func(*apmServiceJSON) rowKey{
	"name":        func(s *apmServiceJSON) rowKey { return textKey(s.ServiceName) },
	"environment": func(s *apmServiceJSON) rowKey { return textKey(s.Environment) },
	"throughput":  func(s *apmServiceJSON) rowKey { return numKey(s.Throughput) },
	"error_rate":  func(s *apmServiceJSON) rowKey { return numKey(s.ErrorRate) },
	"p95":         func(s *apmServiceJSON) rowKey { return optKey(s.P95Ms) },
	"apdex":       func(s *apmServiceJSON) rowKey { return optKey(s.Apdex) },
	"last_seen":   func(s *apmServiceJSON) rowKey { return textKey(s.LastSeen) },
}

// apmServicesList has no page unless a request asks for one: the map, the inbox and the older consoles all read this list as every
// service there is, which is what it has always answered.
var apmServicesList = listSpec{
	columns:  columnsOf(serviceKeys),
	sort:     "name",
	asc:      map[string]bool{"name": true, "environment": true},
	limit:    wholeList,
	maxLimit: wholeList,
}

func serviceTie(a, b *apmServiceJSON) int {
	return cmp.Or(strings.Compare(a.ServiceName, b.ServiceName), strings.Compare(a.ServiceNamespace, b.ServiceNamespace), strings.Compare(a.Environment, b.Environment))
}

// serviceMatches is the services search: every word somewhere in what a row of the list shows. It runs here and not in the statement
// because language and version are aggregates of a GROUP BY.
func serviceMatches(sv *apmServiceJSON, q string) bool {
	text := strings.ToLower(strings.Join([]string{sv.ServiceName, sv.ServiceNamespace, sv.Environment, sv.Language, sv.Version}, " "))
	for _, term := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

// ---- error inbox ----

var inboxKeys = map[string]func(*errorGroupJSON) rowKey{
	"count":      func(g *errorGroupJSON) rowKey { return numKey(g.Count) },
	"last_seen":  func(g *errorGroupJSON) rowKey { return textKey(*g.LastSeen) },
	"first_seen": func(g *errorGroupJSON) rowKey { return textKey(*g.FirstSeen) },
	"type":       func(g *errorGroupJSON) rowKey { return textKey(g.ErrorType) },
	"service":    func(g *errorGroupJSON) rowKey { return textKey(g.ServiceName) },
	"status":     func(g *errorGroupJSON) rowKey { return textKey(g.Status) },
	"comments":   func(g *errorGroupJSON) rowKey { return numKey(float64(g.CommentCount)) },
	"assignee": func(g *errorGroupJSON) rowKey {
		if g.Assignee == nil {
			return rowKey{blank: true}
		}
		return textKey(cmp.Or(g.Assignee.Name, g.Assignee.Email))
	},
}

var apmInboxList = listSpec{
	columns:  columnsOf(inboxKeys),
	sort:     "count",
	asc:      map[string]bool{"type": true, "service": true, "status": true, "assignee": true},
	limit:    50,
	maxLimit: 500,
}

// inboxTie puts the group seen most over retention first among equals, which is what ties have always meant in the inbox.
func inboxTie(a, b *errorGroupJSON) int {
	return cmp.Or(cmp.Compare(b.TotalCount, a.TotalCount), strings.Compare(a.GroupID, b.GroupID))
}
