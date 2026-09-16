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
	// APIKeyName is the name of the authenticating API key (key principals only); it names the
	// actor of the audit events the key writes.
	APIKeyName string
	// EmailVerified is false only for session users who signed up and have not confirmed their address.
	EmailVerified bool
	// Language is the session user's chosen language ("" = automatic: the browser's; D-095).
	Language string

	// Organization context. Empty for a user without memberships.
	OrgID    string
	OrgName  string
	TenantID string
	Role     Role
}

// HasOrg reports whether the principal acts inside an organization.
func (p *Principal) HasOrg() bool { return p != nil && p.OrgID != "" && p.TenantID != "" }

// Actor is who performed a change, for the audit log. A change made with an API
// key has no user: UserID and Email are empty and the key is named instead, so
// the audit log never claims a person did it (D-133). Packages that write audit
// events carry the same five fields.
type Actor struct {
	UserID     string // signed-in user; "" for API keys
	Email      string // "" for API keys
	IP         string
	APIKeyID   string // authenticating API key; "" for users
	APIKeyName string // the key's name when the change was made
}

// ActorOf describes p as the actor of an audit event.
func ActorOf(p *Principal, ip string) Actor {
	if p == nil {
		return Actor{IP: ip}
	}
	return Actor{UserID: p.UserID, Email: p.Email, IP: ip, APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
}

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
