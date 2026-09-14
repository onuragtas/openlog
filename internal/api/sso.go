package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/scim"
	"github.com/onuragtas/openlog/internal/sso"
)

// SetSSO enables single sign-on, domain verification and SCIM (docs/contracts/api.md "Single sign-on", "SCIM";
// D-077, D-078). Requires SetAccounts; must be called before Run.
func (s *Server) SetSSO(svc *sso.Service) {
	s.sso = svc
	s.srv.Handler = s.Handler()
}

// ssoService is the type of Server.sso (declared here so api.go needs no import of internal/sso).
type ssoService = *sso.Service

// maxACSBody bounds the form posted to the SAML assertion consumer service.
const maxACSBody = 1 << 20

// decodeJSONLimit is decodeJSON with a larger body limit (pasted SAML metadata).
func decodeJSONLimit(r *http.Request, v any, limit int64) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		return badRequest("Content-Type must be application/json")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

func (s *Server) ssoRoutes(mux *http.ServeMux) {
	if s.accounts == nil || s.sso == nil {
		return
	}
	authed := func(pattern string, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
	public := func(pattern string, h publicFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			if err := h(rec, r); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}

	public("POST /api/v1/auth/sso/discover", s.ssoDiscover)
	public("POST /api/v1/auth/sso/start", s.ssoStart)
	public("GET /api/v1/sso/oidc/callback", s.ssoOIDCCallback)
	public("GET /api/v1/sso/saml/{connection_id}/metadata", s.ssoSAMLMetadata)
	public("POST /api/v1/sso/saml/{connection_id}/acs", s.ssoSAMLACS)
	public("GET /api/v1/sso/saml/complete", s.ssoSAMLComplete)
	public("POST /api/v1/sso/domains/verify-email", s.ssoVerifyDomainEmail)

	authed("GET /api/v1/sso/connection", s.ssoGetConnection)
	authed("PUT /api/v1/sso/connection", s.ssoSaveConnection)
	authed("DELETE /api/v1/sso/connection", s.ssoDeleteConnection)
	authed("POST /api/v1/sso/connection/test", s.ssoTestConnection)
	authed("POST /api/v1/sso/connection/test/start", s.ssoStartTest)
	authed("PUT /api/v1/sso/enforcement", s.ssoEnforcement)
	authed("GET /api/v1/sso/role-mappings", s.ssoListRoleMappings)
	authed("PUT /api/v1/sso/role-mappings", s.ssoReplaceRoleMappings)
	authed("GET /api/v1/sso/domains", s.ssoListDomains)
	authed("POST /api/v1/sso/domains", s.ssoAddDomain)
	authed("POST /api/v1/sso/domains/{id}/verify", s.ssoVerifyDomain)
	authed("DELETE /api/v1/sso/domains/{id}", s.ssoDeleteDomain)
	authed("GET /api/v1/scim/tokens", s.scimListTokens)
	authed("POST /api/v1/scim/tokens", s.scimCreateToken)
	authed("DELETE /api/v1/scim/tokens/{id}", s.scimRevokeToken)

	h := scim.NewHandler(s.sso, s.accounts.Meta, s.log)
	mux.Handle(scim.BasePath+"/", s.instrument(scim.BasePath, func(rec *statusRecorder, r *http.Request) { h.ServeHTTP(rec, r) }))
}

// ---- response shapes ----

type ssoOIDCJSON struct {
	Issuer               string   `json:"issuer"`
	ClientID             string   `json:"client_id"`
	Scopes               []string `json:"scopes"`
	RequireEmailVerified bool     `json:"require_email_verified"`
	ClientSecretSet      bool     `json:"client_secret_set"`
}

type ssoSAMLJSON struct {
	IdPMetadataURL      string   `json:"idp_metadata_url"`
	IdPEntityID         string   `json:"idp_entity_id"`
	IdPSSOURL           string   `json:"idp_sso_url"`
	IdPCertificates     []string `json:"idp_certificates"`
	IdPCertNotAfter     *string  `json:"idp_cert_not_after"`
	AllowIdPInitiated   bool     `json:"allow_idp_initiated"`
	RelayStateAllowlist []string `json:"relay_state_allowlist"`
	SignAuthnRequests   bool     `json:"sign_authn_requests"`
}

type ssoTestJSON struct {
	At      string         `json:"at"`
	OK      bool           `json:"ok"`
	Current bool           `json:"current"`
	Error   string         `json:"error"`
	Details map[string]any `json:"details"`
}

type ssoConnectionJSON struct {
	ID                   string       `json:"id"`
	Protocol             string       `json:"protocol"`
	Name                 string       `json:"name"`
	Enabled              bool         `json:"enabled"`
	OIDC                 *ssoOIDCJSON `json:"oidc"`
	SAML                 *ssoSAMLJSON `json:"saml"`
	EmailAttribute       string       `json:"email_attribute"`
	NameAttribute        string       `json:"name_attribute"`
	GroupsAttribute      string       `json:"groups_attribute"`
	JITEnabled           bool         `json:"jit_enabled"`
	DefaultRole          string       `json:"default_role"`
	SessionMaxAgeSeconds int64        `json:"session_max_age_seconds"`
	Enforce              bool         `json:"enforce"`
	BreakGlassUserIDs    []string     `json:"break_glass_user_ids"`
	ConfigVersion        int          `json:"config_version"`
	Tested               bool         `json:"tested"`
	LastTest             *ssoTestJSON `json:"last_test"`
	CreatedAt            string       `json:"created_at"`
	UpdatedAt            string       `json:"updated_at"`
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func ssoConnectionResponse(c sso.Connection) *ssoConnectionJSON {
	out := &ssoConnectionJSON{ID: c.ID, Protocol: string(c.Protocol), Name: c.Name, Enabled: c.Enabled,
		EmailAttribute: c.EmailAttribute, NameAttribute: c.NameAttribute, GroupsAttribute: c.GroupsAttribute,
		JITEnabled: c.JITEnabled, DefaultRole: string(c.DefaultRole), SessionMaxAgeSeconds: int64(c.SessionMaxAge / time.Second),
		Enforce: c.Enforce, BreakGlassUserIDs: nonNilStrings(c.BreakGlassUserIDs), ConfigVersion: c.ConfigVersion, Tested: c.Tested(),
		CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt)}
	if c.OIDC != nil {
		out.OIDC = &ssoOIDCJSON{Issuer: c.OIDC.Issuer, ClientID: c.OIDC.ClientID, Scopes: nonNilStrings(c.OIDC.Scopes),
			RequireEmailVerified: c.OIDC.RequireEmailVerified, ClientSecretSet: len(c.SecretEnc) > 0}
	}
	if c.SAML != nil {
		out.SAML = &ssoSAMLJSON{IdPMetadataURL: c.SAML.IdPMetadataURL, IdPEntityID: c.SAML.IdPEntityID, IdPSSOURL: c.SAML.IdPSSOURL,
			IdPCertificates: nonNilStrings(c.SAML.IdPCertificates), AllowIdPInitiated: c.SAML.AllowIdPInitiated,
			RelayStateAllowlist: nonNilStrings(c.SAML.RelayStateAllowlist), SignAuthnRequests: c.SAML.SignAuthnRequests}
		if c.SAML.IdPCertNotAfter != "" {
			v := c.SAML.IdPCertNotAfter
			out.SAML.IdPCertNotAfter = &v
		}
	}
	if c.LastTestAt != nil {
		d := c.LastTestDetails
		if d == nil {
			d = map[string]any{}
		}
		// current: a successful test of the current settings; a failed test is always the latest attempt.
		out.LastTest = &ssoTestJSON{At: formatTime(*c.LastTestAt), OK: c.LastTestOK, Current: !c.LastTestOK || c.Tested(),
			Error: c.LastTestError, Details: d}
	}
	return out
}

func (s *Server) ssoState(w http.ResponseWriter, c *sso.Connection) {
	sp := map[string]any{"oidc_redirect_uri": s.sso.OIDCRedirectURL(), "scim_base_url": s.sso.SCIMBaseURL(),
		"saml_entity_id": nil, "saml_acs_url": nil, "saml_metadata_url": nil, "saml_certificate_pem": nil}
	var conn *ssoConnectionJSON
	if c != nil {
		conn = ssoConnectionResponse(*c)
		if c.Protocol == sso.ProtocolSAML && c.SAML != nil {
			sp["saml_entity_id"], sp["saml_acs_url"] = s.sso.SAMLEntityID(c.ID), s.sso.SAMLACSURL(c.ID)
			sp["saml_metadata_url"], sp["saml_certificate_pem"] = s.sso.SAMLEntityID(c.ID), c.SAML.SPCertificatePEM
		}
	}
	cfg := s.sso.Config()
	writeJSON(w, http.StatusOK, map[string]any{"available": s.sso.Available(), "secrets_encrypted": cfg.SecretBox.Encrypted(),
		"scim_enabled": s.sso.SCIMEnabled(), "email_verification_available": cfg.Mailer != nil && cfg.PublicURL != "",
		"domain_email_local_parts": sso.DomainEmailLocalParts, "service_provider": sp, "connection": conn})
}

// ---- admin ----

func (s *Server) ssoGetConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	c, err := s.sso.GetConnection(r.Context(), p)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			s.ssoState(w, nil)
			return nil
		}
		return err
	}
	s.ssoState(w, &c)
	return nil
}

func (s *Server) ssoSaveConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Protocol string `json:"protocol"`
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		OIDC     *struct {
			Issuer               string   `json:"issuer"`
			ClientID             string   `json:"client_id"`
			ClientSecret         *string  `json:"client_secret"`
			Scopes               []string `json:"scopes"`
			RequireEmailVerified *bool    `json:"require_email_verified"`
		} `json:"oidc"`
		SAML *struct {
			IdPMetadataURL      string   `json:"idp_metadata_url"`
			IdPMetadataXML      string   `json:"idp_metadata_xml"`
			AllowIdPInitiated   bool     `json:"allow_idp_initiated"`
			RelayStateAllowlist []string `json:"relay_state_allowlist"`
			SignAuthnRequests   bool     `json:"sign_authn_requests"`
		} `json:"saml"`
		EmailAttribute       string `json:"email_attribute"`
		NameAttribute        string `json:"name_attribute"`
		GroupsAttribute      string `json:"groups_attribute"`
		JITEnabled           *bool  `json:"jit_enabled"`
		DefaultRole          string `json:"default_role"`
		SessionMaxAgeSeconds int64  `json:"session_max_age_seconds"`
	}
	if err := decodeJSONLimit(r, &in, 1<<20); err != nil {
		return err
	}
	if in.SessionMaxAgeSeconds < 0 || in.SessionMaxAgeSeconds > 1<<31 {
		return badRequest("session_max_age_seconds is out of range")
	}
	ci := sso.ConnectionInput{Protocol: sso.Protocol(in.Protocol), Name: in.Name, Enabled: in.Enabled,
		EmailAttribute: in.EmailAttribute, NameAttribute: in.NameAttribute, GroupsAttribute: in.GroupsAttribute,
		JITEnabled: in.JITEnabled, DefaultRole: auth.Role(in.DefaultRole), SessionMaxAge: time.Duration(in.SessionMaxAgeSeconds) * time.Second}
	switch ci.Protocol {
	case sso.ProtocolOIDC:
		if in.OIDC == nil {
			return badRequest("oidc settings are required")
		}
		ci.Issuer, ci.ClientID, ci.ClientSecret, ci.Scopes, ci.RequireEmailVerified =
			in.OIDC.Issuer, in.OIDC.ClientID, in.OIDC.ClientSecret, in.OIDC.Scopes, in.OIDC.RequireEmailVerified
	case sso.ProtocolSAML:
		if in.SAML == nil {
			return badRequest("saml settings are required")
		}
		ci.IdPMetadataURL, ci.IdPMetadataXML, ci.AllowIdPInitiated, ci.RelayStateAllowlist, ci.SignAuthnRequests =
			in.SAML.IdPMetadataURL, in.SAML.IdPMetadataXML, in.SAML.AllowIdPInitiated, in.SAML.RelayStateAllowlist, in.SAML.SignAuthnRequests
	}
	c, err := s.sso.SaveConnection(r.Context(), p, ci, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	s.ssoState(w, &c)
	return nil
}

func (s *Server) ssoDeleteConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.sso.DeleteConnection(r.Context(), p, s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) ssoTestConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	checks, err := s.sso.TestConnection(r.Context(), p, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	type checkJSON struct {
		Name    string `json:"name"`
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	out := make([]checkJSON, 0, len(checks))
	ok := true
	for _, c := range checks {
		out = append(out, checkJSON{c.Name, c.OK, c.Message})
		ok = ok && c.OK
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "checks": out})
	return nil
}

func (s *Server) ssoStartTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	st, err := s.sso.StartTest(r.Context(), p, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	http.SetCookie(w, s.sso.BindingCookie(st))
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": st.URL})
	return nil
}

func (s *Server) ssoEnforcement(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Enforce           bool     `json:"enforce"`
		BreakGlassUserIDs []string `json:"break_glass_user_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.sso.UpdateEnforcement(r.Context(), p, in.Enforce, in.BreakGlassUserIDs, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	s.ssoState(w, &c)
	return nil
}

type roleMappingJSON struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

func roleMappingsResponse(ms []sso.RoleMapping) map[string]any {
	out := make([]roleMappingJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, roleMappingJSON{m.Group, string(m.Role)})
	}
	return map[string]any{"mappings": out}
}

func (s *Server) ssoListRoleMappings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ms, err := s.sso.ListRoleMappings(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, roleMappingsResponse(ms))
	return nil
}

func (s *Server) ssoReplaceRoleMappings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Mappings []roleMappingJSON `json:"mappings"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ms := make([]sso.RoleMapping, 0, len(in.Mappings))
	for _, m := range in.Mappings {
		ms = append(ms, sso.RoleMapping{Group: m.Group, Role: auth.Role(m.Role)})
	}
	out, err := s.sso.ReplaceRoleMappings(r.Context(), p, ms, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, roleMappingsResponse(out))
	return nil
}

type domainJSON struct {
	ID                 string            `json:"id"`
	Domain             string            `json:"domain"`
	Verified           bool              `json:"verified"`
	VerifiedAt         *string           `json:"verified_at"`
	VerificationMethod *string           `json:"verification_method"`
	DNSRecord          map[string]string `json:"dns_record"`
	EmailAddress       *string           `json:"email_address"`
	EmailExpiresAt     *string           `json:"email_expires_at"`
	LastCheckedAt      *string           `json:"last_checked_at"`
	CreatedAt          string            `json:"created_at"`
}

func ssoOptString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func domainResponse(d sso.Domain) domainJSON {
	name, value := sso.DNSRecord(d)
	out := domainJSON{ID: d.ID, Domain: d.Domain, Verified: d.VerifiedAt != nil, VerifiedAt: optTime(d.VerifiedAt),
		VerificationMethod: ssoOptString(d.VerificationMethod), DNSRecord: map[string]string{"type": "TXT", "name": name, "value": value},
		EmailAddress: ssoOptString(d.EmailAddress), EmailExpiresAt: optTime(d.EmailExpiresAt), LastCheckedAt: optTime(d.LastCheckedAt),
		CreatedAt: formatTime(d.CreatedAt)}
	if d.EmailExpiresAt == nil {
		out.EmailAddress = nil
	}
	return out
}

func (s *Server) ssoListDomains(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ds, err := s.sso.ListDomains(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]domainJSON, 0, len(ds))
	for _, d := range ds {
		out = append(out, domainResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
	return nil
}

func (s *Server) ssoAddDomain(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	d, err := s.sso.AddDomain(r.Context(), p, in.Domain, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, domainResponse(d))
	return nil
}

func (s *Server) ssoVerifyDomain(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Method         string `json:"method"`
		EmailLocalPart string `json:"email_local_part"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	var d sso.Domain
	var err error
	switch in.Method {
	case "dns_txt", "":
		d, err = s.sso.VerifyDomainDNS(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r))
	case "email":
		d, err = s.sso.SendDomainVerificationEmail(r.Context(), p, r.PathValue("id"), in.EmailLocalPart, s.accounts.Meta(r))
	default:
		return badRequest("method must be dns_txt or email")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, domainResponse(d))
	return nil
}

func (s *Server) ssoDeleteDomain(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.sso.DeleteDomain(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type scimTokenJSON struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Prefix         string  `json:"prefix"`
	CreatedByEmail string  `json:"created_by_email"`
	CreatedAt      string  `json:"created_at"`
	LastUsedAt     *string `json:"last_used_at"`
	ExpiresAt      *string `json:"expires_at"`
	RevokedAt      *string `json:"revoked_at"`
}

func scimTokenResponse(t sso.SCIMToken) scimTokenJSON {
	return scimTokenJSON{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedByEmail: t.CreatedByEmail, CreatedAt: formatTime(t.CreatedAt),
		LastUsedAt: optTime(t.LastUsedAt), ExpiresAt: optTime(t.ExpiresAt), RevokedAt: optTime(t.RevokedAt)}
}

func (s *Server) scimListTokens(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ts, err := s.sso.ListSCIMTokens(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]scimTokenJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, scimTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out, "base_url": s.sso.SCIMBaseURL(), "enabled": s.sso.SCIMEnabled()})
	return nil
}

func (s *Server) scimCreateToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Name      string  `json:"name"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	var expires *time.Time
	if in.ExpiresAt != nil && *in.ExpiresAt != "" {
		t, err := parseTime(*in.ExpiresAt)
		if err != nil {
			return badRequest("expires_at: %v", err)
		}
		expires = &t
	}
	t, secret, err := s.sso.CreateSCIMToken(r.Context(), p, in.Name, expires, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": scimTokenResponse(t), "secret": secret})
	return nil
}

func (s *Server) scimRevokeToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.sso.RevokeSCIMToken(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- public sign-in ----

func (s *Server) ssoDiscover(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	d, err := s.sso.Discover(r.Context(), in.Email, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	out := map[string]any{"sso": d.SSO, "organization_name": nil, "protocol": nil, "enforced": d.Enforced}
	if d.SSO {
		out["organization_name"], out["protocol"] = d.OrganizationName, d.Protocol
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Redirect string `json:"redirect"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	st, err := s.sso.StartLogin(r.Context(), in.Email, in.Redirect, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	http.SetCookie(w, s.sso.BindingCookie(st))
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": st.URL})
	return nil
}

func cookieReader(r *http.Request) func(string) string {
	return func(name string) string {
		if c, err := r.Cookie(name); err == nil {
			return c.Value
		}
		return ""
	}
}

// ssoRedirect applies an sso.Outcome: session cookie, binding cookie removal and a 303 to the UI.
func (s *Server) ssoRedirect(w http.ResponseWriter, o sso.Outcome) {
	if o.Session != nil {
		http.SetCookie(w, s.accounts.SessionCookie(o.Session.Token, o.Session.Session.ExpiresAt))
	}
	if o.ClearCookie != "" {
		http.SetCookie(w, s.sso.ClearBindingCookie(o.ClearCookie))
	}
	target := o.Redirect
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		target = "/"
	}
	// The callback URL carries the authorization code: never send it as Referer to the next page.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) ssoOIDCCallback(w http.ResponseWriter, r *http.Request) error {
	s.ssoRedirect(w, s.sso.OIDCCallback(r.Context(), r.URL.Query(), cookieReader(r), s.accounts.Meta(r)))
	return nil
}

func (s *Server) ssoSAMLACS(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxACSBody)
	if err := r.ParseForm(); err != nil {
		return badRequest("invalid form body")
	}
	s.ssoRedirect(w, s.sso.SAMLACS(r.Context(), r.PathValue("connection_id"), r.PostForm.Get("SAMLResponse"), r.PostForm.Get("RelayState"), s.accounts.Meta(r)))
	return nil
}

func (s *Server) ssoSAMLComplete(w http.ResponseWriter, r *http.Request) error {
	s.ssoRedirect(w, s.sso.SAMLComplete(r.Context(), r.URL.Query().Get("state"), cookieReader(r), s.accounts.Meta(r)))
	return nil
}

func (s *Server) ssoSAMLMetadata(w http.ResponseWriter, r *http.Request) error {
	c, err := s.sso.Store().GetConnectionByID(r.Context(), r.PathValue("connection_id"))
	if err != nil || c.Protocol != sso.ProtocolSAML {
		if err == nil || errors.Is(err, auth.ErrNotFound) {
			return notFound("no such SAML connection")
		}
		return err
	}
	b, err := s.sso.SAMLMetadata(c)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return nil
}

func (s *Server) ssoVerifyDomainEmail(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	d, err := s.sso.ConsumeDomainEmailToken(r.Context(), in.Token, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"domain": d.Domain})
	return nil
}
