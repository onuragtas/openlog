package auth

import (
	"context"
	"net/http"
)

// Kind is how a request was authenticated.
type Kind string

// Authentication kinds.
const (
	KindSession    Kind = "session"
	KindAPIKey     Kind = "api_key"
	KindLicenseKey Kind = "license_key" // OPENLOG_AUTH_MODE=static only
)

// HTTP headers used by the API.
const (
	// HeaderCSRF carries the session's CSRF token on mutating cookie-authenticated requests.
	HeaderCSRF = "X-CSRF-Token"
	// HeaderOrg selects the organization for users that belong to several.
	HeaderOrg = "X-Openlog-Org-Id"
)

// Principal is the authenticated caller of one request. The API takes the
// tenant for ClickHouse queries only from TenantID.
type Principal struct {
	Kind Kind

	UserID    string
	Email     string
	Name      string
	SessionID string
	CSRFToken string // session principals only
	APIKeyID  string

	// Organization context. Empty for a user without memberships.
	OrgID    string
	OrgName  string
	TenantID string
	Role     Role
}

// HasOrg reports whether the principal acts inside an organization.
func (p *Principal) HasOrg() bool { return p != nil && p.OrgID != "" && p.TenantID != "" }

// Authenticator authenticates an HTTP request.
//
// Errors: ErrUnauthenticated (401), ErrPermissionDenied (403, e.g. CSRF or an
// organization the user does not belong to), ErrUnavailable (503).
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by WithPrincipal.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok && p != nil
}

// IdentityProvider is the seam for external sign-in (OIDC/SAML, M4). An
// implementation verifies the provider's callback and returns a verified
// email; the service then maps it to a user and creates a normal session.
// Not implemented in M1.
type IdentityProvider interface {
	Name() string
	AuthCodeURL(state string) string
	Exchange(ctx context.Context, r *http.Request) (email string, err error)
}
