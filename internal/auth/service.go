package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultCookieName is the session cookie name.
const DefaultCookieName = "openlog_session"

// touchEvery bounds how often last_seen_at / last_used_at are written.
const touchEvery = time.Minute

// Config configures the Service (docs/contracts/config.md).
type Config struct {
	SessionTTL         time.Duration // absolute session lifetime
	SessionIdleTimeout time.Duration // 0 disables the idle timeout
	CookieName         string
	CookieSecure       bool
	CookieDomain       string
	SignupEnabled      bool
	LoginMaxFailures   int           // failed attempts per (email, IP) within LoginWindow
	LoginWindow        time.Duration //
	InvitationTTL      time.Duration
	TrustedProxies     []netip.Prefix
}

func (c *Config) defaults() {
	if c.SessionTTL <= 0 {
		c.SessionTTL = 7 * 24 * time.Hour
	}
	if c.CookieName == "" {
		c.CookieName = DefaultCookieName
	}
	if c.LoginMaxFailures <= 0 {
		c.LoginMaxFailures = 10
	}
	if c.LoginWindow <= 0 {
		c.LoginWindow = 15 * time.Minute
	}
	if c.InvitationTTL <= 0 {
		c.InvitationTTL = 7 * 24 * time.Hour
	}
}

// Service implements authentication and the management operations. It holds
// no per-user state: sessions, rate limits and keys live in the Store, so any
// number of API replicas can serve any request.
type Service struct {
	store Store
	cfg   Config
	log   *slog.Logger
	now   func() time.Time
}

// NewService creates a Service.
func NewService(store Store, cfg Config, log *slog.Logger) *Service {
	cfg.defaults()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, cfg: cfg, log: log, now: time.Now}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// ClientMeta describes the client of a request for sessions, rate limiting and the audit log.
type ClientMeta struct {
	IP        string
	UserAgent string
}

// Meta extracts ClientMeta from r, honouring OPENLOG_API_TRUSTED_PROXIES.
func (s *Service) Meta(r *http.Request) ClientMeta {
	return ClientMeta{IP: ClientIP(r, s.cfg.TrustedProxies), UserAgent: truncate(r.UserAgent(), 256)}
}

// fail converts store errors: *Error values pass through, anything else is
// logged and reported as unavailable.
func (s *Service) fail(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	s.log.Error("auth store error", "err", err)
	return &Error{Code: CodeUnavailable, Message: "authentication backend unavailable"}
}

func unauthenticated(msg string) error { return &Error{Code: CodeUnauthenticated, Message: msg} }

func denied(msg string) error { return &Error{Code: CodePermissionDenied, Message: msg} }

func invalid(format string, args ...any) error { return newError(CodeInvalidArgument, format, args...) }

// ---- request authentication ----

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func bearerToken(r *http.Request) (string, bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:]), true
	}
	return "", false
}

// Authenticate implements Authenticator: "Authorization: Bearer <API key>" or
// the session cookie. Cookie-authenticated requests with an unsafe method
// must carry the session's CSRF token in X-CSRF-Token. The organization is
// taken from X-Openlog-Org-Id, defaulting to the user's oldest membership.
func (s *Service) Authenticate(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	now := s.now()
	if key, ok := bearerToken(r); ok {
		return s.authenticateAPIKey(ctx, r, key, now)
	}
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return nil, unauthenticated("missing credentials")
	}
	sess, user, err := s.store.GetSessionByTokenHash(ctx, HashSecret(c.Value))
	if errors.Is(err, ErrNotFound) {
		return nil, unauthenticated("session expired or revoked")
	}
	if err != nil {
		return nil, s.fail(err)
	}
	if !sess.Active(now, s.cfg.SessionIdleTimeout) || user.DisabledAt != nil {
		return nil, unauthenticated("session expired or revoked")
	}
	if unsafeMethod(r.Method) && subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderCSRF)), []byte(sess.CSRFToken)) != 1 {
		return nil, denied("missing or invalid CSRF token")
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if err := s.store.TouchSession(ctx, sess.ID, now); err != nil {
			s.log.Warn("cannot update session last_seen_at", "err", err)
		}
	}
	p := &Principal{Kind: KindSession, UserID: user.ID, Email: user.Email, Name: user.Name, SessionID: sess.ID, CSRFToken: sess.CSRFToken}
	if err := s.selectOrg(ctx, p, strings.TrimSpace(r.Header.Get(HeaderOrg))); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) selectOrg(ctx context.Context, p *Principal, orgID string) error {
	var m Membership
	if orgID != "" {
		var err error
		m, err = s.store.GetMembership(ctx, orgID, p.UserID)
		if errors.Is(err, ErrNotFound) {
			return denied("you are not a member of this organization")
		}
		if err != nil {
			return s.fail(err)
		}
	} else {
		ms, err := s.store.ListMemberships(ctx, p.UserID)
		if err != nil {
			return s.fail(err)
		}
		if len(ms) == 0 {
			return nil
		}
		m = ms[0]
	}
	p.OrgID, p.OrgName, p.TenantID, p.Role = m.Org.ID, m.Org.Name, m.Org.TenantID, m.Role
	return nil
}

func (s *Service) authenticateAPIKey(ctx context.Context, r *http.Request, key string, now time.Time) (*Principal, error) {
	if key == "" {
		return nil, unauthenticated("missing credentials")
	}
	k, org, err := s.store.LookupAPIKey(ctx, HashSecret(key))
	if errors.Is(err, ErrNotFound) {
		return nil, unauthenticated("invalid API key")
	}
	if err != nil {
		return nil, s.fail(err)
	}
	if !k.Usable(now) {
		return nil, unauthenticated("API key revoked or expired")
	}
	if h := strings.TrimSpace(r.Header.Get(HeaderOrg)); h != "" && h != org.ID {
		return nil, denied("API key belongs to a different organization")
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= touchEvery {
		if err := s.store.TouchAPIKey(ctx, k.ID, now); err != nil {
			s.log.Warn("cannot update API key last_used_at", "err", err)
		}
	}
	// API keys are read-only: they act as viewers regardless of the creator's role.
	return &Principal{Kind: KindAPIKey, APIKeyID: k.ID, OrgID: org.ID, OrgName: org.Name, TenantID: org.TenantID, Role: RoleViewer}, nil
}

// ---- cookies ----

// SessionCookie returns the Set-Cookie value for a new session: HttpOnly,
// SameSite=Strict, Path=/api, Secure unless OPENLOG_COOKIE_SECURE=false.
func (s *Service) SessionCookie(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: s.cfg.CookieName, Value: token, Path: "/api", Domain: s.cfg.CookieDomain,
		Expires: expires.UTC(), MaxAge: max(1, int(expires.Sub(s.now()).Seconds())),
		Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
}

// ClearedCookie deletes the session cookie.
func (s *Service) ClearedCookie() *http.Cookie {
	return &http.Cookie{Name: s.cfg.CookieName, Value: "", Path: "/api", Domain: s.cfg.CookieDomain,
		MaxAge: -1, Expires: time.Unix(0, 0), Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

// ---- sign-in ----

// LoginResult is a new session. Token is the cookie value (shown only here).
type LoginResult struct {
	Token   string
	Session Session
	User    User
}

// NormalizeEmail lower-cases and trims an email address.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func validEmail(email string) bool {
	if len(email) < 3 || len(email) > 320 {
		return false
	}
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && a.Name == ""
}

func loginKey(email, ip string) []byte {
	h := sha256.Sum256([]byte(email + "|" + ip))
	return h[:]
}

func (s *Service) checkRate(ctx context.Context, key []byte, now time.Time) error {
	n, err := s.store.CountLoginFailures(ctx, key, now.Add(-s.cfg.LoginWindow))
	if err != nil {
		return s.fail(err)
	}
	if n >= s.cfg.LoginMaxFailures {
		return &Error{Code: CodeResourceExhausted, Message: "too many failed sign-in attempts; try again later"}
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, key []byte, now time.Time) {
	if err := s.store.AddLoginFailure(ctx, key, now); err != nil {
		s.log.Warn("cannot record failed login", "err", err)
	}
}

var errBadCredentials = &Error{Code: CodeUnauthenticated, Message: "invalid email or password"}

// Login verifies email and password and creates a session. Failed attempts
// are limited per (email, client IP) across all API replicas.
func (s *Service) Login(ctx context.Context, email, password string, meta ClientMeta) (LoginResult, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return LoginResult{}, invalid("email and password are required")
	}
	now := s.now()
	key := loginKey(email, meta.IP)
	if err := s.checkRate(ctx, key, now); err != nil {
		return LoginResult{}, err
	}
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return LoginResult{}, s.fail(err)
	}
	ok := false
	if err == nil && u.PasswordHash != "" {
		ok, err = VerifyPassword(u.PasswordHash, password)
		if err != nil {
			s.log.Error("stored password hash is malformed", "user_id", u.ID, "err", err)
		}
	} else {
		burnPasswordCheck(password)
	}
	if !ok || u.DisabledAt != nil {
		s.recordFailure(ctx, key, now)
		return LoginResult{}, errBadCredentials
	}
	if err := s.store.ClearLoginFailures(ctx, key); err != nil {
		s.log.Warn("cannot clear failed logins", "err", err)
	}
	res, err := s.startSession(ctx, u, meta, now)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.store.SetUserLastLogin(ctx, u.ID, now); err != nil {
		s.log.Warn("cannot update last_login_at", "err", err)
	}
	s.audit(ctx, "", u.ID, u.Email, meta, "user.login", "session", res.Session.ID, nil)
	return res, nil
}

func (s *Service) startSession(ctx context.Context, u User, meta ClientMeta, now time.Time) (LoginResult, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrf, err := newOpaqueToken()
	if err != nil {
		return LoginResult{}, err
	}
	sess := Session{
		UserID: u.ID, TokenHash: HashSecret(token), CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.cfg.SessionTTL),
		IP: meta.IP, UserAgent: truncate(meta.UserAgent, 256),
	}
	if err := s.store.CreateSession(ctx, &sess); err != nil {
		return LoginResult{}, s.fail(err)
	}
	u.PasswordHash = ""
	return LoginResult{Token: token, Session: sess, User: u}, nil
}

// Logout revokes the caller's session.
func (s *Service) Logout(ctx context.Context, p *Principal, meta ClientMeta) error {
	if err := requireSession(p); err != nil {
		return err
	}
	if err := s.store.RevokeSession(ctx, p.UserID, p.SessionID, s.now()); err != nil && !errors.Is(err, ErrNotFound) {
		return s.fail(err)
	}
	s.audit(ctx, "", p.UserID, p.Email, meta, "user.logout", "session", p.SessionID, nil)
	return nil
}

// SignupInput is a self-service sign-up (OPENLOG_SIGNUP_ENABLED).
type SignupInput struct {
	Email    string
	Password string
	Name     string
	OrgName  string
}

// Signup creates a user, a new organization owned by them and a session.
func (s *Service) Signup(ctx context.Context, in SignupInput, meta ClientMeta) (LoginResult, Organization, error) {
	if !s.cfg.SignupEnabled {
		return LoginResult{}, Organization{}, denied("sign-up is disabled on this server")
	}
	email := NormalizeEmail(in.Email)
	if !validEmail(email) {
		return LoginResult{}, Organization{}, invalid("a valid email address is required")
	}
	if err := ValidatePassword(in.Password); err != nil {
		return LoginResult{}, Organization{}, err
	}
	name, err := cleanName(in.Name, "name", false)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	orgName, err := cleanName(in.OrgName, "organization name", true)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	tenantID, err := NewTenantID()
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	now := s.now()
	org := Organization{TenantID: tenantID, Name: orgName, CreatedAt: now}
	u := User{Email: email, Name: name, PasswordHash: hash, CreatedAt: now}
	if err := s.store.CreateOrganization(ctx, &org, &u); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return LoginResult{}, Organization{}, &Error{Code: CodeAlreadyExists, Message: "an account with this email already exists"}
		}
		return LoginResult{}, Organization{}, s.fail(err)
	}
	res, err := s.startSession(ctx, u, meta, now)
	if err != nil {
		return LoginResult{}, Organization{}, err
	}
	s.audit(ctx, org.ID, u.ID, u.Email, meta, "org.create", "organization", org.ID, map[string]any{"name": org.Name, "via": "signup"})
	return res, org, nil
}

// ChangePassword changes the caller's password and revokes their other sessions.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string, meta ClientMeta) error {
	if err := requireSession(p); err != nil {
		return err
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	now := s.now()
	key := loginKey(p.Email, meta.IP)
	if err := s.checkRate(ctx, key, now); err != nil {
		return err
	}
	u, err := s.store.GetUser(ctx, p.UserID)
	if err != nil {
		return s.fail(err)
	}
	ok := false
	if u.PasswordHash != "" {
		ok, _ = VerifyPassword(u.PasswordHash, current)
	}
	if !ok {
		s.recordFailure(ctx, key, now)
		return denied("current password is incorrect")
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.store.SetUserPassword(ctx, u.ID, hash); err != nil {
		return s.fail(err)
	}
	if err := s.store.RevokeUserSessions(ctx, u.ID, p.SessionID, now); err != nil {
		return s.fail(err)
	}
	s.audit(ctx, "", u.ID, u.Email, meta, "user.password_change", "user", u.ID, nil)
	return nil
}

// MeResult describes the caller.
type MeResult struct {
	Principal   *Principal
	Memberships []Membership // session principals only
}

// Me returns the caller and their organizations.
func (s *Service) Me(ctx context.Context, p *Principal) (MeResult, error) {
	res := MeResult{Principal: p}
	if p.Kind == KindSession {
		ms, err := s.store.ListMemberships(ctx, p.UserID)
		if err != nil {
			return res, s.fail(err)
		}
		res.Memberships = ms
	}
	return res, nil
}

// ---- helpers ----

func (s *Service) audit(ctx context.Context, orgID, actorID, actorEmail string, meta ClientMeta, action, targetType, targetID string, details map[string]any) {
	e := &AuditEvent{OrgID: orgID, ActorUserID: actorID, ActorEmail: actorEmail, Action: action,
		TargetType: targetType, TargetID: targetID, Details: details, IP: meta.IP, CreatedAt: s.now()}
	if err := s.store.AddAuditEvent(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("cannot write audit log", "action", action, "org_id", orgID, "err", err)
	}
}

func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for n > 0 && !utf8.RuneStart(v[n]) {
		n--
	}
	return v[:n]
}

// cleanName trims a display name and rejects control characters.
func cleanName(v, field string, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" && required {
		return "", invalid("%s is required", field)
	}
	if utf8.RuneCountInString(v) > 200 || !utf8.ValidString(v) {
		return "", invalid("%s must be at most 200 characters", field)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", invalid("%s must not contain control characters", field)
		}
	}
	return v, nil
}

func requireSession(p *Principal) error {
	if p == nil || p.Kind != KindSession {
		return denied("this operation requires a signed-in user; API keys are read-only")
	}
	return nil
}

func requireOrg(p *Principal, a Action) error {
	if !p.HasOrg() {
		return denied("you are not a member of any organization")
	}
	if !p.Role.Can(a) {
		return denied("your role (" + string(p.Role) + ") does not allow this operation")
	}
	return nil
}
