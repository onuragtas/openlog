package sso

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
)

// Check is one result of a connection test.
type Check struct {
	Name    string
	OK      bool
	Message string
}

// ConnectionInput is what an administrator saves (PUT /api/v1/sso/connection).
type ConnectionInput struct {
	Protocol Protocol
	Name     string
	Enabled  bool

	// OIDC. ClientSecret nil keeps the stored secret, "" removes it (public client with PKCE).
	Issuer               string
	ClientID             string
	ClientSecret         *string
	Scopes               []string
	RequireEmailVerified *bool // nil = true

	// SAML. With IdPMetadataURL the metadata is fetched now; otherwise IdPMetadataXML is required.
	IdPMetadataURL      string
	IdPMetadataXML      string
	AllowIdPInitiated   bool
	RelayStateAllowlist []string
	SignAuthnRequests   bool

	EmailAttribute  string
	NameAttribute   string
	GroupsAttribute string
	JITEnabled      *bool // nil = true
	DefaultRole     auth.Role
	SessionMaxAge   time.Duration
}

const (
	minSessionMaxAge = 5 * time.Minute
	maxSessionMaxAge = 30 * 24 * time.Hour
	maxBreakGlass    = 10
	maxRoleMappings  = 200
)

func cleanText(v, field string, max int, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" && required {
		return "", invalid("%s is required", field)
	}
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
		return "", invalid("%s must be at most %d characters", field, max)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", invalid("%s must not contain control characters", field)
		}
	}
	return v, nil
}

func assignableRole(r auth.Role) bool {
	return r == auth.RoleAdmin || r == auth.RoleMember || r == auth.RoleViewer
}

// GetConnection returns the organization's connection (admin+); auth.ErrNotFound when there is none.
func (s *Service) GetConnection(ctx context.Context, p *auth.Principal) (Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Connection{}, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return Connection{}, notFound("single sign-on is not configured")
		}
		return Connection{}, s.fail(err)
	}
	return c, nil
}

// SaveConnection creates or replaces the organization's connection (admin+). Every save increases the
// configuration version, so enforcement needs a new successful test after a change.
func (s *Service) SaveConnection(ctx context.Context, p *auth.Principal, in ConnectionInput, meta auth.ClientMeta) (Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Connection{}, err
	}
	if !s.Available() {
		return Connection{}, errUnavailableNoURL
	}
	existing, err := s.store.GetConnection(ctx, p.OrgID)
	isNew := errors.Is(err, auth.ErrNotFound)
	if err != nil && !isNew {
		return Connection{}, s.fail(err)
	}
	now := s.now()
	c := existing
	if isNew {
		c = Connection{ID: uuid.NewString(), OrgID: p.OrgID, CreatedBy: p.UserID, CreatedAt: now, ConfigVersion: 0}
	}
	if !isNew && existing.Enforce && !in.Enabled {
		return Connection{}, precondition("turn off single sign-on enforcement before disabling the connection")
	}
	if c.Name, err = cleanText(in.Name, "name", 200, false); err != nil {
		return Connection{}, err
	}
	for _, f := range []struct {
		dst  *string
		v    string
		name string
	}{{&c.EmailAttribute, in.EmailAttribute, "email_attribute"}, {&c.NameAttribute, in.NameAttribute, "name_attribute"},
		{&c.GroupsAttribute, in.GroupsAttribute, "groups_attribute"}} {
		if *f.dst, err = cleanText(f.v, f.name, 256, false); err != nil {
			return Connection{}, err
		}
	}
	c.DefaultRole = in.DefaultRole
	if c.DefaultRole == "" {
		c.DefaultRole = auth.RoleViewer
	}
	if !assignableRole(c.DefaultRole) {
		return Connection{}, invalid("default_role must be admin, member or viewer (owners are managed in openlog)")
	}
	if in.SessionMaxAge != 0 && (in.SessionMaxAge < minSessionMaxAge || in.SessionMaxAge > maxSessionMaxAge) {
		return Connection{}, invalid("session_max_age_seconds must be 0 or between 300 and 2592000")
	}
	c.SessionMaxAge = in.SessionMaxAge
	c.JITEnabled = in.JITEnabled == nil || *in.JITEnabled
	c.Enabled = in.Enabled

	switch in.Protocol {
	case ProtocolOIDC:
		if err := s.applyOIDC(&c, existing, isNew, in); err != nil {
			return Connection{}, err
		}
	case ProtocolSAML:
		if err := s.applySAML(ctx, &c, existing, isNew, in, now); err != nil {
			return Connection{}, err
		}
	default:
		return Connection{}, invalid("protocol must be oidc or saml")
	}
	c.Protocol = in.Protocol
	c.ConfigVersion++
	c.UpdatedBy, c.UpdatedAt = p.UserID, now
	if isNew {
		if err := s.store.CreateConnection(ctx, &c); err != nil {
			if errors.Is(err, auth.ErrAlreadyExists) {
				return Connection{}, &auth.Error{Code: auth.CodeAlreadyExists, Message: "the organization already has a connection"}
			}
			return Connection{}, s.fail(err)
		}
	} else if err := s.store.UpdateConnection(ctx, &c); err != nil {
		return Connection{}, s.fail(err)
	}
	action := "sso.connection.update"
	if isNew {
		action = "sso.connection.create"
	}
	s.auth.Audit(ctx, p, meta, action, "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "enabled": c.Enabled,
		"jit": c.JITEnabled, "default_role": c.DefaultRole, "version": c.ConfigVersion})
	return c, nil
}

func (s *Service) applyOIDC(c *Connection, existing Connection, isNew bool, in ConnectionInput) error {
	issuer := strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	if _, err := ParseIdPURL(issuer, s.cfg.AllowPrivateNetworks); err != nil {
		return invalid("issuer: %v", err)
	}
	clientID, err := cleanText(in.ClientID, "client_id", 512, true)
	if err != nil {
		return err
	}
	if len(in.Scopes) > 20 {
		return invalid("at most 20 scopes")
	}
	var scopes []string
	for _, sc := range in.Scopes {
		sc = strings.TrimSpace(sc)
		if sc == "" {
			continue
		}
		if len(sc) > 128 || strings.ContainsFunc(sc, func(r rune) bool { return r <= ' ' || r > '~' || r == '"' || r == '\\' }) {
			return invalid("invalid scope %q", sc)
		}
		if !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	reqVerified := in.RequireEmailVerified == nil || *in.RequireEmailVerified
	c.OIDC = &OIDCConfig{Issuer: issuer, ClientID: clientID, Scopes: scopes, RequireEmailVerified: reqVerified}
	c.SAML, c.SPKeyEnc = nil, nil
	switch {
	case in.ClientSecret != nil && *in.ClientSecret == "":
		c.SecretEnc = nil
	case in.ClientSecret != nil:
		if len(*in.ClientSecret) > 2048 {
			return invalid("client_secret is too long")
		}
		sealed, err := s.cfg.SecretBox.Seal([]byte(*in.ClientSecret), secretAAD(c.ID))
		if err != nil {
			return err
		}
		c.SecretEnc = sealed
	case isNew || existing.Protocol != ProtocolOIDC:
		c.SecretEnc = nil
	}
	return nil
}

func (s *Service) applySAML(ctx context.Context, c *Connection, existing Connection, isNew bool, in ConnectionInput, now time.Time) error {
	xmlData := strings.TrimSpace(in.IdPMetadataXML)
	metaURL := strings.TrimSpace(in.IdPMetadataURL)
	if metaURL != "" {
		u, err := ParseIdPURL(metaURL, s.cfg.AllowPrivateNetworks)
		if err != nil {
			return invalid("idp_metadata_url: %v", err)
		}
		fctx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
		defer cancel()
		b, err := fetch(fctx, s.client, u.String())
		if err != nil {
			return invalid("cannot fetch the IdP metadata: %v", err)
		}
		xmlData = string(b)
	} else if xmlData == "" && !isNew && existing.SAML != nil {
		xmlData = existing.SAML.IdPMetadataXML
	}
	if xmlData == "" {
		return invalid("idp_metadata_url or idp_metadata_xml is required")
	}
	_, info, err := idpMetadataInfo([]byte(xmlData), now)
	if err != nil {
		return invalid("%v", err)
	}
	if len(in.RelayStateAllowlist) > 20 {
		return invalid("at most 20 relay_state_allowlist entries")
	}
	var allow []string
	for _, pth := range in.RelayStateAllowlist {
		pth = strings.TrimSpace(pth)
		if pth == "" {
			continue
		}
		if validRedirect(pth) != pth {
			return invalid("relay_state_allowlist: %q must be a relative UI path such as /hosts", pth)
		}
		allow = append(allow, pth)
	}
	cfg := &SAMLConfig{IdPMetadataURL: metaURL, IdPMetadataXML: xmlData, IdPEntityID: info.IdPEntityID, IdPSSOURL: info.IdPSSOURL,
		IdPCertificates: info.IdPCertificates, IdPCertNotAfter: info.IdPCertNotAfter,
		AllowIdPInitiated: in.AllowIdPInitiated, RelayStateAllowlist: allow, SignAuthnRequests: in.SignAuthnRequests}
	if !isNew && existing.SAML != nil && len(existing.SPKeyEnc) > 0 {
		cfg.SPCertificatePEM, c.SPKeyEnc = existing.SAML.SPCertificatePEM, existing.SPKeyEnc
	} else {
		keyDER, certPEM, err := newSPKeyPair("openlog SP "+c.OrgID, now)
		if err != nil {
			return err
		}
		sealed, err := s.cfg.SecretBox.Seal(keyDER, spKeyAAD(c.ID))
		if err != nil {
			return err
		}
		cfg.SPCertificatePEM, c.SPKeyEnc = certPEM, sealed
	}
	c.SAML, c.OIDC, c.SecretEnc = cfg, nil, nil
	return nil
}

// DeleteConnection removes the organization's connection (admin+). Its sessions end (sessions.sso_connection_id
// cascades; the session policy refuses them as well).
func (s *Service) DeleteConnection(ctx context.Context, p *auth.Principal, meta auth.ClientMeta) error {
	c, err := s.GetConnection(ctx, p)
	if err != nil {
		return err
	}
	if c.Enforce {
		return precondition("turn off single sign-on enforcement before deleting the connection")
	}
	if _, err := s.store.DeleteConnection(ctx, p.OrgID); err != nil {
		return s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.delete", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol})
	return nil
}

// TestConnection runs the server-side checks of the connection (admin+): discovery/JWKS or metadata/certificates.
func (s *Service) TestConnection(ctx context.Context, p *auth.Principal, meta auth.ClientMeta) ([]Check, error) {
	c, err := s.GetConnection(ctx, p)
	if err != nil {
		return nil, err
	}
	checks := []Check{{Name: "public_url", OK: s.Available(), Message: s.cfg.PublicURL}}
	switch c.Protocol {
	case ProtocolOIDC:
		checks = append(checks, s.testOIDC(ctx, c)...)
	case ProtocolSAML:
		checks = append(checks, s.testSAML(c)...)
	}
	ok := true
	for _, ch := range checks {
		ok = ok && ch.OK
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.test", "sso_connection", c.ID, map[string]any{"ok": ok})
	return checks, nil
}

// StartTest starts a test sign-in of the current settings (admin+; also for a disabled connection). The result is
// stored on the connection; no user, membership or session is created.
func (s *Service) StartTest(ctx context.Context, p *auth.Principal, meta auth.ClientMeta) (Start, error) {
	c, err := s.GetConnection(ctx, p)
	if err != nil {
		return Start{}, err
	}
	if !s.Available() {
		return Start{}, errUnavailableNoURL
	}
	st, err := s.begin(ctx, c, PurposeTest, p.UserID, "/settings/sso")
	if err != nil {
		return Start{}, precondition("cannot start a test sign-in: %v", err)
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.test_start", "sso_connection", c.ID, nil)
	return st, nil
}

// UpdateEnforcement turns SSO enforcement on or off and sets the break-glass owners (owner only).
//
// Lockout safeguards for turning it on: the connection is enabled, its current settings passed a test sign-in,
// the organization has a verified domain, at least one break-glass owner is named (all must be owners), and the
// caller keeps access (is a break-glass owner or uses an SSO session of this connection).
func (s *Service) UpdateEnforcement(ctx context.Context, p *auth.Principal, enforce bool, breakGlass []string, meta auth.ClientMeta) (Connection, error) {
	if err := s.gate(p, auth.RoleOwner); err != nil {
		return Connection{}, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID)
	if errors.Is(err, auth.ErrNotFound) {
		return Connection{}, notFound("single sign-on is not configured")
	}
	if err != nil {
		return Connection{}, s.fail(err)
	}
	ids := []string{}
	for _, id := range breakGlass {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxBreakGlass {
		return Connection{}, invalid("at most %d break-glass owners", maxBreakGlass)
	}
	members, err := s.users.ListMembers(ctx, p.OrgID)
	if err != nil {
		return Connection{}, s.fail(err)
	}
	for _, id := range ids {
		if !slices.ContainsFunc(members, func(m auth.Member) bool { return m.UserID == id && m.Role == auth.RoleOwner }) {
			return Connection{}, invalid("break-glass accounts must be owners of the organization")
		}
	}
	if enforce {
		switch {
		case !c.Enabled:
			return Connection{}, precondition("enable the connection before enforcing single sign-on")
		case !c.Tested():
			return Connection{}, precondition("run a successful test sign-in of the current settings before enforcing single sign-on")
		case len(ids) == 0:
			return Connection{}, precondition("name at least one break-glass owner who can still sign in with a password")
		}
		domains, err := s.store.ListDomains(ctx, p.OrgID)
		if err != nil {
			return Connection{}, s.fail(err)
		}
		if !slices.ContainsFunc(domains, func(d Domain) bool { return d.VerifiedAt != nil }) {
			return Connection{}, precondition("verify at least one e-mail domain before enforcing single sign-on")
		}
		if !slices.Contains(ids, p.UserID) {
			sso, err := s.sessionUsesConnection(ctx, p, c.ID)
			if err != nil {
				return Connection{}, err
			}
			if !sso {
				return Connection{}, precondition("you would lose access: add yourself as a break-glass owner or sign in with SSO first")
			}
		}
	}
	c.Enforce, c.BreakGlassUserIDs = enforce, ids
	c.UpdatedBy, c.UpdatedAt = p.UserID, s.now()
	if err := s.store.UpdateConnection(ctx, &c); err != nil {
		return Connection{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.enforcement.update", "sso_connection", c.ID, map[string]any{"enforce": enforce, "break_glass_user_ids": ids})
	return c, nil
}

func (s *Service) sessionUsesConnection(ctx context.Context, p *auth.Principal, connectionID string) (bool, error) {
	ss, err := s.users.ListSessions(ctx, p.UserID, s.now())
	if err != nil {
		return false, s.fail(err)
	}
	for _, x := range ss {
		if x.ID == p.SessionID {
			return x.ConnectionID == connectionID, nil
		}
	}
	return false, nil
}

// ListRoleMappings returns the organization's group → role mappings (admin+).
func (s *Service) ListRoleMappings(ctx context.Context, p *auth.Principal) ([]RoleMapping, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	ms, err := s.store.ListRoleMappings(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ms, nil
}

// ReplaceRoleMappings replaces the mappings (admin+). Roles of SCIM-provisioned members are recomputed.
func (s *Service) ReplaceRoleMappings(ctx context.Context, p *auth.Principal, in []RoleMapping, meta auth.ClientMeta) ([]RoleMapping, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if len(in) > maxRoleMappings {
		return nil, invalid("at most %d role mappings", maxRoleMappings)
	}
	out := make([]RoleMapping, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		g, err := cleanText(m.Group, "group", 512, true)
		if err != nil {
			return nil, err
		}
		if !assignableRole(m.Role) {
			return nil, invalid("role of group %q must be admin, member or viewer", g)
		}
		if seen[g] {
			return nil, invalid("group %q is mapped twice", g)
		}
		seen[g] = true
		out = append(out, RoleMapping{Group: g, Role: m.Role})
	}
	if err := s.store.ReplaceRoleMappings(ctx, p.OrgID, out); err != nil {
		return nil, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.role_mappings.update", "organization", p.OrgID, map[string]any{"count": len(out)})
	if s.cfg.SCIMEnabled {
		if n, err := s.SyncSCIMRoles(ctx, p.OrgID, "", meta.IP); err != nil {
			s.log.Warn("cannot recompute SCIM roles", "org_id", p.OrgID, "err", err)
		} else if n > 0 {
			s.log.Info("recomputed SCIM member roles", "org_id", p.OrgID, "changed", n)
		}
	}
	return out, nil
}
