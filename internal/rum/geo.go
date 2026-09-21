package rum

import "strings"

// The visitor's country (rum.md §3.7).
//
// openlog resolves no addresses of its own. There is no GeoIP database to ship, license and keep fresh, and
// — the part that matters — **no visitor address is ever stored**: the country arrives already resolved, in
// a header a proxy or CDN in front of ingest wrote, and the address it was derived from never leaves that
// proxy. The operator names the header (OPENLOG_RUM_GEO_HEADER) because only they know what is in front of
// them; unset, no country is recorded at all.
//
// The trust argument is short: anything openlog is fronted by can be trusted to write this header exactly as
// far as it can be trusted to forward the request in the first place. If that is not true of a deployment,
// the setting stays empty and the field stays blank — an honest blank rather than a guess.

// ParseCountry normalizes what the proxy declared into an ISO 3166-1 alpha-2 code, or "" when it declared
// nothing usable.
//
// Two ASCII letters, upper-cased. Anything else is discarded rather than stored as-is: the value reaches a
// facet in the UI, and a header nobody validated is exactly where a surprise string comes from.
func ParseCountry(raw string) string {
	c := strings.ToUpper(strings.TrimSpace(raw))
	if len(c) != 2 {
		return ""
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return ""
		}
	}
	// Codes the CDNs use to say "I could not tell": Cloudflare sends XX when it has no answer and T1 for
	// traffic arriving over Tor. Both are "unknown" wearing a country's clothes, and storing them would put
	// two fake countries at the top of every geography breakdown.
	if c == "XX" || c == "T1" {
		return ""
	}
	return c
}
