package memstore_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/tenant"
)

var bg = context.Background()

// recMailer records e-mails; fail makes Send return an error.
type recMailer struct {
	mu   sync.Mutex
	sent []auth.Mail
	fail error
}

func (m *recMailer) Send(_ context.Context, mail auth.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, mail)
	return nil
}

func (m *recMailer) last(t *testing.T) auth.Mail {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no e-mail sent")
	}
	return m.sent[len(m.sent)-1]
}

func (m *recMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// tokenFrom extracts "#token=<value>" from an e-mail body.
func tokenFrom(t *testing.T, m auth.Mail) string {
	t.Helper()
	_, rest, ok := strings.Cut(m.Text, "#token=")
	if !ok {
		t.Fatalf("no link in %q", m.Text)
	}
	return strings.Fields(rest)[0]
}

type fakeCaptcha struct{ err error }

func (fakeCaptcha) Provider() string { return "turnstile" }
func (fakeCaptcha) SiteKey() string  { return "site" }
func (c fakeCaptcha) Verify(_ context.Context, token, _ string) (bool, error) {
	return token == "pass", c.err
}

func saasEnv(t *testing.T, mod func(*auth.Config)) (*authtest.Env, *memstore.Store, *recMailer) {
	st := memstore.New()
	m := &recMailer{}
	cfg := auth.Config{SignupEnabled: true, Mailer: m, PublicURL: "https://ol.example/", RequireEmailVerification: true,
		InvitationTTL: 72 * time.Hour, LoginMaxFailures: 5}
	if mod != nil {
		mod(&cfg)
	}
	return authtest.NewEnv(t, st, cfg), st, m
}

func TestSignupVerificationGating(t *testing.T) {
	e, _, m := saasEnv(t, nil)
	res, _, err := e.Svc.Signup(bg, auth.SignupInput{Email: e.Email("sv"), Password: authtest.Password, OrgName: "SV"}, e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	s := &authtest.Session{Token: res.Token, CSRF: res.Session.CSRFToken}
	p, err := e.Auth(http.MethodGet, s)
	if err != nil || p.EmailVerified || p.Role != auth.RoleOwner {
		t.Fatalf("new sign-up principal %+v %v", p, err)
	}
	mail := m.last(t)
	if mail.To != e.Email("sv") || !strings.Contains(mail.Text, "https://ol.example/verify-email#token=olv_") || !strings.Contains(mail.HTML, "verify-email#token=olv_") {
		t.Fatalf("verification mail %+v", mail)
	}

	// Unverified: no license keys, API keys or invitations (they could send data or e-mail to anyone).
	_, _, err = e.Svc.CreateLicenseKey(bg, p, "k", "", e.Meta)
	if !errors.Is(err, auth.ErrPermissionDenied) || !strings.Contains(err.Error(), "confirm your e-mail") {
		t.Fatalf("unverified creates license key: %v", err)
	}
	if _, _, err = e.Svc.CreateAPIKey(bg, p, "k", nil, e.Meta); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("unverified creates API key: %v", err)
	}
	if _, _, err = e.Svc.CreateInvitation(bg, p, e.Email("friend"), auth.RoleViewer, e.Meta); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("unverified invites: %v", err)
	}
	// Reads still work.
	if _, err := e.Svc.ListMembers(bg, p); err != nil {
		t.Fatal(err)
	}

	// Resend: new token, rate limited per user.
	if err := e.Svc.ResendVerification(bg, p, e.Meta); err != nil {
		t.Fatal(err)
	}
	token := tokenFrom(t, m.last(t))
	for i := 0; i < 4; i++ {
		_ = e.Svc.ResendVerification(bg, p, e.Meta)
	}
	if err := e.Svc.ResendVerification(bg, p, e.Meta); !errors.Is(err, auth.ErrResourceExhausted) {
		t.Fatalf("resend limit: %v", err)
	}

	if err := e.Svc.VerifyEmail(bg, "olv_bogus", e.Meta); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("bogus token: %v", err)
	}
	if err := e.Svc.VerifyEmail(bg, token, e.Meta); err != nil {
		t.Fatal(err)
	}
	if err := e.Svc.VerifyEmail(bg, token, e.Meta); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("token reused: %v", err)
	}
	p, _ = e.Auth(http.MethodGet, s)
	if !p.EmailVerified {
		t.Fatal("not verified after confirming")
	}
	if _, _, err := e.Svc.CreateLicenseKey(bg, p, "k", "", e.Meta); err != nil {
		t.Fatalf("verified creates license key: %v", err)
	}
	if err := e.Svc.ResendVerification(bg, p, e.Meta); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("resend when verified: %v", err)
	}

	// Expired verification links do not work.
	res2, _, err := e.Svc.Signup(bg, auth.SignupInput{Email: e.Email("late"), Password: authtest.Password, OrgName: "L"}, e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	late := tokenFrom(t, m.last(t))
	e.Advance(49 * time.Hour)
	if err := e.Svc.VerifyEmail(bg, late, e.Meta); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	_ = res2
}

func TestSignupWithoutVerificationAndMailFailure(t *testing.T) {
	e, _, m := saasEnv(t, func(c *auth.Config) { c.RequireEmailVerification = false })
	res, _, err := e.Svc.Signup(bg, auth.SignupInput{Email: e.Email("nv"), Password: authtest.Password, OrgName: "NV"}, e.Meta)
	if err != nil || res.User.EmailVerifiedAt == nil || m.count() != 0 {
		t.Fatalf("signup without verification: %v verified=%v mails=%d", err, res.User.EmailVerifiedAt, m.count())
	}

	e2, _, m2 := saasEnv(t, nil)
	m2.fail = errors.New("smtp down")
	if _, _, err := e2.Svc.Signup(bg, auth.SignupInput{Email: e2.Email("down"), Password: authtest.Password, OrgName: "D"}, e2.Meta); err != nil {
		t.Fatalf("signup must succeed when the verification e-mail fails: %v", err)
	}
}

func TestSignupAbuseProtection(t *testing.T) {
	blocked, _ := auth.ParseDomainBlocklist([]string{"mailinator.com"}, strings.NewReader("# disposable\ntrash.example # comment\n"))
	e, _, _ := saasEnv(t, func(c *auth.Config) {
		c.SignupMaxPerIP, c.SignupMaxPerEmail, c.SignupRateWindow = 4, 2, time.Hour
		c.EmailDomainBlocked = blocked.Blocked
		c.Captcha = fakeCaptcha{}
	})
	in := func(email, captcha string) auth.SignupInput {
		return auth.SignupInput{Email: email, Password: authtest.Password, OrgName: "X", CaptchaToken: captcha}
	}
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c1"), ""), e.Meta); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("missing captcha: %v", err)
	}
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c1"), "fail"), e.Meta); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("rejected captcha: %v", err)
	}
	// Two attempts for c1 used the per-e-mail budget.
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c1"), "pass"), e.Meta); !errors.Is(err, auth.ErrResourceExhausted) {
		t.Fatalf("per-email limit: %v", err)
	}
	if _, _, err := e.Svc.Signup(bg, in("x@sub.mailinator.com", "pass"), e.Meta); !errors.Is(err, auth.ErrInvalidArgument) || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("blocked domain: %v", err)
	}
	// 4 attempts from this IP so far: the next one is refused even for a fresh address.
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c2"), "pass"), e.Meta); !errors.Is(err, auth.ErrResourceExhausted) {
		t.Fatalf("per-IP limit: %v", err)
	}
	other := e.Meta
	other.IP = "198.51.100.7"
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c2"), "pass"), other); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	e.Advance(61 * time.Minute)
	if _, _, err := e.Svc.Signup(bg, in(e.Email("c3"), "pass"), e.Meta); err != nil {
		t.Fatalf("after the window: %v", err)
	}

	unavailable, _, _ := saasEnv(t, func(c *auth.Config) { c.Captcha = fakeCaptcha{err: errors.New("timeout")} })
	if _, _, err := unavailable.Svc.Signup(bg, in(unavailable.Email("u"), "pass"), unavailable.Meta); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("captcha provider down: %v", err)
	}
	if _, _, err := e.Svc.Signup(bg, auth.SignupInput{Email: e.Email("pw"), Password: e.Email("pw"), OrgName: "X", CaptchaToken: "pass"}, other); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("password equal to e-mail: %v", err)
	}
}

func TestInvitationEmailAndResend(t *testing.T) {
	e, st, m := saasEnv(t, nil)
	_, owner := e.Bootstrap("inv")
	inv, token, err := e.Svc.CreateInvitation(bg, owner.P, e.Email("new"), auth.RoleMember, e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	mail := m.last(t)
	if inv.LastSentAt == nil || mail.To != e.Email("new") || tokenFrom(t, mail) != token || !strings.Contains(mail.Subject, "Org inv") ||
		!strings.Contains(mail.Text, "https://ol.example/invite#token=") {
		t.Fatalf("invitation mail %+v inv %+v", mail, inv)
	}

	// Resend after expiry: new token, new expiry, old link dead.
	e.Advance(73 * time.Hour)
	owner = e.Login(e.Email("owner-inv"), authtest.Password)
	if list, _ := e.Svc.ListInvitations(bg, owner.P, false); len(list) != 0 {
		t.Fatalf("expired invitation listed as pending: %+v", list)
	}
	list, _ := e.Svc.ListInvitations(bg, owner.P, true)
	if len(list) != 1 || !list[0].Expired(e.Now()) || list[0].SendCount != 1 {
		t.Fatalf("include_expired list %+v", list)
	}
	renewed, token2, sent, err := e.Svc.ResendInvitation(bg, owner.P, inv.ID, e.Meta)
	if err != nil || !sent || token2 == token || !renewed.ExpiresAt.After(e.Now()) || renewed.SendCount != 2 {
		t.Fatalf("resend: %+v %v sent=%v", renewed, err, sent)
	}
	if _, err := e.Svc.LookupInvitation(bg, token); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("old token after resend: %v", err)
	}
	if _, err := e.Svc.LookupInvitation(bg, token2); err != nil {
		t.Fatalf("new token: %v", err)
	}
	// Per-address e-mail limit (5/h): resends keep working, only the e-mail is skipped.
	for i := 0; i < 4; i++ {
		_, _, _, _ = e.Svc.ResendInvitation(bg, owner.P, inv.ID, e.Meta)
	}
	_, token3, sent, err := e.Svc.ResendInvitation(bg, owner.P, inv.ID, e.Meta)
	if err != nil || sent || token3 == "" {
		t.Fatalf("resend over the e-mail limit: sent=%v err=%v", sent, err)
	}

	// Without e-mail: link only; unknown and revoked invitations cannot be resent.
	e2, _, _ := saasEnv(t, func(c *auth.Config) { c.Mailer = nil })
	_, owner2 := e2.Bootstrap("nomail")
	inv2, _, err := e2.Svc.CreateInvitation(bg, owner2.P, e2.Email("x"), auth.RoleViewer, e2.Meta)
	if err != nil || inv2.LastSentAt != nil {
		t.Fatalf("invite without mailer %+v %v", inv2, err)
	}
	if err := e2.Svc.RevokeInvitation(bg, owner2.P, inv2.ID, e2.Meta); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e2.Svc.ResendInvitation(bg, owner2.P, inv2.ID, e2.Meta); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("resend revoked: %v", err)
	}
	admin := e.AddUser(owner, "adm", auth.RoleAdmin)
	ownerInv, _, err := e.Svc.CreateInvitation(bg, owner.P, e.Email("o2"), auth.RoleOwner, e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.Svc.ResendInvitation(bg, admin.P, ownerInv.ID, e.Meta); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("admin resends owner invitation: %v", err)
	}
	_ = st
}

// A removed member loses the organization on the very next request, and their API keys stop working.
func TestRemovedMemberRevocation(t *testing.T) {
	st := memstore.New()
	e := authtest.NewEnv(t, st, auth.Config{})
	org, owner := e.Bootstrap("rm")
	member := e.AddUser(owner, "member", auth.RoleMember)
	_, secret, err := e.Svc.CreateAPIKey(bg, member.P, "script", nil, e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth(http.MethodGet, nil, authtest.WithBearer(secret)); err != nil {
		t.Fatal(err)
	}
	// Demotion applies immediately.
	if err := e.Svc.UpdateMemberRole(bg, owner.P, member.P.UserID, auth.RoleViewer, e.Meta); err != nil {
		t.Fatal(err)
	}
	p, err := e.Auth(http.MethodGet, member, authtest.WithOrg(org.ID))
	if err != nil || p.Role != auth.RoleViewer {
		t.Fatalf("after demotion: %+v %v", p, err)
	}
	if err := e.Svc.RemoveMember(bg, owner.P, member.P.UserID, e.Meta); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth(http.MethodGet, member, authtest.WithOrg(org.ID)); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("removed member selecting the org: %v", err)
	}
	if p, err := e.Auth(http.MethodGet, member); err != nil || p.HasOrg() {
		t.Fatalf("removed member default org: %+v %v", p, err)
	}
	if _, err := e.Auth(http.MethodGet, nil, authtest.WithBearer(secret)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("API key of removed member: %v", err)
	}
	evs, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{Action: "member.remove"})
	if len(evs) != 1 || evs[0].Details["revoked_api_keys"] != 1 {
		t.Fatalf("audit %+v", evs)
	}
}

func TestKeyHashSecretMigration(t *testing.T) {
	st := memstore.New()
	plain := authtest.NewEnv(t, st, auth.Config{})
	org, owner := plain.Bootstrap("kh")
	lk, lkSecret, err := plain.Svc.CreateLicenseKey(bg, owner.P, "old", "", plain.Meta)
	if err != nil {
		t.Fatal(err)
	}
	custom := "imported-value-0123456789abcdef"
	if _, _, err := plain.Svc.CreateLicenseKey(bg, owner.P, "imported", custom, plain.Meta); err != nil {
		t.Fatal(err)
	}
	ak, akSecret, err := plain.Svc.CreateAPIKey(bg, owner.P, "old", nil, plain.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(st.LicenseKeyHash(lk.ID), auth.HashSecret(lkSecret)) {
		t.Fatal("without a secret keys are SHA-256")
	}

	// The operator sets OPENLOG_KEY_HASH_SECRET: old rows still resolve and are rewritten on first use.
	h := tenant.NewKeyHasher("0123456789abcdef0123456789abcdef-secret", "")
	keyed := authtest.NewEnv(t, st, auth.Config{KeyHasher: h})
	cache := tenant.NewCached(st, tenant.CacheOptions{Hasher: h, Now: keyed.Now})
	if got, err := cache.Resolve(bg, lkSecret); err != nil || got != org.TenantID {
		t.Fatalf("legacy license key with secret: %q %v", got, err)
	}
	if !bytes.Equal(st.LicenseKeyHash(lk.ID), h.Hash(lkSecret)) {
		t.Fatal("legacy license key not rehashed to HMAC")
	}
	if _, err := keyed.Auth(http.MethodGet, nil, authtest.WithBearer(akSecret)); err != nil {
		t.Fatalf("legacy API key with secret: %v", err)
	}
	if !bytes.Equal(st.APIKeyHash(ak.ID), h.Hash(akSecret)) {
		t.Fatal("legacy API key not rehashed")
	}
	// A value stored under the old format still counts as taken (also when revoked).
	owner2 := keyed.Login(plain.Email("owner-kh"), authtest.Password)
	if _, _, err := keyed.Svc.CreateLicenseKey(bg, owner2.P, "dup", custom, keyed.Meta); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("legacy custom value reused: %v", err)
	}
	nk, nkSecret, err := keyed.Svc.CreateLicenseKey(bg, owner2.P, "new", "", keyed.Meta)
	if err != nil || !bytes.Equal(st.LicenseKeyHash(nk.ID), h.Hash(nkSecret)) {
		t.Fatalf("new key must be stored as HMAC: %v", err)
	}
	// Without the secret an HMAC row does not resolve: every service must share OPENLOG_KEY_HASH_SECRET.
	if _, err := tenant.NewCached(st, tenant.CacheOptions{Now: keyed.Now}).Resolve(bg, nkSecret); !errors.Is(err, tenant.ErrUnknownKey) {
		t.Fatalf("HMAC key without secret: %v", err)
	}

	// Rotation: the previous secret keeps working and rows move to the new one.
	h2 := tenant.NewKeyHasher("fedcba9876543210fedcba9876543210-secret", "0123456789abcdef0123456789abcdef-secret")
	if got, err := tenant.NewCached(st, tenant.CacheOptions{Hasher: h2, Now: keyed.Now}).Resolve(bg, nkSecret); err != nil || got != org.TenantID {
		t.Fatalf("rotation: %q %v", got, err)
	}
	if !bytes.Equal(st.LicenseKeyHash(nk.ID), h2.Hash(nkSecret)) {
		t.Fatal("not rehashed to the new secret")
	}
	// Bootstrap uses the hasher too.
	boot := authtest.NewEnv(t, st, auth.Config{KeyHasher: h2})
	res, err := boot.Svc.Bootstrap(bg, auth.BootstrapSpec{TenantID: org.TenantID, LicenseKey: lkSecret})
	if err != nil || res.CreatedLicenseKey {
		t.Fatalf("bootstrap with an existing (rehashed) key: %+v %v", res, err)
	}
}

func TestAuditFilter(t *testing.T) {
	st := memstore.New()
	e := authtest.NewEnv(t, st, auth.Config{})
	_, owner := e.Bootstrap("af")
	for i := 0; i < 5; i++ {
		e.Advance(time.Minute)
		if _, _, err := e.Svc.CreateAPIKey(bg, owner.P, "k", nil, e.Meta); err != nil {
			t.Fatal(err)
		}
	}
	mid := e.Now()
	e.Advance(time.Minute)
	e.AddUser(owner, "Grace", auth.RoleViewer)

	all, err := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{})
	if err != nil || len(all) < 7 {
		t.Fatalf("all %d %v", len(all), err)
	}
	keys, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{Action: "api_key."})
	if len(keys) != 5 {
		t.Fatalf("action prefix: %d", len(keys))
	}
	grace, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{Actor: "GRACE-"})
	if len(grace) != 1 || grace[0].Action != "invitation.accept" {
		t.Fatalf("actor filter: %+v", grace)
	}
	late, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{From: mid.Add(time.Second)})
	if len(late) != 2 { // invitation.create + invitation.accept
		t.Fatalf("from filter: %+v", late)
	}
	page1, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{Action: "api_key.", Limit: 3})
	last := page1[len(page1)-1]
	page2, _ := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{Action: "api_key.", Limit: 3, Before: &auth.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}})
	if len(page1) != 3 || len(page2) != 2 || page2[0].ID >= last.ID {
		t.Fatalf("pages %d %d", len(page1), len(page2))
	}
	if _, err := e.Svc.ListAuditEvents(bg, owner.P, auth.AuditFilter{From: mid, To: mid}); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("from == to: %v", err)
	}
}
