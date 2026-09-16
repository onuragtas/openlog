package synthetics

import (
	"errors"
	"strings"
	"testing"
)

func validInput() Input {
	return Input{Name: "Checkout", URL: "https://shop.example.com/health", Enabled: true}
}

func TestValidateDefaults(t *testing.T) {
	in := validInput()
	if err := in.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if in.Type != TypeHTTP || in.Method != "GET" || in.TimeoutMs != DefaultTimeoutMs || in.IntervalSeconds != DefaultInterval {
		t.Fatalf("defaults: %+v", in)
	}
	if len(in.ExpectedStatus) != 1 || in.ExpectedStatus[0] != 200 {
		t.Fatalf("expected_status default %v", in.ExpectedStatus)
	}
	if len(in.Locations) != 1 || in.Locations[0] != LocationLocal {
		t.Fatalf("locations default %v", in.Locations)
	}
	if in.AssertionType != AssertNone || in.Headers == nil {
		t.Fatalf("assertion/headers default: %q %v", in.AssertionType, in.Headers)
	}
}

func TestValidateNormalizes(t *testing.T) {
	in := validInput()
	in.Name = "  Checkout  "
	in.Method = "post"
	in.Body = `{"ping":true}`
	in.ExpectedStatus = []int{204, 200, 200}
	in.Locations = []string{LocationLocal, LocationLocal}
	in.Headers = map[string]string{" X-Token ": "abc"}
	if err := in.Validate(); err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if in.Name != "Checkout" || in.Method != "POST" {
		t.Errorf("name/method: %q %q", in.Name, in.Method)
	}
	// Duplicates are dropped and the codes sorted, so the stored definition is canonical.
	if len(in.ExpectedStatus) != 2 || in.ExpectedStatus[0] != 200 || in.ExpectedStatus[1] != 204 {
		t.Errorf("expected_status %v", in.ExpectedStatus)
	}
	if len(in.Locations) != 1 {
		t.Errorf("locations %v", in.Locations)
	}
	if _, ok := in.Headers["X-Token"]; !ok {
		t.Errorf("header name not trimmed: %v", in.Headers)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Input){
		"empty name":          func(in *Input) { in.Name = "  " },
		"long name":           func(in *Input) { in.Name = strings.Repeat("x", MaxNameRunes+1) },
		"no url":              func(in *Input) { in.URL = "" },
		"wrong scheme":        func(in *Input) { in.URL = "ftp://example.com" },
		"relative url":        func(in *Input) { in.URL = "/health" },
		"url credentials":     func(in *Input) { in.URL = "https://user:pw@example.com/" },
		"url fragment":        func(in *Input) { in.URL = "https://example.com/#top" },
		"long url":            func(in *Input) { in.URL = "https://example.com/" + strings.Repeat("x", MaxURLBytes) },
		"unknown method":      func(in *Input) { in.Method = "TRACE" },
		"body without method": func(in *Input) { in.Method = "GET"; in.Body = "x" },
		"long body":           func(in *Input) { in.Method = "POST"; in.Body = strings.Repeat("x", MaxBodyBytes+1) },
		"reserved header":     func(in *Input) { in.Headers = map[string]string{"Host": "evil.example.com"} },
		"header newline":      func(in *Input) { in.Headers = map[string]string{"X-A": "a\r\nX-B: b"} },
		"header name":         func(in *Input) { in.Headers = map[string]string{"X A": "b"} },
		"too many headers":    func(in *Input) { in.Headers = manyHeaders(MaxHeaders + 1) },
		"status out of range": func(in *Input) { in.ExpectedStatus = []int{99} },
		"too many statuses":   func(in *Input) { in.ExpectedStatus = []int{200, 201, 202, 203, 204, 205, 206, 207, 208, 226, 300} },
		"timeout too small":   func(in *Input) { in.TimeoutMs = 10 },
		"timeout too large":   func(in *Input) { in.TimeoutMs = MaxTimeoutMs + 1 },
		"timeout over interval": func(in *Input) {
			in.TimeoutMs = 60000
			in.IntervalSeconds = 30
		},
		"interval too small": func(in *Input) { in.IntervalSeconds = 5 },
		"interval too large": func(in *Input) { in.IntervalSeconds = MaxIntervalSecs + 1 },
		"unknown location":   func(in *Input) { in.Locations = []string{"eu-west"} },
		"unknown assertion":  func(in *Input) { in.AssertionType = "regex" },
		"contains no value":  func(in *Input) { in.AssertionType = AssertContains },
		"json_path no path":  func(in *Input) { in.AssertionType = AssertJSONPath },
		"json_path bad path": func(in *Input) { in.AssertionType = AssertJSONPath; in.AssertionPath = "data..x" },
		"unknown type":       func(in *Input) { in.Type = "tcp" },
	}
	for name, mutate := range cases {
		in := validInput()
		mutate(&in)
		if err := in.Validate(); err == nil {
			t.Errorf("%s accepted: %+v", name, in)
		}
	}
}

func TestValidationErrorNamesTheField(t *testing.T) {
	in := validInput()
	in.URL = "ftp://example.com"
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "url" {
		t.Fatalf("error %v (%T)", err, err)
	}
	if !strings.Contains(ve.Error(), "url:") {
		t.Errorf("message %q", ve.Error())
	}
}

// An assertion that no longer applies is cleared, so a check switched back to "none" keeps no stale value.
func TestValidateClearsUnusedAssertion(t *testing.T) {
	in := validInput()
	in.AssertionType = AssertNone
	in.AssertionPath, in.AssertionValue = "data.ok", "true"
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if in.AssertionPath != "" || in.AssertionValue != "" {
		t.Fatalf("assertion not cleared: %q %q", in.AssertionPath, in.AssertionValue)
	}
}

func TestExpectsStatus(t *testing.T) {
	in := validInput()
	in.ExpectedStatus = []int{200, 204}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if !in.ExpectsStatus(204) || in.ExpectsStatus(500) {
		t.Fatal("ExpectsStatus")
	}
}

func TestJSONPath(t *testing.T) {
	doc := map[string]any{
		"status": "ok",
		"count":  float64(3),
		"live":   true,
		"nil":    nil,
		"data":   map[string]any{"items": []any{map[string]any{"state": "up"}}},
	}
	cases := map[string]string{
		"status":             "ok",
		"count":              "3",
		"live":               "true",
		"nil":                "null",
		"data.items.0.state": "up",
		"data.items.0":       `{"state":"up"}`,
	}
	for path, want := range cases {
		got, ok := JSONPath(doc, path)
		if !ok || got != want {
			t.Errorf("%s = %q %v, want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{"missing", "data.items.9", "data.items.x", "status.deep", "count.x"} {
		if got, ok := JSONPath(doc, path); ok {
			t.Errorf("%s resolved to %q", path, got)
		}
	}
}

func manyHeaders(n int) map[string]string {
	out := map[string]string{}
	for i := range n {
		out["X-H"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	return out
}
