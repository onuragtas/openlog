package rum

import (
	"fmt"
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// What these protect is who decides (analytics.md §1). Recognising a visitor across visits and long-lived storage are
// the key's decision, so a payload must not be able to obtain either one by sending the attribute. Getting that wrong
// is silent: everything still works, and a public key has opted itself into storing personal data.

const testVisitor = "fedcba9876543210fedcba9876543210"

func analyticsKey(mode string) Key {
	k := testKey()
	k.AnalyticsMode = mode
	return k
}

func TestSanitizeForcesTheAnalyticsMode(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"", ""},
		{AnalyticsOff, ""},
		{AnalyticsSessions, AnalyticsSessions},
		{AnalyticsVisitors, AnalyticsVisitors},
	} {
		// The payload claims visitors every time, and must never be what decides.
		req := request(nil, rumSpan(attr(AttrAnalyticsMode, AnalyticsVisitors)))
		Sanitize(req, analyticsKey(tc.mode), Scope{})
		sp := firstSpan(t, req)
		if got := spanAttr(sp, AttrAnalyticsMode); got != tc.want {
			t.Errorf("key mode %q: stored mode %q, want %q", tc.mode, got, tc.want)
		}
		if tc.want != "" && countAttr(sp, AttrAnalyticsMode) != 1 {
			t.Errorf("key mode %q: mode stored %d times", tc.mode, countAttr(sp, AttrAnalyticsMode))
		}
	}
}

func TestSanitizeKeepsTheVisitorOnlyInVisitorsMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"", ""},
		{AnalyticsOff, ""},
		{AnalyticsSessions, ""},
		{AnalyticsVisitors, testVisitor},
	} {
		req := request(nil, rumSpan(attr(AttrVisitorID, testVisitor)))
		Sanitize(req, analyticsKey(tc.mode), Scope{})
		if got := spanAttr(firstSpan(t, req), AttrVisitorID); got != tc.want {
			t.Errorf("mode %q: visitor %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// A malformed id is removed and the span kept: the page view is still a page view, and only the recognising is lost.
func TestSanitizeRemovesAMalformedVisitorAndKeepsTheSpan(t *testing.T) {
	for _, bad := range []string{"short", strings.ToUpper(testVisitor), "person@example.com", testVisitor + "00"} {
		req := request(nil, rumSpan(attr(AttrVisitorID, bad)))
		if res := Sanitize(req, analyticsKey(AnalyticsVisitors), Scope{}); res.Kept != 1 {
			t.Fatalf("%q: span was dropped", bad)
		}
		if got := spanAttr(firstSpan(t, req), AttrVisitorID); got != "" {
			t.Errorf("%q survived as %q", bad, got)
		}
	}
}

func TestSanitizeAttribution(t *testing.T) {
	sent := []*commonpb.KeyValue{
		attr(AttrReferrerHost, "WWW.Google.com"), attr(AttrUTMSource, "newsletter"),
		attr(AttrUTMMedium, "email"), attr(AttrUTMCampaign, strings.Repeat("c", MaxUTMBytes+10)),
	}

	req := request(nil, rumSpan(sent...))
	Sanitize(req, analyticsKey(AnalyticsSessions), Scope{})
	sp := firstSpan(t, req)
	if got := spanAttr(sp, AttrReferrerHost); got != "www.google.com" {
		t.Errorf("referrer = %q", got)
	}
	if got := spanAttr(sp, AttrUTMSource); got != "newsletter" {
		t.Errorf("utm source = %q", got)
	}
	if got := spanAttr(sp, AttrUTMCampaign); len(got) != MaxUTMBytes {
		t.Errorf("utm campaign kept %d bytes", len(got))
	}

	// 'off' stores what it stored before this feature, and that means none of it.
	req = request(nil, rumSpan(sent...))
	Sanitize(req, analyticsKey(AnalyticsOff), Scope{})
	for _, k := range []string{AttrReferrerHost, AttrUTMSource, AttrUTMMedium, AttrUTMCampaign} {
		if got := spanAttr(firstSpan(t, req), k); got != "" {
			t.Errorf("off mode kept %s = %q", k, got)
		}
	}
}

// A referrer is a host. A value with a path is refused rather than trimmed, since the path is what may be personal.
func TestSanitizeRefusesAReferrerThatIsNotAHost(t *testing.T) {
	for _, bad := range []string{"https://a.example/x", "a.example/reset?token=1", "user@a.example", "a b.example"} {
		req := request(nil, rumSpan(attr(AttrReferrerHost, bad)))
		Sanitize(req, analyticsKey(AnalyticsVisitors), Scope{})
		if got := spanAttr(firstSpan(t, req), AttrReferrerHost); got != "" {
			t.Errorf("%q survived as %q", bad, got)
		}
	}
}

// Attribution describes how a session started, so an event of any other kind cannot carry it.
func TestSanitizeKeepsAttributionOnPageViewsOnly(t *testing.T) {
	sp := &tracepb.Span{Name: "e", Attributes: []*commonpb.KeyValue{
		attr(AttrEvent, EventCustom), attr(AttrSessionID, testSession), attr(AttrCustomName, "signup"),
		attr(AttrReferrerHost, "a.example"), attr(AttrUTMSource, "x"),
	}}
	req := request(nil, sp)
	Sanitize(req, analyticsKey(AnalyticsVisitors), Scope{})
	if got := spanAttr(firstSpan(t, req), AttrReferrerHost) + spanAttr(firstSpan(t, req), AttrUTMSource); got != "" {
		t.Errorf("custom event kept attribution %q", got)
	}
}

func namedEvent(name string) *tracepb.Span {
	return &tracepb.Span{Name: "e", Attributes: []*commonpb.KeyValue{
		attr(AttrEvent, EventCustom), attr(AttrSessionID, testSession), attr(AttrCustomName, name),
	}}
}

// capped sends one event through Sanitize and the cap, and returns the name it would be stored under.
func capped(t *testing.T, k *Keys, key Key, name string) string {
	t.Helper()
	req := request(nil, namedEvent(name))
	Sanitize(req, key, Scope{})
	k.CapEventNames(req, key)
	return spanAttr(firstSpan(t, req), AttrCustomName)
}

func TestCapEventNamesBoundsDistinctNames(t *testing.T) {
	k := NewKeys(nil, CacheOptions{})
	key := analyticsKey(AnalyticsVisitors)
	for i := range MaxEventNamesPerKey {
		name := fmt.Sprintf("event_%d", i)
		if got := capped(t, k, key, name); got != name {
			t.Fatalf("name %d under the bound became %q", i, got)
		}
	}
	if got := capped(t, k, key, "one_too_many"); got != OtherEventName {
		t.Errorf("name past the bound stored as %q", got)
	}
	// A name admitted earlier keeps working, or the application's real events would turn into (other).
	if got := capped(t, k, key, "event_0"); got != "event_0" {
		t.Errorf("known name became %q", got)
	}
	// The bound is per key: one flooding key must not use up another's.
	other := analyticsKey(AnalyticsVisitors)
	other.KeyID = "k2"
	if got := capped(t, k, other, "signup"); got != "signup" {
		t.Errorf("another key's name became %q", got)
	}
}

func TestCapEventNamesLeavesAnOffKeyAlone(t *testing.T) {
	k := NewKeys(nil, CacheOptions{})
	key := analyticsKey(AnalyticsOff)
	for i := range MaxEventNamesPerKey + 5 {
		name := fmt.Sprintf("event_%d", i)
		if got := capped(t, k, key, name); got != name {
			t.Fatalf("off key: %q became %q", name, got)
		}
	}
	if len(k.eventNames) != 0 {
		t.Errorf("off key was tracked: %d entries", len(k.eventNames))
	}
}
