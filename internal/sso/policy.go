package sso

import (
	"context"
	"slices"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// Policy implements auth.SessionPolicy (D-077):
//
//   - An SSO session acts only in its organization, only while that organization's connection is the one that
//     created it and is enabled, and only until the connection's maximum session age (re-authentication).
//   - When a connection enforces SSO, password sessions of members whose e-mail domain is verified by the
//     organization cannot act in it, except break-glass owners.
type Policy struct{ s *Service }

// Policy returns the session policy to install with auth.Service.SetSessionPolicy.
func (s *Service) Policy() *Policy { return &Policy{s: s} }

var _ auth.SessionPolicy = (*Policy)(nil)

var (
	errSSOSessionOtherOrg = &auth.Error{Code: auth.CodePermissionDenied, Message: "this single sign-on session is limited to the organization it signed in to"}
	errSSOSessionEnded    = &auth.Error{Code: auth.CodeUnauthenticated, Message: "single sign-on session ended; sign in again"}
	errSSORequired        = &auth.Error{Code: auth.CodePermissionDenied, Message: "this organization requires single sign-on; sign in with SSO"}
)

// CheckSession implements auth.SessionPolicy.
func (p *Policy) CheckSession(ctx context.Context, sess auth.Session, u auth.User, m auth.Membership, now time.Time) error {
	if sess.OrgID != "" {
		if sess.OrgID != m.Org.ID {
			return errSSOSessionOtherOrg
		}
		pol, err := p.s.store.GetOrgPolicy(ctx, m.Org.ID, "")
		if err != nil {
			return err
		}
		if pol.ConnectionID == "" || pol.ConnectionID != sess.ConnectionID || !pol.Enabled {
			return errSSOSessionEnded
		}
		if pol.SessionMaxAge > 0 && now.Sub(sess.CreatedAt) >= pol.SessionMaxAge {
			return errSSOSessionEnded
		}
		return nil
	}
	pol, err := p.s.store.GetOrgPolicy(ctx, m.Org.ID, emailDomain(u.Email))
	if err != nil {
		return err
	}
	if requiresSSO(pol, u.ID, m.Role) {
		return errSSORequired
	}
	return nil
}

func requiresSSO(pol OrgPolicy, userID string, role auth.Role) bool {
	if pol.ConnectionID == "" || !pol.Enabled || !pol.Enforce || !pol.DomainVerified {
		return false
	}
	return role != auth.RoleOwner || !slices.Contains(pol.BreakGlassUserIDs, userID)
}

// CheckPasswordLogin implements auth.SessionPolicy: a password sign-in is refused when every organization of the
// user requires SSO for them (a user without memberships may still sign in).
func (p *Policy) CheckPasswordLogin(ctx context.Context, u auth.User, ms []auth.Membership, _ time.Time) error {
	if len(ms) == 0 {
		return nil
	}
	domain := emailDomain(u.Email)
	for _, m := range ms {
		pol, err := p.s.store.GetOrgPolicy(ctx, m.Org.ID, domain)
		if err != nil {
			return err
		}
		if !requiresSSO(pol, u.ID, m.Role) {
			return nil
		}
	}
	return errSSORequired
}
