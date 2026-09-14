package auth

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// DomainBlocklist blocks sign-ups from e-mail domains and their subdomains (e.g. disposable address
// providers; OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS and _FILE). Use Blocked as Config.EmailDomainBlocked.
type DomainBlocklist map[string]struct{}

// ParseDomainBlocklist reads domains from items and from r (one per line, "#" starts a comment; r may be nil).
func ParseDomainBlocklist(items []string, r io.Reader) (DomainBlocklist, error) {
	out := DomainBlocklist{}
	add := func(d string) error {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "@")
		if d == "" {
			return nil
		}
		if strings.ContainsAny(d, " \t/@") || !strings.Contains(d, ".") {
			return fmt.Errorf("invalid e-mail domain %q", d)
		}
		out[strings.TrimSuffix(d, ".")] = struct{}{}
		return nil
	}
	for _, it := range items {
		if err := add(it); err != nil {
			return nil, err
		}
	}
	if r != nil {
		sc := bufio.NewScanner(r)
		for n := 1; sc.Scan(); n++ {
			line, _, _ := strings.Cut(sc.Text(), "#")
			if err := add(line); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Blocked reports whether domain or one of its parent domains is listed.
func (b DomainBlocklist) Blocked(domain string) bool {
	d := strings.TrimSuffix(strings.ToLower(domain), ".")
	for d != "" {
		if _, ok := b[d]; ok {
			return true
		}
		_, rest, found := strings.Cut(d, ".")
		if !found {
			return false
		}
		d = rest
	}
	return false
}
