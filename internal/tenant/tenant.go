// Package tenant resolves license keys to tenant ids. M0 uses a static map
// parsed from OPENLOG_LICENSE_KEYS; M1 replaces it with a PostgreSQL-backed
// implementation of the same Resolver interface.
package tenant

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrUnknownKey is returned for missing or unknown license keys.
var ErrUnknownKey = errors.New("unknown or missing license key")

// Resolver maps a license key to a tenant id.
type Resolver interface {
	Resolve(ctx context.Context, licenseKey string) (tenantID string, err error)
}

// HeaderLicenseKey is the dedicated license key header (and gRPC metadata key).
const HeaderLicenseKey = "openlog-license-key"

// Static is an in-memory Resolver.
type Static struct {
	keys map[string]string
}

// ParseStatic parses "key1=tenant1,key2=tenant2". Whitespace around items is
// ignored. Duplicate keys, empty keys or empty tenants are errors.
func ParseStatic(spec string) (*Static, error) {
	s := &Static{keys: map[string]string{}}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		k, t, ok := strings.Cut(item, "=")
		k, t = strings.TrimSpace(k), strings.TrimSpace(t)
		if !ok || k == "" || t == "" {
			return nil, fmt.Errorf("OPENLOG_LICENSE_KEYS: invalid entry %q (want key=tenant)", item)
		}
		if _, dup := s.keys[k]; dup {
			return nil, fmt.Errorf("OPENLOG_LICENSE_KEYS: duplicate key %q", redact(k))
		}
		s.keys[k] = t
	}
	return s, nil
}

// Len returns the number of configured keys.
func (s *Static) Len() int { return len(s.keys) }

// Resolve implements Resolver. The comparison is done against every key in
// constant time per key to avoid leaking key prefixes through timing.
func (s *Static) Resolve(_ context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrUnknownKey
	}
	var tenant string
	for k, t := range s.keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			tenant = t
		}
	}
	if tenant == "" {
		return "", ErrUnknownKey
	}
	return tenant, nil
}

// HeaderAPIKey is accepted as an alias of HeaderLicenseKey so OTLP senders configured for other
// backends (e.g. browser SDKs sending "x-api-key") work unchanged.
const HeaderAPIKey = "x-api-key"

// KeyFromValues extracts the license key from the openlog-license-key header, the x-api-key
// header or an "Authorization: Bearer <key>" header, in that order. get returns the first value of
// a (lower- or canonical-case) header name.
func KeyFromValues(get func(name string) string) string {
	if k := strings.TrimSpace(get(HeaderLicenseKey)); k != "" {
		return k
	}
	if k := strings.TrimSpace(get(HeaderAPIKey)); k != "" {
		return k
	}
	auth := strings.TrimSpace(get("authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// KeyFromHTTP extracts the license key from HTTP headers.
func KeyFromHTTP(h http.Header) string {
	return KeyFromValues(h.Get)
}

func redact(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return k[:4] + "****"
}
