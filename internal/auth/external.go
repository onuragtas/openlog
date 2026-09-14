package auth

import (
	"context"
	"errors"
	"time"
)

// Session authentication methods (Session.AuthMethod).
const (
	MethodPassword = "password"
	MethodOIDC     = "oidc"
	MethodSAML     = "saml"
)

// SessionBinding describes a session created by single sign-on (internal/sso, D-077).
type SessionBinding struct {
	Method       string // MethodOIDC or MethodSAML
	OrgID        string // the session may act only in this organization
	ConnectionID string
	// MaxAge shortens the session lifetime below OPENLOG_SESSION_TTL (re-authentication); 0 = TTL only.
	MaxAge time.Duration
}

// SessionPolicy restricts the organizations a session may act in (implemented by internal/sso). It is consulted
// on every session-authenticated request, so changes (enforcement, a deleted connection) apply immediately on
// every API pod.
type SessionPolicy interface {
	// CheckSession is called before sess acts in organization m. nil allows it; an *Error with
	// CodePermissionDenied refuses this organization; CodeUnauthenticated ends the request as signed out; any
	// other error is reported as unavailable.
	CheckSession(ctx context.Context, sess Session, u User, m Membership, now time.Time) error
	// CheckPasswordLogin is called after a correct password with the user's memberships; an error refuses the
	// sign-in (e.g. every organization of the user enforces single sign-on).
	CheckPasswordLogin(ctx context.Context, u User, ms []Membership, now time.Time) error
}

// SetSessionPolicy installs p (nil removes it). Must be called before serving requests.
func (s *Service) SetSessionPolicy(p SessionPolicy) { s.policy = p }

// Store returns the service's store (used by internal/sso for users, memberships, sessions and the audit log).
func (s *Service) Store() Store { return s.store }

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// StartExternalSession creates a session for a user authenticated by single sign-on. The caller has verified the
// identity and the membership; the session is bound to b.OrgID.
func (s *Service) StartExternalSession(ctx context.Context, u User, b SessionBinding, meta ClientMeta) (LoginResult, error) {
	if u.DisabledAt != nil {
		return LoginResult{}, unauthenticated("this account is disabled")
	}
	now := s.now()
	token, err := newOpaqueToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrf, err := newOpaqueToken()
	if err != nil {
		return LoginResult{}, err
	}
	expires := now.Add(s.cfg.SessionTTL)
	if b.MaxAge > 0 && now.Add(b.MaxAge).Before(expires) {
		expires = now.Add(b.MaxAge)
	}
	sess := Session{
		UserID: u.ID, TokenHash: HashSecret(token), CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: expires,
		IP: meta.IP, UserAgent: truncate(meta.UserAgent, 256),
		AuthMethod: b.Method, OrgID: b.OrgID, ConnectionID: b.ConnectionID,
	}
	if err := s.store.CreateSession(ctx, &sess); err != nil {
		return LoginResult{}, s.fail(err)
	}
	if err := s.store.SetUserLastLogin(ctx, u.ID, now); err != nil {
		s.log.Warn("cannot update last_login_at", "err", err)
	}
	u.PasswordHash = ""
	return LoginResult{Token: token, Session: sess, User: u}, nil
}

// NewOpaqueToken returns a random URL-safe token (32 bytes).
func NewOpaqueToken() (string, error) { return newOpaqueToken() }

// policyError passes *Error values through and reports anything else as unavailable.
func (s *Service) policyError(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return s.fail(err)
}

// selectSessionOrg is selectOrg for session principals with a SessionPolicy: an SSO session defaults to its
// organization, and a session without an explicit organization gets its oldest membership the policy allows
// (none: the principal acts without an organization).
func (s *Service) selectSessionOrg(ctx context.Context, p *Principal, sess Session, u User, orgID string, now time.Time) error {
	if s.policy == nil {
		return s.selectOrg(ctx, p, orgID)
	}
	if orgID == "" && sess.OrgID != "" {
		orgID = sess.OrgID
	}
	if orgID != "" {
		m, err := s.store.GetMembership(ctx, orgID, p.UserID)
		if errors.Is(err, ErrNotFound) {
			return denied("you are not a member of this organization")
		}
		if err != nil {
			return s.fail(err)
		}
		if err := s.policy.CheckSession(ctx, sess, u, m, now); err != nil {
			return s.policyError(err)
		}
		p.OrgID, p.OrgName, p.TenantID, p.Role = m.Org.ID, m.Org.Name, m.Org.TenantID, m.Role
		return nil
	}
	ms, err := s.store.ListMemberships(ctx, p.UserID)
	if err != nil {
		return s.fail(err)
	}
	for _, m := range ms {
		err := s.policy.CheckSession(ctx, sess, u, m, now)
		if err == nil {
			p.OrgID, p.OrgName, p.TenantID, p.Role = m.Org.ID, m.Org.Name, m.Org.TenantID, m.Role
			return nil
		}
		if !errors.Is(err, ErrPermissionDenied) {
			return s.policyError(err)
		}
	}
	return nil
}

// checkPasswordLogin applies the SessionPolicy to a password sign-in.
func (s *Service) checkPasswordLogin(ctx context.Context, u User, now time.Time) error {
	if s.policy == nil {
		return nil
	}
	ms, err := s.store.ListMemberships(ctx, u.ID)
	if err != nil {
		return s.fail(err)
	}
	if err := s.policy.CheckPasswordLogin(ctx, u, ms, now); err != nil {
		return s.policyError(err)
	}
	return nil
}
