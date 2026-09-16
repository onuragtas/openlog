package alert

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/slo"
)

const testSLOID = "8f14e45f-ceea-467a-9f1b-8e2c3a4d5e6f"

func parseSLOBurn(t *testing.T, raw string) SLOBurnCondition {
	t.Helper()
	c, err := ruleTypes[TypeSLOBurn].Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return c.(SLOBurnCondition)
}

// The defaults are the multi-window multi-burn-rate pair of alerting.md §2.11.
func TestSLOBurnDefaults(t *testing.T) {
	c := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`"}`)
	if len(c.Windows) != 2 {
		t.Fatalf("windows = %+v", c.Windows)
	}
	fast, slow := c.Windows[0], c.Windows[1]
	if fast.Name != "fast" || fast.Factor != 14.4 || fast.LongSeconds != 3600 || fast.ShortSeconds != 300 {
		t.Errorf("fast window = %+v", fast)
	}
	if slow.Name != "slow" || slow.Factor != 6 || slow.LongSeconds != 21600 || slow.ShortSeconds != 1800 {
		t.Errorf("slow window = %+v", slow)
	}
	if got := c.Window(); got != 6*time.Hour {
		t.Errorf("Window() = %v, want 6h", got)
	}
	if j := c.Judge(); j.Operator != "gte" || j.Threshold != 1 || j.Recovery != 1 {
		t.Errorf("judge = %+v", j)
	}
	if c.IgnoresFor() || c.Missing() != "keep" {
		t.Errorf("IgnoresFor = %v, Missing = %q", c.IgnoresFor(), c.Missing())
	}
}

func TestSLOBurnValidation(t *testing.T) {
	cases := map[string]string{
		"no slo":            `{}`,
		"invalid id":        `{"slo_id":"nope"}`,
		"factor 0":          `{"slo_id":"` + testSLOID + `","windows":[{"name":"fast","factor":0,"long_seconds":3600,"short_seconds":300}]}`,
		"short above long":  `{"slo_id":"` + testSLOID + `","windows":[{"name":"fast","factor":2,"long_seconds":600,"short_seconds":1200}]}`,
		"long too short":    `{"slo_id":"` + testSLOID + `","windows":[{"name":"fast","factor":2,"long_seconds":60,"short_seconds":60}]}`,
		"duplicate name":    `{"slo_id":"` + testSLOID + `","windows":[{"name":"a","factor":2,"long_seconds":3600,"short_seconds":300},{"name":"a","factor":3,"long_seconds":7200,"short_seconds":600}]}`,
		"bad name":          `{"slo_id":"` + testSLOID + `","windows":[{"name":"Fast Window","factor":2,"long_seconds":3600,"short_seconds":300}]}`,
		"negative requests": `{"slo_id":"` + testSLOID + `","min_requests":-1}`,
		"unknown field":     `{"slo_id":"` + testSLOID + `","nope":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ruleTypes[TypeSLOBurn].Parse(json.RawMessage(raw)); err == nil {
				t.Fatalf("Parse(%s) accepted", raw)
			}
		})
	}
	// Windows are rounded up to whole minutes (the rollup is per minute).
	c := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`","windows":[{"name":"custom","factor":3,"long_seconds":3661,"short_seconds":301}]}`)
	if c.Windows[0].LongSeconds != 3720 || c.Windows[0].ShortSeconds != 360 {
		t.Errorf("rounded window = %+v", c.Windows[0])
	}
}

// fakeSLOStore serves one definition; the other methods are unused by conditions.
type fakeSLOStore struct {
	s   *slo.SLO
	err error
}

func (f fakeSLOStore) List(context.Context, string) ([]slo.SLO, error) { return nil, nil }
func (f fakeSLOStore) Get(_ context.Context, _, id string) (*slo.SLO, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.s == nil || f.s.ID != id {
		return nil, slo.ErrNotFound
	}
	return f.s, nil
}
func (f fakeSLOStore) Create(context.Context, string, slo.Input, slo.Actor) (*slo.SLO, error) {
	return nil, nil
}
func (f fakeSLOStore) Update(context.Context, string, string, slo.Input, slo.Actor) (*slo.SLO, error) {
	return nil, nil
}
func (f fakeSLOStore) Delete(context.Context, string, string, slo.Actor) error { return nil }

// Evaluating without the PostgreSQL store (static auth mode) is an evaluation error, not a silent "ok".
func TestSLOBurnNeedsStore(t *testing.T) {
	c := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`"}`)
	if _, err := c.Evaluate(context.Background(), nil, time.Now(), Limits{}); !errors.Is(err, ErrSLOUnavailable) {
		t.Fatalf("Evaluate without a store = %v, want ErrSLOUnavailable", err)
	}
	ctx := WithSLOs(context.Background(), SLOLookup{OrgID: "org", Store: fakeSLOStore{}})
	if _, err := c.Evaluate(ctx, nil, time.Now(), Limits{}); err == nil {
		t.Fatal("Evaluate with a deleted SLO succeeded")
	}
}

// worst picks the window that is furthest past its factor and reports its name; windows below min_requests
// and windows without data have no value.
func TestSLOBurnWorstWindow(t *testing.T) {
	c := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`"}`)
	s := &slo.SLO{ID: testSLOID, Input: slo.Input{Name: "Checkout availability", ServiceName: "checkout",
		SLIType: slo.SLIAvailability, Objective: 99.9, WindowDays: 30}}
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)
	buckets := make([]slo.Bucket, 360)
	for i := range buckets {
		// Clean for 5.5 h, then 5 % failures: the short windows see 50× the 0.1 % budget.
		requests, errs := 100.0, 0.0
		if i >= 330 {
			errs = 5
		}
		buckets[i] = slo.Bucket{Start: start.Add(time.Duration(i) * time.Minute), Requests: requests,
			Errors: errs, Good: requests - errs}
	}
	v, name := c.worst(buckets, s, end)
	if math.IsNaN(v) || name == "" {
		t.Fatalf("worst = %v (%q), want a value", v, name)
	}
	// fast: long 1 h has 30 bad minutes of 60 → 2.5 % of 6 000 → burn 25×, ratio 25/14.4 ≈ 1.74.
	// slow: long 6 h has 150 bad of 36 000 → burn 4.17×, ratio 0.69 (not breaching).
	if name != "fast" {
		t.Errorf("worst window = %q, want fast", name)
	}
	if math.Abs(v-25.0/14.4) > 1e-6 {
		t.Errorf("ratio = %v, want %v", v, 25.0/14.4)
	}

	// min_requests suppresses windows with too little traffic (no value at all, not a 0).
	quiet := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`","min_requests":1000000}`)
	if v, _ := quiet.worst(buckets, s, end); !math.IsNaN(v) {
		t.Errorf("value with min_requests = %v, want NaN", v)
	}
	// Before any data both windows are empty.
	if v, _ := c.worst(buckets, s, start); !math.IsNaN(v) {
		t.Errorf("value before the data = %v, want NaN", v)
	}
}

func TestSLOBurnSummary(t *testing.T) {
	c := parseSLOBurn(t, `{"slo_id":"`+testSLOID+`"}`)
	s := Sample{Key: "slo.id=" + testSLOID, Value: 1.5,
		Labels: map[string]string{"slo.name": "Checkout availability", "slo.window": "fast", "service.name": "checkout"}}
	got := c.Summary(s, "1")
	want := "error budget of Checkout availability burns 21.6× too fast over 1h and 5m (≥ 14.4×)"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}
