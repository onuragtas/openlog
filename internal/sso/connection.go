package sso

import (
	"context"
	"crypto/x509"
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

// ConnectionInput is what an administrator saves (POST /api/v1/sso/connections, PUT …/connections/{id}).
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
	IdPMetadataURL string
	IdPMetadataXML string
	// Metadata trust (D-098), only with IdPMetadataURL. MetadataSigningCertificatePEM: nil keeps the pinned
	// certificates of an unchanged URL (else the signer of signed metadata is pinned), "" removes them.
	MetadataSigningCertificatePEM *string
	AllowUnsignedMetadata         bool
	AllowIdPInitiated             bool
	RelayStateAllowlist           []string
	SignAuthnRequests             bool

	EmailAttribute  string
	NameAttribute   string
	GroupsAttribute string
	JITEnabled      *bool // nil = true
	DefaultRole     auth.Role
	SessionMaxAge   time.Duration

	// LogoutRedirectAllowlist: relative UI paths single logout may return to besides /login.
	LogoutRedirectAllowlist []string
	// AllowExternalInvitations: nil keeps the stored value (true for a new connection).
	AllowExternalInvitations *bool
}

const (
	minSessionMaxAge     = 5 * time.Minute
	maxSessionMaxAge     = 30 * 24 * time.Hour
	maxBreakGlass        = 10
	maxRoleMappings      = 200
	maxConnectionsPerOrg = 10
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

var errNoConnection = notFound("single sign-on is not configured")

// ListConnections returns the organization's connections, the default connection first (admin+).
func (s *Service) ListConnections(ctx context.Context, p *auth.Principal) ([]Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	cs, err := s.store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return cs, nil
}

// GetConnection returns connection id of the organization, id "" = the default connection (admin+);
// auth.ErrNotFound when there is none.
func (s *Service) GetConnection(ctx context.Context, p *auth.Principal, id string) (Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Connection{}, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return Connection{}, errNoConnection
		}
		return Connection{}, s.fail(err)
	}
	return c, nil
}

// SaveConnection updates the organization's default connection, or creates it when there is none (admin+; the
// single-connection API PUT /api/v1/sso/connection).
func (s *Service) SaveConnection(ctx context.Context, p *auth.Principal, in ConnectionInput, meta auth.ClientMeta) (Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Connection{}, err
	}
	existing, err := s.store.GetConnection(ctx, p.OrgID, "")
	if errors.Is(err, auth.ErrNotFound) {
		return s.CreateConnection(ctx, p, in, meta)
	}
	if err != nil {
		return Connection{}, s.fail(err)
	}
	return s.UpdateConnection(ctx, p, existing.ID, in, meta)
}

// CreateConnection adds a connection to the organization (admin+, at most 10).
func (s *Service) CreateConnection(ctx context.Context, p *auth.Principal, in ConnectionInput, meta auth.ClientMeta) (Connection, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Connection{}, err
	}
	if !s.Available() {
		return Connection{}, errUnavailableNoURL
	}
	cs, err := s.store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return Connection{}, s.fail(err)
	}
	if len(cs) >= maxConnectionsPerOrg {
		return Connection{}, precondition("an organization can have at most %d single sign-on connections", maxConnectionsPerOrg)
	}
	now := s.now()
	c := Connection{ID: uuid.NewString(), OrgID: p.OrgID, CreatedBy: p.UserID, CreatedAt: now, AllowExternalInvitations: true}
	if err := s.applyInput(ctx, &c, Connection{}, true, in, now); err != nil {
		return Connection{}, err
	}
	c.UpdatedBy, c.UpdatedAt = p.UserID, now
	if err := s.store.CreateConnection(ctx, &c); err != nil {
		return Connection{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.create", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "enabled": c.Enabled,
		"jit": c.JITEnabled, "default_role": c.DefaultRole, "version": c.ConfigVersion, "name": c.Name})
	return c, nil
}

// UpdateConnection replaces the settings of a connection (admin+). Every save increases the configuration version,
// so enforcement needs a new successful test after a change.
func (s *Service) UpdateConnection(ctx context.Context, p *auth.Principal, id string, in ConnectionInput, meta auth.ClientMeta) (Connection, error) {
	existing, err := s.GetConnection(ctx, p, id)
	if err != nil {
		return Connection{}, err
	}
	if !s.Available() {
		return Connection{}, errUnavailableNoURL
	}
	if id == "" {
		return Connection{}, invalid("connection id is required")
	}
	if existing.Enforce && !in.Enabled {
		return Connection{}, precondition("turn off single sign-on enforcement before disabling the connection")
	}
	now := s.now()
	c := existing
	if err := s.applyInput(ctx, &c, existing, false, in, now); err != nil {
		return Connection{}, err
	}
	c.UpdatedBy, c.UpdatedAt = p.UserID, now
	if err := s.store.UpdateConnection(ctx, &c); err != nil {
		return Connection{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.update", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "enabled": c.Enabled,
		"jit": c.JITEnabled, "default_role": c.DefaultRole, "version": c.ConfigVersion, "name": c.Name})
	return c, nil
}

// applyInput validates in and writes it onto c (existing: the stored connection of an update).
func (s *Service) applyInput(ctx context.Context, c *Connection, existing Connection, isNew bool, in ConnectionInput, now time.Time) error {
	var err error
	if c.Name, err = cleanText(in.Name, "name", 200, false); err != nil {
		return err
	}
	for _, f := range []struct {
		dst  *string
		v    string
		name string
	}{{&c.EmailAttribute, in.EmailAttribute, "email_attribute"}, {&c.NameAttribute, in.NameAttribute, "name_attribute"},
		{&c.GroupsAttribute, in.GroupsAttribute, "groups_attribute"}} {
		if *f.dst, err = cleanText(f.v, f.name, 256, false); err != nil {
			return err
		}
	}
	c.DefaultRole = in.DefaultRole
	if c.DefaultRole == "" {
		c.DefaultRole = auth.RoleViewer
	}
	if !assignableRole(c.DefaultRole) {
		return invalid("default_role must be admin, member or viewer (owners are managed in openlog)")
	}
	if in.SessionMaxAge != 0 && (in.SessionMaxAge < minSessionMaxAge || in.SessionMaxAge > maxSessionMaxAge) {
		return invalid("session_max_age_seconds must be 0 or between 300 and 2592000")
	}
	c.SessionMaxAge = in.SessionMaxAge
	c.JITEnabled = in.JITEnabled == nil || *in.JITEnabled
	c.Enabled = in.Enabled
	if in.AllowExternalInvitations != nil {
		c.AllowExternalInvitations = *in.AllowExternalInvitations
	}
	if c.LogoutRedirectAllowlist, err = cleanPaths(in.LogoutRedirectAllowlist, "logout_redirect_allowlist"); err != nil {
		return err
	}
	switch in.Protocol {
	case ProtocolOIDC:
		if err := s.applyOIDC(c, existing, isNew, in); err != nil {
			return err
		}
	case ProtocolSAML:
		if err := s.applySAML(ctx, c, existing, isNew, in, now); err != nil {
			return err
		}
	default:
		return invalid("protocol must be oidc or saml")
	}
	c.Protocol = in.Protocol
	c.ConfigVersion++
	return nil
}

// cleanPaths validates an allowlist of relative UI paths (at most 20).
func cleanPaths(in []string, field string) ([]string, error) {
	if len(in) > 20 {
		return nil, invalid("at most 20 %s entries", field)
	}
	var out []string
	for _, pth := range in {
		pth = strings.TrimSpace(pth)
		if pth == "" {
			continue
		}
		if validRedirect(pth) != pth {
			return nil, invalid("%s: %q must be a relative UI path such as /hosts", field, pth)
		}
		if !slices.Contains(out, pth) {
			out = append(out, pth)
		}
	}
	return out, nil
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
	// A connection saved before metadata trust (D-098) has neither pinned signing certificates nor an explicit
	// "allow unsigned" decision. Saving it without touching its metadata URL or trust settings (e.g. enable/disable)
	// keeps its stored metadata instead of requiring a trust decision; the refresh job still reports the missing
	// decision in health, and any change of URL or trust settings goes through metadataTrustOnSave.
	legacyTrust := metaURL != "" && !isNew && existing.SAML != nil && existing.SAML.IdPMetadataURL == metaURL &&
		in.MetadataSigningCertificatePEM == nil && !in.AllowUnsignedMetadata && strings.TrimSpace(in.IdPMetadataXML) == "" &&
		len(existing.SAML.MetadataSigningCerts) == 0 && !existing.SAML.AllowUnsignedMetadata && existing.SAML.IdPMetadataXML != ""
	if legacyTrust {
		xmlData = existing.SAML.IdPMetadataXML
	} else if metaURL != "" {
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
	var pins []*x509.Certificate
	if metaURL != "" && !legacyTrust {
		switch pemIn := in.MetadataSigningCertificatePEM; {
		case pemIn != nil && strings.TrimSpace(*pemIn) != "":
			if pins, err = parseCertificatesPEM(*pemIn); err != nil {
				return invalid("metadata_signing_certificate_pem: %v", err)
			}
		case pemIn == nil && !isNew && existing.SAML != nil && existing.SAML.IdPMetadataURL == metaURL:
			pins, _ = parseStoredCerts(existing.SAML.MetadataSigningCerts)
		}
		if pins, err = metadataTrustOnSave([]byte(xmlData), pins, in.AllowUnsignedMetadata, now); err != nil {
			return invalid("%v", err)
		}
	}
	allow, err := cleanPaths(in.RelayStateAllowlist, "relay_state_allowlist")
	if err != nil {
		return err
	}
	cfg := &SAMLConfig{IdPMetadataURL: metaURL, IdPMetadataXML: xmlData, IdPEntityID: info.IdPEntityID, IdPSSOURL: info.IdPSSOURL,
		IdPSLOURL: info.IdPSLOURL, IdPSLOBinding: info.IdPSLOBinding, IdPCertificates: info.IdPCertificates, IdPCertNotAfter: info.IdPCertNotAfter,
		AllowIdPInitiated: in.AllowIdPInitiated, RelayStateAllowlist: allow, SignAuthnRequests: in.SignAuthnRequests,
		MetadataSigningCerts: certPEMs(pins), AllowUnsignedMetadata: metaURL != "" && in.AllowUnsignedMetadata}
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

// DeleteConnection removes a connection (admin+). Its sessions end (sessions.sso_connection_id cascades; the
// session policy refuses them as well); its domains route to the default connection again.
func (s *Service) DeleteConnection(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) error {
	c, err := s.GetConnection(ctx, p, id)
	if err != nil {
		return err
	}
	if c.Enforce {
		return precondition("turn off single sign-on enforcement before deleting the connection")
	}
	conns, err := s.store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return s.fail(err)
	}
	if len(conns) > 1 && conns[0].ID == c.ID {
		// The next connection would become the default one and silently take over the unassigned domains.
		ds, err := s.store.ListDomains(ctx, p.OrgID)
		if err != nil {
			return s.fail(err)
		}
		if slices.ContainsFunc(ds, func(d Domain) bool { return d.VerifiedAt != nil && d.ConnectionID == "" }) {
			return precondition("assign the verified domains of the default connection to another connection before deleting it")
		}
	}
	if _, err := s.store.DeleteConnection(ctx, p.OrgID, c.ID); err != nil {
		return s.fail(err)
	}
	s.forgetProvider(c.ID)
	s.auth.Audit(ctx, p, meta, "sso.connection.delete", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "name": c.Name})
	return nil
}

// TestConnection runs the server-side checks of a connection (admin+): discovery/JWKS or metadata/certificates.
func (s *Service) TestConnection(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) ([]Check, error) {
	c, err := s.GetConnection(ctx, p, id)
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

// StartTest starts a test sign-in of a connection's current settings (admin+; also for a disabled connection).
// The result is stored on the connection; no user, membership or session is created.
func (s *Service) StartTest(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) (Start, error) {
	c, err := s.GetConnection(ctx, p, id)
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

// routesTo reports whether domain d routes to connection connID given the organization's connections (default
// first).
func routesTo(d Domain, conns []Connection, connID string) bool {
	if d.ConnectionID != "" {
		return d.ConnectionID == connID
	}
	return len(conns) > 0 && conns[0].ID == connID
}

// UpdateEnforcement turns SSO enforcement of a connection on or off and sets its break-glass owners (owner only).
// Enforcement applies to members whose e-mail domain routes to the connection.
//
// Lockout safeguards for turning it on: the connection is enabled, its current settings passed a test sign-in, a
// verified domain routes to it, at least one break-glass owner is named (all must be owners), and the caller keeps
// access (is a break-glass owner or uses an SSO session of this connection).
func (s *Service) UpdateEnforcement(ctx context.Context, p *auth.Principal, id string, enforce bool, breakGlass []string, meta auth.ClientMeta) (Connection, error) {
	if err := s.gate(p, auth.RoleOwner); err != nil {
		return Connection{}, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return Connection{}, errNoConnection
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
		conns, err := s.store.ListConnections(ctx, p.OrgID)
		if err != nil {
			return Connection{}, s.fail(err)
		}
		if !slices.ContainsFunc(domains, func(d Domain) bool { return d.VerifiedAt != nil && routesTo(d, conns, c.ID) }) {
			return Connection{}, precondition("verify at least one e-mail domain that signs in through this connection before enforcing single sign-on")
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

// checkMappingScope validates the connection of a role mapping request ("" = organization-wide).
func (s *Service) checkMappingScope(ctx context.Context, p *auth.Principal, connectionID string) error {
	if connectionID == "" {
		return nil
	}
	_, err := s.GetConnection(ctx, p, connectionID)
	return err
}

// ListRoleMappings returns the group → role mappings of a connection, connectionID "" = organization-wide (admin+).
func (s *Service) ListRoleMappings(ctx context.Context, p *auth.Principal, connectionID string) ([]RoleMapping, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if err := s.checkMappingScope(ctx, p, connectionID); err != nil {
		return nil, err
	}
	ms, err := s.store.ListRoleMappings(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ms, nil
}

// ReplaceRoleMappings replaces the mappings of a connection or, connectionID "", the organization-wide mappings
// (admin+). A connection with own mappings uses only those at sign-in; SCIM uses the organization-wide mappings, and
// the roles of SCIM-provisioned members are recomputed when they change.
func (s *Service) ReplaceRoleMappings(ctx context.Context, p *auth.Principal, connectionID string, in []RoleMapping, meta auth.ClientMeta) ([]RoleMapping, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if err := s.checkMappingScope(ctx, p, connectionID); err != nil {
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
	if err := s.store.ReplaceRoleMappings(ctx, p.OrgID, connectionID, out); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return nil, errNoConnection
		}
		return nil, s.fail(err)
	}
	details := map[string]any{"count": len(out)}
	target, targetID := "organization", p.OrgID
	if connectionID != "" {
		target, targetID = "sso_connection", connectionID
	}
	s.auth.Audit(ctx, p, meta, "sso.role_mappings.update", target, targetID, details)
	if s.cfg.SCIMEnabled && connectionID == "" {
		if n, err := s.SyncSCIMRoles(ctx, p.OrgID, "", meta.IP); err != nil {
			s.log.Warn("cannot recompute SCIM roles", "org_id", p.OrgID, "err", err)
		} else if n > 0 {
			s.log.Info("recomputed SCIM member roles", "org_id", p.OrgID, "changed", n)
		}
	}
	return out, nil
}

// mappingsFor returns the role mappings used at a sign-in through c: its own, else the organization-wide ones.
func (s *Service) mappingsFor(ctx context.Context, c Connection) ([]RoleMapping, error) {
	ms, err := s.store.ListRoleMappings(ctx, c.OrgID, c.ID)
	if err != nil || len(ms) > 0 {
		return ms, err
	}
	return s.store.ListRoleMappings(ctx, c.OrgID, "")
}
