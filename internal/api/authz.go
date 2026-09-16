package api

import (
	"errors"
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
)

// Authorization of every API request is one decision, auth.Allow (and its
// table-driven form auth.Authorize), in internal/auth/roles.go: it is the only
// code that looks at a principal's kind and role. Handlers in this package ask
// it for an action and render the answer; none of them compares auth.KindSession
// or a role itself, which is what lets an API key's role (D-133) be enforced in
// exactly one place. TestAuthorizationGoesThroughOneGate (authz_test.go) keeps
// it that way.

// denyError renders an authorization decision as an API error.
func denyError(err error) *apiError {
	var ae *auth.Error
	if errors.As(err, &ae) {
		if status, ok := authStatus[ae.Code]; ok {
			return &apiError{status, string(ae.Code), ae.Message}
		}
	}
	return &apiError{http.StatusForbidden, "permission_denied", "this operation is not allowed"}
}

// allowed reports whether p may perform a. Use it where a permission selects
// behaviour (what a viewer may edit) rather than rejecting the request.
func allowed(p *auth.Principal, a auth.Action) bool { return auth.Authorize(p, a) == nil }

// isUser reports whether the request was made by a signed-in person rather than
// an API key, for the operator surfaces that exist only for people.
func isUser(p *auth.Principal) bool { return auth.Allow(p, auth.Permission{UserOnly: true}) == nil }

// authorize refuses the request unless p may perform a.
func authorize(p *auth.Principal, a auth.Action) *apiError {
	if err := auth.Authorize(p, a); err != nil {
		return denyError(err)
	}
	return nil
}
