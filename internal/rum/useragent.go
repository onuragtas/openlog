package rum

import "strings"

// browserFacts turns a user agent into the two facets the rollups are keyed by. The SDK sends the user agent and never
// these, so without this the browser column and filter of every browser application stay empty.
//
// Derived here rather than in the SDK so that it also corrects data from SDKs already shipped, and from a closed list
// because the result reaches a rollup key: a page must not be able to mint browser names (rum.md §3.3). Anything else is
// "Other", and the order matters because every Chromium browser also says Chrome and Safari.
func browserFacts(ua string) (browser, os string) {
	if ua == "" {
		return "", ""
	}
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case has("Edg/"), has("EdgA/"), has("EdgiOS/"):
		browser = "Edge"
	case has("OPR/"), has("OPiOS/"):
		browser = "Opera"
	case has("SamsungBrowser/"):
		browser = "Samsung Internet"
	case has("Firefox/"), has("FxiOS/"):
		browser = "Firefox"
	case has("Chrome/"), has("CriOS/"):
		browser = "Chrome"
	case has("Safari/"):
		browser = "Safari"
	default:
		browser = "Other"
	}
	switch {
	case has("iPhone"), has("iPad"), has("iPod"):
		os = "iOS"
	case has("Android"):
		os = "Android"
	case has("Windows"):
		os = "Windows"
	case has("Mac OS X"), has("Macintosh"):
		os = "macOS"
	case has("CrOS"):
		os = "ChromeOS"
	case has("Linux"):
		os = "Linux"
	default:
		os = "Other"
	}
	return browser, os
}
