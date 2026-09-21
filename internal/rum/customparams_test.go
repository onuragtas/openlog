package rum

import (
	"strconv"
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
)

func param(name, value string) *commonpb.KeyValue { return attr(AttrCustomParamPrefix+name, value) }

// Parameters are the application's own vocabulary, so they travel in a namespace of their own rather than
// through the allowlist — which could never enumerate them.
func TestSanitizeKeepsCustomParams(t *testing.T) {
	req := request(nil, customSpan(
		attr(AttrCustomName, "checkout_started"),
		param("plan", "pro"),
		param("step.index", "2"),
		param("ab_test", "variant-b"),
	))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	for name, want := range map[string]string{"plan": "pro", "step.index": "2", "ab_test": "variant-b"} {
		if got[AttrCustomParamPrefix+name] != want {
			t.Errorf("%s = %q, want %q", name, got[AttrCustomParamPrefix+name], want)
		}
	}
}

// A name the query language cannot address without quoting is not a usable dimension, so it is dropped
// rather than escaped into something the application did not choose.
func TestSanitizeDropsUnusableParamNames(t *testing.T) {
	req := request(nil, customSpan(
		attr(AttrCustomName, "checkout_started"),
		param("Plan", "pro"),               // upper case
		param("plan name", "pro"),          // space
		param("plan'); DROP--", "pro"),     // punctuation
		param("", "pro"),                   // empty
		param(strings.Repeat("x", 65), ""), // longer than MaxCustomParamKeyBytes
		param("kept", "yes"),
	))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if got[AttrCustomParamPrefix+"kept"] != "yes" {
		t.Errorf("the valid parameter did not survive: %v", got)
	}
	for _, bad := range []string{"Plan", "plan name", "plan'); DROP--", "", strings.Repeat("x", 65)} {
		if _, ok := got[AttrCustomParamPrefix+bad]; ok {
			t.Errorf("parameter %q survived", bad)
		}
	}
}

// The cap is applied after sorting. Go map order is random, so without the sort "the first 16" would be a
// different 16 on every request and this test would pass only sometimes — which is the failure it exists to
// prevent: the payload would decide what is stored, and so would the run.
func TestSanitizeCapsParamsDeterministically(t *testing.T) {
	attrs := []*commonpb.KeyValue{attr(AttrCustomName, "checkout_started")}
	for i := 0; i < MaxCustomParams+10; i++ {
		attrs = append(attrs, param("p"+strconv.Itoa(100+i), "v"+strconv.Itoa(i)))
	}
	for run := 0; run < 5; run++ {
		req := request(nil, customSpan(attrs...))
		if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
			t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
		}
		got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
		var kept []string
		for k := range got {
			if strings.HasPrefix(k, AttrCustomParamPrefix) {
				kept = append(kept, k)
			}
		}
		if len(kept) != MaxCustomParams {
			t.Fatalf("run %d: kept %d parameters, want %d", run, len(kept), MaxCustomParams)
		}
		// Sorted by name, so the survivors are the first 16 of p100..p125 on every run.
		for i := 0; i < MaxCustomParams; i++ {
			name := AttrCustomParamPrefix + "p" + strconv.Itoa(100+i)
			if _, ok := got[name]; !ok {
				t.Fatalf("run %d: %s did not survive; the cap is not applied in a stable order", run, name)
			}
		}
	}
}

// Values are bounded like every other attribute: a public key must not be able to store a document.
func TestSanitizeBoundsParamValues(t *testing.T) {
	long := strings.Repeat("v", MaxCustomParamValueBytes+500)
	req := request(nil, customSpan(attr(AttrCustomName, "checkout_started"), param("blob", long)))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if n := len(got[AttrCustomParamPrefix+"blob"]); n != MaxCustomParamValueBytes {
		t.Errorf("value kept %d bytes, want %d", n, MaxCustomParamValueBytes)
	}
}

// Parameters belong to custom events. On any other kind they are not "already decided" and must fall to the
// allowlist, which does not know them — otherwise the namespace would be a way to write arbitrary attributes
// onto a page view.
func TestSanitizeDropsParamsOnOtherEvents(t *testing.T) {
	req := request(nil, rumSpan(param("plan", "pro")))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	if got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes()); got[AttrCustomParamPrefix+"plan"] != "" {
		t.Errorf("a parameter survived on a page view: %v", got)
	}
}
