package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
)

type captureMailer struct {
	mu   sync.Mutex
	sent []auth.Mail
}

func (m *captureMailer) Send(_ context.Context, mail auth.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mail)
	return nil
}

func (m *captureMailer) token(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no e-mail")
	}
	_, rest, _ := strings.Cut(m.sent[len(m.sent)-1].Text, "#token=")
	return strings.Fields(rest)[0]
}

func newSaaSEnv(t *testing.T) (*accountEnv, *captureMailer) {
	t.Helper()
	st := memstore.New()
	m := &captureMailer{}
	svc := auth.NewService(st, auth.Config{CookieSecure: true, SignupEnabled: true, Mailer: m, PublicURL: "https://ol.example",
		RequireEmailVerification: true, Captcha: nil}, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	return &accountEnv{h: s.srv.Handler, svc: svc, st: st, conn: conn}, m
}

func TestSignupVerificationHTTP(t *testing.T) {
	e, mailer := newSaaSEnv(t)
	anon := &client{t: t, h: e.h}
	rec := anon.do(http.MethodGet, "/api/v1/auth/config", nil)
	if !strings.Contains(rec.Body.String(), `"email_enabled":true`) || !strings.Contains(rec.Body.String(), `"email_verification_required":true`) ||
		!strings.Contains(rec.Body.String(), `"captcha":null`) {
		t.Fatalf("auth config %s", rec.Body)
	}
	rec = anon.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{"email": "ada@example.com", "password": ownerPassword, "name": "Ada", "organization_name": "Ada Inc"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"email_verified":false`) {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	c := &client{t: t, h: e.h}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.DefaultCookieName {
			c.cookie = &http.Cookie{Name: ck.Name, Value: ck.Value}
		}
	}
	c.csrf = *decode[meJSON](t, rec).CSRFToken

	if rec := c.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "k"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confirm your e-mail") {
		t.Fatalf("unverified license key: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "x@example.com", "role": "viewer"}); rec.Code != http.StatusForbidden {
		t.Fatalf("unverified invite: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/v1/auth/verify-email/resend", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body)
	}
	if rec := anon.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": "olv_nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad token: %d", rec.Code)
	}
	if rec := anon.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": mailer.token(t)}); rec.Code != http.StatusNoContent {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodGet, "/api/v1/auth/me", nil); !strings.Contains(rec.Body.String(), `"email_verified":true`) {
		t.Fatalf("me after verify %s", rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "k"}); rec.Code != http.StatusCreated {
		t.Fatalf("verified license key: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/v1/auth/verify-email/resend", nil); rec.Code != http.StatusConflict {
		t.Fatalf("resend when verified: %d", rec.Code)
	}

	// Per-e-mail sign-up limit (default 5 attempts per hour) → 429 with Retry-After.
	var last int
	for i := 0; i < 6; i++ {
		rec = anon.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{"email": "ada@example.com", "password": ownerPassword, "organization_name": "Again"})
		last = rec.Code
	}
	if last != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("signup rate limit: %d", last)
	}
}

func TestInvitationResendAndAuditHTTP(t *testing.T) {
	e, _ := newSaaSEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)
	rec := owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "viewer@example.com", "role": "viewer"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"email_sent":true`) || !strings.Contains(rec.Body.String(), `"send_count":1`) {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	created := decode[struct {
		Invitation invitationJSON `json:"invitation"`
		Token      string         `json:"token"`
	}](t, rec)
	rec = owner.do(http.MethodPost, "/api/v1/invitations/"+created.Invitation.ID+"/resend", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email_sent":true`) || strings.Contains(rec.Body.String(), created.Token) {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body)
	}
	if rec := owner.do(http.MethodPost, "/api/v1/invitations/00000000-0000-0000-0000-000000000000/resend", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("resend unknown: %d", rec.Code)
	}
	if rec := owner.do(http.MethodGet, "/api/v1/invitations?include_expired=true", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"expired":false`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := owner.do(http.MethodGet, "/api/v1/invitations?include_expired=maybe", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad include_expired: %d", rec.Code)
	}

	// Audit log filters and cursor pagination.
	for i := 0; i < 3; i++ {
		owner.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "k"})
	}
	rec = owner.do(http.MethodGet, "/api/v1/audit-log?action=api_key.&limit=2", nil)
	page := decode[struct {
		Events []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"events"`
		NextCursor *string `json:"next_cursor"`
	}](t, rec)
	if rec.Code != http.StatusOK || len(page.Events) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body)
	}
	rec = owner.do(http.MethodGet, "/api/v1/audit-log?action=api_key.&limit=2&cursor="+*page.NextCursor, nil)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"api_key.create"`) != 1 || !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body)
	}
	if rec := owner.do(http.MethodGet, "/api/v1/audit-log?actor=OWNER@&action=invitation.resend", nil); !strings.Contains(rec.Body.String(), `"invitation.resend"`) {
		t.Fatalf("actor filter: %s", rec.Body)
	}
	for _, bad := range []string{"cursor=!!", "from=yesterday", "limit=0", "from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if rec := owner.do(http.MethodGet, "/api/v1/audit-log?"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("audit %s: %d", bad, rec.Code)
		}
	}
}

// Removing a member takes effect on their very next request, on any API pod: nothing is cached.
func TestRemovedMemberHTTP(t *testing.T) {
	e := newAccountEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)
	rec := owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "m@example.com", "role": "member"})
	token := decode[struct {
		Token string `json:"token"`
	}](t, rec).Token
	anon := &client{t: t, h: e.h}
	if rec := anon.do(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": token, "password": ownerPassword}); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	member := e.login(t, "m@example.com", ownerPassword)
	me := decode[meJSON](t, member.do(http.MethodGet, "/api/v1/auth/me", nil))
	member.org = me.Organization.ID
	rec = member.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "script"})
	key := decode[struct {
		Key string `json:"key"`
	}](t, rec).Key
	bearer := &client{t: t, h: e.h, bearer: key}
	if rec := bearer.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusOK {
		t.Fatalf("api key before removal: %d", rec.Code)
	}
	if rec := member.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusOK {
		t.Fatalf("member before removal: %d", rec.Code)
	}
	if rec := owner.do(http.MethodDelete, "/api/v1/members/"+me.User.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("removed member with org header: %d", rec.Code)
	}
	member.org = ""
	if rec := member.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("removed member without org: %d", rec.Code)
	}
	if rec := bearer.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("api key after removal: %d", rec.Code)
	}
}
