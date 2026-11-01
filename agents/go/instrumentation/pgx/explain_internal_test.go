package openlogpgx

import (
	"strings"
	"testing"
)

func TestRedactPlan(t *testing.T) {
	in := "Index Scan on users t1  (cost=0.15..8.17 rows=1 width=36)\n  Index Cond: (id = 42)\n  Filter: ((name = 'ay\u015fe''s'::text) AND (col_2 > 1.5) AND (x = $1))\nSeq Scan on t  (cost=0.00..1.00 rows=10 width=4)"
	want := "Index Scan on users t1  (cost=0.15..8.17 rows=1 width=36)\n  Index Cond: (id = ?)\n  Filter: ((name = '?'::text) AND (col_2 > ?) AND (x = $1))\nSeq Scan on t  (cost=0.00..1.00 rows=10 width=4)"
	if got := redactPlan(in); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if got := redactPlan(strings.Repeat("line\n", 10000)); len(got) > maxPlanBytes {
		t.Errorf("%d bytes", len(got))
	}
}

func TestExplainable(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1":                           true,
		"select * from t where a = $1;":      true,
		"-- c\nSELECT 1":                     true,
		"INSERT INTO t VALUES (1)":           false,
		"DELETE FROM t":                      false,
		"WITH d AS (DELETE FROM t) SELECT 1": false,
		"SELECT 1; DROP TABLE t":             false,
		"prepared_statement_name":            false,
	} {
		if got := explainable(sql); got != want {
			t.Errorf("%q: %v", sql, got)
		}
	}
}
