package rum

import (
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestBrowserFacts(t *testing.T) {
	for _, tc := range []struct{ ua, browser, os string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "Chrome", "macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "Edge", "Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0", "Firefox", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1", "Safari", "iOS"},
		{"Mozilla/5.0 (Linux; Android 14; SM-S911B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36", "Samsung Internet", "Android"},
		{"anything a page feels like saying", "Other", "Other"},
		{"", "", ""},
	} {
		b, o := browserFacts(tc.ua)
		if b != tc.browser || o != tc.os {
			t.Errorf("%q = %q, %q, want %q, %q", tc.ua, b, o, tc.browser, tc.os)
		}
	}
}

// These are rollup keys and session columns, so they have to reach the span, where the views read them.
func TestSanitizePutsTheBrowserOnTheSpan(t *testing.T) {
	ua := []*commonpb.KeyValue{attr("user_agent.original", "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0")}
	req := request(ua, rumSpan())
	Sanitize(req, testKey(), Scope{})
	sp := firstSpan(t, req)
	if got := spanAttr(sp, AttrBrowserName); got != "Firefox" {
		t.Errorf("browser = %q", got)
	}
	if got := spanAttr(sp, AttrOSName); got != "Linux" {
		t.Errorf("os = %q", got)
	}
}

// A mobile application has no user agent and says what it runs on in the resource.
func TestSanitizeCopiesTheOSOfAMobileResourceToTheSpan(t *testing.T) {
	req := request([]*commonpb.KeyValue{attr(AttrOSName, "Android")}, rumSpan())
	Sanitize(req, testKey(), Scope{})
	sp := firstSpan(t, req)
	if got := spanAttr(sp, AttrOSName); got != "Android" {
		t.Errorf("os = %q", got)
	}
	if got := spanAttr(sp, AttrBrowserName); got != "" {
		t.Errorf("browser = %q on an application with none", got)
	}
}

func TestSanitizeDoesNotDuplicateWhatThePayloadSent(t *testing.T) {
	ua := []*commonpb.KeyValue{attr("user_agent.original", "Firefox/131.0")}
	req := request(ua, rumSpan(attr(AttrBrowserName, strings.Repeat("x", 8))))
	Sanitize(req, testKey(), Scope{})
	if n := countAttr(firstSpan(t, req), AttrBrowserName); n != 1 {
		t.Errorf("browser stored %d times", n)
	}
}
