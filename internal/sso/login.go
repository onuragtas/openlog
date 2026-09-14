package sso

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/onuragtas/openlog/internal/auth"
)

// Rate limits of the public sign-in endpoints (per client IP, shared through login_failures).
const (
	discoverPerIP  = 60
	startPerIP     = 30
	publicRateSpan = 10 * time.Minute
	// minAssertionTTL keeps assertion ids in the replay cache at least this long.
	minAssertionTTL = 10 * time.Minute
)

// Sign-in error codes, sent to the login page as ?sso_error= (docs/contracts/api.md "Single sign-on").
const (
	ErrCodeExpired           = "expired"
	ErrCodeInvalidRequest    = "invalid_request"
	ErrCodeIdP               = "idp_error"
	ErrCodeInvalidResponse   = "invalid_response"
	ErrCodeReplay            = "replay"
	ErrCodeDisabled          = "disabled"
	ErrCodeEmailMissing      = "email_missing"
	ErrCodeEmailNotVerified  = "email_not_verified"
	ErrCodeDomainNotVerified = "domain_not_verified"
	ErrCodeNotMember         = "not_member"
	ErrCodeDeprovisioned     = "deprovisioned"
	ErrCodeAccountDisabled   = "account_disabled"
	ErrCodeUnavailable       = "unavailable"
)

// BindingCookiePrefix is the prefix of the per-sign-in browser binding cookie (SameSite=Lax, Path=/api/v1/sso).
const BindingCookiePrefix = "openlog_sso_"

// BindingCookieName is the cookie of the sign-in with this state (several sign-ins may run in one browser).
func BindingCookieName(state string) string {
	h := sha256.Sum256([]byte(state))
	return BindingCookiePrefix + hex.EncodeToString(h[:6])
}

// Start is a started sign-in: the browser goes to URL; the handler sets the binding cookie.
type Start struct {
	URL     string
	State   string
	Binding string
	Expires time.Time
}

// BindingCookie returns the Set-Cookie of a started sign-in.
func (s *Service) BindingCookie(st Start) *http.Cookie {
	return &http.Cookie{Name: BindingCookieName(st.State), Value: st.Binding, Path: "/api/v1/sso", Expires: st.Expires.UTC(),
		MaxAge: max(1, int(st.Expires.Sub(s.now()).Seconds())), Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// ClearBindingCookie deletes a binding cookie.
func (s *Service) ClearBindingCookie(name string) *http.Cookie {
	return &http.Cookie{Name: name, Value: "", Path: "/api/v1/sso", MaxAge: -1, Expires: time.Unix(0, 0),
		Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// Outcome is where a sign-in step sends the browser. Session is set when a session was created.
type Outcome struct {
	Redirect    string
	Session     *auth.LoginResult
	ClearCookie string
}

func (s *Service) allowIP(ctx context.Context, purpose, ip string, max int) error {
	h := sha256.Sum256([]byte("sso-" + purpose + "\x00" + ip))
	n, err := s.users.CountLoginFailures(ctx, h[:], s.now().Add(-publicRateSpan))
	if err != nil {
		return s.fail(err)
	}
	if n >= max {
		return &auth.Error{Code: auth.CodeResourceExhausted, Message: "too many single sign-on attempts; try again later"}
	}
	if err := s.users.AddLoginFailure(ctx, h[:], s.now()); err != nil {
		return s.fail(err)
	}
	return nil
}

// Discovery tells the login page whether an e-mail address signs in with SSO.
type Discovery struct {
	SSO              bool
	OrganizationName string
	Protocol         Protocol
	Enforced         bool
}

func normalizeEmail(email string) (string, bool) {
	email = auth.NormalizeEmail(email)
	if len(email) < 3 || len(email) > 320 {
		return email, false
	}
	a, err := mail.ParseAddress(email)
	return email, err == nil && a.Address == email && a.Name == ""
}

// enabledConnectionFor returns the enabled connection of the organization that verified the domain of email.
func (s *Service) enabledConnectionFor(ctx context.Context, email string) (Connection, bool, error) {
	email, ok := normalizeEmail(email)
	if !ok {
		return Connection{}, false, invalid("a valid email address is required")
	}
	d, err := s.store.FindVerifiedDomain(ctx, emailDomain(email))
	if errors.Is(err, auth.ErrNotFound) {
		return Connection{}, false, nil
	}
	if err != nil {
		return Connection{}, false, s.fail(err)
	}
	c, err := s.store.GetConnection(ctx, d.OrgID)
	if errors.Is(err, auth.ErrNotFound) {
		return Connection{}, false, nil
	}
	if err != nil {
		return Connection{}, false, s.fail(err)
	}
	return c, c.Enabled, nil
}

// Discover reports whether email belongs to a domain with an enabled connection (public, rate limited). It
// reveals only that a domain uses SSO, never whether an account exists.
func (s *Service) Discover(ctx context.Context, email string, meta auth.ClientMeta) (Discovery, error) {
	if err := s.allowIP(ctx, "discover", meta.IP, discoverPerIP); err != nil {
		return Discovery{}, err
	}
	if !s.Available() {
		return Discovery{}, nil
	}
	c, ok, err := s.enabledConnectionFor(ctx, email)
	if err != nil || !ok {
		return Discovery{}, err
	}
	org, err := s.users.GetOrganization(ctx, c.OrgID)
	if err != nil {
		return Discovery{}, s.fail(err)
	}
	return Discovery{SSO: true, OrganizationName: org.Name, Protocol: c.Protocol, Enforced: c.Enforce}, nil
}

// StartLogin starts an SP-initiated sign-in for email (public, rate limited).
func (s *Service) StartLogin(ctx context.Context, email, redirect string, meta auth.ClientMeta) (Start, error) {
	if err := s.allowIP(ctx, "start", meta.IP, startPerIP); err != nil {
		return Start{}, err
	}
	if !s.Available() {
		return Start{}, notFound("single sign-on is not set up for this e-mail domain")
	}
	c, ok, err := s.enabledConnectionFor(ctx, email)
	if err != nil {
		return Start{}, err
	}
	if !ok {
		return Start{}, notFound("single sign-on is not set up for this e-mail domain")
	}
	st, err := s.begin(ctx, c, PurposeLogin, "", redirect)
	if err != nil {
		s.log.Warn("cannot start single sign-on", "org_id", c.OrgID, "connection_id", c.ID, "err", err)
		s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.login_failed", "sso_connection", c.ID, map[string]any{"reason": ErrCodeUnavailable, "error": truncate(err.Error(), 300)})
		return Start{}, &auth.Error{Code: auth.CodeUnavailable, Message: "the identity provider of your organization cannot be reached; try again later"}
	}
	return st, nil
}

// begin creates the login state and the IdP URL.
func (s *Service) begin(ctx context.Context, c Connection, purpose, actorID, redirect string) (Start, error) {
	state, err := auth.NewOpaqueToken()
	if err != nil {
		return Start{}, err
	}
	binding, err := auth.NewOpaqueToken()
	if err != nil {
		return Start{}, err
	}
	now := s.now()
	st := LoginState{StateHash: hashToken(state), BindingHash: hashToken(binding), OrgID: c.OrgID, ConnectionID: c.ID,
		Purpose: purpose, RedirectTo: validRedirect(redirect), ActorUserID: actorID, ConfigVersion: c.ConfigVersion,
		CreatedAt: now, ExpiresAt: now.Add(s.cfg.LoginTTL)}
	var target string
	switch c.Protocol {
	case ProtocolOIDC:
		if st.Nonce, err = auth.NewOpaqueToken(); err != nil {
			return Start{}, err
		}
		st.PKCEVerifier = oauth2.GenerateVerifier()
		target, err = s.oidcAuthURL(ctx, c, state, st.Nonce, st.PKCEVerifier)
	case ProtocolSAML:
		target, st.SAMLRequestID, err = s.samlAuthURL(c, state)
	default:
		err = errors.New("unknown protocol")
	}
	if err != nil {
		return Start{}, err
	}
	if err := s.store.CreateLoginState(ctx, &st); err != nil {
		return Start{}, s.fail(err)
	}
	return Start{URL: target, State: state, Binding: binding, Expires: st.ExpiresAt}, nil
}

func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

// failure ends a sign-in step: logs, audits, records a failed test and sends the browser to the login page (or
// the SSO settings for a test).
func (s *Service) failure(ctx context.Context, st *LoginState, c *Connection, code string, err error, meta auth.ClientMeta, email string) Outcome {
	orgID, connID := "", ""
	if c != nil {
		orgID, connID = c.OrgID, c.ID
	} else if st != nil {
		orgID, connID = st.OrgID, st.ConnectionID
	}
	msg := code
	if err != nil {
		msg = err.Error()
	}
	s.log.Warn("single sign-on failed", "reason", code, "org_id", orgID, "connection_id", connID, "err", msg)
	if orgID != "" {
		d := map[string]any{"reason": code, "error": truncate(msg, 300)}
		if st != nil {
			d["purpose"] = st.Purpose
		}
		s.audit(ctx, orgID, "", email, meta.IP, "sso.login_failed", "sso_connection", connID, d)
	}
	if st != nil && st.Purpose == PurposeTest && connID != "" {
		if rerr := s.store.RecordTest(ctx, connID, st.ConfigVersion, false, truncate(code+": "+msg, 500), map[string]any{"email": email}, s.now()); rerr != nil {
			s.log.Warn("cannot record SSO test result", "err", rerr)
		}
		return Outcome{Redirect: "/settings/sso?sso_test=failed"}
	}
	return Outcome{Redirect: "/login?sso_error=" + code}
}

func (s *Service) checkBinding(st LoginState, state string, cookie func(string) string) bool {
	if st.BindingHash == nil {
		return false
	}
	v := cookie(BindingCookieName(state))
	return v != "" && subtle.ConstantTimeCompare(hashToken(v), st.BindingHash) == 1
}

// loadStateConnection returns the connection of a consumed state; settings changed since the start expire it.
func (s *Service) loadStateConnection(ctx context.Context, st LoginState, want Protocol) (Connection, string, error) {
	c, err := s.store.GetConnectionByID(ctx, st.ConnectionID)
	if errors.Is(err, auth.ErrNotFound) {
		return Connection{}, ErrCodeDisabled, err
	}
	if err != nil {
		return Connection{}, ErrCodeUnavailable, err
	}
	if c.OrgID != st.OrgID || c.Protocol != want || c.ConfigVersion != st.ConfigVersion {
		return c, ErrCodeExpired, errors.New("the connection settings changed during the sign-in")
	}
	return c, "", nil
}

// OIDCCallback completes an OIDC sign-in (GET /api/v1/sso/oidc/callback). IdP-initiated OIDC is not supported:
// the state must belong to a sign-in started in this browser (binding cookie).
func (s *Service) OIDCCallback(ctx context.Context, q url.Values, cookie func(string) string, meta auth.ClientMeta) Outcome {
	state := q.Get("state")
	if state == "" || len(state) > 256 {
		return s.failure(ctx, nil, nil, ErrCodeInvalidRequest, errors.New("missing state"), meta, "")
	}
	out := func(o Outcome) Outcome {
		o.ClearCookie = BindingCookieName(state)
		return o
	}
	st, err := s.store.ConsumeLoginState(ctx, hashToken(state), s.now())
	if errors.Is(err, auth.ErrNotFound) {
		return out(s.failure(ctx, nil, nil, ErrCodeExpired, errors.New("unknown, used or expired state"), meta, ""))
	}
	if err != nil {
		return out(s.failure(ctx, nil, nil, ErrCodeUnavailable, err, meta, ""))
	}
	if !s.checkBinding(st, state, cookie) {
		return out(s.failure(ctx, &st, nil, ErrCodeExpired, errors.New("browser binding cookie missing or wrong"), meta, ""))
	}
	c, code, err := s.loadStateConnection(ctx, st, ProtocolOIDC)
	if err != nil {
		return out(s.failure(ctx, &st, nil, code, err, meta, ""))
	}
	if e := q.Get("error"); e != "" {
		return out(s.failure(ctx, &st, &c, ErrCodeIdP, errors.New(truncate(e+": "+q.Get("error_description"), 300)), meta, ""))
	}
	codeParam := q.Get("code")
	if codeParam == "" {
		return out(s.failure(ctx, &st, &c, ErrCodeInvalidRequest, errors.New("missing code"), meta, ""))
	}
	id, err := s.oidcIdentity(ctx, c, st, codeParam)
	if err != nil {
		return out(s.failure(ctx, &st, &c, ErrCodeInvalidResponse, err, meta, ""))
	}
	return out(s.finish(ctx, c, st, id, meta))
}

// SAMLACS verifies a SAMLResponse posted by the IdP (POST /api/v1/sso/saml/{connection_id}/acs). The result is
// stored on the login state and the browser continues to SAMLComplete, a top-level GET that carries the
// SameSite=Lax binding cookie (a cross-site POST does not).
func (s *Service) SAMLACS(ctx context.Context, connectionID, samlResponse, relayState string, meta auth.ClientMeta) Outcome {
	c, err := s.store.GetConnectionByID(ctx, connectionID)
	if err != nil || c.Protocol != ProtocolSAML || c.SAML == nil {
		if err == nil || errors.Is(err, auth.ErrNotFound) {
			return s.failure(ctx, nil, nil, ErrCodeDisabled, errors.New("unknown SAML connection"), meta, "")
		}
		return s.failure(ctx, nil, nil, ErrCodeUnavailable, err, meta, "")
	}
	doc, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(samlResponse), ""))
	if err != nil || len(doc) == 0 {
		return s.failure(ctx, nil, &c, ErrCodeInvalidRequest, errors.New("SAMLResponse is not base64"), meta, "")
	}
	now := s.now()
	var st *LoginState
	var possible []string
	if relayState != "" && len(relayState) <= 256 {
		if x, err := s.store.GetLoginState(ctx, hashToken(relayState), now); err == nil && x.ConnectionID == c.ID && x.Result == nil && !x.IdPInitiated {
			st, possible = &x, []string{x.SAMLRequestID}
		} else if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return s.failure(ctx, nil, &c, ErrCodeUnavailable, err, meta, "")
		}
	}
	if st == nil && !c.SAML.AllowIdPInitiated {
		return s.failure(ctx, nil, &c, ErrCodeExpired, errors.New("no matching sign-in and IdP-initiated sign-in is not allowed"), meta, "")
	}
	if st != nil && st.ConfigVersion != c.ConfigVersion {
		return s.failure(ctx, st, &c, ErrCodeExpired, errors.New("the connection settings changed during the sign-in"), meta, "")
	}
	if !c.Enabled && (st == nil || st.Purpose != PurposeTest) {
		return s.failure(ctx, st, &c, ErrCodeDisabled, errors.New("connection disabled"), meta, "")
	}
	a, err := s.samlAssertion(c, doc, possible)
	if err != nil {
		return s.failure(ctx, st, &c, ErrCodeInvalidResponse, err, meta, "")
	}
	fresh, err := s.store.RecordAssertion(ctx, c.ID, a.ID, assertionExpiry(a, minAssertionTTL, s.cfg.ClockSkew, now))
	if err != nil {
		return s.failure(ctx, st, &c, ErrCodeUnavailable, err, meta, "")
	}
	if !fresh {
		return s.failure(ctx, st, &c, ErrCodeReplay, errors.New("assertion "+truncate(a.ID, 80)+" was already used"), meta, "")
	}
	id := samlIdentity(c, a)
	if st != nil {
		if err := s.store.SetLoginResult(ctx, st.ID, id, now); err != nil {
			return s.failure(ctx, st, &c, ErrCodeExpired, err, meta, id.Email)
		}
		return Outcome{Redirect: "/api/v1/sso/saml/complete?state=" + url.QueryEscape(relayState)}
	}
	// IdP-initiated: RelayState is only a target path, from the allowlist.
	target := "/"
	if relayState != "" {
		if !slices.Contains(c.SAML.RelayStateAllowlist, relayState) {
			return s.failure(ctx, nil, &c, ErrCodeInvalidRequest, errors.New("RelayState is not in the allowlist"), meta, id.Email)
		}
		target = relayState
	}
	state, err := auth.NewOpaqueToken()
	if err != nil {
		return s.failure(ctx, nil, &c, ErrCodeUnavailable, err, meta, id.Email)
	}
	ns := LoginState{StateHash: hashToken(state), OrgID: c.OrgID, ConnectionID: c.ID, Purpose: PurposeLogin, IdPInitiated: true,
		RedirectTo: target, ConfigVersion: c.ConfigVersion, Result: &id, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.store.CreateLoginState(ctx, &ns); err != nil {
		return s.failure(ctx, nil, &c, ErrCodeUnavailable, err, meta, id.Email)
	}
	return Outcome{Redirect: "/api/v1/sso/saml/complete?state=" + url.QueryEscape(state)}
}

// SAMLComplete finishes a SAML sign-in (GET /api/v1/sso/saml/complete).
func (s *Service) SAMLComplete(ctx context.Context, state string, cookie func(string) string, meta auth.ClientMeta) Outcome {
	if state == "" || len(state) > 256 {
		return s.failure(ctx, nil, nil, ErrCodeInvalidRequest, errors.New("missing state"), meta, "")
	}
	out := func(o Outcome) Outcome {
		o.ClearCookie = BindingCookieName(state)
		return o
	}
	st, err := s.store.ConsumeLoginState(ctx, hashToken(state), s.now())
	if errors.Is(err, auth.ErrNotFound) {
		return out(s.failure(ctx, nil, nil, ErrCodeExpired, errors.New("unknown, used or expired state"), meta, ""))
	}
	if err != nil {
		return out(s.failure(ctx, nil, nil, ErrCodeUnavailable, err, meta, ""))
	}
	if st.Result == nil {
		return out(s.failure(ctx, &st, nil, ErrCodeExpired, errors.New("no verified assertion for this sign-in"), meta, ""))
	}
	if !st.IdPInitiated && !s.checkBinding(st, state, cookie) {
		return out(s.failure(ctx, &st, nil, ErrCodeExpired, errors.New("browser binding cookie missing or wrong"), meta, st.Result.Email))
	}
	c, code, err := s.loadStateConnection(ctx, st, ProtocolSAML)
	if err != nil {
		return out(s.failure(ctx, &st, nil, code, err, meta, st.Result.Email))
	}
	return out(s.finish(ctx, c, st, *st.Result, meta))
}

// finish provisions the user and creates the session, or records a test result.
func (s *Service) finish(ctx context.Context, c Connection, st LoginState, id Identity, meta auth.ClientMeta) Outcome {
	if st.Purpose == PurposeTest {
		return s.finishTest(ctx, c, st, id, meta)
	}
	if !c.Enabled {
		return s.failure(ctx, &st, &c, ErrCodeDisabled, errors.New("connection disabled"), meta, id.Email)
	}
	u, jit, code, err := s.provision(ctx, c, id, meta)
	if err != nil {
		return s.failure(ctx, &st, &c, code, err, meta, id.Email)
	}
	method := auth.MethodOIDC
	if c.Protocol == ProtocolSAML {
		method = auth.MethodSAML
	}
	res, err := s.auth.StartExternalSession(ctx, u, auth.SessionBinding{Method: method, OrgID: c.OrgID, ConnectionID: c.ID, MaxAge: c.SessionMaxAge}, meta)
	if err != nil {
		code := ErrCodeUnavailable
		if errors.Is(err, auth.ErrUnauthenticated) {
			code = ErrCodeAccountDisabled
		}
		return s.failure(ctx, &st, &c, code, err, meta, id.Email)
	}
	s.audit(ctx, c.OrgID, u.ID, u.Email, meta.IP, "sso.login", "session", res.Session.ID, map[string]any{"protocol": c.Protocol,
		"connection_id": c.ID, "subject": truncate(id.Subject, 256), "jit": jit, "idp_initiated": st.IdPInitiated})
	return Outcome{Redirect: st.RedirectTo, Session: &res}
}

// checkIdentity applies the identity requirements shared by sign-in and test: an e-mail address, verified when
// required (OIDC), in a domain verified by the connection's organization.
func (s *Service) checkIdentity(ctx context.Context, c Connection, id Identity) (string, string, error) {
	email, ok := normalizeEmail(id.Email)
	if !ok {
		return "", ErrCodeEmailMissing, errors.New("the identity provider sent no valid e-mail address (check the e-mail attribute)")
	}
	if c.Protocol == ProtocolOIDC && c.OIDC != nil && c.OIDC.RequireEmailVerified && !id.EmailVerified {
		return "", ErrCodeEmailNotVerified, errors.New("the identity provider did not mark the e-mail address as verified")
	}
	d, err := s.store.FindVerifiedDomain(ctx, emailDomain(email))
	if errors.Is(err, auth.ErrNotFound) || (err == nil && d.OrgID != c.OrgID) {
		return "", ErrCodeDomainNotVerified, errors.New("the e-mail domain " + emailDomain(email) + " is not a verified domain of the organization")
	}
	if err != nil {
		return "", ErrCodeUnavailable, err
	}
	return email, "", nil
}

// provision maps the identity to a user and membership: JIT creation with the mapped or default role, and role
// synchronisation from IdP groups for existing non-owner members.
func (s *Service) provision(ctx context.Context, c Connection, id Identity, meta auth.ClientMeta) (auth.User, bool, string, error) {
	email, code, err := s.checkIdentity(ctx, c, id)
	if err != nil {
		return auth.User{}, false, code, err
	}
	mappings, err := s.store.ListRoleMappings(ctx, c.OrgID)
	if err != nil {
		return auth.User{}, false, ErrCodeUnavailable, err
	}
	now := s.now()
	jit := false
	u, err := s.users.GetUserByEmail(ctx, email)
	if errors.Is(err, auth.ErrNotFound) {
		if !c.JITEnabled {
			return auth.User{}, false, ErrCodeNotMember, errors.New("no account and just-in-time provisioning is off")
		}
		name, _ := cleanText(id.Name, "name", 200, false)
		u = auth.User{Email: email, Name: name, CreatedAt: now, EmailVerifiedAt: &now}
		if err := s.users.CreateUser(ctx, &u); err != nil && !errors.Is(err, auth.ErrAlreadyExists) {
			return auth.User{}, false, ErrCodeUnavailable, err
		} else if err != nil {
			if u, err = s.users.GetUserByEmail(ctx, email); err != nil {
				return auth.User{}, false, ErrCodeUnavailable, err
			}
		}
	} else if err != nil {
		return auth.User{}, false, ErrCodeUnavailable, err
	}
	if u.DisabledAt != nil {
		return auth.User{}, false, ErrCodeAccountDisabled, errors.New("account disabled")
	}
	m, err := s.users.GetMembership(ctx, c.OrgID, u.ID)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		if !c.JITEnabled {
			return auth.User{}, false, ErrCodeNotMember, errors.New("not a member and just-in-time provisioning is off")
		}
		if su, err := s.store.GetSCIMUser(ctx, c.OrgID, u.ID); err == nil && !su.Active {
			return auth.User{}, false, ErrCodeDeprovisioned, errors.New("the user was deactivated by SCIM provisioning")
		} else if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return auth.User{}, false, ErrCodeUnavailable, err
		}
		role := c.DefaultRole
		if r, ok := roleFor(mappings, id.Groups); ok {
			role = r
		}
		if err := s.users.AddMember(ctx, c.OrgID, u.ID, role); err != nil && !errors.Is(err, auth.ErrAlreadyExists) {
			return auth.User{}, false, ErrCodeUnavailable, err
		}
		jit = true
		s.audit(ctx, c.OrgID, u.ID, u.Email, meta.IP, "member.add", "user", u.ID, map[string]any{"role": role, "via": "sso_jit", "protocol": c.Protocol})
	case err != nil:
		return auth.User{}, false, ErrCodeUnavailable, err
	case id.GroupsPresent && len(mappings) > 0 && m.Role != auth.RoleOwner:
		role := c.DefaultRole
		if r, ok := roleFor(mappings, id.Groups); ok {
			role = r
		}
		if role != m.Role {
			if err := s.users.UpdateMemberRole(ctx, c.OrgID, u.ID, role); err != nil {
				return auth.User{}, false, ErrCodeUnavailable, err
			}
			s.audit(ctx, c.OrgID, u.ID, u.Email, meta.IP, "member.role_change", "user", u.ID, map[string]any{"from": m.Role, "to": role, "via": "sso_groups"})
		}
	}
	return u, jit, "", nil
}

// finishTest records the result of a test sign-in without creating anything.
func (s *Service) finishTest(ctx context.Context, c Connection, st LoginState, id Identity, meta auth.ClientMeta) Outcome {
	email, code, err := s.checkIdentity(ctx, c, id)
	if err != nil {
		return s.failure(ctx, &st, &c, code, err, meta, id.Email)
	}
	mappings, err := s.store.ListRoleMappings(ctx, c.OrgID)
	if err != nil {
		return s.failure(ctx, &st, &c, ErrCodeUnavailable, err, meta, email)
	}
	role, mapped := roleFor(mappings, id.Groups)
	if !mapped {
		role = c.DefaultRole
	}
	groups := id.Groups
	if len(groups) > 50 {
		groups = groups[:50]
	}
	details := map[string]any{"email": email, "name": id.Name, "subject": truncate(id.Subject, 256), "groups": groups,
		"groups_present": id.GroupsPresent, "role": role, "role_mapped": mapped}
	if err := s.store.RecordTest(ctx, c.ID, st.ConfigVersion, true, "", details, s.now()); err != nil {
		return s.failure(ctx, &st, &c, ErrCodeUnavailable, err, meta, email)
	}
	s.audit(ctx, c.OrgID, st.ActorUserID, "", meta.IP, "sso.connection.test_login", "sso_connection", c.ID, map[string]any{"ok": true, "email": email})
	return Outcome{Redirect: "/settings/sso?sso_test=ok"}
}
