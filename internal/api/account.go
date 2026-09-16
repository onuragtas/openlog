package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// accountFunc is a management handler for an authenticated principal.
type accountFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

// publicFunc is an unauthenticated handler (sign-in, sign-up, invitations).
type publicFunc func(w http.ResponseWriter, r *http.Request) error

const maxJSONBody = 64 << 10

func (s *Server) accountRoutes(mux *http.ServeMux) {
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

	// Available in both auth modes.
	public("GET /api/v1/auth/config", s.authConfig)
	authed("GET /api/v1/auth/me", s.me)
	if s.accounts == nil {
		return
	}
	public("POST /api/v1/auth/login", s.login)
	public("POST /api/v1/auth/signup", s.signup)
	public("POST /api/v1/invitations/lookup", s.lookupInvitation)
	public("POST /api/v1/invitations/accept", s.acceptInvitation)
	public("POST /api/v1/auth/verify-email", s.verifyEmail)               // account_email.go
	authed("POST /api/v1/auth/verify-email/resend", s.resendVerification) // account_email.go
	authed("POST /api/v1/invitations/{id}/resend", s.resendInvitation)    // account_email.go
	authed("PATCH /api/v1/auth/me", s.updateMe)
	authed("POST /api/v1/auth/logout", s.logout)
	authed("POST /api/v1/auth/password", s.changePassword)
	authed("GET /api/v1/orgs/current", s.currentOrg)
	authed("PATCH /api/v1/orgs/current", s.renameOrg)
	authed("GET /api/v1/members", s.listMembers)
	authed("PATCH /api/v1/members/{user_id}", s.updateMember)
	authed("DELETE /api/v1/members/{user_id}", s.removeMember)
	authed("GET /api/v1/invitations", s.listInvitations)
	authed("POST /api/v1/invitations", s.createInvitation)
	authed("DELETE /api/v1/invitations/{id}", s.revokeInvitation)
	authed("GET /api/v1/license-keys", s.listLicenseKeys)
	authed("POST /api/v1/license-keys", s.createLicenseKey)
	authed("DELETE /api/v1/license-keys/{id}", s.revokeLicenseKey)
	authed("GET /api/v1/api-keys", s.listAPIKeys)
	authed("POST /api/v1/api-keys", s.createAPIKey)
	authed("DELETE /api/v1/api-keys/{id}", s.revokeAPIKey)
	authed("GET /api/v1/sessions", s.listSessions)
	authed("DELETE /api/v1/sessions/{id}", s.revokeSession)
	authed("GET /api/v1/audit-log", s.listAudit)
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func (s *Server) writeAccountError(w http.ResponseWriter, route string, err error) {
	ae := s.toAPIError(err)
	if ae.status >= 500 {
		s.log.Error("api request failed", "route", route, "err", err)
	}
	writeError(w, ae)
}

// decodeJSON reads a JSON request body. application/json is required: HTML
// forms cannot send it cross-site without a CORS preflight, which the API
// never grants (defense against login CSRF on unauthenticated endpoints).
func decodeJSON(r *http.Request, v any) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		return badRequest("Content-Type must be application/json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

// ---- response shapes ----

func optTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := formatTime(*t)
	return &v
}

type userJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// EmailVerified is false only for sign-ups that have not confirmed their address yet.
	EmailVerified bool `json:"email_verified"`
	// Language is the user's chosen language: "auto" (browser) or "en"/"tr" (D-095).
	Language string `json:"language"`
}

type orgJSON struct {
	ID        string  `json:"id"`
	TenantID  string  `json:"tenant_id"`
	Name      string  `json:"name"`
	Role      string  `json:"role,omitempty"`
	CreatedAt *string `json:"created_at,omitempty"`
	// Language is the default e-mail language ("" = none; GET/PATCH /orgs/current only, D-095).
	Language *string `json:"language,omitempty"`
}

func currentOrgJSON(org auth.Organization, p *auth.Principal) orgJSON {
	created, lang := formatTime(org.CreatedAt), org.Locale
	return orgJSON{ID: org.ID, TenantID: org.TenantID, Name: org.Name, Role: string(p.Role), CreatedAt: &created, Language: &lang}
}

// apiKeyRefJSON names the API key that authenticated the request and the role it
// acts with (GET /auth/me; D-133).
type apiKeyRefJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type meJSON struct {
	Auth          string         `json:"auth"`
	User          *userJSON      `json:"user"`
	APIKey        *apiKeyRefJSON `json:"api_key"`
	Organization  *orgJSON       `json:"organization"`
	Role          *string        `json:"role"`
	Organizations []orgJSON      `json:"organizations"`
	CSRFToken     *string        `json:"csrf_token"`
}

func meResponse(p *auth.Principal, ms []auth.Membership) meJSON {
	out := meJSON{Auth: string(p.Kind), Organizations: []orgJSON{}}
	if p.Kind == auth.KindAPIKey {
		out.APIKey = &apiKeyRefJSON{ID: p.APIKeyID, Name: p.APIKeyName, Role: string(p.Role)}
	}
	if p.Kind == auth.KindSession {
		out.User = &userJSON{ID: p.UserID, Email: p.Email, Name: p.Name, EmailVerified: p.EmailVerified, Language: auth.LanguageAuto}
		if p.Language != "" {
			out.User.Language = p.Language
		}
		tok := p.CSRFToken
		out.CSRFToken = &tok
	}
	if p.HasOrg() {
		out.Organization = &orgJSON{ID: p.OrgID, TenantID: p.TenantID, Name: p.OrgName}
		role := string(p.Role)
		out.Role = &role
	}
	for _, m := range ms {
		out.Organizations = append(out.Organizations, orgJSON{ID: m.Org.ID, TenantID: m.Org.TenantID, Name: m.Org.Name, Role: string(m.Role)})
	}
	return out
}

// ---- auth ----

func (s *Server) authConfig(w http.ResponseWriter, _ *http.Request) error {
	out := map[string]any{"mode": "static", "signup_enabled": false, "password_min_length": auth.MinPasswordLen,
		"email_enabled": false, "email_verification_required": false, "captcha": nil, "sso_enabled": false}
	if s.sso != nil { // sso.go: "Sign in with SSO" on the login page
		out["sso_enabled"] = s.sso.Available()
	}
	if s.accounts != nil {
		cfg := s.accounts.Config()
		out["mode"], out["signup_enabled"] = "postgres", cfg.SignupEnabled
		out["email_enabled"], out["email_verification_required"] = s.accounts.EmailEnabled(), cfg.RequireEmailVerification
		if cfg.SignupEnabled && cfg.Captcha != nil {
			out["captcha"] = map[string]string{"provider": cfg.Captcha.Provider(), "site_key": cfg.Captcha.SiteKey()}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var ms []auth.Membership
	if s.accounts != nil {
		res, err := s.accounts.Me(r.Context(), p)
		if err != nil {
			return err
		}
		ms = res.Memberships
	}
	writeJSON(w, http.StatusOK, meResponse(p, ms))
	return nil
}

// updateMe changes the signed-in user's preferences: {"language": "auto" | "en" | "tr"} (D-095).
func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Language *string `json:"language"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Language == nil {
		return &auth.Error{Code: auth.CodeInvalidArgument, Message: "language is required"}
	}
	if err := s.accounts.SetLanguage(r.Context(), p, *in.Language, s.accounts.Meta(r)); err != nil {
		return err
	}
	return s.me(w, r, p)
}

// startSession sets the session cookie and answers with the /auth/me shape
// for the new session (organization: the user's default one).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, res auth.LoginResult, status int) error {
	http.SetCookie(w, s.accounts.SessionCookie(res.Token, res.Session.ExpiresAt))
	p := &auth.Principal{Kind: auth.KindSession, UserID: res.User.ID, Email: res.User.Email, Name: res.User.Name,
		SessionID: res.Session.ID, CSRFToken: res.Session.CSRFToken, EmailVerified: res.User.EmailVerifiedAt != nil, Language: res.User.Preference()}
	me, err := s.accounts.Me(r.Context(), p)
	if err != nil {
		return err
	}
	if len(me.Memberships) > 0 {
		m := me.Memberships[0]
		p.OrgID, p.OrgName, p.TenantID, p.Role = m.Org.ID, m.Org.Name, m.Org.TenantID, m.Role
	}
	writeJSON(w, status, meResponse(p, me.Memberships))
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.accounts.Login(r.Context(), in.Email, in.Password, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	return s.startSession(w, r, res, http.StatusOK)
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email            string `json:"email"`
		Password         string `json:"password"`
		Name             string `json:"name"`
		OrganizationName string `json:"organization_name"`
		CaptchaToken     string `json:"captcha_token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, _, err := s.accounts.Signup(r.Context(), auth.SignupInput{Email: in.Email, Password: in.Password, Name: in.Name,
		OrgName: in.OrganizationName, CaptchaToken: in.CaptchaToken}, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	return s.startSession(w, r, res, http.StatusCreated)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.accounts.Logout(r.Context(), p, s.accounts.Meta(r)); err != nil {
		return err
	}
	http.SetCookie(w, s.accounts.ClearedCookie())
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.accounts.ChangePassword(r.Context(), p, in.CurrentPassword, in.NewPassword, s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- organization & members ----

func (s *Server) currentOrg(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	org, err := s.accounts.CurrentOrg(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, currentOrgJSON(org, p))
	return nil
}

// renameOrg changes the organization's name and/or default e-mail language: {"name"?, "language"?: "" | "en" | "tr"}.
func (s *Server) renameOrg(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Name     *string `json:"name"`
		Language *string `json:"language"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Name == nil && in.Language == nil {
		return &auth.Error{Code: auth.CodeInvalidArgument, Message: "name or language is required"}
	}
	var (
		org auth.Organization
		err error
	)
	if in.Name != nil {
		if org, err = s.accounts.RenameOrg(r.Context(), p, *in.Name, s.accounts.Meta(r)); err != nil {
			return err
		}
	}
	if in.Language != nil {
		if org, err = s.accounts.SetOrgLanguage(r.Context(), p, *in.Language, s.accounts.Meta(r)); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, currentOrgJSON(org, p))
	return nil
}

type memberJSON struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ms, err := s.accounts.ListMembers(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]memberJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberJSON{UserID: m.UserID, Email: m.Email, Name: m.Name, Role: string(m.Role), JoinedAt: formatTime(m.JoinedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
	return nil
}

func (s *Server) updateMember(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.accounts.UpdateMemberRole(r.Context(), p, r.PathValue("user_id"), auth.Role(in.Role), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.accounts.RemoveMember(r.Context(), p, r.PathValue("user_id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- invitations ----

type invitationJSON struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	Role           string  `json:"role"`
	InvitedByEmail string  `json:"invited_by_email"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at"`
	Expired        bool    `json:"expired"`
	LastSentAt     *string `json:"last_sent_at"`
	SendCount      int     `json:"send_count"`
}

func invitationResponse(i auth.Invitation, now time.Time) invitationJSON {
	return invitationJSON{ID: i.ID, Email: i.Email, Role: string(i.Role), InvitedByEmail: i.InvitedByEmail,
		CreatedAt: formatTime(i.CreatedAt), ExpiresAt: formatTime(i.ExpiresAt), Expired: i.Expired(now),
		LastSentAt: optTime(i.LastSentAt), SendCount: i.SendCount}
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	includeExpired := false
	if v := r.URL.Query().Get("include_expired"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return badRequest("include_expired must be true or false")
		}
		includeExpired = b
	}
	invs, err := s.accounts.ListInvitations(r.Context(), p, includeExpired)
	if err != nil {
		return err
	}
	now := s.now()
	out := make([]invitationJSON, 0, len(invs))
	for _, i := range invs {
		out = append(out, invitationResponse(i, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": out})
	return nil
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	inv, token, err := s.accounts.CreateInvitation(r.Context(), p, in.Email, auth.Role(in.Role), s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitationResponse(inv, s.now()), "token": token, "email_sent": inv.LastSentAt != nil})
	return nil
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.accounts.RevokeInvitation(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) lookupInvitation(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	info, err := s.accounts.LookupInvitation(r.Context(), in.Token)
	if err != nil {
		return err
	}
	out := map[string]any{
		"organization_name": info.Org.Name, "email": info.Invitation.Email, "role": info.Invitation.Role,
		"expires_at": formatTime(info.Invitation.ExpiresAt), "user_exists": info.UserExists, "sso": nil,
	}
	if s.sso != nil { // sso.go: claimed-domain redirection (D-089)
		red, err := s.sso.InvitationRedirect(r.Context(), info.Invitation)
		if err != nil {
			return err
		}
		if red != nil {
			out["sso"] = map[string]any{"required": true, "organization_name": red.OrganizationName, "connection_name": red.ConnectionName,
				"protocol": red.Protocol, "same_organization": red.SameOrganization}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, _, err := s.accounts.AcceptInvitation(r.Context(), in.Token, in.Password, in.Name, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	return s.startSession(w, r, res, http.StatusOK)
}

// ---- keys ----

type licenseKeyJSON struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Prefix         string  `json:"prefix"`
	Custom         bool    `json:"custom"`
	CreatedByEmail string  `json:"created_by_email"`
	CreatedAt      string  `json:"created_at"`
	LastUsedAt     *string `json:"last_used_at"`
	RevokedAt      *string `json:"revoked_at"`
}

func licenseKeyResponse(k auth.LicenseKey) licenseKeyJSON {
	return licenseKeyJSON{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Custom: k.Custom, CreatedByEmail: k.CreatedByEmail,
		CreatedAt: formatTime(k.CreatedAt), LastUsedAt: optTime(k.LastUsedAt), RevokedAt: optTime(k.RevokedAt)}
}

func (s *Server) listLicenseKeys(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ks, err := s.accounts.ListLicenseKeys(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]licenseKeyJSON, 0, len(ks))
	for _, k := range ks {
		out = append(out, licenseKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"license_keys": out})
	return nil
}

func (s *Server) createLicenseKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Name string  `json:"name"`
		Key  *string `json:"key"` // optional: import an existing key value
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	custom := ""
	if in.Key != nil {
		if custom = strings.TrimSpace(*in.Key); custom == "" {
			return badRequest("key must not be empty; omit it to generate a key")
		}
	}
	k, secret, err := s.accounts.CreateLicenseKey(r.Context(), p, in.Name, custom, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	k.CreatedByEmail = p.Email
	var key *string // null for imported keys: the caller already knows the value
	if secret != "" {
		key = &secret
	}
	writeJSON(w, http.StatusCreated, map[string]any{"license_key": licenseKeyResponse(k), "key": key})
	return nil
}

func (s *Server) revokeLicenseKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if _, err := s.accounts.RevokeLicenseKey(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiKeyJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// Role is what the key may do: viewer (read-only), member or admin (D-133).
	Role string `json:"role"`
	// Scope is derived from Role and kept for older clients: "read" or "write".
	Scope           string  `json:"scope"`
	CreatedByUserID string  `json:"created_by_user_id"`
	CreatedByEmail  string  `json:"created_by_email"`
	CreatedAt       string  `json:"created_at"`
	LastUsedAt      *string `json:"last_used_at"`
	ExpiresAt       *string `json:"expires_at"`
	RevokedAt       *string `json:"revoked_at"`
}

func apiKeyResponse(k auth.APIKey) apiKeyJSON {
	// Keys created before 0091_api_key_roles have no role and are read-only viewers.
	role := k.Role
	if !role.ValidKeyRole() {
		role = auth.RoleViewer
	}
	return apiKeyJSON{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Role: string(role), Scope: auth.KeyScope(role),
		CreatedByUserID: k.CreatedBy, CreatedByEmail: k.CreatedByEmail,
		CreatedAt: formatTime(k.CreatedAt), LastUsedAt: optTime(k.LastUsedAt), ExpiresAt: optTime(k.ExpiresAt), RevokedAt: optTime(k.RevokedAt)}
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ks, err := s.accounts.ListAPIKeys(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]apiKeyJSON, 0, len(ks))
	for _, k := range ks {
		out = append(out, apiKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
	return nil
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Name string `json:"name"`
		// Role is optional and defaults to viewer, so a client that does not know about
		// roles keeps creating read-only keys.
		Role      string  `json:"role"`
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
	k, secret, err := s.accounts.CreateAPIKey(r.Context(), p, in.Name, auth.Role(in.Role), expires, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	k.CreatedByEmail = p.Email
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": apiKeyResponse(k), "key": secret})
	return nil
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if _, err := s.accounts.RevokeAPIKey(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- sessions & audit ----

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ss, err := s.accounts.ListSessions(r.Context(), p)
	if err != nil {
		return err
	}
	type sessionJSON struct {
		ID         string `json:"id"`
		CreatedAt  string `json:"created_at"`
		LastSeenAt string `json:"last_seen_at"`
		ExpiresAt  string `json:"expires_at"`
		IP         string `json:"ip"`
		UserAgent  string `json:"user_agent"`
		Current    bool   `json:"current"`
	}
	out := make([]sessionJSON, 0, len(ss))
	for _, x := range ss {
		out = append(out, sessionJSON{ID: x.ID, CreatedAt: formatTime(x.CreatedAt), LastSeenAt: formatTime(x.LastSeenAt),
			ExpiresAt: formatTime(x.ExpiresAt), IP: x.IP, UserAgent: x.UserAgent, Current: x.ID == p.SessionID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	return nil
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id := r.PathValue("id")
	if err := s.accounts.RevokeSession(r.Context(), p, id, s.accounts.Meta(r)); err != nil {
		return err
	}
	if id == p.SessionID {
		http.SetCookie(w, s.accounts.ClearedCookie())
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	f, err := auditFilter(r) // audit.go
	if err != nil {
		return err
	}
	evs, err := s.accounts.ListAuditEvents(r.Context(), p, f)
	if err != nil {
		return err
	}
	// actorAPIKeyJSON names the API key that made the change; null when a user did (D-133).
	type actorAPIKeyJSON struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type eventJSON struct {
		ID          int64            `json:"id"`
		ActorEmail  string           `json:"actor_email"`
		ActorAPIKey *actorAPIKeyJSON `json:"actor_api_key"`
		Action      string           `json:"action"`
		TargetType  string           `json:"target_type"`
		TargetID    string           `json:"target_id"`
		Details     map[string]any   `json:"details"`
		IP          string           `json:"ip"`
		CreatedAt   string           `json:"created_at"`
	}
	out := make([]eventJSON, 0, len(evs))
	for _, e := range evs {
		d := e.Details
		if d == nil {
			d = map[string]any{}
		}
		ev := eventJSON{ID: e.ID, ActorEmail: e.ActorEmail, Action: e.Action, TargetType: e.TargetType,
			TargetID: e.TargetID, Details: d, IP: e.IP, CreatedAt: formatTime(e.CreatedAt)}
		if e.ActorAPIKeyID != "" || e.ActorAPIKeyName != "" {
			ev.ActorAPIKey = &actorAPIKeyJSON{ID: e.ActorAPIKeyID, Name: e.ActorAPIKeyName}
		}
		out = append(out, ev)
	}
	var next *string
	if len(evs) == f.Limit {
		c := encodeAuditCursor(evs[len(evs)-1])
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "next_cursor": next})
	return nil
}
