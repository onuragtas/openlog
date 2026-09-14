package auth

import (
	"context"
	"errors"
)

// MemberLimitFunc returns a *Error with CodeQuotaExceeded when one more member of orgID would exceed the plan's users
// limit (SaaS mode, internal/operator.MemberLimiter). includePending also counts pending invitations.
type MemberLimitFunc func(ctx context.Context, orgID string, includePending bool) error

// SetMemberLimit enables the users limit for invitations, invitation acceptance, SSO just-in-time provisioning and
// SCIM. Must be called before serving requests.
func (s *Service) SetMemberLimit(fn MemberLimitFunc) { s.memberLimit = fn }

// CheckMemberLimit returns nil when orgID may get one more member. Errors other than *Error are reported as unavailable.
func (s *Service) CheckMemberLimit(ctx context.Context, orgID string, includePending bool) error {
	if s == nil || s.memberLimit == nil {
		return nil
	}
	err := s.memberLimit(ctx, orgID, includePending)
	var e *Error
	if err == nil || errors.As(err, &e) {
		return err
	}
	return s.fail(err)
}

// SendVerificationTo e-mails a new address verification link to an unverified user (operator action "resend
// verification to owner"); it bypasses the per-user resend rate limit, which the operator API replaces with its own.
func (s *Service) SendVerificationTo(ctx context.Context, userID string) error {
	if !s.EmailEnabled() {
		return &Error{Code: CodeFailedPrecondition, Message: "e-mail is not configured on this server"}
	}
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return s.fail(err)
	}
	if u.EmailVerifiedAt != nil {
		return &Error{Code: CodeFailedPrecondition, Message: "the e-mail address is already confirmed"}
	}
	return s.startVerification(ctx, u, "")
}
