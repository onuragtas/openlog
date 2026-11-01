package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Sorting and paging of a list on the server, because a browser can only ever sort the page it was sent (api.md "Lists").
//
// A list names the columns it can be sorted by, and the request picks one by name. The name selects a fragment that was written here:
// nothing from the request reaches a statement.

// listSpec is what one list allows.
type listSpec struct {
	// columns maps a sort name to the expression it orders by.
	columns map[string]string
	// sort is the default column. A column starts descending, largest first, unless asc says otherwise: a name reads best A to Z.
	sort string
	asc  map[string]bool
	// tiebreak makes the order total. Without one, two rows with the same value can swap between pages, and a row shows twice or never.
	tiebreak []string
	limit    int
	maxLimit int
}

// wholeList is the limit of a list that answered with all of its rows before it had pages. An older client sends no limit and expects
// them still, and OPENLOG_API_MAX_ROWS remains what bounds it.
const wholeList = 1 << 30

// listRequest is one request's sort and page.
type listRequest struct {
	sort   string
	asc    bool
	limit  int
	offset int
}

// maxListOffset bounds how deep a page can go. ClickHouse reads and throws away everything before an offset, so past this a filter is the
// better tool, and the statement that asks for it must not be able to cost whatever it likes.
const maxListOffset = 100_000

func (s listSpec) parse(r *http.Request, maxRows int) (listRequest, error) {
	q := r.URL.Query()
	l := listRequest{sort: q.Get("sort")}
	if l.sort == "" {
		l.sort = s.sort
	}
	if _, ok := s.columns[l.sort]; !ok {
		names := make([]string, 0, len(s.columns))
		for name := range s.columns {
			names = append(names, name)
		}
		sort.Strings(names)
		return l, badRequest("sort must be one of %s", strings.Join(names, ", "))
	}
	l.asc = s.asc[l.sort]
	switch q.Get("dir") {
	case "":
	case "asc":
		l.asc = true
	case "desc":
		l.asc = false
	default:
		return l, badRequest("dir must be asc or desc")
	}
	l.limit = min(s.limit, maxRows)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return l, badRequest("limit must be a positive integer")
		}
		l.limit = min(n, s.maxLimit, maxRows)
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxListOffset {
			return l, badRequest("offset must be between 0 and %d", maxListOffset)
		}
		l.offset = n
	}
	return l, nil
}

// order is the ORDER BY of this request, with the tiebreak.
func (s listSpec) order(l listRequest) []string {
	dir := "DESC"
	if l.asc {
		dir = "ASC"
	}
	return append([]string{fmt.Sprintf("%s %s", s.columns[l.sort], dir)}, s.tiebreak...)
}

// fetch is how many rows to ask for: one more than a page, which is how has_more is known without counting the whole list.
func (l listRequest) fetch() int { return l.limit + 1 }

// page cuts the extra row and says whether it was there.
func page[T any](rows []T, l listRequest) ([]T, bool) {
	if len(rows) > l.limit {
		return rows[:l.limit], true
	}
	return rows, false
}
