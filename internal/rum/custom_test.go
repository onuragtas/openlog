package rum

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
)

func customSpan(attrs ...*commonpb.KeyValue) *tracepb.Span {
	base := []*commonpb.KeyValue{attr(AttrEvent, EventCustom), attr(AttrSessionID, testSession)}
	return &tracepb.Span{Name: "checkout_started", Attributes: append(base, attrs...)}
}

func TestSanitizeKeepsNamedCustomEvent(t *testing.T) {
	req := request(nil, customSpan(attr(AttrCustomName, "checkout_started")))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if got[AttrCustomName] != "checkout_started" {
		t.Errorf("name = %q", got[AttrCustomName])
	}
	// Counting is as valid as timing: an event with no number is not half an event.
	if _, ok := got[AttrCustomValue]; ok {
		t.Errorf("a value appeared from nowhere: %v", got[AttrCustomValue])
	}
}

func TestSanitizeKeepsCustomTiming(t *testing.T) {
	req := request(nil, customSpan(
		attr(AttrCustomName, "cart_priced"),
		attr(AttrCustomValue, "42.5"),
		attr(AttrCustomUnit, "ms"),
	))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if got[AttrCustomName] != "cart_priced" || got[AttrCustomValue] != "42.5" || got[AttrCustomUnit] != "ms" {
		t.Errorf("timing = %v", got)
	}
}

// The name is what every query written against a custom event groups by, so an unnamed one is not a smaller
// event — it is a row nothing can ever ask for.
func TestSanitizeRefusesUnnamedCustomEvent(t *testing.T) {
	for _, span := range []*tracepb.Span{
		customSpan(),
		customSpan(attr(AttrCustomName, "   ")),
	} {
		res := Sanitize(request(nil, span), testKey(), Scope{})
		if res.Kept != 0 || res.Dropped[ReasonBadCustom] != 1 {
			t.Errorf("kept %d, dropped %v; want the span refused", res.Kept, res.Dropped)
		}
	}
}

// A value that is not a usable number would reach every average and percentile computed over it. "NaN" is in
// the list on purpose: ParseFloat accepts it, so rejecting it takes an explicit check.
func TestSanitizeRefusesUnusableCustomValue(t *testing.T) {
	for _, bad := range []string{"not-a-number", "NaN", "Inf", "-Inf", "1e13", "-1e13"} {
		span := customSpan(attr(AttrCustomName, "cart_priced"), attr(AttrCustomValue, bad))
		res := Sanitize(request(nil, span), testKey(), Scope{})
		if res.Kept != 0 || res.Dropped[ReasonBadCustom] != 1 {
			t.Errorf("value %q: kept %d, dropped %v; want the span refused", bad, res.Kept, res.Dropped)
		}
	}
}

// A unit without a value measures nothing, so it is not stored: "ms" alone on a counted event would make a
// chart claim a measurement that was never taken.
func TestSanitizeDropsCustomUnitWithoutValue(t *testing.T) {
	req := request(nil, customSpan(attr(AttrCustomName, "checkout_started"), attr(AttrCustomUnit, "ms")))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	if got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes()); got[AttrCustomUnit] != "" {
		t.Errorf("unit = %q, want it dropped without a value", got[AttrCustomUnit])
	}
}

// A custom event must not become a way to write attributes the allowlist refuses everywhere else.
func TestSanitizeDropsForeignAttributesOnCustomEvents(t *testing.T) {
	req := request(nil, customSpan(
		attr(AttrCustomName, "checkout_started"),
		attr("host.id", "prod-db-1"),
		attr("openlog.entity.type", "host"),
	))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d, dropped %v", res.Kept, res.Dropped)
	}
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if _, ok := got["host.id"]; ok {
		t.Errorf("host.id survived on a custom event: %v", got)
	}
	if _, ok := got["openlog.entity.type"]; ok {
		t.Errorf("entity type survived on a custom event: %v", got)
	}
}
