package alert

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

func TestOQLConditionParse(t *testing.T) {
	c := mustParse(t, TypeOQL, `{"query": "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name, host.name", "operator": "gt", "threshold": 10}`).(OQLCondition)
	if c.WindowSeconds != 300 || c.MissingData != "keep" || c.column != "count(*)" || strings.Join(c.facets, ",") != "service.name,host.name" || c.Judge().Threshold != 10 {
		t.Errorf("parsed %+v", c)
	}
	for raw, want := range map[string]string{
		`{"query": " ", "operator": "gt", "threshold": 1}`:                                                "required",
		`{"query": "SELECT count(*) FROM Log TIMESERIES", "operator": "gt", "threshold": 1}`:              "TIMESERIES is not allowed",
		`{"query": "SELECT count(*) FROM Log SINCE 1 day ago", "operator": "gt", "threshold": 1}`:         "SINCE and UNTIL",
		`{"query": "SELECT count(*) FROM Log COMPARE WITH 1 day ago", "operator": "gt", "threshold": 1}`:  "COMPARE WITH",
		`{"query": "SELECT histogram(duration.ms, 10) FROM Span", "operator": "gt", "threshold": 1}`:      "histogram",
		`{"query": "SELECT count(*), max(duration) FROM Span", "operator": "gt", "threshold": 1}`:         "exactly one result column",
		`{"query": "SELECT percentile(duration, 50, 95) FROM Span", "operator": "gt", "threshold": 1}`:    "exactly one result column",
		`{"query": "SELECT latest(message) FROM Log", "operator": "gt", "threshold": 1}`:                  "must be a number",
		`{"query": "SELECT count(*) FROM Log WHERE host.name = {{h}}", "operator": "gt", "threshold": 1}`: "variables are not allowed",
		`{"query": "SELECT count(* FROM Log", "operator": "gt", "threshold": 1}`:                          "line 1, column 16",
		`{"query": "SELECT count(*) FROM Log", "operator": "gt", "threshold": 1, "window_seconds": 30}`:   "window_seconds",
		`{"query": "SELECT count(*) FROM Log", "operator": "between", "threshold": 1}`:                    "operator",
		`{"query": "SELECT count(*) FROM Log", "operator": "gt"}`:                                         "threshold",
		`{"query": "SELECT count(*) FROM Log", "operator": "gt", "threshold": 1, "missing_data": "zero"}`: "missing_data",
		`{"query": "SELECT count(*) FROM Log", "operator": "gt", "threshold": 1, "group_by": ["host"]}`:   "unknown field",
	} {
		_, err := ruleTypes[TypeOQL].Parse(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", raw, err, want)
		}
	}
	// A whole rule validates with the registered type.
	d, err := RuleInput{Name: "errors", Type: TypeOQL, Condition: json.RawMessage(`{"query": "SELECT count(*) FROM Log", "operator": "gte", "threshold": 5}`)}.Validate()
	if err != nil || d.IntervalSeconds != 60 || !strings.Contains(string(d.ConditionJSON), `"window_seconds":300`) {
		t.Fatalf("rule: %v %s", err, d.ConditionJSON)
	}
	var found bool
	for _, rt := range RuleTypes() {
		found = found || (rt.Type == TypeOQL && rt.Available)
	}
	if !found {
		t.Error("oql not listed as available rule type")
	}
}

func TestOQLConditionQueries(t *testing.T) {
	ctx := context.Background()
	end := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	conn := &recordingConn{}
	sc, err := query.New(conn, "openlog", time.Second).Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	faceted := mustParse(t, TypeOQL, `{"query": "SELECT average(duration.ms) FROM Transaction WHERE service.name = 'checkout' FACET transaction.name", "operator": "gt", "threshold": 500, "window_seconds": 600}`)
	if _, err := faceted.Evaluate(ctx, sc, end, Limits{MaxSeries: 50}); err != nil {
		t.Fatal(err)
	}
	rr, err := faceted.Range(ctx, sc, end.Add(-time.Hour), end, time.Minute, Limits{MaxSeries: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Ends) != 60 || len(rr.Series) != 0 {
		t.Errorf("range %d ends %d series", len(rr.Ends), len(rr.Series))
	}
	assertScoped(t, conn.sql)
	if len(conn.sql) != 2 || !strings.Contains(conn.sql[0], "LIMIT 52") || !strings.Contains(conn.sql[1], "arrayJoin(range(") ||
		!strings.Contains(conn.sql[1], "is_entry") || strings.Contains(conn.sql[1], "metrics_1m") {
		t.Errorf("statements %v", conn.sql)
	}

	// Counts without facets have a zero series when nothing matched.
	count := mustParse(t, TypeOQL, `{"query": "SELECT count(*) FROM Log WHERE severity = 'ERROR'", "operator": "gt", "threshold": 0}`)
	rr, err = count.Range(ctx, sc, end.Add(-10*time.Minute), end, time.Minute, Limits{})
	if err != nil || len(rr.Series) != 1 || rr.Series[0].Key != "*" || rr.Series[0].Values[9] != 0 {
		t.Fatalf("count range %+v %v", rr, err)
	}
	avg := mustParse(t, TypeOQL, `{"query": "SELECT average(value) FROM Metric WHERE metricName = 'x'", "operator": "gt", "threshold": 0}`)
	rr, err = avg.Range(ctx, sc, end.Add(-10*time.Minute), end, time.Minute, Limits{})
	if err != nil || len(rr.Series) != 0 {
		t.Fatalf("avg range %+v %v", rr, err)
	}

	// Window much longer than the step.
	long := mustParse(t, TypeOQL, `{"query": "SELECT count(*) FROM Log", "operator": "gt", "threshold": 0, "window_seconds": 21600}`)
	var le *LimitError
	if _, err := long.Range(ctx, sc, end.Add(-time.Hour), end, 10*time.Second, Limits{}); !errors.As(err, &le) {
		t.Errorf("long window: %v", err)
	}
	// LIMIT above the series limit is reported as a limit error at evaluation.
	lim := mustParse(t, TypeOQL, `{"query": "SELECT count(*) FROM Log FACET host.name LIMIT 500", "operator": "gt", "threshold": 0}`)
	if _, err := lim.Evaluate(ctx, sc, end, Limits{MaxSeries: 10}); !errors.As(err, &le) {
		t.Errorf("limit: %v", err)
	}

	key, labels := faceted.(OQLCondition).labels([]string{"GET /a|b"})
	if key != `transaction.name=GET /a\|b` || labels["transaction.name"] != "GET /a|b" {
		t.Errorf("labels %q %v", key, labels)
	}
	if !math.IsNaN(math.NaN()) {
		t.Fatal()
	}
}
