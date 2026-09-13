package alert

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/api/query"
)

func TestAPMNoDataParse(t *testing.T) {
	c := mustParse(t, TypeAPMNoData, `{"service_name":"checkout","window_seconds":90,"group_by":["environment","namespace","environment"]}`).(APMNoDataCondition)
	if c.WindowSeconds != 120 || c.LookbackSeconds != 86400 {
		t.Errorf("window/lookback = %d/%d, want 120/86400 (window rounded up to minutes)", c.WindowSeconds, c.LookbackSeconds)
	}
	if strings.Join(c.GroupBy, ",") != "namespace,environment" {
		t.Errorf("group_by = %v", c.GroupBy)
	}
	if j := c.Judge(); j.Operator != "gte" || j.Threshold != 120 {
		t.Errorf("judge = %+v", j)
	}
	if c.Missing() != "expire" {
		t.Errorf("missing = %s", c.Missing())
	}
	bad := []string{
		`{"service_name":"a","window_seconds":30}`,
		`{"service_name":"a","window_seconds":600,"lookback_seconds":600}`,
		`{"service_name":"a","lookback_seconds":700000}`,
		`{"service_name":"a","group_by":["transaction"]}`,
		`{"service_name":"a","metric":"p95_ms"}`,
	}
	for _, raw := range bad {
		if _, err := ruleTypes[TypeAPMNoData].Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	// A full rule with this type validates (API path) and keeps for_seconds.
	d, err := RuleInput{Name: "checkout silent", Type: TypeAPMNoData, ForSeconds: 60,
		Condition: json.RawMessage(`{"service_name":"checkout","environment":"prod"}`)}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if d.ForSeconds != 60 || d.IntervalSeconds != 60 {
		t.Errorf("definition = %+v", d)
	}
}

// fakeRows returns fixed rows (scanned positionally into string/int64 destinations).
type fakeRows struct {
	driver.Rows
	rows [][]any
	i    int
}

func (r *fakeRows) Next() bool   { r.i++; return r.i <= len(r.rows) }
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Err() error   { return nil }
func (r *fakeRows) Scan(dest ...any) error {
	for k, v := range r.rows[r.i-1] {
		if v != nil {
			reflect.ValueOf(dest[k]).Elem().Set(reflect.ValueOf(v))
		}
	}
	return nil
}

type fixedConn struct {
	driver.Conn
	sql  []string
	rows func(sql string) [][]any
}

func (c *fixedConn) Query(_ context.Context, sql string, _ ...any) (driver.Rows, error) {
	c.sql = append(c.sql, sql)
	return &fakeRows{rows: c.rows(sql)}, nil
}

func TestAPMNoDataEvaluate(t *testing.T) {
	end := time.Date(2026, 9, 13, 10, 20, 30, 0, time.UTC) // whole minutes end at 10:20
	lastMinute := func(m int) int64 {                      // unix ms of the end of minute 10:m
		return time.Date(2026, 9, 13, 10, m, 0, 0, time.UTC).Add(time.Minute).UnixMilli()
	}
	conn := &fixedConn{rows: func(string) [][]any {
		return [][]any{
			{"checkout", "prod", int64(0), lastMinute(19)}, // reported in the last complete minute
			{"checkout", "stage", int64(0), lastMinute(5)}, // last transactions in minute 10:05: silent since 10:06
		}
	}}
	sc, _ := query.New(conn, "openlog", time.Second).Scope("t1")
	c := mustParse(t, TypeAPMNoData, `{"service_name":"checkout","group_by":["environment"],"window_seconds":600}`)
	res, err := c.Evaluate(context.Background(), sc, end, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 2 {
		t.Fatalf("samples = %+v", res.Samples)
	}
	got := map[string]Sample{}
	for _, s := range res.Samples {
		got[s.Labels["environment"]] = s
	}
	if v := got["prod"].Value; v != 0 {
		t.Errorf("prod age = %v, want 0", v)
	}
	if v := got["stage"].Value; v != 840 {
		t.Errorf("stage age = %v, want 840", v)
	}
	j := c.Judge()
	if j.Breach(got["prod"].Value) || !j.Breach(got["stage"].Value) {
		t.Errorf("breach prod=%v stage=%v", j.Breach(got["prod"].Value), j.Breach(got["stage"].Value))
	}
	if got["stage"].Key != "service.name=checkout|environment=stage" {
		t.Errorf("key = %q", got["stage"].Key)
	}
	sql := conn.sql[0]
	for _, frag := range []string{"apm_transactions_1m", "service_name = {svc:String}", "GROUP BY d_svc, d_env, bk"} {
		if !strings.Contains(sql, frag) {
			t.Errorf("SQL lacks %q: %s", frag, sql)
		}
	}
	if s := c.Summary(got["stage"], "s"); s != "service checkout (stage) reported no transactions for 14m (limit 10m)" {
		t.Errorf("summary = %q", s)
	}

	// The plan opens an incident for the silent series only, and a series that disappears from the lookback
	// resolves as expired (no_data semantics).
	rule := &Rule{Definition: Definition{Name: "silent", Type: TypeAPMNoData, IntervalSeconds: 60, Condition: c, Flapping: Flapping{}}, ID: "r1", OrgID: "o1"}
	p := BuildPlan(PlanInput{Rule: rule, States: map[string]SeriesState{}, End: end, Result: res, NewID: func() string { return "i1" }})
	if len(p.Opens) != 1 || p.Opens[0].Labels["environment"] != "stage" {
		t.Fatalf("opens = %+v", p.Opens)
	}
	states := map[string]SeriesState{}
	for _, s := range p.SeriesUpserts {
		st := s
		st.Incident = &p.Opens[0]
		states[s.Key] = st
	}
	p2 := BuildPlan(PlanInput{Rule: rule, States: states, PrevEvalEnd: end, End: end.Add(time.Minute), Result: &EvalResult{Unit: "s"}})
	if len(p2.Resolves) != 1 || p2.Resolves[0].Reason != ReasonExpired {
		t.Errorf("resolves = %+v", p2.Resolves)
	}
}

func TestAPMNoDataRange(t *testing.T) {
	from := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	to := from.Add(5 * time.Minute)
	var origin int64
	conn := &fixedConn{rows: func(sql string) [][]any {
		// lookback 10m, step 1m: origin = from − 10m, buckets 0..14. Data in bucket 9 (minute 09:59) only.
		origin = from.Add(-10 * time.Minute).Unix()
		return [][]any{{"api", int64(9), (origin + 9*60 + 60) * 1000}}
	}}
	sc, _ := query.New(conn, "openlog", time.Second).Scope("t1")
	c := mustParse(t, TypeAPMNoData, `{"service_name":"api","window_seconds":120,"lookback_seconds":600}`)
	rr, err := c.Range(context.Background(), sc, from, to, time.Minute, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Ends) != 5 || len(rr.Series) != 1 {
		t.Fatalf("range = %+v", rr)
	}
	// Last data at 10:00; ends 10:01..10:05 → ages 60..300 s.
	for i, v := range rr.Series[0].Values {
		if want := float64(60 * (i + 1)); math.Abs(v-want) > 1e-9 {
			t.Errorf("end %d: age %v, want %v", i, v, want)
		}
	}
	if rr.Approximate {
		t.Error("whole-minute step reported approximate")
	}
}
