package auth

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Browser key management (docs/contracts/rum.md §3, api.md "Browser keys", D-136).
//
// A browser key is public by construction, so the operations below are not about protecting a secret. They
// are about keeping the blast radius of a value anyone can copy small and revocable: every key is bound to
// one application name, one origin allowlist and one rate limit, and revoking it is a single statement whose
// effect reaches every ingest pod within OPENLOG_AUTH_CACHE_TTL.
//
// **Origin syntax is validated by the caller** (internal/api, through rum.ParseOrigins) rather than here:
// internal/quota already imports this package, so importing internal/rum from it would close a cycle. What
// this file guarantees is that a key never reaches the store with an empty allowlist, an out-of-range limit
// or an unusable name.

// Bounds of a browser key, enforced here and by the CHECK constraints of 0093_browser_keys.sql.
const (
	MinBrowserRateLimit = 60
	MaxBrowserRateLimit = 10000000
	// DefaultBrowserRateLimit is generous for a normal site (100 events/second) and still bounds a runaway
	// page: at roughly 8 events per page view it is about 750 page views a minute per ingest pod.
	DefaultBrowserRateLimit = 6000
	MaxBrowserOrigins       = 50
	maxServiceNameRunes     = 512
	maxEnvironmentRunes     = 256
)

// normalize validates and cleans a browser key input. Origins must already be normalized by the caller.
func (in *BrowserKeyInput) normalize() error {
	name, err := cleanName(in.Name, "name", true)
	if err != nil {
		return err
	}
	in.Name = name

	in.ServiceName = strings.TrimSpace(in.ServiceName)
	if in.ServiceName == "" {
		return invalid("service_name is required: it is the application this key's data is stored under")
	}
	if utf8.RuneCountInString(in.ServiceName) > maxServiceNameRunes || !utf8.ValidString(in.ServiceName) {
		return invalid("service_name must be at most %d characters", maxServiceNameRunes)
	}
	if hasControl(in.ServiceName) {
		return invalid("service_name must not contain control characters")
	}

	in.Environment = strings.TrimSpace(in.Environment)
	if utf8.RuneCountInString(in.Environment) > maxEnvironmentRunes || !utf8.ValidString(in.Environment) {
		return invalid("environment must be at most %d characters", maxEnvironmentRunes)
	}
	if hasControl(in.Environment) {
		return invalid("environment must not contain control characters")
	}

	if len(in.Origins) == 0 {
		return invalid("at least one origin is required; a browser key without an origin allowlist would accept data from any website")
	}
	if len(in.Origins) > MaxBrowserOrigins {
		return invalid("at most %d origins are allowed", MaxBrowserOrigins)
	}

	if in.RateLimitPerMinute == 0 {
		in.RateLimitPerMinute = DefaultBrowserRateLimit
	}
	if in.RateLimitPerMinute < MinBrowserRateLimit || in.RateLimitPerMinute > MaxBrowserRateLimit {
		return invalid("rate_limit_per_minute must be between %d and %d", MinBrowserRateLimit, MaxBrowserRateLimit)
	}

	if in.SampleRate == 0 {
		in.SampleRate = 1
	}
	if in.SampleRate <= 0 || in.SampleRate > 1 {
		return invalid("sample_rate must be greater than 0 and at most 1")
	}
	return nil
}

func hasControl(v string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// browserKeyDetails is the audit payload of a browser key change. The value never appears in it — not
// because it is secret, but because an audit log that quotes credentials becomes a credential store.
func browserKeyDetails(k BrowserKey) map[string]any {
	return map[string]any{
		"name": k.Name, "prefix": k.Prefix, "service_name": k.ServiceName,
		"environment": k.Environment, "origins": k.Origins,
		"rate_limit_per_minute": k.RateLimitPerMinute, "sample_rate": k.SampleRate,
	}
}

// ListBrowserKeys lists the organization's browser keys (member+).
func (s *Service) ListBrowserKeys(ctx context.Context, p *Principal) ([]BrowserKey, error) {
	if err := s.gate(p, ActListBrowserKeys); err != nil {
		return nil, err
	}
	ks, err := s.store.ListBrowserKeys(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ks, nil
}

// GetBrowserKey returns one browser key (member+).
func (s *Service) GetBrowserKey(ctx context.Context, p *Principal, id string) (BrowserKey, error) {
	if err := s.gate(p, ActListBrowserKeys); err != nil {
		return BrowserKey{}, err
	}
	k, err := s.store.GetBrowserKey(ctx, p.OrgID, id)
	if err != nil {
		return BrowserKey{}, s.fail(err)
	}
	return k, nil
}

// CreateBrowserKey creates a browser key (admin+, signed-in user) and returns it together with its
// plaintext value. Unlike a license key the value is returned again by nothing — but it is also not a
// secret, so an operator who loses it can read it out of their own web page, or simply create another.
func (s *Service) CreateBrowserKey(ctx context.Context, p *Principal, in BrowserKeyInput, meta ClientMeta) (BrowserKey, string, error) {
	if err := s.gate(p, ActManageBrowserKeys); err != nil {
		return BrowserKey{}, "", err
	}
	if err := s.requireVerified(p); err != nil {
		return BrowserKey{}, "", err
	}
	if err := in.normalize(); err != nil {
		return BrowserKey{}, "", err
	}
	// Always generated, never operator-chosen: an imported browser key would end up in a public bundle
	// with whatever entropy the operator felt like, and the prefix exists so a leaked value is traceable.
	secret, err := NewSecret(PrefixBrowserKey)
	if err != nil {
		return BrowserKey{}, "", err
	}
	hash, _ := s.keyHashes(secret)
	now := s.now()
	k := BrowserKey{
		OrgID: p.OrgID, Name: in.Name, Prefix: DisplayPrefix(secret), Hash: hash,
		ServiceName: in.ServiceName, Environment: in.Environment, Origins: in.Origins,
		RateLimitPerMinute: in.RateLimitPerMinute, SampleRate: in.SampleRate,
		CreatedBy: p.UserID, CreatedByEmail: p.Email, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateBrowserKey(ctx, &k); err != nil {
		return BrowserKey{}, "", s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "browser_key.create", "browser_key", k.ID, browserKeyDetails(k))
	return k, secret, nil
}

// UpdateBrowserKey replaces the editable fields of a browser key (admin+, signed-in user). The value and the
// application it writes under are the two things a change must be able to reach: moving a key to another
// origin list is how an operator responds to abuse without redeploying the site.
func (s *Service) UpdateBrowserKey(ctx context.Context, p *Principal, id string, in BrowserKeyInput, meta ClientMeta) (BrowserKey, error) {
	if err := s.gate(p, ActManageBrowserKeys); err != nil {
		return BrowserKey{}, err
	}
	if err := in.normalize(); err != nil {
		return BrowserKey{}, err
	}
	k, err := s.store.UpdateBrowserKey(ctx, p.OrgID, id, in, p.UserID, s.now())
	if err != nil {
		return BrowserKey{}, s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "browser_key.update", "browser_key", k.ID, browserKeyDetails(k))
	return k, nil
}

// RevokeBrowserKey revokes a browser key (admin+, signed-in user). Revoking twice is a no-op.
//
// Revocation is the only real remedy for a public key, and it is not instant: ingest pods keep accepting the
// value until their cache entry expires (OPENLOG_AUTH_CACHE_TTL, 60 s by default), and pages already loaded
// keep sending it until they are reloaded. The API says so rather than implying an immediate cut-off.
func (s *Service) RevokeBrowserKey(ctx context.Context, p *Principal, id string, meta ClientMeta) (BrowserKey, error) {
	if err := s.gate(p, ActManageBrowserKeys); err != nil {
		return BrowserKey{}, err
	}
	k, err := s.store.RevokeBrowserKey(ctx, p.OrgID, id, p.UserID, s.now())
	if err != nil {
		return BrowserKey{}, s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "browser_key.revoke", "browser_key", id,
		map[string]any{"name": k.Name, "prefix": k.Prefix, "service_name": k.ServiceName})
	return k, nil
}

// PrefixBrowserKey is the visible prefix of a generated browser key ("olb_"), so a value found in a bundle or
// a log is immediately recognizable as a browser key rather than an ingest license key ("olk_") or an API key
// ("ola_"). It matches rum.PrefixBrowserKey; the constant is repeated rather than imported because
// internal/rum reaches this package transitively and the import would close a cycle.
const PrefixBrowserKey = "olb_"
