package alert

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

func parseAnomaly(t *testing.T, raw string) AnomalyCondition {
	t.Helper()
	c, err := ruleTypes[TypeAnomaly].Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return c.(AnomalyCondition)
}

// The defaults are a daily baseline over a week at 3σ on the upper side (alerting.md §2.12).
func TestAnomalyDefaults(t *testing.T) {
	c := parseAnomaly(t, `{"metric":"system.cpu.utilization"}`)
	if c.Signal != AnomalySignalMetric || c.Aggregation != "avg" || c.SeriesAggregation != "avg" {
		t.Errorf("signal defaults: %+v", c)
	}
	if c.WindowSeconds != 300 || c.Seasonality != "daily" || c.LookbackDays != 7 || c.Direction != "upper" ||
		c.Sensitivity != 3 || c.MinSamples != 3 || c.MinDeviation != 0 {
		t.Errorf("baseline defaults: %+v", c)
	}
	if c.lags() != 7 || c.periodMinutes() != 1440 {
		t.Errorf("lags = %d, period = %d", c.lags(), c.periodMinutes())
	}
	if j := c.Judge(); j.Operator != "gte" || j.Threshold != 1 || j.Recovery != 1 {
		t.Errorf("judge = %+v", j)
	}
	if c.Window() != 5*time.Minute || c.Missing() != "keep" || c.IgnoresFor() {
		t.Errorf("window = %v, missing = %q, ignoresFor = %v", c.Window(), c.Missing(), c.IgnoresFor())
	}
	// Without seasonality the baseline is the run of preceding windows.
	none := parseAnomaly(t, `{"metric":"m","seasonality":"none","window_seconds":600,"lookback_days":1}`)
	if none.periodMinutes() != 10 || none.lags() != 144 {
		t.Errorf("none: period = %d, lags = %d", none.periodMinutes(), none.lags())
	}
	// The APM signal keeps the apm selector and its metric names.
	a := parseAnomaly(t, `{"signal":"apm","service_name":"checkout","metric":"p95_ms","group_by":["transaction"],"environment":"prod"}`)
	if a.apmInner().Metric != "p95_ms" || a.apmInner().ServiceName != "checkout" {
		t.Errorf("apm inner = %+v", a.apmInner())
	}
	var found bool
	for _, rt := range RuleTypes() {
		found = found || (rt.Type == TypeAnomaly && rt.Available)
	}
	if !found {
		t.Error("anomaly is not listed as an available rule type")
	}
	// A whole rule validates through the registered type.
	d, err := RuleInput{Name: "cpu baseline", Type: TypeAnomaly, Condition: json.RawMessage(`{"metric":"system.cpu.utilization"}`)}.Validate()
	if err != nil || d.IntervalSeconds != 60 || !strings.Contains(string(d.ConditionJSON), `"seasonality":"daily"`) {
		t.Fatalf("rule: %v %s", err, d.ConditionJSON)
	}
}

func TestAnomalyValidation(t *testing.T) {
	cases := map[string]string{
		"no metric":             `{}`,
		"rate aggregation":      `{"metric":"m","aggregation":"rate"}`,
		"percentile":            `{"metric":"m","aggregation":"p95"}`,
		"unknown seasonality":   `{"metric":"m","seasonality":"monthly"}`,
		"window not a divisor":  `{"metric":"m","seasonality":"hourly","window_seconds":420}`,
		"window too short":      `{"metric":"m","window_seconds":30}`,
		"sensitivity too low":   `{"metric":"m","sensitivity":0.2}`,
		"sensitivity too high":  `{"metric":"m","sensitivity":25}`,
		"lookback too long":     `{"metric":"m","lookback_days":40}`,
		"min_samples too low":   `{"metric":"m","min_samples":1}`,
		"negative deviation":    `{"metric":"m","min_deviation":-1}`,
		"bad direction":         `{"metric":"m","direction":"up"}`,
		"too few weekly slots":  `{"metric":"m","seasonality":"weekly","lookback_days":14,"min_samples":3}`,
		"resource filter":       `{"metric":"m","filters":[{"field":"resource.env","op":"eq","values":["prod"]}]}`,
		"host name filter":      `{"metric":"m","filters":[{"field":"host.name","op":"eq","values":["web-1"]}]}`,
		"apm fields on metric":  `{"metric":"m","service_name":"checkout"}`,
		"apm without service":   `{"signal":"apm","metric":"p95_ms"}`,
		"apm bad metric":        `{"signal":"apm","service_name":"checkout","metric":"cpu"}`,
		"apm with filters":      `{"signal":"apm","service_name":"checkout","metric":"p95_ms","filters":[{"field":"host.id","op":"eq","values":["h"]}]}`,
		"apm with aggregation":  `{"signal":"apm","service_name":"checkout","metric":"p95_ms","aggregation":"avg"}`,
		"apm host group":        `{"signal":"apm","service_name":"checkout","metric":"p95_ms","group_by":["host"]}`,
		"unknown signal":        `{"signal":"log","metric":"m"}`,
		"unknown field":         `{"metric":"m","nope":1}`,
		"metrics rollup rate":   `{"metric":"m","series_aggregation":"p95"}`,
		"too many group_by dim": `{"metric":"m","group_by":["host","service","attr.a","attr.b","attr.c","attr.d"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ruleTypes[TypeAnomaly].Parse(json.RawMessage(raw)); err == nil {
				t.Fatalf("Parse(%s) accepted", raw)
			}
		})
	}
	// The window is rounded up to whole minutes (the rollups are per minute) and must then divide the season.
	c := parseAnomaly(t, `{"metric":"m","seasonality":"hourly","window_seconds":541,"lookback_days":1}`)
	if c.WindowSeconds != 600 {
		t.Errorf("rounded window = %d, want 600", c.WindowSeconds)
	}
	// Enough weekly slots: 28 days give 4 samples.
	weekly := parseAnomaly(t, `{"metric":"m","seasonality":"weekly","lookback_days":28,"min_samples":4}`)
	if weekly.lags() != 4 {
		t.Errorf("weekly lags = %d, want 4", weekly.lags())
	}
	// Hourly over a week is 168 samples; a longer lookback is bounded at maxAnomalyLags.
	if n := parseAnomaly(t, `{"metric":"m","seasonality":"hourly","lookback_days":7}`).lags(); n != 168 {
		t.Errorf("hourly lags over 7 days = %d, want 168", n)
	}
	if n := parseAnomaly(t, `{"metric":"m","seasonality":"hourly","lookback_days":14}`).lags(); n != maxAnomalyLags {
		t.Errorf("hourly lags over 14 days = %d, want %d", n, maxAnomalyLags)
	}
}

// Every minute a baseline window needs must land inside the compact slice of that window, in time order, for
// both grid layouts: separate runs per seasonal lag and one contiguous range without seasonality.
func TestAnomalyGridBuckets(t *testing.T) {
	anchor := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		cond  string
		ends  int
		step  int
		dense bool
	}{
		{"daily evaluation", `{"metric":"m","seasonality":"daily","lookback_days":3,"min_samples":3}`, 1, 5, false},
		{"daily preview", `{"metric":"m","seasonality":"daily","lookback_days":3,"min_samples":3}`, 6, 5, false},
		{"no seasonality", `{"metric":"m","seasonality":"none","window_seconds":600,"lookback_days":1,"min_samples":3}`, 4, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := parseAnomaly(t, tc.cond)
			g := c.grid(anchor, tc.ends, tc.step, min(c.lags(), 6))
			if g.dense != tc.dense {
				t.Fatalf("dense = %v, want %v", g.dense, tc.dense)
			}
			seen := map[int]bool{}
			for j := 0; j < g.ends; j++ {
				for m := 0; m <= g.lags; m++ {
					lo, hi := g.slice(j, m)
					if lo < 0 || hi > g.size() || hi-lo != g.window {
						t.Fatalf("slice(%d,%d) = [%d,%d) outside [0,%d) or not %d wide", j, m, lo, hi, g.size(), g.window)
					}
					prev := -1
					for q := 0; q < g.window; q++ {
						bk := int64(j*g.step + m*g.period + q)
						i := g.index(bk)
						if i < lo || i >= hi {
							t.Fatalf("index(bk=%d) = %d outside slice(%d,%d) = [%d,%d)", bk, i, j, m, lo, hi)
						}
						if prev >= 0 && i != prev-1 {
							t.Fatalf("bucket %d is not one step older than the previous one (%d vs %d)", bk, i, prev)
						}
						prev = i
						seen[i] = true
					}
				}
			}
			// Buckets outside the grid are dropped instead of colliding with a needed one.
			if i := g.index(int64(g.spanMinutes() + 1)); i != -1 {
				t.Errorf("index past the span = %d, want -1", i)
			}
			if !g.dense && len(seen) != g.size() {
				t.Errorf("runs cover %d of %d compact buckets", len(seen), g.size())
			}
		})
	}
}

// A single past incident in the history must not widen the band: median + MAD ignore it where mean + stddev
// would not (the reason the baseline uses robust statistics).
func TestAnomalyRobustToPastSpike(t *testing.T) {
	c := parseAnomaly(t, `{"metric":"m","lookback_days":10,"min_samples":3}`)
	clean := []float64{10, 10.5, 9.5, 10.2, 9.8, 10.1, 9.9}
	withSpike := append(append([]float64(nil), clean...), 300)

	base := c.score(12, clean)
	spiked := c.score(12, withSpike)
	if math.IsNaN(base.ratio) || base.ratio <= 1 {
		t.Fatalf("clean history: ratio = %v, want > 1", base.ratio)
	}
	if spiked.ratio <= 1 {
		t.Errorf("history with one past spike: ratio = %v, want > 1 (the band must not swallow the anomaly)", spiked.ratio)
	}
	if math.Abs(spiked.baseline-10.05) > 1e-9 {
		t.Errorf("baseline = %v, want the median 10.05", spiked.baseline)
	}
	// The same comparison with mean + standard deviation would not fire at all.
	var sum float64
	for _, v := range withSpike {
		sum += v
	}
	mean := sum / float64(len(withSpike))
	var sq float64
	for _, v := range withSpike {
		sq += (v - mean) * (v - mean)
	}
	if std := math.Sqrt(sq / float64(len(withSpike))); (12-mean)/(3*std) > 1 {
		t.Errorf("mean/stddev would have fired too (%v); the test no longer shows the difference", (12-mean)/(3*std))
	}
}

func TestAnomalyScore(t *testing.T) {
	samples := []float64{10, 10.5, 9.5, 10.2, 9.8, 10.1, 9.9}
	upper := parseAnomaly(t, `{"metric":"m","direction":"upper","min_samples":3}`)
	lower := parseAnomaly(t, `{"metric":"m","direction":"lower","min_samples":3}`)
	both := parseAnomaly(t, `{"metric":"m","direction":"both","min_samples":3}`)

	// Direction: only movement on the watched side produces a deviation.
	if s := upper.score(5, samples); s.ratio != 0 || s.above {
		t.Errorf("upper below the baseline: %+v, want ratio 0", s)
	}
	if s := lower.score(5, samples); s.ratio <= 1 || s.above {
		t.Errorf("lower below the baseline: %+v, want ratio > 1", s)
	}
	if s := lower.score(15, samples); s.ratio != 0 || !s.above {
		t.Errorf("lower above the baseline: %+v, want ratio 0", s)
	}
	for _, v := range []float64{5, 15} {
		if s := both.score(v, samples); s.ratio <= 1 {
			t.Errorf("both at %v: ratio = %v, want > 1", v, s.ratio)
		}
	}
	// Inside the band the series still has a value, so it exists and can recover; on the unwatched side it is 0.
	if s := upper.score(10.1, samples); s.ratio <= 0 || s.ratio >= 1 {
		t.Errorf("value inside the band: ratio = %v, want between 0 and 1", s.ratio)
	}
	if s := upper.score(10, samples); s.ratio != 0 {
		t.Errorf("value at the baseline: ratio = %v, want 0", s.ratio)
	}
	// Insufficient data: no value at all (missing data), never a 0.
	if s := upper.score(99, samples[:2]); !math.IsNaN(s.ratio) {
		t.Errorf("two samples with min_samples 3: ratio = %v, want NaN", s.ratio)
	}
	if s := upper.score(math.NaN(), samples); !math.IsNaN(s.ratio) {
		t.Errorf("no current value: ratio = %v, want NaN", s.ratio)
	}
	// A history without any variation: every movement is an anomaly, but the absolute floor can suppress it.
	flat := []float64{0, 0, 0, 0, 0}
	if s := upper.score(5, flat); s.ratio != maxAnomalyRatio {
		t.Errorf("flat history: ratio = %v, want %v", s.ratio, maxAnomalyRatio)
	}
	quiet := parseAnomaly(t, `{"metric":"m","min_samples":3,"min_deviation":10}`)
	if s := quiet.score(5, flat); s.ratio != 0 {
		t.Errorf("below min_deviation: ratio = %v, want 0", s.ratio)
	}
	// Sensitivity scales the band: 1σ alerts where 6σ does not.
	sensitive := parseAnomaly(t, `{"metric":"m","sensitivity":1,"min_samples":3}`)
	relaxed := parseAnomaly(t, `{"metric":"m","sensitivity":6,"min_samples":3}`)
	if s, r := sensitive.score(11, samples), relaxed.score(11, samples); s.ratio <= 1 || r.ratio >= 1 {
		t.Errorf("sensitivity: 1σ = %v, 6σ = %v", s.ratio, r.ratio)
	}
}

// scoreAt reads the current window and the seasonal slots out of one compact array.
func TestAnomalyScoreAtUsesSeasonalSlots(t *testing.T) {
	c := parseAnomaly(t, `{"metric":"m","seasonality":"daily","lookback_days":5,"min_samples":3}`)
	anchor := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	g := c.grid(anchor, 1, c.WindowSeconds/60, c.lags())
	vals := make([]float64, g.size())
	for m := 0; m <= g.lags; m++ {
		lo, hi := g.slice(0, m)
		level := 10.0
		switch m {
		case 0:
			level = 30 // the current window
		case 2:
			level = 200 // a past incident at the same slot
		}
		for i := lo; i < hi; i++ {
			vals[i] = level
		}
	}
	grp := anomalyGroup{key: "*", labels: map[string]string{}, value: func(lo, hi int) float64 {
		sum := 0.0
		for _, v := range vals[lo:hi] {
			sum += v
		}
		return sum / float64(hi-lo)
	}}
	s := c.scoreAt(g, grp, 0)
	if s.baseline != 10 {
		t.Errorf("baseline = %v, want the median 10 of the seasonal slots", s.baseline)
	}
	if s.ratio <= 1 || !s.above {
		t.Errorf("score = %+v, want a breaching deviation above the baseline", s)
	}
	if l := labelsFor(grp, s); l["anomaly.direction"] != "above" || l["anomaly.baseline"] != "10" {
		t.Errorf("labels = %v", l)
	}
}

func TestAnomalySummary(t *testing.T) {
	c := parseAnomaly(t, `{"metric":"system.cpu.utilization","seasonality":"daily"}`)
	s := Sample{Key: "host.id=h1", Value: 2,
		Labels: map[string]string{"host.id": "h1", "anomaly.baseline": "0.41", "anomaly.direction": "above"}}
	want := "system.cpu.utilization avg over 5m is 6σ above the daily baseline 0.41 (≥ 3σ) (host.id=h1)"
	if got := c.Summary(s, "1"); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	apmCond := parseAnomaly(t, `{"signal":"apm","service_name":"checkout","metric":"p95_ms","direction":"lower","seasonality":"none"}`)
	low := Sample{Key: "service.name=checkout", Value: 1.5,
		Labels: map[string]string{"service.name": "checkout", "anomaly.baseline": "210", "anomaly.direction": "below"}}
	want = "checkout p95_ms over 5m is 4.5σ below the recent baseline 210 (≥ 3σ) (service.name=checkout)"
	if got := apmCond.Summary(low, "ms"); got != want {
		t.Errorf("apm Summary() = %q, want %q", got, want)
	}
}

// Both signals read the rollups through the tenant-scoped query layer and only the minutes the baseline needs.
func TestAnomalyQueries(t *testing.T) {
	ctx := context.Background()
	end := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	evilJSON, _ := json.Marshal(evil)
	for name, raw := range map[string]string{
		"metric": `{"metric":"system.cpu.utilization","aggregation":"max","group_by":["host","attr.cpu.mode"],
		            "filters":[{"field":"attr.cpu.mode","op":"not_in","values":[` + string(evilJSON) + `]}]}`,
		"apm": `{"signal":"apm","service_name":` + string(evilJSON) + `,"metric":"p95_ms","group_by":["transaction"],"min_requests":10}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := mustParse(t, TypeAnomaly, raw)
			conn := &recordingConn{}
			sc, err := query.New(conn, "openlog", time.Second).Scope("tenant-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Evaluate(ctx, sc, end, Limits{}); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			rr, err := c.Range(ctx, sc, end.Add(-time.Hour), end, time.Minute, Limits{})
			if err != nil {
				t.Fatalf("range: %v", err)
			}
			if len(rr.Ends) != 60 || rr.Unit != "1" || rr.Approximate {
				t.Errorf("range: %d ends, unit %q, approximate %v", len(rr.Ends), rr.Unit, rr.Approximate)
			}
			assertScoped(t, conn.sql)
			table := "metrics_1m"
			if name == "apm" {
				table = "apm_transactions_1m"
			}
			for _, sql := range conn.sql {
				if !strings.Contains(sql, table) {
					t.Errorf("statement does not read %s: %s", table, sql)
				}
				// Only the seasonal slots of the lookback are read, not every minute of it.
				if !strings.Contains(sql, "modulo(intDiv(") {
					t.Errorf("statement reads the whole lookback: %s", sql)
				}
				if strings.Contains(sql, "UNION") || strings.Contains(sql, "1=1") || strings.Contains(sql, "logs_local") {
					t.Errorf("user value reached SQL text: %s", sql)
				}
			}
		})
	}
	// Without seasonality the runs overlap, so one contiguous range is read instead of a modulo filter.
	conn := &recordingConn{}
	sc, err := query.New(conn, "openlog", time.Second).Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	none := mustParse(t, TypeAnomaly, `{"metric":"m","seasonality":"none","window_seconds":600,"lookback_days":1}`)
	if _, err := none.Evaluate(ctx, sc, end, Limits{}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if strings.Contains(conn.sql[0], "modulo(") {
		t.Errorf("contiguous grid still filters by slot: %s", conn.sql[0])
	}
	assertScoped(t, conn.sql)
}

// A preview that would read more history than one query should falls back to fewer baseline samples.
func TestAnomalyPreviewBudget(t *testing.T) {
	c := parseAnomaly(t, `{"metric":"m","seasonality":"hourly","lookback_days":7,"min_samples":3}`)
	ctx := context.Background()
	end := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	conn := &recordingConn{}
	sc, err := query.New(conn, "openlog", time.Second).Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	rr, err := c.Range(ctx, sc, end.Add(-6*time.Hour), end, time.Minute, Limits{})
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if !rr.Approximate {
		t.Errorf("a shortened baseline must be reported as approximate (%d lags configured)", c.lags())
	}
}
