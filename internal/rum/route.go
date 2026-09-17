package rum

import (
	"net/url"
	"strings"

	"github.com/onuragtas/openlog/internal/apm"
)

// Route normalization (rum.md §4). The cardinality of `rum_page_views_1m` and `rum_vitals_1m` is the
// cardinality of this function's output, and its input is a URL chosen by a page running a public key. So it
// is not a convenience — it is the bound that keeps a rollup from growing once per visitor.
//
// Three things narrow the input, in order:
//
//  1. The SDK may send a route it knows (`openlog.rum.route`), because a framework router knows
//     `/orders/:id` and no amount of path inspection can recover that from `/orders/8f3a`.
//  2. Whatever it sent is normalized here anyway, with the same segment rules as an APM transaction name
//     (apm.md §2.2) — ids, UUIDs, hashes and tokens become placeholders — so an SDK that sends raw paths
//     (or a caller that sends hostile ones) produces the same bounded shape.
//  3. The result is truncated and, past MaxSegments, collapsed.
//
// A route is never invented from the query string or the fragment: both are dropped before anything else,
// because they are where applications put session tokens and personal data.

// MaxRouteBytes bounds one stored route. The segment rules and the 8-segment cap come from
// apm.NormalizePath, so a browser route and a backend transaction name of the same URL look alike.
const MaxRouteBytes = 256

// RouteFromURL derives the stored route. hint is the route the SDK sent (may be empty); raw is the page URL
// or path it was measured on. The result always starts with "/" and is never empty.
func RouteFromURL(hint, raw string) string {
	if r := normalizeRoute(hint); r != "" {
		return r
	}
	return normalizeRoute(pathOf(raw))
}

// pathOf extracts the path of an absolute URL, a protocol-relative URL or a bare path. Anything unparseable
// becomes "/" rather than an error: a page view with a URL openlog cannot read is still a page view.
func pathOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/"
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		return u.Path
	}
	// A value with no parseable path (e.g. "https://example.com") is the site root.
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	return raw
}

// normalizeRoute applies the segment rules to a path. It returns "" for input that carries no path at all,
// so RouteFromURL can fall back to the URL.
func normalizeRoute(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Drop the query and the fragment before anything else: never store what an application put there.
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		v = v[:i]
	}
	// A hint may arrive as a full URL (some routers expose href); reduce it to its path.
	if strings.Contains(v, "://") || strings.HasPrefix(v, "//") {
		v = pathOf(v)
	}
	v = strings.Map(dropControl, v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	// apm.Normalize applies the shared segment rules (uuid, id, hex, token, email) and the segment cap, so
	// `/orders/42` is the same route here as it is a transaction name in APM.
	v = apm.NormalizePath(v)
	if v == "" || v == "/" {
		return "/"
	}
	if len(v) > MaxRouteBytes {
		v = truncateUTF8(v, MaxRouteBytes)
	}
	return v
}

// dropControl removes control characters and anything that would break a route as a display string.
func dropControl(r rune) rune {
	if r < 0x20 || r == 0x7f {
		return -1
	}
	return r
}

// truncateUTF8 cuts v to at most n bytes without splitting a rune.
func truncateUTF8(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for n > 0 && v[n]&0xc0 == 0x80 {
		n--
	}
	return v[:n]
}

// Domain returns the host of a page URL, lower-cased and without the port, or "" when there is none. It is
// stored on the span (not in a rollup key) so a page view can be told apart from one of another host when an
// application serves several.
func Domain(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
