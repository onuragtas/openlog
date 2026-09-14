package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ---- organization ----

// CurrentOrg returns the caller's organization.
func (s *Service) CurrentOrg(ctx context.Context, p *Principal) (Organization, error) {
	if err := requireOrg(p, ActReadOrg); err != nil {
		return Organization{}, err
	}
	org, err := s.store.GetOrganization(ctx, p.OrgID)
	if err != nil {
		return Organization{}, s.fail(err)
	}
	return org, nil
}

// RenameOrg changes the organization's display name (admin+).
func (s *Service) RenameOrg(ctx context.Context, p *Principal, name string, meta ClientMeta) (Organization, error) {
	if err := s.gate(p, ActUpdateOrg); err != nil {
		return Organization{}, err
	}
	name, err := cleanName(name, "name", true)
	if err != nil {
		return Organization{}, err
	}
	if err := s.store.UpdateOrganizationName(ctx, p.OrgID, name); err != nil {
		return Organization{}, s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "org.rename", "organization", p.OrgID, map[string]any{"name": name})
	return s.CurrentOrg(ctx, p)
}

// gate requires a session principal with an organization role that allows a.
func (s *Service) gate(p *Principal, a Action) error {
	if err := requireSession(p); err != nil {
		return err
	}
	return requireOrg(p, a)
}

// ---- members ----

// ListMembers lists the organization's members.
func (s *Service) ListMembers(ctx context.Context, p *Principal) ([]Member, error) {
	if err := s.gate(p, ActListMembers); err != nil {
		return nil, err
	}
	ms, err := s.store.ListMembers(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ms, nil
}

// UpdateMemberRole changes a member's role (admin+; owner role changes need an owner).
func (s *Service) UpdateMemberRole(ctx context.Context, p *Principal, userID string, role Role, meta ClientMeta) error {
	if err := s.gate(p, ActManageMembers); err != nil {
		return err
	}
	if !role.Valid() {
		return invalid("role must be one of owner, admin, member, viewer")
	}
	cur, err := s.store.GetMembership(ctx, p.OrgID, userID)
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: "member not found"}
	}
	if err != nil {
		return s.fail(err)
	}
	if !CanAssign(p.Role, cur.Role, role) {
		return denied("only owners can grant or remove the owner role")
	}
	if cur.Role == role {
		return nil
	}
	if err := s.store.UpdateMemberRole(ctx, p.OrgID, userID, role); err != nil {
		return s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "member.role_change", "user", userID, map[string]any{"from": cur.Role, "to": role})
	return nil
}

// RemoveMember removes a member (admin+), or the caller leaves the organization.
func (s *Service) RemoveMember(ctx context.Context, p *Principal, userID string, meta ClientMeta) error {
	if err := s.gate(p, ActListMembers); err != nil {
		return err
	}
	cur, err := s.store.GetMembership(ctx, p.OrgID, userID)
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: "member not found"}
	}
	if err != nil {
		return s.fail(err)
	}
	if userID != p.UserID {
		if !p.Role.Can(ActManageMembers) {
			return denied("your role (" + string(p.Role) + ") does not allow this operation")
		}
		if cur.Role == RoleOwner && p.Role != RoleOwner {
			return denied("only owners can remove an owner")
		}
	}
	if err := s.store.RemoveMember(ctx, p.OrgID, userID); err != nil {
		return s.fail(err)
	}
	// The removed user's sessions lose this organization on their next request (membership is not cached).
	// API keys they created would keep reading the organization's data, so they are revoked too (D-046).
	details := map[string]any{"role": cur.Role}
	if n, err := s.store.RevokeAPIKeysCreatedBy(ctx, p.OrgID, userID, p.UserID, s.now()); err != nil {
		s.log.Error("cannot revoke API keys of a removed member", "org_id", p.OrgID, "user_id", userID, "err", err)
	} else if n > 0 {
		details["revoked_api_keys"] = n
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "member.remove", "user", userID, details)
	return nil
}

// ---- invitations ----

// CreateInvitation invites email with role (admin+). The returned token is
// shown once; the invitee accepts it with AcceptInvitation. When e-mail is
// enabled the invitation is also e-mailed; the returned LastSentAt is set when
// that succeeded.
func (s *Service) CreateInvitation(ctx context.Context, p *Principal, email string, role Role, meta ClientMeta) (Invitation, string, error) {
	if err := s.gate(p, ActManageInvitations); err != nil {
		return Invitation{}, "", err
	}
	if err := s.requireVerified(p); err != nil {
		return Invitation{}, "", err
	}
	email = NormalizeEmail(email)
	if !validEmail(email) {
		return Invitation{}, "", invalid("a valid email address is required")
	}
	if !role.Valid() {
		return Invitation{}, "", invalid("role must be one of owner, admin, member, viewer")
	}
	if role == RoleOwner && p.Role != RoleOwner {
		return Invitation{}, "", denied("only owners can invite owners")
	}
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		if _, err := s.store.GetMembership(ctx, p.OrgID, u.ID); err == nil {
			return Invitation{}, "", &Error{Code: CodeAlreadyExists, Message: "this user is already a member"}
		} else if !errors.Is(err, ErrNotFound) {
			return Invitation{}, "", s.fail(err)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return Invitation{}, "", s.fail(err)
	}
	token, err := NewSecret(PrefixInvitation)
	if err != nil {
		return Invitation{}, "", err
	}
	now := s.now()
	inv := Invitation{OrgID: p.OrgID, Email: email, Role: role, TokenHash: HashSecret(token),
		InvitedBy: p.UserID, InvitedByEmail: p.Email, CreatedAt: now, ExpiresAt: now.Add(s.cfg.InvitationTTL), Locale: meta.Locale}
	if err := s.store.CreateInvitation(ctx, &inv); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return Invitation{}, "", &Error{Code: CodeAlreadyExists, Message: "a pending invitation for this email already exists"}
		}
		return Invitation{}, "", s.fail(err)
	}
	if sent := s.mailInvitation(ctx, p, inv, token, meta.Locale); sent != nil {
		inv.LastSentAt, inv.SendCount = sent, inv.SendCount+1
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "invitation.create", "invitation", inv.ID,
		map[string]any{"email": email, "role": role, "email_sent": inv.LastSentAt != nil})
	return inv, token, nil
}

// ListInvitations lists pending invitations (admin+); with includeExpired also
// expired ones that were neither accepted nor revoked (they can be resent).
func (s *Service) ListInvitations(ctx context.Context, p *Principal, includeExpired bool) ([]Invitation, error) {
	if err := s.gate(p, ActManageInvitations); err != nil {
		return nil, err
	}
	invs, err := s.store.ListInvitations(ctx, p.OrgID, s.now(), includeExpired)
	if err != nil {
		return nil, s.fail(err)
	}
	return invs, nil
}

// ResendInvitation issues a new token for an invitation that is neither
// accepted nor revoked (also after it expired), restarts its validity
// (OPENLOG_INVITATION_TTL) and e-mails it when e-mail is enabled (admin+). The
// previous link stops working. Returns the new token (shown once) and whether
// the e-mail was sent by this call.
func (s *Service) ResendInvitation(ctx context.Context, p *Principal, id string, meta ClientMeta) (Invitation, string, bool, error) {
	if err := s.gate(p, ActManageInvitations); err != nil {
		return Invitation{}, "", false, err
	}
	if err := s.requireVerified(p); err != nil {
		return Invitation{}, "", false, err
	}
	now := s.now()
	open, err := s.store.ListInvitations(ctx, p.OrgID, now, true)
	if err != nil {
		return Invitation{}, "", false, s.fail(err)
	}
	var cur *Invitation
	for i := range open {
		if open[i].ID == id {
			cur = &open[i]
		}
	}
	errGone := &Error{Code: CodeNotFound, Message: "invitation not found, already accepted or revoked"}
	if cur == nil {
		return Invitation{}, "", false, errGone
	}
	if cur.Role == RoleOwner && p.Role != RoleOwner {
		return Invitation{}, "", false, denied("only owners can invite owners")
	}
	token, err := NewSecret(PrefixInvitation)
	if err != nil {
		return Invitation{}, "", false, err
	}
	inv, err := s.store.RenewInvitation(ctx, p.OrgID, id, HashSecret(token), now.Add(s.cfg.InvitationTTL))
	if errors.Is(err, ErrNotFound) {
		return Invitation{}, "", false, errGone
	}
	if err != nil {
		return Invitation{}, "", false, s.fail(err)
	}
	inv.InvitedByEmail = cur.InvitedByEmail
	sent := s.mailInvitation(ctx, p, inv, token, meta.Locale)
	if sent != nil {
		inv.LastSentAt, inv.SendCount = sent, inv.SendCount+1
	}
	inv.TokenHash = nil
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "invitation.resend", "invitation", inv.ID,
		map[string]any{"email": inv.Email, "role": inv.Role, "email_sent": sent != nil})
	return inv, token, sent != nil, nil
}

// RevokeInvitation revokes a pending invitation (admin+).
func (s *Service) RevokeInvitation(ctx context.Context, p *Principal, id string, meta ClientMeta) error {
	if err := s.gate(p, ActManageInvitations); err != nil {
		return err
	}
	inv, err := s.store.RevokeInvitation(ctx, p.OrgID, id, s.now())
	if err != nil {
		return s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "invitation.revoke", "invitation", id, map[string]any{"email": inv.Email})
	return nil
}

// InvitationInfo is what an invitee sees before accepting.
type InvitationInfo struct {
	Invitation Invitation
	Org        Organization
	UserExists bool // sign in with the existing password instead of choosing one
}

// LookupInvitation resolves an invitation token (unauthenticated).
func (s *Service) LookupInvitation(ctx context.Context, token string) (InvitationInfo, error) {
	inv, org, err := s.store.GetInvitationByTokenHash(ctx, HashSecret(token))
	if errors.Is(err, ErrNotFound) || (err == nil && !inv.Pending(s.now())) {
		return InvitationInfo{}, &Error{Code: CodeNotFound, Message: "invitation is invalid or has expired"}
	}
	if err != nil {
		return InvitationInfo{}, s.fail(err)
	}
	_, uerr := s.store.GetUserByEmail(ctx, inv.Email)
	if uerr != nil && !errors.Is(uerr, ErrNotFound) {
		return InvitationInfo{}, s.fail(uerr)
	}
	inv.TokenHash = nil
	return InvitationInfo{Invitation: inv, Org: org, UserExists: uerr == nil}, nil
}

// AcceptInvitation joins the invited organization and signs the invitee in.
// A new user chooses a password (and name); an existing user proves their
// password.
func (s *Service) AcceptInvitation(ctx context.Context, token, password, name string, meta ClientMeta) (LoginResult, Organization, error) {
	info, err := s.LookupInvitation(ctx, token)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	inv := info.Invitation
	now := s.now()
	key := loginKey(inv.Email, meta.IP)
	if err := s.checkRate(ctx, key, now); err != nil {
		return LoginResult{}, Organization{}, err
	}
	if err := s.checkClaimedInvitation(ctx, inv); err != nil { // claimed domain with SSO (external.go, D-089)
		return LoginResult{}, Organization{}, err
	}
	var u User
	if info.UserExists {
		u, err = s.store.GetUserByEmail(ctx, inv.Email)
		if err != nil {
			return LoginResult{}, Organization{}, s.fail(err)
		}
		ok := false
		if u.PasswordHash != "" {
			ok, _ = VerifyPassword(u.PasswordHash, password)
		}
		if !ok || u.DisabledAt != nil {
			s.recordFailure(ctx, key, now)
			return LoginResult{}, Organization{}, errBadCredentials
		}
	} else {
		if err := ValidatePassword(password); err != nil {
			return LoginResult{}, Organization{}, err
		}
		clean, err := cleanName(name, "name", false)
		if err != nil {
			return LoginResult{}, Organization{}, err
		}
		hash, err := HashPassword(password)
		if err != nil {
			return LoginResult{}, Organization{}, err
		}
		// The inviting admin vouches for the address (or it received the invitation e-mail).
		u = User{Email: inv.Email, Name: clean, PasswordHash: hash, CreatedAt: now, EmailVerifiedAt: &now, Locale: meta.Locale}
	}
	if err := s.store.AcceptInvitation(ctx, inv.ID, &u, now); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return LoginResult{}, Organization{}, &Error{Code: CodeAlreadyExists, Message: "you are already a member of this organization"}
		}
		if errors.Is(err, ErrNotFound) {
			return LoginResult{}, Organization{}, &Error{Code: CodeNotFound, Message: "invitation is invalid or has expired"}
		}
		return LoginResult{}, Organization{}, s.fail(err)
	}
	res, err := s.startSession(ctx, u, meta, now)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	s.audit(ctx, inv.OrgID, u.ID, u.Email, meta, "invitation.accept", "invitation", inv.ID, map[string]any{"role": inv.Role})
	return res, info.Org, nil
}

// ---- ingest license keys ----

// ListLicenseKeys lists the organization's ingest keys (member+).
func (s *Service) ListLicenseKeys(ctx context.Context, p *Principal) ([]LicenseKey, error) {
	if err := s.gate(p, ActListLicenseKeys); err != nil {
		return nil, err
	}
	ks, err := s.store.ListLicenseKeys(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ks, nil
}

// CreateLicenseKey creates an ingest key (admin+). With customKey == "" a key is
// generated and its plaintext returned once. Otherwise customKey (trimmed,
// validated with ValidateCustomKey) is imported: only its hash is stored, the
// key is marked Custom and the returned plaintext is "". A value that already
// exists in any organization, active or revoked, fails with already_exists.
func (s *Service) CreateLicenseKey(ctx context.Context, p *Principal, name, customKey string, meta ClientMeta) (LicenseKey, string, error) {
	if err := s.gate(p, ActManageLicenseKeys); err != nil {
		return LicenseKey{}, "", err
	}
	if err := s.requireVerified(p); err != nil {
		return LicenseKey{}, "", err
	}
	name, err := cleanName(name, "name", true)
	if err != nil {
		return LicenseKey{}, "", err
	}
	secret := strings.TrimSpace(customKey)
	custom := secret != ""
	if custom {
		if err := ValidateCustomKey(secret, MinCustomKeyLen); err != nil {
			return LicenseKey{}, "", err
		}
	} else if secret, err = NewSecret(PrefixLicenseKey); err != nil {
		return LicenseKey{}, "", err
	}
	hash, legacy := s.keyHashes(secret)
	if !custom {
		legacy = nil // 192 random bits: no earlier row can have this value
	}
	k := LicenseKey{OrgID: p.OrgID, Name: name, Prefix: DisplayPrefix(secret), Hash: hash, LegacyHashes: legacy, Custom: custom,
		CreatedBy: p.UserID, CreatedByEmail: p.Email, CreatedAt: s.now()}
	if err := s.store.CreateLicenseKey(ctx, &k); err != nil {
		if custom && errors.Is(err, ErrAlreadyExists) {
			// Never reveal which organization uses the value (or that it was revoked).
			return LicenseKey{}, "", &Error{Code: CodeAlreadyExists, Message: "this key value is already in use; choose another value"}
		}
		return LicenseKey{}, "", s.fail(err)
	}
	details := map[string]any{"name": name, "prefix": k.Prefix}
	if custom {
		details["custom"] = true
		secret = "" // the caller already knows it
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "license_key.create", "license_key", k.ID, details)
	return k, secret, nil
}

// RevokeLicenseKey revokes an ingest key (admin+). Ingest pods stop accepting
// it within OPENLOG_AUTH_CACHE_TTL. Revoking twice is a no-op.
func (s *Service) RevokeLicenseKey(ctx context.Context, p *Principal, id string, meta ClientMeta) (LicenseKey, error) {
	if err := s.gate(p, ActManageLicenseKeys); err != nil {
		return LicenseKey{}, err
	}
	k, err := s.store.RevokeLicenseKey(ctx, p.OrgID, id, p.UserID, s.now())
	if err != nil {
		return LicenseKey{}, s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "license_key.revoke", "license_key", id, map[string]any{"name": k.Name, "prefix": k.Prefix})
	return k, nil
}

// ---- API keys ----

// ListAPIKeys lists the organization's API keys (member+).
func (s *Service) ListAPIKeys(ctx context.Context, p *Principal) ([]APIKey, error) {
	if err := s.gate(p, ActListAPIKeys); err != nil {
		return nil, err
	}
	ks, err := s.store.ListAPIKeys(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ks, nil
}

// CreateAPIKey creates a read-only API key (member+). The plaintext is returned once.
func (s *Service) CreateAPIKey(ctx context.Context, p *Principal, name string, expiresAt *time.Time, meta ClientMeta) (APIKey, string, error) {
	if err := s.gate(p, ActCreateAPIKey); err != nil {
		return APIKey{}, "", err
	}
	if err := s.requireVerified(p); err != nil {
		return APIKey{}, "", err
	}
	name, err := cleanName(name, "name", true)
	if err != nil {
		return APIKey{}, "", err
	}
	now := s.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return APIKey{}, "", invalid("expires_at must be in the future")
	}
	secret, err := NewSecret(PrefixAPIKey)
	if err != nil {
		return APIKey{}, "", err
	}
	k := APIKey{OrgID: p.OrgID, Name: name, Prefix: DisplayPrefix(secret), Hash: s.cfg.KeyHasher.Hash(secret), Scope: "read",
		CreatedBy: p.UserID, CreatedByEmail: p.Email, CreatedAt: now, ExpiresAt: expiresAt}
	if err := s.store.CreateAPIKey(ctx, &k); err != nil {
		return APIKey{}, "", s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "api_key.create", "api_key", k.ID, map[string]any{"name": name, "prefix": k.Prefix})
	return k, secret, nil
}

// RevokeAPIKey revokes an API key: its creator or an admin+. Takes effect immediately.
func (s *Service) RevokeAPIKey(ctx context.Context, p *Principal, id string, meta ClientMeta) (APIKey, error) {
	if err := s.gate(p, ActListAPIKeys); err != nil {
		return APIKey{}, err
	}
	k, err := s.store.GetAPIKey(ctx, p.OrgID, id)
	if err != nil {
		return APIKey{}, s.fail(err)
	}
	if k.CreatedBy != p.UserID && !p.Role.Can(ActRevokeAnyAPIKey) {
		return APIKey{}, denied("only the key's creator or an admin can revoke it")
	}
	k, err = s.store.RevokeAPIKey(ctx, p.OrgID, id, p.UserID, s.now())
	if err != nil {
		return APIKey{}, s.fail(err)
	}
	s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "api_key.revoke", "api_key", id, map[string]any{"name": k.Name, "prefix": k.Prefix})
	return k, nil
}

// ---- sessions ----

// ListSessions lists the caller's active sessions.
func (s *Service) ListSessions(ctx context.Context, p *Principal) ([]Session, error) {
	if err := requireSession(p); err != nil {
		return nil, err
	}
	now := s.now()
	all, err := s.store.ListSessions(ctx, p.UserID, now)
	if err != nil {
		return nil, s.fail(err)
	}
	out := all[:0]
	for _, sess := range all {
		if sess.Active(now, s.cfg.SessionIdleTimeout) {
			out = append(out, sess)
		}
	}
	return out, nil
}

// RevokeSession revokes one of the caller's sessions.
func (s *Service) RevokeSession(ctx context.Context, p *Principal, id string, meta ClientMeta) error {
	if err := requireSession(p); err != nil {
		return err
	}
	if err := s.store.RevokeSession(ctx, p.UserID, id, s.now()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &Error{Code: CodeNotFound, Message: "session not found"}
		}
		return s.fail(err)
	}
	s.audit(ctx, "", p.UserID, p.Email, meta, "session.revoke", "session", id, nil)
	return nil
}

// ---- audit ----

// ListAuditEvents returns the organization's audit events matching f, newest first (admin+).
func (s *Service) ListAuditEvents(ctx context.Context, p *Principal, f AuditFilter) ([]AuditEvent, error) {
	if err := s.gate(p, ActReadAudit); err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	f.Actor = strings.TrimSpace(f.Actor)
	f.Action = strings.TrimSpace(f.Action)
	if len(f.Actor) > 320 || len(f.Action) > 100 {
		return nil, invalid("actor and action filters are too long")
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		return nil, invalid("from must be before to")
	}
	evs, err := s.store.ListAuditEvents(ctx, p.OrgID, f)
	if err != nil {
		return nil, s.fail(err)
	}
	return evs, nil
}
