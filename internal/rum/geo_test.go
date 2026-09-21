package rum

import "testing"

// The country is the one RUM field openlog takes from a header rather than from the payload, so what it
// accepts is the whole of its validation. Everything here is about refusing to store something that merely
// looks like a country: the value reaches a facet in the UI, and a header nobody checked is exactly where a
// surprise string comes from.
func TestParseCountry(t *testing.T) {
	for raw, want := range map[string]string{
		"TR":      "TR",
		"DE":      "DE",
		"tr":      "TR", // proxies differ on case; the stored value must not
		"  tr  ":  "TR",
		"":        "",
		"T":       "",
		"TUR":     "",
		"T1":      "", // Tor, per Cloudflare: not a country
		"XX":      "", // "I could not tell", per Cloudflare: not a country
		"12":      "",
		"T-":      "",
		"tr,de":   "",
		"Türkiye": "",
		"  ":      "",
	} {
		if got := ParseCountry(raw); got != want {
			t.Errorf("ParseCountry(%q) = %q, want %q", raw, got, want)
		}
	}
}

// XX and T1 are the reason this function exists at all rather than a length check: both are two ASCII
// letters and would sail through one, and both would then sit at the top of every geography breakdown as
// though they were places.
func TestParseCountryRefusesTheUnknownPlaceholders(t *testing.T) {
	for _, raw := range []string{"XX", "xx", " Xx ", "T1", "t1"} {
		if got := ParseCountry(raw); got != "" {
			t.Errorf("ParseCountry(%q) = %q, want the empty string", raw, got)
		}
	}
}
