package alert

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type recInserter struct {
	mu     sync.Mutex
	fail   int
	tokens []string
	rows   [][][]any
	keys   []string
}

func (r *recInserter) Insert(_ context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
	if r.fail > 0 {
		r.fail--
		return errors.New("clickhouse unavailable")
	}
	if table != "alert_evaluations" || strings.Join(keyColumns, ",") != "tenant_id,rule_id" || len(columns) != len(rows[0]) {
		return errors.New("unexpected insert shape")
	}
	r.rows = append(r.rows, rows)
	return nil
}

func TestEvaluationRows(t *testing.T) {
	rule := &Rule{Definition: Definition{Type: TypeMetricThreshold}, ID: "r1", TenantID: "t1"}
	end := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	p := &Plan{EvalEnd: end, Result: "ok", Duration: 42 * time.Millisecond, Summaries: []SeriesSummary{
		{Key: "a", Labels: map[string]string{"host.name": "a"}, Value: 0.95, State: StateFiring},
		{Key: "b", Value: math.NaN(), State: StateOK},
	}}
	rows := EvaluationRows(rule, p)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	if r := rows[0]; r.SeriesKey != "" || r.Value != 1 || r.State != StateFiring || r.DurationMs != 42 || !r.At.Equal(end) || r.TenantID != "t1" {
		t.Errorf("rule row %+v", r)
	}
	if r := rows[2]; r.SeriesKey != "b" || !math.IsNaN(r.Value) || r.Labels == nil {
		t.Errorf("series row %+v", r)
	}
	// An evaluation error records only the rule row.
	rows = EvaluationRows(rule, &Plan{EvalEnd: end, Result: "error"})
	if len(rows) != 1 || rows[0].Result != "error" || rows[0].State != StateOK {
		t.Errorf("error rows %+v", rows)
	}

	// BuildPlan fills the summaries with the state after the step.
	c := mustParse(t, TypeMetricThreshold, `{"metric":"m","operator":"gt","threshold":1}`)
	r := &Rule{Definition: Definition{Type: TypeMetricThreshold, IntervalSeconds: 60, ForSeconds: 120, Condition: c}, ID: "r1"}
	bp := BuildPlan(PlanInput{Rule: r, States: map[string]SeriesState{}, End: end,
		Result: &EvalResult{Samples: []Sample{{Key: "x", Value: 2}, {Key: "y", Value: 0}}}})
	if len(bp.Summaries) != 2 || bp.Summaries[0].State != StatePending || bp.Summaries[1].State != StateOK || bp.Summaries[1].Value != 0 {
		t.Errorf("summaries %+v", bp.Summaries)
	}
}

func TestEvaluationWriter(t *testing.T) {
	ins := &recInserter{fail: 1}
	w := NewEvaluationWriter(ins, EvaluationWriterOptions{MaxBatch: 2, MaxBuffered: 4})
	row := func(k string) EvaluationRow {
		return EvaluationRow{TenantID: "t", RuleID: "r", SeriesKey: k, At: time.Unix(0, 0), Value: math.NaN(), State: StateOK, Result: "ok"}
	}
	w.Add([]EvaluationRow{row("1"), row("2"), row("3")})
	ctx := context.Background()
	if n := w.Flush(ctx); n != 0 {
		t.Fatalf("failed flush wrote %d", n)
	}
	// While the batch waits for its retry, the buffer bound counts it: 2 in retry + 1 buffered + 3 new → 2 dropped.
	w.Add([]EvaluationRow{row("4"), row("5"), row("6")})
	if n := w.Flush(ctx); n != 2 {
		t.Fatalf("retry wrote %d", n)
	}
	if ins.tokens[0] != ins.tokens[1] {
		t.Errorf("retry used a new token: %v", ins.tokens)
	}
	if n := w.Flush(ctx); n != 2 {
		t.Fatalf("second batch wrote %d", n)
	}
	if n := w.Flush(ctx); n != 0 {
		t.Fatalf("empty flush wrote %d", n)
	}
	if ins.tokens[2] == ins.tokens[1] {
		t.Error("new batch reused the token")
	}
	var keys []string
	for _, b := range ins.rows {
		for _, r := range b {
			keys = append(keys, r[2].(string))
			if r[5] != (*float64)(nil) {
				t.Errorf("NaN value not NULL: %v", r[5])
			}
		}
	}
	if strings.Join(keys, ",") != "1,2,5,6" {
		t.Errorf("written keys %v (oldest rows are dropped)", keys)
	}
}

func TestEvaluationHistory(t *testing.T) {
	from := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	f := func(v float64) *float64 { return &v }
	ms := func(m int) int64 { return from.Add(time.Duration(m) * time.Minute).UnixMilli() }
	conn := &fixedConn{rows: func(string) [][]any {
		return [][]any{
			{"", int64(0), ms(1), f(1), uint8(2), map[string]string{}, f(1), uint64(2), uint64(0), uint32(12), uint32(10)},
			{"host.id=a", int64(0), ms(1), f(0.95), uint8(2), map[string]string{"host.id": "a"}, f(0.95), uint64(2), uint64(0), uint32(12), uint32(10)},
			{"host.id=b", int64(0), ms(1), (*float64)(nil), uint8(0), map[string]string{"host.id": "b"}, (*float64)(nil), uint64(2), uint64(0), uint32(12), uint32(10)},
		}
	}}
	sc, _ := query.New(conn, "openlog", time.Second).Scope("tenant-a")
	h, err := EvaluationHistory(context.Background(), sc, "r1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if h.Step != 10*time.Second {
		t.Errorf("step %s", h.Step)
	}
	if len(h.Evaluations) != 1 || h.Evaluations[0].Firing != 1 || h.Evaluations[0].LastDuration != 10 {
		t.Errorf("evaluations %+v", h.Evaluations)
	}
	if len(h.Series) != 2 || h.Series[0].Key != "host.id=a" || h.Series[0].Points[0].State != StateFiring ||
		!math.IsNaN(h.Series[1].Points[0].Value) || h.Series[1].Labels["host.id"] != "b" {
		t.Errorf("series %+v", h.Series)
	}
	assertScoped(t, conn.sql)
	if !strings.Contains(conn.sql[0], "alert_evaluations") || !strings.Contains(conn.sql[0], "rule_id = {rule_id:String}") {
		t.Errorf("sql %s", conn.sql[0])
	}
	if _, err := EvaluationHistory(context.Background(), sc, "r1", from, from.Add(31*24*time.Hour)); err == nil {
		t.Error("range over 30 days accepted")
	}
}
