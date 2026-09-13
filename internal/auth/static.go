package auth

import (
	"errors"
	"net/http"

	"github.com/onuragtas/openlog/internal/tenant"
)

// StaticAuthenticator authenticates Query API requests with license keys
// (OPENLOG_AUTH_MODE=static, for development and tests). Every key acts as a
// viewer of the tenant it maps to; there are no users or management endpoints.
type StaticAuthenticator struct {
	Resolver tenant.Resolver
}

// Authenticate implements Authenticator.
func (a StaticAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	tenantID, err := a.Resolver.Resolve(r.Context(), tenant.KeyFromHTTP(r.Header))
	switch {
	case errors.Is(err, tenant.ErrUnavailable):
		return nil, ErrUnavailable
	case err != nil:
		return nil, &Error{Code: CodeUnauthenticated, Message: "invalid or missing credentials"}
	}
	return &Principal{Kind: KindLicenseKey, OrgID: tenantID, OrgName: tenantID, TenantID: tenantID, Role: RoleViewer}, nil
}
