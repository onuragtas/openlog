package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

var testList = listSpec{
	columns:  map[string]string{"time": "a_time", "name": "a_name"},
	sort:     "time",
	asc:      map[string]bool{"name": true},
	tiebreak: []string{"a_id"},
	limit:    50,
	maxLimit: 500,
}

func parseList(t *testing.T, query string) (listRequest, error) {
	t.Helper()
	return testList.parse(httptest.NewRequest("GET", "/x?"+query, nil), 1000)
}

func TestListDefaults(t *testing.T) {
	l, err := parseList(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.sort != "time" || l.asc || l.limit != 50 || l.offset != 0 {
		t.Errorf("%+v", l)
	}
	if got := strings.Join(testList.order(l), ","); got != "a_time DESC,a_id" {
		t.Errorf("%s", got)
	}
	if l, _ := parseList(t, "sort=name"); !l.asc {
		t.Error("a name should start A to Z")
	}
	if l, _ := parseList(t, "sort=name&dir=desc"); l.asc {
		t.Error("dir should win")
	}
}

func TestListRefusesWhatItDoesNotName(t *testing.T) {
	for _, q := range []string{"sort=a_time%3BDROP", "sort=unknown", "dir=sideways", "limit=0", "limit=x", "offset=-1", "offset=100001"} {
		if _, err := parseList(t, q); err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
	if l, _ := parseList(t, "limit=99999"); l.limit != 500 {
		t.Errorf("limit %d", l.limit)
	}
}

func TestPageSaysWhetherThereIsMore(t *testing.T) {
	l := listRequest{limit: 2}
	if rows, more := page([]int{1, 2, 3}, l); len(rows) != 2 || !more {
		t.Errorf("%v %v", rows, more)
	}
	if rows, more := page([]int{1, 2}, l); len(rows) != 2 || more {
		t.Errorf("%v %v", rows, more)
	}
}
