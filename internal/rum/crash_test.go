package rum

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const testCrashID = "abcdefabcdefabcdefabcdefabcdefab"

func crashSpan(kind string, at time.Time) *tracepb.Span {
	return &tracepb.Span{Name: "error X", StartTimeUnixNano: uint64(at.UnixNano()), EndTimeUnixNano: uint64(at.UnixNano()),
		Attributes: []*commonpb.KeyValue{
			attr(AttrEvent, EventError), attr(AttrSessionID, testSession),
			attr(AttrCrashKind, kind), attr(AttrCrashID, testCrashID),
		}}
}

func TestSanitizeKeepsACrashAtTheMomentItHappened(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := now.Add(-3 * 24 * time.Hour)
	req := request(nil, crashSpan("signal", at))
	if res := Sanitize(req, testKey(), Scope{Now: now}); res.Kept != 1 {
		t.Fatalf("kept %d", res.Kept)
	}
	sp := firstSpan(t, req)
	if got := spanAttr(sp, AttrCrashKind); got != "signal" {
		t.Errorf("kind = %q", got)
	}
	if got := spanAttr(sp, AttrCrashID); got != testCrashID {
		t.Errorf("id = %q", got)
	}
	if sp.GetStartTimeUnixNano() != uint64(at.UnixNano()) {
		t.Errorf("the time of the crash was changed")
	}
}

func TestSanitizeDropsACrashPastTheRetention(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	res := Sanitize(request(nil, crashSpan("signal", now.Add(-8*24*time.Hour))), testKey(), Scope{Now: now})
	if res.Kept != 0 || res.Dropped[ReasonCrashTooOld] != 1 {
		t.Errorf("result = %+v", res)
	}
}

// A wrong device clock must not put a crash ahead of every chart.
func TestSanitizeMovesACrashFromTheFutureToNow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	req := request(nil, crashSpan("signal", now.Add(24*time.Hour)))
	Sanitize(req, testKey(), Scope{Now: now})
	if got := firstSpan(t, req).GetStartTimeUnixNano(); got != uint64(now.UnixNano()) {
		t.Errorf("start = %d", got)
	}
}

// Release health counts this, so a page must not be able to invent kinds or claim one on a span that is no error.
func TestSanitizeRefusesAnUnknownKindAndKeepsTheError(t *testing.T) {
	req := request(nil, crashSpan("made_up", time.Now()))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d", res.Kept)
	}
	if got := spanAttr(firstSpan(t, req), AttrCrashKind); got != "" {
		t.Errorf("kind = %q", got)
	}
}

func TestSanitizeKeepsCrashAttributesOnErrorsOnly(t *testing.T) {
	req := request(nil, rumSpan(attr(AttrCrashKind, "signal")))
	Sanitize(req, testKey(), Scope{})
	if got := spanAttr(firstSpan(t, req), AttrCrashKind); got != "" {
		t.Errorf("a page view kept kind %q", got)
	}
}

func TestSanitizeKeepsTheCaptureSignalOnTheResource(t *testing.T) {
	req := request([]*commonpb.KeyValue{attr(AttrCrashCapture, "1")}, rumSpan())
	Sanitize(req, testKey(), Scope{})
	var found bool
	for _, kv := range req.GetResourceSpans()[0].GetResource().GetAttributes() {
		found = found || (kv.GetKey() == AttrCrashCapture && kv.GetValue().GetStringValue() == "1")
	}
	if !found {
		t.Error("the capture signal was dropped")
	}
}
