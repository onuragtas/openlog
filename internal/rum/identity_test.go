package rum

import (
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// User identity and country are the two RUM fields that come from opposite directions, and the tests below
// exist to keep them pointing that way (rum.md §3.7):
//
//	user.id               the application declares it, because nothing on the server can know it
//	geo.country.iso_code  the server decides it, because a value a page can send is a value a page can invent
//
// Getting that backwards is silent: a forced field that became allowlisted still appears in the UI, still
// groups, still looks right — and is now whatever the page felt like saying.

// spanAttr returns the value of one attribute of a sanitized span, or "" when it is absent.
func spanAttr(sp *tracepb.Span, key string) string {
	for _, kv := range sp.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func countAttr(sp *tracepb.Span, key string) int {
	n := 0
	for _, kv := range sp.GetAttributes() {
		if kv.GetKey() == key {
			n++
		}
	}
	return n
}

func TestSanitizeForcesTheCountryFromTheServer(t *testing.T) {
	req := request(nil, rumSpan())
	if res := Sanitize(req, testKey(), Scope{Country: "TR"}); res.Kept != 1 {
		t.Fatalf("kept %d spans", res.Kept)
	}
	if got := spanAttr(firstSpan(t, req), AttrGeoCountry); got != "TR" {
		t.Errorf("country = %q, want TR", got)
	}
}

// Without a trusted proxy there is no country, and the field must stay blank rather than fall back to
// anything: an absent header is not an invitation to believe the page.
func TestSanitizeLeavesTheCountryBlankWithoutOne(t *testing.T) {
	req := request(nil, rumSpan())
	Sanitize(req, testKey(), Scope{})
	if got := spanAttr(firstSpan(t, req), AttrGeoCountry); got != "" {
		t.Errorf("country = %q, want empty", got)
	}
}

func TestSanitizeDropsACountryThePageSent(t *testing.T) {
	// No proxy resolved one, and the page offers its own. It must not be stored.
	req := request(nil, rumSpan(attr(AttrGeoCountry, "US")))
	Sanitize(req, testKey(), Scope{})
	if got := spanAttr(firstSpan(t, req), AttrGeoCountry); got != "" {
		t.Errorf("country = %q: the page's own value was stored", got)
	}
}

func TestSanitizeCountryOfTheServerWinsOverThePage(t *testing.T) {
	req := request(nil, rumSpan(attr(AttrGeoCountry, "US")))
	Sanitize(req, testKey(), Scope{Country: "TR"})
	sp := firstSpan(t, req)
	if got := spanAttr(sp, AttrGeoCountry); got != "TR" {
		t.Errorf("country = %q, want the server's TR", got)
	}
	// Not merely first: a duplicate key would let whichever consumer reads last see "US".
	if n := countAttr(sp, AttrGeoCountry); n != 1 {
		t.Errorf("country appears %d times, want exactly 1", n)
	}
}

func TestSanitizeKeepsTheUserIDTheApplicationSent(t *testing.T) {
	req := request(nil, rumSpan(attr(AttrUserID, "acct_8f3a2b")))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d spans", res.Kept)
	}
	if got := spanAttr(firstSpan(t, req), AttrUserID); got != "acct_8f3a2b" {
		t.Errorf("user.id = %q", got)
	}
}

func TestSanitizeBoundsTheUserID(t *testing.T) {
	long := strings.Repeat("u", MaxUserIDBytes*2)
	req := request(nil, rumSpan(attr(AttrUserID, long)))
	Sanitize(req, testKey(), Scope{})
	if got := spanAttr(firstSpan(t, req), AttrUserID); len(got) != MaxUserIDBytes {
		t.Errorf("user.id kept %d bytes, want %d", len(got), MaxUserIDBytes)
	}
}

// A session that never identified anyone carries no user id at all, rather than an empty one: an empty
// string is a value, and it would make "signed out" indistinguishable from "identified as nothing".
func TestSanitizeOmitsAnAbsentUserID(t *testing.T) {
	req := request(nil, rumSpan())
	Sanitize(req, testKey(), Scope{})
	if n := countAttr(firstSpan(t, req), AttrUserID); n != 0 {
		t.Errorf("user.id present %d times on a span that sent none", n)
	}
}

// The resource is rebuilt from the key, so neither field may sneak in as a resource attribute — that would
// put it on every span of the request regardless of what each one said.
func TestSanitizeDropsIdentityAndCountryFromTheResource(t *testing.T) {
	req := request([]*commonpb.KeyValue{
		attr(AttrUserID, "acct_from_resource"),
		attr(AttrGeoCountry, "US"),
	}, rumSpan())
	Sanitize(req, testKey(), Scope{Country: "TR"})
	for _, rs := range req.GetResourceSpans() {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == AttrUserID || kv.GetKey() == AttrGeoCountry {
				t.Errorf("resource kept %s = %q", kv.GetKey(), kv.GetValue().GetStringValue())
			}
		}
	}
}
