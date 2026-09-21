package rum

import (
	"fmt"
	"strings"
)

// Application allowlist of a mobile key (rum.md §3.6).
//
// **This is weaker than the origin allowlist, and the difference is not a detail.** A browser sets `Origin`
// itself and page JavaScript cannot forge it, so an origin allowlist really does stop a copied key from
// working on another website. An application identifier is *self-declared*: the app sends its own package
// name, and curl sends whatever it likes. The allowlist narrows casual reuse — a key lifted from one app's
// APK does not work in another developer's app by accident — and nothing more.
//
// What actually bounds a determined attacker is the same as for a browser key (rum.md §3.5): the key
// authorizes one endpoint, every payload is rewritten server-side from the key's own configuration, the rate
// limit is per key, and revocation takes effect within a minute. Real attestation (Play Integrity,
// App Attest) is the upgrade path and is deliberately not pretended at here.

// MaxAppIDs is how many applications one mobile key may list.
const MaxAppIDs = 50

// maxAppIDBytes bounds one identifier. Android package names are limited well below this and iOS bundle
// identifiers in practice too; the cap exists so the allowlist cannot become a storage device.
const maxAppIDBytes = 200

// ParseAppIDs validates and normalizes a mobile key's application allowlist: duplicates removed, order
// preserved. An empty list is an error for the same reason an empty origin list is — a key that accepts data
// from any application is the unsafe case, and reaching it by leaving a field blank would make it the easy
// one.
//
// Identifiers are **not** lower-cased, unlike origins: an iOS bundle identifier is case-sensitive, so
// folding case here would let `com.example.App` authorize `com.example.app`.
func ParseAppIDs(list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("at least one application id is required; a mobile key without one would accept data from any application")
	}
	if len(list) > MaxAppIDs {
		return nil, fmt.Errorf("at most %d application ids are allowed, got %d", MaxAppIDs, len(list))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		id, err := ParseAppID(raw)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// ParseAppID validates one Android package name or iOS bundle identifier. Both are reverse-DNS names, so the
// shape is checked rather than the platform guessed at: at least two dot-separated labels, each starting with
// a letter and made of letters, digits, underscores or hyphens.
//
// "*" is refused for the same reason it is refused as an origin: an allowlist that can be "anything" is the
// same as no allowlist, and would silently undo the check above.
func ParseAppID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("application id must not be empty")
	}
	if id == "*" {
		return "", fmt.Errorf(`"*" is not a valid application id for a mobile key; list the applications the key ships in`)
	}
	if len(id) > maxAppIDBytes {
		return "", fmt.Errorf("application id %q is longer than %d bytes", raw, maxAppIDBytes)
	}
	labels := strings.Split(id, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("application id %q must be a reverse-DNS name such as com.example.app", raw)
	}
	for _, label := range labels {
		if err := validAppLabel(label); err != nil {
			return "", fmt.Errorf("application id %q: %w", raw, err)
		}
	}
	return id, nil
}

func validAppLabel(label string) error {
	if label == "" {
		return fmt.Errorf("empty label (two dots in a row, or a leading or trailing dot)")
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-'):
		default:
			if i == 0 {
				return fmt.Errorf("label %q must start with a letter", label)
			}
			return fmt.Errorf("label %q may only contain letters, digits, underscores and hyphens", label)
		}
	}
	return nil
}

// AppIDAllowed reports whether the application identifier the request declared is on the key's allowlist.
// An empty declaration never matches: a mobile key always names the applications it ships in, so a request
// that claims nothing is a request the key was not issued for.
func AppIDAllowed(allow []string, appID string) bool {
	if appID == "" {
		return false
	}
	for _, a := range allow {
		if a == appID {
			return true
		}
	}
	return false
}
