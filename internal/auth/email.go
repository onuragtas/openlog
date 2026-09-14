package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

// Mail is one transactional e-mail (invitation, address verification).
type Mail struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer delivers transactional e-mail (internal/mail over OPENLOG_SMTP_*). Nil in Config disables e-mail:
// invitations are shared as links and sign-ups are not verified.
type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

// CaptchaVerifier checks a CAPTCHA response token at sign-up (internal/captcha). ok=false means the token was
// rejected; err means the provider could not be asked.
type CaptchaVerifier interface {
	Provider() string
	SiteKey() string
	Verify(ctx context.Context, token, remoteIP string) (ok bool, err error)
}

// PrefixVerification is the visible prefix of e-mail verification tokens.
const PrefixVerification = "olv_"

// mailTimeout bounds one synchronous SMTP delivery inside an API request.
const mailTimeout = 15 * time.Second

// Fixed limits of e-mail sending (docs/contracts/api.md "Invitations", "Sign-up").
const (
	inviteMailsPerAddress = 5   // per (organization, e-mail) per hour
	inviteMailsPerOrg     = 100 // per organization per hour
	verifyMailsPerUser    = 5   // per user per hour
	mailRateWindow        = time.Hour
)

// EmailEnabled reports whether invitation and verification e-mails are sent (a Mailer and OPENLOG_PUBLIC_URL).
func (s *Service) EmailEnabled() bool { return s.cfg.Mailer != nil && s.cfg.PublicURL != "" }

func (s *Service) link(path, token string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + path + "#token=" + token
}

// rateKey derives a login_failures key for a purpose and subject.
func rateKey(purpose string, parts ...string) []byte {
	h := sha256.Sum256([]byte(purpose + "\x00" + strings.Join(parts, "\x00")))
	return h[:]
}

// allow counts one event under key and reports whether fewer than max events happened within window before it.
func (s *Service) allow(ctx context.Context, key []byte, max int, window time.Duration, now time.Time) (bool, error) {
	n, err := s.store.CountLoginFailures(ctx, key, now.Add(-window))
	if err != nil {
		return false, s.fail(err)
	}
	if n >= max {
		return false, nil
	}
	if err := s.store.AddLoginFailure(ctx, key, now); err != nil {
		return false, s.fail(err)
	}
	return true, nil
}

func (s *Service) send(ctx context.Context, m Mail) error {
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mailTimeout)
	defer cancel()
	return s.cfg.Mailer.Send(mctx, m)
}

// invitationMail renders the invitation e-mail in locale (internal/mail/templates).
func (s *Service) invitationMail(inv Invitation, orgName, inviter, token, locale string) Mail {
	m := mailtemplates.Invitation(locale, mailtemplates.InvitationData{Inviter: inviter, OrgName: orgName, Role: string(inv.Role),
		ExpiresAt: inv.ExpiresAt, Link: s.link("/invite", token)})
	return Mail{To: inv.Email, Subject: m.Subject, Text: m.Text, HTML: m.HTML}
}

func (s *Service) verificationMail(email, token string, expires time.Time, locale string) Mail {
	m := mailtemplates.Verification(locale, mailtemplates.VerificationData{ExpiresAt: expires, Link: s.link("/verify-email", token)})
	return Mail{To: email, Subject: m.Subject, Text: m.Text, HTML: m.HTML}
}

// mailInvitation e-mails an invitation when e-mail is enabled and the send limits allow it, in the invitation's
// language (fallback: the current request's). It returns the send time (nil when not sent); failures are logged, never
// returned: the inviter still gets the link.
func (s *Service) mailInvitation(ctx context.Context, p *Principal, inv Invitation, token, fallbackLocale string) *time.Time {
	if !s.EmailEnabled() {
		return nil
	}
	now := s.now()
	for _, lim := range []struct {
		key []byte
		max int
	}{{rateKey("invite-mail-org", inv.OrgID), inviteMailsPerOrg}, {rateKey("invite-mail", inv.OrgID, inv.Email), inviteMailsPerAddress}} {
		ok, err := s.allow(ctx, lim.key, lim.max, mailRateWindow, now)
		if err != nil || !ok {
			s.log.Warn("invitation e-mail not sent: rate limited", "org_id", inv.OrgID, "err", err)
			return nil
		}
	}
	locale := inv.Locale
	if locale == "" {
		locale = fallbackLocale
	}
	if err := s.send(ctx, s.invitationMail(inv, p.OrgName, p.Email, token, locale)); err != nil {
		s.log.Warn("cannot send invitation e-mail", "org_id", inv.OrgID, "invitation_id", inv.ID, "err", err)
		return nil
	}
	if err := s.store.MarkInvitationSent(ctx, inv.ID, now); err != nil {
		s.log.Warn("cannot record invitation e-mail", "invitation_id", inv.ID, "err", err)
	}
	return &now
}

// startVerification creates a verification token for u and e-mails it in locale (fallback: the user's) (best effort;
// logged).
func (s *Service) startVerification(ctx context.Context, u User, locale string) error {
	token, err := NewSecret(PrefixVerification)
	if err != nil {
		return err
	}
	if locale == "" {
		locale = u.Locale
	}
	now := s.now()
	v := EmailVerification{UserID: u.ID, Email: u.Email, TokenHash: HashSecret(token), CreatedAt: now, ExpiresAt: now.Add(s.cfg.VerificationTTL),
		Locale: locale}
	if err := s.store.CreateEmailVerification(ctx, &v); err != nil {
		return s.fail(err)
	}
	if err := s.send(ctx, s.verificationMail(u.Email, token, v.ExpiresAt, locale)); err != nil {
		s.log.Warn("cannot send verification e-mail", "user_id", u.ID, "err", err)
		return &Error{Code: CodeUnavailable, Message: "the verification e-mail could not be sent; try again later"}
	}
	return nil
}

// errNotVerified is returned for gated operations of unverified sign-ups.
var errNotVerified = &Error{Code: CodePermissionDenied, Message: "confirm your e-mail address first (see the verification e-mail or resend it)"}

// requireVerified blocks unverified sign-ups from creating credentials and sending invitations.
func (s *Service) requireVerified(p *Principal) error {
	if s.cfg.RequireEmailVerification && p != nil && p.Kind == KindSession && !p.EmailVerified {
		return errNotVerified
	}
	return nil
}

// ResendVerification e-mails a new verification link to the signed-in, unverified caller.
func (s *Service) ResendVerification(ctx context.Context, p *Principal, meta ClientMeta) error {
	if err := requireSession(p); err != nil {
		return err
	}
	if p.EmailVerified {
		return &Error{Code: CodeFailedPrecondition, Message: "your e-mail address is already confirmed"}
	}
	if !s.EmailEnabled() {
		return &Error{Code: CodeFailedPrecondition, Message: "e-mail is not configured on this server"}
	}
	ok, err := s.allow(ctx, rateKey("verify-mail", p.UserID), verifyMailsPerUser, mailRateWindow, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Code: CodeResourceExhausted, Message: "too many verification e-mails; try again later"}
	}
	u, err := s.store.GetUser(ctx, p.UserID)
	if err != nil {
		return s.fail(err)
	}
	if err := s.startVerification(ctx, u, meta.Locale); err != nil {
		return err
	}
	s.audit(ctx, "", p.UserID, p.Email, meta, "user.verification_resend", "user", p.UserID, nil)
	return nil
}

// VerifyEmail confirms an e-mail address with a token from the verification e-mail (unauthenticated).
func (s *Service) VerifyEmail(ctx context.Context, token string, meta ClientMeta) error {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, PrefixVerification) {
		return &Error{Code: CodeNotFound, Message: "the verification link is invalid, already used or expired"}
	}
	u, err := s.store.ConsumeEmailVerification(ctx, HashSecret(token), s.now())
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: "the verification link is invalid, already used or expired"}
	}
	if err != nil {
		return s.fail(err)
	}
	s.audit(ctx, "", u.ID, u.Email, meta, "user.email_verified", "user", u.ID, nil)
	return nil
}
