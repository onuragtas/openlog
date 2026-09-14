package alert

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/apm"
)

func TestAPMErrorConditionParse(t *testing.T) {
	c, err := apmErrorType{}.Parse(json.RawMessage(`{"event":"new_group","service_name":"orders","environment":"prod","window_seconds":90,"min_count":3,"match":"shard"}`))
	if err != nil {
		t.Fatal(err)
	}
	ac := c.(APMErrorCondition)
	if ac.WindowSeconds != 120 || ac.MinCount != 3 || !ac.IgnoresFor() || ac.Missing() != "expire" || ac.Judge().Threshold != 1 {
		t.Errorf("parsed %+v", ac)
	}
	for _, raw := range []string{
		`{"event":"appeared"}`,
		`{"event":"regressed","min_count":2}`,
		`{"event":"new_group","window_seconds":10}`,
		`{"event":"new_group","min_count":-1}`,
		`{"event":"new_group","unknown":1}`,
	} {
		if _, err := (apmErrorType{}).Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	d, err := apmErrorType{}.Parse(json.RawMessage(`{"event":"regressed"}`))
	if err != nil || d.Window() != 5*time.Minute {
		t.Errorf("defaults: %+v %v", d, err)
	}
	s := d.Summary(Sample{Labels: map[string]string{"service.name": "orders", "environment": "prod", "error.type": "Timeout", "error.message": "db <n>"}}, "")
	if s != "error group regressed in orders (prod): Timeout: db <n>" {
		t.Errorf("summary %q", s)
	}
	if _, ok := ruleTypes[TypeAPMError]; !ok {
		t.Error("apm_error not registered")
	}
}

func TestAPMErrorRegressedNeedsWorkflow(t *testing.T) {
	c := APMErrorCondition{Event: APMErrorRegressed, WindowSeconds: 300}
	if _, err := c.Evaluate(context.Background(), nil, time.Now(), Limits{}); !errors.Is(err, ErrErrorWorkflowUnavailable) {
		t.Errorf("evaluate without workflow: %v", err)
	}
	ctx := WithErrorWorkflow(context.Background(), ErrorWorkflow{OrgID: "o", Store: apm.PGErrorStates{}})
	if _, ok := errorWorkflowFrom(ctx); !ok {
		t.Error("workflow not found in context")
	}
	if !math.IsNaN(nanSlice(1)[0]) {
		t.Error("nanSlice")
	}
}
