package slo

import (
	"errors"
	"testing"
)

func ptr(s string) *string { return &s }

func validAvailability() Input {
	return Input{Name: "Checkout availability", ServiceName: "checkout", Environment: ptr("prod"),
		SLIType: SLIAvailability, Objective: 99.9, WindowDays: 28}
}

func TestInputValidate(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		field string // "" = valid
	}{
		{"availability", validAvailability(), ""},
		{"latency", Input{Name: "Fast checkout", ServiceName: "checkout", SLIType: SLILatency,
			LatencyThresholdMs: 300, Objective: 99, WindowDays: 7}, ""},
		{"namespace and environment may be empty strings (exact match)", Input{Name: "x", ServiceName: "s",
			ServiceNamespace: ptr(""), Environment: ptr(""), SLIType: SLIAvailability, Objective: 99, WindowDays: 30}, ""},
		{"no name", Input{ServiceName: "s", SLIType: SLIAvailability, Objective: 99, WindowDays: 7}, "name"},
		{"no service", Input{Name: "x", SLIType: SLIAvailability, Objective: 99, WindowDays: 7}, "service_name"},
		{"unknown sli", Input{Name: "x", ServiceName: "s", SLIType: "apdex", Objective: 99, WindowDays: 7}, "sli_type"},
		{"latency without threshold", Input{Name: "x", ServiceName: "s", SLIType: SLILatency, Objective: 99, WindowDays: 7}, "latency_threshold_ms"},
		{"fractional threshold", Input{Name: "x", ServiceName: "s", SLIType: SLILatency, LatencyThresholdMs: 0.5,
			Objective: 99, WindowDays: 7}, "latency_threshold_ms"},
		{"threshold on availability", Input{Name: "x", ServiceName: "s", SLIType: SLIAvailability,
			LatencyThresholdMs: 300, Objective: 99, WindowDays: 7}, "latency_threshold_ms"},
		{"objective 100", Input{Name: "x", ServiceName: "s", SLIType: SLIAvailability, Objective: 100, WindowDays: 7}, "objective"},
		{"objective too low", Input{Name: "x", ServiceName: "s", SLIType: SLIAvailability, Objective: 49, WindowDays: 7}, "objective"},
		{"window", Input{Name: "x", ServiceName: "s", SLIType: SLIAvailability, Objective: 99, WindowDays: 14}, "window_days"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			err := in.Validate()
			if c.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != c.field {
				t.Fatalf("Validate() = %v, want an error on %q", err, c.field)
			}
		})
	}
}

func TestInputNormalizes(t *testing.T) {
	in := Input{Name: "  Checkout  ", Description: " budget ", ServiceName: " checkout ",
		SLIType: SLIAvailability, Objective: 99.95, WindowDays: 30}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if in.Name != "Checkout" || in.ServiceName != "checkout" || in.Description != "budget" {
		t.Fatalf("not trimmed: %+v", in)
	}
	if got := in.ObjectiveFraction(); got != 0.9995 {
		t.Fatalf("ObjectiveFraction() = %v, want 0.9995", got)
	}
	if got := in.Window().Hours(); got != 720 {
		t.Fatalf("Window() = %v h, want 720 h", got)
	}
}
