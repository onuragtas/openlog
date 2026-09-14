package sso

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// PrefixSCIMToken is the visible prefix of SCIM bearer tokens.
const PrefixSCIMToken = "ols_"

const maxSCIMTokens = 20

var errSCIMDisabled = &auth.Error{Code: auth.CodeFailedPrecondition, Message: "SCIM provisioning is disabled on this server (OPENLOG_SCIM_ENABLED)"}

// CreateSCIMToken creates a SCIM token (admin+). The plaintext is returned once.
func (s *Service) CreateSCIMToken(ctx context.Context, p *auth.Principal, name string, expiresAt *time.Time, meta auth.ClientMeta) (SCIMToken, string, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return SCIMToken{}, "", err
	}
	if err := requireVerified(p); err != nil {
		return SCIMToken{}, "", err
	}
	if !s.cfg.SCIMEnabled {
		return SCIMToken{}, "", errSCIMDisabled
	}
	name, err := cleanText(name, "name", 200, true)
	if err != nil {
		return SCIMToken{}, "", err
	}
	now := s.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return SCIMToken{}, "", invalid("expires_at must be in the future")
	}
	ts, err := s.store.ListSCIMTokens(ctx, p.OrgID)
	if err != nil {
		return SCIMToken{}, "", s.fail(err)
	}
	active := 0
	for _, t := range ts {
		if t.Usable(now) {
			active++
		}
	}
	if active >= maxSCIMTokens {
		return SCIMToken{}, "", precondition("an organization can have at most %d active SCIM tokens", maxSCIMTokens)
	}
	secret, err := auth.NewSecret(PrefixSCIMToken)
	if err != nil {
		return SCIMToken{}, "", err
	}
	t := SCIMToken{OrgID: p.OrgID, Name: name, Prefix: secret[:len(PrefixSCIMToken)+8], Hash: s.cfg.KeyHasher.Hash(secret),
		CreatedBy: p.UserID, CreatedByEmail: p.Email, CreatedAt: now, ExpiresAt: expiresAt}
	if err := s.store.CreateSCIMToken(ctx, &t); err != nil {
		return SCIMToken{}, "", s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "scim.token.create", "scim_token", t.ID, map[string]any{"name": name})
	return t, secret, nil
}

// ListSCIMTokens lists the organization's SCIM tokens (admin+).
func (s *Service) ListSCIMTokens(ctx context.Context, p *auth.Principal) ([]SCIMToken, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	ts, err := s.store.ListSCIMTokens(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ts, nil
}

// RevokeSCIMToken revokes a token (admin+); revocation is immediate.
func (s *Service) RevokeSCIMToken(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) error {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return err
	}
	t, err := s.store.RevokeSCIMToken(ctx, p.OrgID, id, s.now())
	if errors.Is(err, auth.ErrNotFound) {
		return notFound("SCIM token not found")
	}
	if err != nil {
		return s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "scim.token.revoke", "scim_token", t.ID, map[string]any{"name": t.Name})
	return nil
}

// AuthenticateSCIM resolves a SCIM bearer token to its organization.
func (s *Service) AuthenticateSCIM(ctx context.Context, token string) (SCIMToken, auth.Organization, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, PrefixSCIMToken) || len(token) > 256 {
		return SCIMToken{}, auth.Organization{}, &auth.Error{Code: auth.CodeUnauthenticated, Message: "invalid SCIM token"}
	}
	t, org, err := s.store.LookupSCIMToken(ctx, s.cfg.KeyHasher.Candidates(token))
	if errors.Is(err, auth.ErrNotFound) {
		return SCIMToken{}, auth.Organization{}, &auth.Error{Code: auth.CodeUnauthenticated, Message: "invalid SCIM token"}
	}
	if err != nil {
		return SCIMToken{}, auth.Organization{}, s.fail(err)
	}
	now := s.now()
	if !t.Usable(now) {
		return SCIMToken{}, auth.Organization{}, &auth.Error{Code: auth.CodeUnauthenticated, Message: "SCIM token revoked or expired"}
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= time.Minute {
		if err := s.store.TouchSCIMToken(ctx, t.ID, now); err != nil {
			s.log.Warn("cannot update SCIM token last_used_at", "err", err)
		}
	}
	return t, org, nil
}
