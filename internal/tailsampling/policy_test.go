package tailsampling

import (
	"errors"
	"testing"
)

func ratio(v float64) *float64 { return &v }

func TestParsePolicyValidation(t *testing.T) {
	ok := `{"enabled":true,"baseline_ratio":0.1,"max_spans_per_second":1000,"rules":[
		{"name":"errors","type":"error"},
		{"name":"slow","type":"latency","threshold_ms":1000},
		{"name":"pay","type":"service","services":["payments"],"ratio":0.5},
		{"name":"health","type":"route","route":"/health*","ratio":0},
		{"name":"vip","type":"attribute","key":"customer.tier","value":"gold"}]}`
	p, err := ParsePolicy([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 5 || p.Rules[2].KeepRatio() != 0.5 || p.Rules[0].KeepRatio() != 1 {
		t.Fatalf("unexpected policy %+v", p)
	}
	bad := map[string]string{
		"unknown field":    `{"enabled":true,"baseline":1}`,
		"baseline range":   `{"baseline_ratio":1.5}`,
		"rate negative":    `{"max_spans_per_second":-1}`,
		"no name":          `{"rules":[{"type":"error"}]}`,
		"dup name":         `{"rules":[{"name":"a","type":"error"},{"name":"a","type":"error"}]}`,
		"reserved name":    `{"rules":[{"name":"baseline","type":"error"}]}`,
		"bad type":         `{"rules":[{"name":"a","type":"drop"}]}`,
		"latency no thr":   `{"rules":[{"name":"a","type":"latency"}]}`,
		"service empty":    `{"rules":[{"name":"a","type":"service"}]}`,
		"route empty":      `{"rules":[{"name":"a","type":"route"}]}`,
		"route bad glob":   `{"rules":[{"name":"a","type":"route","route":"[x"}]}`,
		"attribute no key": `{"rules":[{"name":"a","type":"attribute"}]}`,
		"ratio range":      `{"rules":[{"name":"a","type":"error","ratio":2}]}`,
	}
	for name, doc := range bad {
		if _, err := ParsePolicy([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if name != "unknown field" && !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: error %v is not ErrInvalidPolicy", name, err)
		}
	}
}

func TestPolicyEvaluateFirstMatch(t *testing.T) {
	p := Policy{Enabled: true, BaselineRatio: 0.1, Rules: []Rule{
		{Name: "health", Type: RuleRoute, Route: "/health*", Ratio: ratio(0)},
		{Name: "errors", Type: RuleError},
		{Name: "slow", Type: RuleLatency, ThresholdMs: 500},
		{Name: "slow-db", Type: RuleLatency, ThresholdMs: 100, Service: "db"},
		{Name: "pay", Type: RuleService, Services: []string{"payments"}, Ratio: ratio(0.5)},
		{Name: "vip", Type: RuleAttribute, Key: "tier", Value: "gold"},
		{Name: "region", Type: RuleAttribute, Key: "cloud.region"},
	}}
	cases := []struct {
		name string
		sum  TraceSummary
		rule string
		p    float64
	}{
		{"baseline", TraceSummary{Items: []SpanFacts{{Service: "shop", Route: "/cart"}}}, "baseline", 0.1},
		{"health error still dropped (first match)", TraceSummary{Items: []SpanFacts{{Route: "/healthz", Error: true}}}, "health", 0},
		{"span name used without route", TraceSummary{Items: []SpanFacts{{Name: "/health/live"}}}, "health", 0},
		{"error", TraceSummary{Items: []SpanFacts{{Service: "a"}, {Service: "b", Error: true}}}, "errors", 1},
		{"trace latency", TraceSummary{DurationMs: 700, Items: []SpanFacts{{Service: "a"}}}, "slow", 1},
		{"service latency", TraceSummary{DurationMs: 150, Items: []SpanFacts{{Service: "db", DurationMs: 120}}}, "slow-db", 1},
		{"service latency other service", TraceSummary{DurationMs: 150, Items: []SpanFacts{{Service: "x", DurationMs: 120}}}, "baseline", 0.1},
		{"service", TraceSummary{Items: []SpanFacts{{Service: "payments"}}}, "pay", 0.5},
		{"attribute value", TraceSummary{Items: []SpanFacts{{Attributes: map[string]string{"tier": "gold"}}}}, "vip", 1},
		{"attribute other value", TraceSummary{Items: []SpanFacts{{Attributes: map[string]string{"tier": "silver"}}}}, "baseline", 0.1},
		{"resource attribute presence", TraceSummary{Items: []SpanFacts{{Resource: map[string]string{"cloud.region": "eu"}}}}, "region", 1},
	}
	for _, c := range cases {
		d := p.Evaluate(&c.sum)
		if d.Rule != c.rule || d.Ratio != c.p {
			t.Errorf("%s: got %s/%v, want %s/%v", c.name, d.Rule, d.Ratio, c.rule, c.p)
		}
	}
	off := p
	off.Enabled = false
	if d := off.Evaluate(&TraceSummary{}); d.Ratio != 1 {
		t.Errorf("disabled policy must keep everything, got %v", d)
	}
}

func TestMatchRoute(t *testing.T) {
	for _, c := range []struct {
		pattern, value string
		want           bool
	}{
		{"/api/*", "/api/v1/users", true}, // trailing * is a prefix match across "/"
		{"/api/*/users", "/api/v1/users", true},
		{"/api/*/users", "/api/v1/x/users", false},
		{"GET /orders/{id}", "GET /orders/{id}", true},
		{"/x", "", false},
	} {
		if got := MatchRoute(c.pattern, c.value); got != c.want {
			t.Errorf("MatchRoute(%q, %q) = %v", c.pattern, c.value, got)
		}
	}
}
