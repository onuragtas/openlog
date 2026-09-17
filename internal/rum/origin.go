package rum

import (
	"fmt"
	"net/url"
	"strings"
)

// Origin allowlist of a browser key (rum.md §3.2).
//
// The syntax is deliberately the same as OPENLOG_INGEST_CORS_ALLOWED_ORIGINS (config.md, internal/ingest
// cors.go): an exact origin (`https://app.example.com`, port included when it is not the scheme default) or a
// subdomain wildcard (`https://*.example.com`). An operator who already allowed an origin for CORS should not
// have to learn a second matcher to allow it for a key.
//
// **What this check is and is not.** The `Origin` header is set by the browser and cannot be forged by page
// JavaScript, so the allowlist does stop a copied key from working on another *website*. It is not a defence
// against a program: curl sends whatever Origin it likes. The allowlist narrows casual reuse; the rate limit,
// the payload validation and revocation are what bound a determined attacker (rum.md §3.5).

// MaxOrigins is how many entries one key's allowlist may have.
const MaxOrigins = 50

// ParseOrigins validates and normalizes an allowlist: lower-cased, without a trailing slash, duplicates
// removed, order preserved. An empty list is an error — a browser key without an allowlist accepts data from
// any page on the internet, and making that reachable by leaving a field blank would make the unsafe case the
// easy one.
func ParseOrigins(list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("at least one origin is required; a browser key without an origin allowlist would accept data from any website")
	}
	if len(list) > MaxOrigins {
		return nil, fmt.Errorf("at most %d origins are allowed, got %d", MaxOrigins, len(list))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		o, err := ParseOrigin(raw)
		if err != nil {
			return nil, err
		}
		if seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out, nil
}

// ParseOrigin validates one entry. "*" is refused on purpose: it exists in the CORS variable because an
// operator configuring their own server may knowingly open it, but a per-key allowlist that can be "anything"
// is the same as no allowlist and would silently undo the check above.
func ParseOrigin(raw string) (string, error) {
	o := strings.ToLower(strings.TrimSpace(raw))
	o = strings.TrimSuffix(o, "/")
	if o == "" {
		return "", fmt.Errorf("origin must not be empty")
	}
	if o == "*" {
		return "", fmt.Errorf(`"*" is not a valid origin for a browser key; list the sites the key is used on`)
	}
	scheme, rest, ok := strings.Cut(o, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return "", fmt.Errorf("origin %q must start with http:// or https://", raw)
	}
	host := rest
	wildcard := strings.HasPrefix(rest, "*.")
	if wildcard {
		host = rest[2:]
		if strings.Count(host, ".") < 1 {
			return "", fmt.Errorf("origin %q: a wildcard needs a domain with at least two labels, e.g. https://*.example.com", raw)
		}
	}
	if host == "" || strings.ContainsAny(host, "/?#@ \t") {
		return "", fmt.Errorf("origin %q must be a scheme and a host only, without a path, query or credentials", raw)
	}
	// Parse the host form to reject anything url.Parse would not accept as a host (spaces, brackets, …).
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("origin %q must be a scheme and a host only, without a path, query or credentials", raw)
	}
	return o, nil
}

// OriginAllowed reports whether origin (the browser's Origin header) matches the allowlist. The matching is
// the ingest CORS matcher's: an exact comparison, or a wildcard whose domain suffix matches and whose prefix
// contains no "/" or ":" — so `https://*.example.com` covers `a.example.com` and `a.b.example.com` (any
// subdomain depth) but not a different port, which must be listed exactly.
func OriginAllowed(allow []string, origin string) bool {
	o := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(origin), "/"))
	if o == "" || o == "null" {
		return false
	}
	for _, a := range allow {
		scheme, rest, ok := strings.Cut(a, "://")
		if !ok {
			continue
		}
		if !strings.HasPrefix(rest, "*.") {
			if a == o {
				return true
			}
			continue
		}
		domain := rest[1:] // ".example.com"
		host, ok := strings.CutPrefix(o, scheme+"://")
		if !ok || !strings.HasSuffix(host, domain) || len(host) <= len(domain) {
			continue
		}
		if !strings.ContainsAny(host[:len(host)-len(domain)], "/:") {
			return true
		}
	}
	return false
}
