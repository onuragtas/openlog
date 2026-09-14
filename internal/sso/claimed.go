package sso

import (
	"context"
	"errors"
	"fmt"

	"github.com/onuragtas/openlog/internal/auth"
)

// Claimed-domain redirection (D-089): an e-mail address in a domain verified by an organization whose routed
// connection is enabled signs in with that organization's SSO instead of creating a password account — no
// self-service sign-up, and no password acceptance of an invitation of that organization. Invitations of other
// organizations are accepted with a password only when the claiming connection allows external invitations.

// ClaimedRedirect is the single sign-on a sign-up or invitation acceptance must use instead of a password.
type ClaimedRedirect struct {
	Email            string
	OrganizationName string
	ConnectionName   string
	Protocol         Protocol
	// SameOrganization: the invitation is for the claiming organization (accepted by signing in with SSO).
	SameOrganization bool
}

func (s *Service) claimedRedirect(ctx context.Context, email string) (*ClaimedRedirect, Connection, error) {
	if !s.Available() {
		return nil, Connection{}, nil
	}
	c, ok, err := s.enabledConnectionFor(ctx, email)
	if err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) && ae.Code == auth.CodeInvalidArgument {
			return nil, Connection{}, nil // not an address SSO can route; the caller validates it
		}
		return nil, Connection{}, err
	}
	if !ok {
		return nil, Connection{}, nil
	}
	org, err := s.users.GetOrganization(ctx, c.OrgID)
	if err != nil {
		return nil, Connection{}, s.fail(err)
	}
	email, _ = normalizeEmail(email)
	return &ClaimedRedirect{Email: email, OrganizationName: org.Name, ConnectionName: c.Name, Protocol: c.Protocol}, c, nil
}

// SignupRedirect returns the SSO a self-service sign-up of email must use instead (nil: sign-up allowed).
func (s *Service) SignupRedirect(ctx context.Context, email string) (*ClaimedRedirect, error) {
	r, _, err := s.claimedRedirect(ctx, email)
	return r, err
}

// InvitationRedirect returns the SSO the acceptance of inv must use instead of a password (nil: allowed).
func (s *Service) InvitationRedirect(ctx context.Context, inv auth.Invitation) (*ClaimedRedirect, error) {
	r, c, err := s.claimedRedirect(ctx, inv.Email)
	if err != nil || r == nil {
		return nil, err
	}
	if c.OrgID == inv.OrgID {
		r.SameOrganization = true
		return r, nil
	}
	if c.AllowExternalInvitations {
		return nil, nil
	}
	return r, nil
}

func claimedError(r *ClaimedRedirect) error {
	return &auth.Error{Code: auth.CodeFailedPrecondition, Message: fmt.Sprintf(
		"the e-mail domain %s uses single sign-on of the organization %q; continue with single sign-on", emailDomain(r.Email), r.OrganizationName)}
}

var _ auth.ClaimedDomainPolicy = (*Policy)(nil)

// CheckSignup implements auth.ClaimedDomainPolicy.
func (p *Policy) CheckSignup(ctx context.Context, email string) error {
	r, err := p.s.SignupRedirect(ctx, email)
	if err != nil || r == nil {
		return err
	}
	return claimedError(r)
}

// CheckInvitation implements auth.ClaimedDomainPolicy.
func (p *Policy) CheckInvitation(ctx context.Context, inv auth.Invitation) error {
	r, err := p.s.InvitationRedirect(ctx, inv)
	if err != nil || r == nil {
		return err
	}
	return claimedError(r)
}
