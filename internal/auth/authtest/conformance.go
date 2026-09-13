// Package authtest is a behavioral test suite for auth.Service over an
// auth.Store. The in-memory store runs it as a unit test; the PostgreSQL store
// runs it in the integration suite (build tag integration). Every scenario
// uses unique emails and tenant ids, so one database can serve all of them.
package authtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/tenant"
)

// Password is the password of every user created by the suite.
const Password = "correct horse battery staple"

var ctx = context.Background()

// Env is one scenario's service, store and controllable clock.
type Env struct {
	T     *testing.T
	Store auth.Store
	Svc   *auth.Service
	Meta  auth.ClientMeta
	now   time.Time
	sfx   string
}

// NewEnv creates an Env. The clock starts two days in the past (microsecond
// precision, like PostgreSQL) so rows stamped by the database with now() sort
// after rows stamped by the service.
func NewEnv(t *testing.T, st auth.Store, cfg auth.Config) *Env {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	e := &Env{T: t, Store: st, Meta: auth.ClientMeta{IP: "192.0.2.10", UserAgent: "authtest"},
		now: time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour), sfx: hex.EncodeToString(b)}
	e.Svc = auth.NewService(st, cfg, nil)
	e.Svc.SetClock(e.Now)
	return e
}

// Now is the scenario clock.
func (e *Env) Now() time.Time { return e.now }

// Advance moves the clock.
func (e *Env) Advance(d time.Duration) { e.now = e.now.Add(d) }

// Email returns a unique email address for name.
func (e *Env) Email(name string) string { return name + "-" + e.sfx + "@example.com" }

// Tenant returns a unique tenant id for name.
func (e *Env) Tenant(name string) string { return name + "-" + e.sfx }

// Session is a signed-in user.
type Session struct {
	Token string
	CSRF  string
	ID    string
	P     *auth.Principal
}

func (e *Env) must(err error) {
	e.T.Helper()
	if err != nil {
		e.T.Fatal(err)
	}
}

// Bootstrap creates organization name owned by e.Email("owner-"+name) and signs the owner in.
func (e *Env) Bootstrap(name string) (auth.Organization, *Session) {
	e.T.Helper()
	res, err := e.Svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: e.Tenant(name), OrgName: "Org " + name,
		OwnerEmail: e.Email("owner-" + name), OwnerPassword: Password})
	e.must(err)
	return res.Org, e.Login(e.Email("owner-"+name), Password)
}

// Login signs in and authenticates a GET request with the new session.
func (e *Env) Login(email, password string) *Session {
	e.T.Helper()
	res, err := e.Svc.Login(ctx, email, password, e.Meta)
	e.must(err)
	return e.session(res)
}

func (e *Env) session(res auth.LoginResult) *Session {
	e.T.Helper()
	s := &Session{Token: res.Token, CSRF: res.Session.CSRFToken, ID: res.Session.ID}
	p, err := e.Auth(http.MethodGet, s)
	e.must(err)
	s.P = p
	return s
}

// Refresh re-authenticates s (role changes).
func (e *Env) Refresh(s *Session) {
	e.T.Helper()
	p, err := e.Auth(http.MethodGet, s)
	e.must(err)
	s.P = p
}

// ReqOpt modifies a request.
type ReqOpt func(*http.Request)

// WithCSRF sets the CSRF header.
func WithCSRF(tok string) ReqOpt { return func(r *http.Request) { r.Header.Set(auth.HeaderCSRF, tok) } }

// WithOrg sets the organization header.
func WithOrg(id string) ReqOpt { return func(r *http.Request) { r.Header.Set(auth.HeaderOrg, id) } }

// WithBearer sets an Authorization bearer token.
func WithBearer(k string) ReqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+k) }
}

// Auth authenticates a request carrying s's cookie (s may be nil).
func (e *Env) Auth(method string, s *Session, opts ...ReqOpt) (*auth.Principal, error) {
	r := httptest.NewRequest(method, "/api/v1/test", nil)
	if s != nil {
		r.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: s.Token})
	}
	for _, o := range opts {
		o(r)
	}
	return e.Svc.Authenticate(r)
}

// AddUser invites name with role into owner's organization and accepts as a new user.
func (e *Env) AddUser(owner *Session, name string, role auth.Role) *Session {
	e.T.Helper()
	_, token, err := e.Svc.CreateInvitation(ctx, owner.P, e.Email(name), role, e.Meta)
	e.must(err)
	res, _, err := e.Svc.AcceptInvitation(ctx, token, Password, name, e.Meta)
	e.must(err)
	return e.session(res)
}

func expect(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: err = %v, want %v", what, err, target)
	}
}

func ok(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// Run runs every scenario. newStore returns the store for one scenario.
func Run(t *testing.T, newStore func(t *testing.T) auth.Store) {
	def := auth.Config{SessionTTL: 24 * time.Hour, SessionIdleTimeout: time.Hour, LoginMaxFailures: 3, LoginWindow: 15 * time.Minute, InvitationTTL: 72 * time.Hour}
	scenarios := []struct {
		name string
		cfg  func(c *auth.Config)
		fn   func(t *testing.T, e *Env)
	}{
		{"login_and_authenticate", nil, loginAndAuthenticate},
		{"login_rate_limit", nil, loginRateLimit},
		{"csrf", nil, csrf},
		{"session_expiry", nil, sessionExpiry},
		{"org_selection", nil, orgSelection},
		{"api_keys", nil, apiKeys},
		{"roles", nil, roles},
		{"last_owner", nil, lastOwner},
		{"invitations", nil, invitations},
		{"license_key_cache_revocation", nil, licenseKeyCache},
		{"bootstrap_idempotent", nil, bootstrapIdempotent},
		{"audit_log", nil, auditLog},
		{"change_password", nil, changePassword},
		{"sessions", nil, sessions},
		{"signup_disabled", nil, signupDisabled},
		{"signup", func(c *auth.Config) { c.SignupEnabled = true }, signup},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cfg := def
			if sc.cfg != nil {
				sc.cfg(&cfg)
			}
			sc.fn(t, NewEnv(t, newStore(t), cfg))
		})
	}
}

func loginAndAuthenticate(t *testing.T, e *Env) {
	org, owner := e.Bootstrap("acme")
	p := owner.P
	if p.Kind != auth.KindSession || p.TenantID != org.TenantID || p.OrgID != org.ID || p.Role != auth.RoleOwner || p.Email != e.Email("owner-acme") {
		t.Fatalf("principal %+v", p)
	}
	_, errWrong := e.Svc.Login(ctx, e.Email("owner-acme"), "wrong password here", e.Meta)
	expect(t, errWrong, auth.ErrUnauthenticated, "wrong password")
	_, errUnknown := e.Svc.Login(ctx, e.Email("nobody"), Password, e.Meta)
	expect(t, errUnknown, auth.ErrUnauthenticated, "unknown email")
	if errWrong.Error() != errUnknown.Error() {
		t.Errorf("unknown email distinguishable: %q vs %q", errWrong, errUnknown)
	}
	e.Login(strings.ToUpper(e.Email("owner-acme")), Password) // emails are case-insensitive
	_, err := e.Auth(http.MethodGet, nil)
	expect(t, err, auth.ErrUnauthenticated, "no cookie")
	_, err = e.Auth(http.MethodGet, &Session{Token: "forged"})
	expect(t, err, auth.ErrUnauthenticated, "forged cookie")
	_, err = e.Svc.Login(ctx, "", "", e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "empty credentials")
}

func loginRateLimit(t *testing.T, e *Env) {
	e.Bootstrap("rl")
	email := e.Email("owner-rl")
	for i := 0; i < 3; i++ {
		_, err := e.Svc.Login(ctx, email, "not the password", e.Meta)
		expect(t, err, auth.ErrUnauthenticated, "wrong password")
	}
	_, err := e.Svc.Login(ctx, email, Password, e.Meta)
	expect(t, err, auth.ErrResourceExhausted, "correct password after limit")
	other := e.Meta
	other.IP = "198.51.100.7"
	_, err = e.Svc.Login(ctx, email, Password, other)
	ok(t, err, "other IP")
	e.Advance(16 * time.Minute)
	e.Login(email, Password)
}

func csrf(t *testing.T, e *Env) {
	_, owner := e.Bootstrap("csrf")
	_, err := e.Auth(http.MethodPost, owner)
	expect(t, err, auth.ErrPermissionDenied, "POST without CSRF token")
	_, err = e.Auth(http.MethodDelete, owner, WithCSRF("wrong"))
	expect(t, err, auth.ErrPermissionDenied, "DELETE with wrong CSRF token")
	_, err = e.Auth(http.MethodPatch, owner, WithCSRF(owner.CSRF))
	ok(t, err, "PATCH with CSRF token")
	_, err = e.Auth(http.MethodGet, owner)
	ok(t, err, "GET without CSRF token")
	other := e.Login(e.Email("owner-csrf"), Password)
	_, err = e.Auth(http.MethodPost, owner, WithCSRF(other.CSRF))
	expect(t, err, auth.ErrPermissionDenied, "CSRF token of another session")
}

func sessionExpiry(t *testing.T, e *Env) {
	_, s := e.Bootstrap("exp")
	e.Advance(59 * time.Minute)
	_, err := e.Auth(http.MethodGet, s)
	ok(t, err, "within idle timeout")
	e.Advance(59 * time.Minute)
	_, err = e.Auth(http.MethodGet, s)
	ok(t, err, "activity extends idle timeout")
	e.Advance(61 * time.Minute)
	_, err = e.Auth(http.MethodGet, s)
	expect(t, err, auth.ErrUnauthenticated, "idle session")

	s2 := e.Login(e.Email("owner-exp"), Password)
	start := e.Now()
	for {
		e.Advance(50 * time.Minute)
		_, err := e.Auth(http.MethodGet, s2)
		if e.Now().Sub(start) < 24*time.Hour {
			ok(t, err, "active session")
			continue
		}
		expect(t, err, auth.ErrUnauthenticated, "absolute session expiry")
		break
	}

	s3 := e.Login(e.Email("owner-exp"), Password)
	ok(t, e.Svc.Logout(ctx, s3.P, e.Meta), "logout")
	_, err = e.Auth(http.MethodGet, s3)
	expect(t, err, auth.ErrUnauthenticated, "after logout")
}

func orgSelection(t *testing.T, e *Env) {
	orgA, owner := e.Bootstrap("a")
	e.Advance(time.Second)
	resB, err := e.Svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: e.Tenant("b"), OrgName: "Org b", OwnerEmail: e.Email("owner-a")})
	ok(t, err, "second org for the same owner")
	if !resB.CreatedOrg || resB.CreatedUser {
		t.Fatalf("bootstrap b: %+v", resB)
	}
	p, err := e.Auth(http.MethodGet, owner)
	ok(t, err, "default org")
	if p.OrgID != orgA.ID {
		t.Fatalf("default org = %s, want oldest membership %s", p.TenantID, orgA.TenantID)
	}
	p, err = e.Auth(http.MethodGet, owner, WithOrg(resB.Org.ID))
	ok(t, err, "select org b")
	if p.TenantID != resB.Org.TenantID || p.Role != auth.RoleOwner {
		t.Fatalf("selected %+v", p)
	}
	_, err = e.Auth(http.MethodGet, owner, WithOrg(uuid.NewString()))
	expect(t, err, auth.ErrPermissionDenied, "unknown org")
	_, err = e.Auth(http.MethodGet, owner, WithOrg("not-a-uuid"))
	expect(t, err, auth.ErrPermissionDenied, "malformed org id")
	_, stranger := e.Bootstrap("c")
	_, err = e.Auth(http.MethodGet, stranger, WithOrg(orgA.ID))
	expect(t, err, auth.ErrPermissionDenied, "org of another user")
	me, err := e.Svc.Me(ctx, owner.P)
	ok(t, err, "me")
	if len(me.Memberships) != 2 {
		t.Fatalf("memberships = %d", len(me.Memberships))
	}
}

func apiKeys(t *testing.T, e *Env) {
	org, owner := e.Bootstrap("keys")
	k, secret, err := e.Svc.CreateAPIKey(ctx, owner.P, "ci", nil, e.Meta)
	ok(t, err, "create API key")
	if !strings.HasPrefix(secret, auth.PrefixAPIKey) || len(secret) != 52 || !strings.HasPrefix(secret, k.Prefix) || k.Scope != "read" {
		t.Fatalf("key %q %+v", secret, k)
	}
	p, err := e.Auth(http.MethodGet, nil, WithBearer(secret))
	ok(t, err, "bearer auth")
	if p.Kind != auth.KindAPIKey || p.Role != auth.RoleViewer || p.TenantID != org.TenantID || p.UserID != "" {
		t.Fatalf("API key principal %+v", p)
	}
	pPost, err := e.Auth(http.MethodPost, nil, WithBearer(secret))
	ok(t, err, "bearer POST needs no CSRF token")
	_, _, err = e.Svc.CreateLicenseKey(ctx, pPost, "x", e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "API keys are read-only")
	_, _, err = e.Svc.CreateAPIKey(ctx, pPost, "x", nil, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "API key creating API keys")
	_, err = e.Auth(http.MethodGet, nil, WithBearer(secret), WithOrg(uuid.NewString()))
	expect(t, err, auth.ErrPermissionDenied, "org header mismatch")
	_, err = e.Auth(http.MethodGet, nil, WithBearer("ola_forged"))
	expect(t, err, auth.ErrUnauthenticated, "forged key")

	exp := e.Now().Add(time.Hour)
	_, expSecret, err := e.Svc.CreateAPIKey(ctx, owner.P, "temp", &exp, e.Meta)
	ok(t, err, "create expiring key")
	past := e.Now().Add(-time.Minute)
	_, _, err = e.Svc.CreateAPIKey(ctx, owner.P, "past", &past, e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "expiry in the past")
	e.Advance(2 * time.Hour)
	_, err = e.Auth(http.MethodGet, nil, WithBearer(expSecret))
	expect(t, err, auth.ErrUnauthenticated, "expired key")

	_, err = e.Svc.RevokeAPIKey(ctx, e.Refreshed(owner), k.ID, e.Meta)
	ok(t, err, "revoke")
	_, err = e.Auth(http.MethodGet, nil, WithBearer(secret))
	expect(t, err, auth.ErrUnauthenticated, "revoked key")
	list, err := e.Svc.ListAPIKeys(ctx, owner.P)
	ok(t, err, "list")
	if len(list) != 2 || list[0].CreatedByEmail != e.Email("owner-keys") {
		t.Fatalf("list %+v", list)
	}
	for _, x := range list {
		if x.ID == k.ID && (x.RevokedAt == nil || x.LastUsedAt == nil) {
			t.Errorf("revoked key row %+v", x)
		}
	}
}

// Refreshed re-authenticates s (after clock jumps) and returns its principal.
func (e *Env) Refreshed(s *Session) *auth.Principal {
	e.T.Helper()
	fresh := e.Login(e.Email(strings.TrimSuffix(strings.SplitN(s.P.Email, "@", 2)[0], "-"+e.sfx)), Password)
	*s = *fresh
	return s.P
}

func roles(t *testing.T, e *Env) {
	_, owner := e.Bootstrap("roles")
	admin := e.AddUser(owner, "admin", auth.RoleAdmin)
	member := e.AddUser(owner, "member", auth.RoleMember)
	viewer := e.AddUser(owner, "viewer", auth.RoleViewer)
	if admin.P.Role != auth.RoleAdmin || member.P.Role != auth.RoleMember || viewer.P.Role != auth.RoleViewer {
		t.Fatalf("roles %s %s %s", admin.P.Role, member.P.Role, viewer.P.Role)
	}

	_, _, err := e.Svc.CreateLicenseKey(ctx, viewer.P, "k", e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "viewer creates license key")
	_, _, err = e.Svc.CreateAPIKey(ctx, viewer.P, "k", nil, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "viewer creates API key")
	_, err = e.Svc.ListLicenseKeys(ctx, viewer.P)
	expect(t, err, auth.ErrPermissionDenied, "viewer lists license keys")
	_, err = e.Svc.ListMembers(ctx, viewer.P)
	ok(t, err, "viewer lists members")
	_, err = e.Svc.CurrentOrg(ctx, viewer.P)
	ok(t, err, "viewer reads org")
	_, _, err = e.Svc.CreateInvitation(ctx, viewer.P, e.Email("x"), auth.RoleViewer, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "viewer invites")
	_, err = e.Svc.RenameOrg(ctx, member.P, "new", e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "member renames org")

	memberKey, _, err := e.Svc.CreateAPIKey(ctx, member.P, "mine", nil, e.Meta)
	ok(t, err, "member creates API key")
	_, _, err = e.Svc.CreateLicenseKey(ctx, member.P, "k", e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "member creates license key")
	_, err = e.Svc.ListLicenseKeys(ctx, member.P)
	ok(t, err, "member lists license keys")
	err = e.Svc.UpdateMemberRole(ctx, member.P, viewer.P.UserID, auth.RoleMember, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "member changes roles")
	_, err = e.Svc.ListAuditEvents(ctx, member.P, 10)
	expect(t, err, auth.ErrPermissionDenied, "member reads audit log")

	adminKey, _, err := e.Svc.CreateAPIKey(ctx, admin.P, "admin", nil, e.Meta)
	ok(t, err, "admin creates API key")
	_, _, err = e.Svc.CreateLicenseKey(ctx, admin.P, "prod", e.Meta)
	ok(t, err, "admin creates license key")
	_, _, err = e.Svc.CreateInvitation(ctx, admin.P, e.Email("o2"), auth.RoleOwner, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "admin invites owner")
	err = e.Svc.UpdateMemberRole(ctx, admin.P, viewer.P.UserID, auth.RoleMember, e.Meta)
	ok(t, err, "admin promotes viewer")
	err = e.Svc.UpdateMemberRole(ctx, admin.P, viewer.P.UserID, auth.RoleOwner, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "admin grants owner")
	err = e.Svc.UpdateMemberRole(ctx, admin.P, owner.P.UserID, auth.RoleAdmin, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "admin demotes owner")
	err = e.Svc.RemoveMember(ctx, admin.P, owner.P.UserID, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "admin removes owner")
	err = e.Svc.UpdateMemberRole(ctx, admin.P, uuid.NewString(), auth.RoleMember, e.Meta)
	expect(t, err, auth.ErrNotFound, "unknown member")
	err = e.Svc.UpdateMemberRole(ctx, admin.P, viewer.P.UserID, auth.Role("root"), e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "unknown role")

	_, err = e.Svc.RevokeAPIKey(ctx, member.P, adminKey.ID, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "member revokes another user's key")
	_, err = e.Svc.RevokeAPIKey(ctx, member.P, memberKey.ID, e.Meta)
	ok(t, err, "member revokes own key")
	_, err = e.Svc.RevokeAPIKey(ctx, admin.P, adminKey.ID, e.Meta)
	ok(t, err, "admin revokes key")
	_, err = e.Svc.ListAuditEvents(ctx, admin.P, 10)
	ok(t, err, "admin reads audit log")

	// Keys and members of another organization are invisible.
	_, other := e.Bootstrap("other")
	_, err = e.Svc.RevokeAPIKey(ctx, other.P, adminKey.ID, e.Meta)
	expect(t, err, auth.ErrNotFound, "revoke key of another org")
	err = e.Svc.RemoveMember(ctx, other.P, member.P.UserID, e.Meta)
	expect(t, err, auth.ErrNotFound, "remove member of another org")

	// A member leaves on their own; afterwards they have no organization.
	ok(t, e.Svc.RemoveMember(ctx, member.P, member.P.UserID, e.Meta), "member leaves")
	e.Refresh(member)
	if member.P.HasOrg() {
		t.Fatalf("member still in org: %+v", member.P)
	}
}

func lastOwner(t *testing.T, e *Env) {
	_, owner := e.Bootstrap("lo")
	err := e.Svc.UpdateMemberRole(ctx, owner.P, owner.P.UserID, auth.RoleAdmin, e.Meta)
	expect(t, err, auth.ErrFailedPrecondition, "demote last owner")
	err = e.Svc.RemoveMember(ctx, owner.P, owner.P.UserID, e.Meta)
	expect(t, err, auth.ErrFailedPrecondition, "remove last owner")
	owner2 := e.AddUser(owner, "owner2", auth.RoleOwner)
	ok(t, e.Svc.UpdateMemberRole(ctx, owner.P, owner.P.UserID, auth.RoleAdmin, e.Meta), "demote with a second owner")
	e.Refresh(owner)
	if owner.P.Role != auth.RoleAdmin {
		t.Fatalf("role after demotion %s", owner.P.Role)
	}
	err = e.Svc.RemoveMember(ctx, owner2.P, owner2.P.UserID, e.Meta)
	expect(t, err, auth.ErrFailedPrecondition, "remaining owner leaves")
}

func invitations(t *testing.T, e *Env) {
	org, owner := e.Bootstrap("inv")
	inv, token, err := e.Svc.CreateInvitation(ctx, owner.P, strings.ToUpper(e.Email("new")), auth.RoleMember, e.Meta)
	ok(t, err, "invite")
	if inv.Email != e.Email("new") || !strings.HasPrefix(token, auth.PrefixInvitation) {
		t.Fatalf("invitation %+v %q", inv, token)
	}
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("new"), auth.RoleViewer, e.Meta)
	expect(t, err, auth.ErrAlreadyExists, "duplicate pending invitation")
	list, err := e.Svc.ListInvitations(ctx, owner.P)
	ok(t, err, "list invitations")
	if len(list) != 1 || list[0].InvitedByEmail != e.Email("owner-inv") {
		t.Fatalf("invitations %+v", list)
	}
	info, err := e.Svc.LookupInvitation(ctx, token)
	ok(t, err, "lookup")
	if info.UserExists || info.Org.ID != org.ID || info.Invitation.Role != auth.RoleMember {
		t.Fatalf("lookup %+v", info)
	}
	_, _, err = e.Svc.AcceptInvitation(ctx, token, "short", "", e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "weak password")
	res, _, err := e.Svc.AcceptInvitation(ctx, token, Password, "New User", e.Meta)
	ok(t, err, "accept")
	s := e.session(res)
	if s.P.OrgID != org.ID || s.P.Role != auth.RoleMember || s.P.Name != "New User" {
		t.Fatalf("invitee %+v", s.P)
	}
	_, _, err = e.Svc.AcceptInvitation(ctx, token, Password, "", e.Meta)
	expect(t, err, auth.ErrNotFound, "accept twice")
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("new"), auth.RoleViewer, e.Meta)
	expect(t, err, auth.ErrAlreadyExists, "invite existing member")

	inv2, token2, err := e.Svc.CreateInvitation(ctx, owner.P, e.Email("revoked"), auth.RoleViewer, e.Meta)
	ok(t, err, "invite 2")
	ok(t, e.Svc.RevokeInvitation(ctx, owner.P, inv2.ID, e.Meta), "revoke invitation")
	_, err = e.Svc.LookupInvitation(ctx, token2)
	expect(t, err, auth.ErrNotFound, "revoked invitation")

	_, token3, err := e.Svc.CreateInvitation(ctx, owner.P, e.Email("late"), auth.RoleViewer, e.Meta)
	ok(t, err, "invite 3")
	e.Advance(73 * time.Hour)
	owner = e.Login(e.Email("owner-inv"), Password)
	_, err = e.Svc.LookupInvitation(ctx, token3)
	expect(t, err, auth.ErrNotFound, "expired invitation")
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("late"), auth.RoleViewer, e.Meta)
	ok(t, err, "re-invite after expiry")

	// An existing user (owner of another org) proves their password.
	_, _ = e.Bootstrap("x")
	_, tokenX, err := e.Svc.CreateInvitation(ctx, owner.P, e.Email("owner-x"), auth.RoleViewer, e.Meta)
	ok(t, err, "invite existing user")
	info, err = e.Svc.LookupInvitation(ctx, tokenX)
	ok(t, err, "lookup existing")
	if !info.UserExists {
		t.Fatal("UserExists = false for an existing user")
	}
	_, _, err = e.Svc.AcceptInvitation(ctx, tokenX, "wrong password!!", "", e.Meta)
	expect(t, err, auth.ErrUnauthenticated, "existing user with wrong password")
	resX, _, err := e.Svc.AcceptInvitation(ctx, tokenX, Password, "", e.Meta)
	ok(t, err, "existing user accepts")
	me, err := e.Svc.Me(ctx, e.session(resX).P)
	ok(t, err, "me")
	if len(me.Memberships) != 2 {
		t.Fatalf("memberships after accept = %d", len(me.Memberships))
	}
}

func licenseKeyCache(t *testing.T, e *Env) {
	org, owner := e.Bootstrap("lk")
	k, secret, err := e.Svc.CreateLicenseKey(ctx, owner.P, "prod", e.Meta)
	ok(t, err, "create license key")
	if !strings.HasPrefix(secret, auth.PrefixLicenseKey) || k.Prefix != secret[:12] {
		t.Fatalf("key %q prefix %q", secret, k.Prefix)
	}
	cache := tenant.NewCached(e.Store, tenant.CacheOptions{TTL: time.Minute, NegativeTTL: 10 * time.Second, Now: e.Now})
	got, err := cache.Resolve(ctx, secret)
	if err != nil || got != org.TenantID {
		t.Fatalf("resolve = %q %v", got, err)
	}
	if _, err := cache.Resolve(ctx, secret+"x"); !errors.Is(err, tenant.ErrUnknownKey) {
		t.Fatalf("wrong key: %v", err)
	}
	_, err = e.Svc.RevokeLicenseKey(ctx, owner.P, k.ID, e.Meta)
	ok(t, err, "revoke")
	_, err = e.Svc.RevokeLicenseKey(ctx, owner.P, k.ID, e.Meta)
	ok(t, err, "revoke twice")
	if got, err := cache.Resolve(ctx, secret); err != nil || got != org.TenantID {
		t.Fatalf("revoked key within cache TTL = %q %v (documented: accepted until TTL)", got, err)
	}
	e.Advance(61 * time.Second)
	if _, err := cache.Resolve(ctx, secret); !errors.Is(err, tenant.ErrUnknownKey) {
		t.Fatalf("revoked key after TTL: %v", err)
	}
	ok(t, cache.FlushTouches(ctx), "flush last_used_at")
	owner = e.Login(e.Email("owner-lk"), Password)
	keys, err := e.Svc.ListLicenseKeys(ctx, owner.P)
	ok(t, err, "list")
	if len(keys) != 1 || keys[0].LastUsedAt == nil || keys[0].RevokedAt == nil || keys[0].CreatedByEmail != e.Email("owner-lk") {
		t.Fatalf("keys %+v", keys)
	}
	_, other := e.Bootstrap("lk2")
	_, err = e.Svc.RevokeLicenseKey(ctx, other.P, k.ID, e.Meta)
	expect(t, err, auth.ErrNotFound, "revoke key of another org")
	_, err = e.Svc.RevokeLicenseKey(ctx, other.P, "not-a-uuid", e.Meta)
	expect(t, err, auth.ErrNotFound, "malformed id")
}

func bootstrapIdempotent(t *testing.T, e *Env) {
	spec := auth.BootstrapSpec{TenantID: e.Tenant("boot"), OrgName: "Boot", OwnerEmail: e.Email("boot"), OwnerPassword: Password,
		LicenseKey: "dev-" + e.sfx + "-license-key", APIKey: "dev-" + e.sfx + "-api-key"}
	r1, err := e.Svc.Bootstrap(ctx, spec)
	ok(t, err, "bootstrap")
	if !r1.CreatedOrg || !r1.CreatedUser || !r1.AddedOwner || !r1.CreatedLicenseKey || !r1.CreatedAPIKey {
		t.Fatalf("first run %+v", r1)
	}
	r2, err := e.Svc.Bootstrap(ctx, spec)
	ok(t, err, "bootstrap again")
	if r2.CreatedOrg || r2.CreatedUser || r2.AddedOwner || r2.CreatedLicenseKey || r2.CreatedAPIKey || r2.Org.ID != r1.Org.ID {
		t.Fatalf("second run %+v", r2)
	}
	cache := tenant.NewCached(e.Store, tenant.CacheOptions{Now: e.Now})
	if got, err := cache.Resolve(ctx, spec.LicenseKey); err != nil || got != spec.TenantID {
		t.Fatalf("bootstrap license key = %q %v", got, err)
	}
	p, err := e.Auth(http.MethodGet, nil, WithBearer(spec.APIKey))
	ok(t, err, "bootstrap API key")
	if p.TenantID != spec.TenantID {
		t.Fatalf("API key tenant %s", p.TenantID)
	}
	other := spec
	other.TenantID = e.Tenant("boot2")
	if _, err := e.Svc.Bootstrap(ctx, other); err == nil {
		t.Fatal("license key of another organization accepted")
	}
	other.LicenseKey, other.APIKey, other.OwnerPassword = "", "", ""
	r3, err := e.Svc.Bootstrap(ctx, other)
	ok(t, err, "existing owner without password")
	if !r3.CreatedOrg || r3.CreatedUser {
		t.Fatalf("third run %+v", r3)
	}
	_, err = e.Svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: e.Tenant("boot3"), OwnerEmail: e.Email("nopw")})
	if err == nil {
		t.Fatal("new owner without password accepted")
	}
	if _, err := e.Svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: "Bad Tenant", OwnerEmail: e.Email("boot")}); err == nil {
		t.Fatal("invalid tenant id accepted")
	}
}

func auditLog(t *testing.T, e *Env) {
	_, owner := e.Bootstrap("audit")
	k, _, err := e.Svc.CreateLicenseKey(ctx, owner.P, "prod", e.Meta)
	ok(t, err, "create key")
	evs, err := e.Svc.ListAuditEvents(ctx, owner.P, 50)
	ok(t, err, "list audit")
	found := false
	for _, ev := range evs {
		if ev.Action == "license_key.create" && ev.TargetID == k.ID && ev.ActorEmail == e.Email("owner-audit") && ev.IP == e.Meta.IP {
			found = true
		}
		if strings.Contains(strings.ToLower(ev.Action), "password") {
			t.Errorf("unexpected event %+v", ev)
		}
	}
	if !found {
		t.Fatalf("license_key.create not audited: %+v", evs)
	}
}

func changePassword(t *testing.T, e *Env) {
	_, s1 := e.Bootstrap("pw")
	email := e.Email("owner-pw")
	s2 := e.Login(email, Password)
	expect(t, e.Svc.ChangePassword(ctx, s1.P, "wrong current pw", "a brand new password", e.Meta), auth.ErrPermissionDenied, "wrong current password")
	expect(t, e.Svc.ChangePassword(ctx, s1.P, Password, "short", e.Meta), auth.ErrInvalidArgument, "weak new password")
	ok(t, e.Svc.ChangePassword(ctx, s1.P, Password, "a brand new password", e.Meta), "change password")
	_, err := e.Auth(http.MethodGet, s2)
	expect(t, err, auth.ErrUnauthenticated, "other session after password change")
	_, err = e.Auth(http.MethodGet, s1)
	ok(t, err, "current session kept")
	_, err = e.Svc.Login(ctx, email, Password, e.Meta)
	expect(t, err, auth.ErrUnauthenticated, "old password")
	e.Login(email, "a brand new password")
}

func sessions(t *testing.T, e *Env) {
	_, s0 := e.Bootstrap("sess")
	s1 := e.Login(e.Email("owner-sess"), Password)
	list, err := e.Svc.ListSessions(ctx, s1.P)
	ok(t, err, "list sessions")
	if len(list) != 2 {
		t.Fatalf("sessions = %d", len(list))
	}
	ok(t, e.Svc.RevokeSession(ctx, s1.P, s0.ID, e.Meta), "revoke other session")
	_, err = e.Auth(http.MethodGet, s0)
	expect(t, err, auth.ErrUnauthenticated, "revoked session")
	list, _ = e.Svc.ListSessions(ctx, s1.P)
	if len(list) != 1 || list[0].ID != s1.ID || list[0].IP != e.Meta.IP || list[0].UserAgent != "authtest" {
		t.Fatalf("sessions after revoke %+v", list)
	}
	expect(t, e.Svc.RevokeSession(ctx, s1.P, uuid.NewString(), e.Meta), auth.ErrNotFound, "unknown session")
	_, stranger := e.Bootstrap("sess2")
	expect(t, e.Svc.RevokeSession(ctx, stranger.P, s1.ID, e.Meta), auth.ErrNotFound, "session of another user")
	key, secret, err := e.Svc.CreateAPIKey(ctx, s1.P, "k", nil, e.Meta)
	ok(t, err, "api key")
	_ = key
	pk, err := e.Auth(http.MethodGet, nil, WithBearer(secret))
	ok(t, err, "bearer")
	_, err = e.Svc.ListSessions(ctx, pk)
	expect(t, err, auth.ErrPermissionDenied, "API key lists sessions")
}

func signupDisabled(t *testing.T, e *Env) {
	_, _, err := e.Svc.Signup(ctx, auth.SignupInput{Email: e.Email("su"), Password: Password, OrgName: "SU"}, e.Meta)
	expect(t, err, auth.ErrPermissionDenied, "signup disabled")
}

func signup(t *testing.T, e *Env) {
	res, org, err := e.Svc.Signup(ctx, auth.SignupInput{Email: e.Email("su"), Password: Password, Name: "Su", OrgName: "SU Inc"}, e.Meta)
	ok(t, err, "signup")
	s := e.session(res)
	if s.P.OrgID != org.ID || s.P.Role != auth.RoleOwner || !auth.ValidTenantID(org.TenantID) || org.Name != "SU Inc" {
		t.Fatalf("signup principal %+v org %+v", s.P, org)
	}
	_, _, err = e.Svc.Signup(ctx, auth.SignupInput{Email: e.Email("su"), Password: Password, OrgName: "Again"}, e.Meta)
	expect(t, err, auth.ErrAlreadyExists, "duplicate email")
	_, _, err = e.Svc.Signup(ctx, auth.SignupInput{Email: e.Email("su2"), Password: "short", OrgName: "X"}, e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "weak password")
	_, _, err = e.Svc.Signup(ctx, auth.SignupInput{Email: "not an email", Password: Password, OrgName: "X"}, e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "invalid email")
	_, _, err = e.Svc.Signup(ctx, auth.SignupInput{Email: e.Email("su3"), Password: Password}, e.Meta)
	expect(t, err, auth.ErrInvalidArgument, "missing organization name")
}
