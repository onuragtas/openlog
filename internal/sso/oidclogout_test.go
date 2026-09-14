package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

const bclEvent = "http://schemas.openid.net/event/backchannel-logout"

var jtiSeq atomic.Int64

// logoutTokenClaims returns the overrides that turn the fake IdP's ID token claims into a back-channel logout token
// for sid (a nil value deletes a claim).
func logoutTokenClaims(sid string, overrides map[string]any) map[string]any {
	c := map[string]any{"email": nil, "email_verified": nil, "name": nil, "groups": nil, "sid": sid,
		"events": map[string]any{bclEvent: map[string]any{}}, "jti": fmt.Sprintf("jti-%d", jtiSeq.Add(1))}
	if sid == "" {
		c["sid"] = nil
	}
	for k, v := range overrides {
		c[k] = v
	}
	return c
}

// oidcLoginSID signs alice in with an ID token carrying sid.
func oidcLoginSID(t *testing.T, env *testEnv, idp *fakeIdP, sid string) sso.Outcome {
	t.Helper()
	idp.setClaims(map[string]any{"sid": sid})
	defer idp.setClaims(nil)
	out := runOIDCLogin(t, env, idp, "alice@example.com")
	if out.Session == nil {
		t.Fatalf("login %s: %+v", sid, out)
	}
	return out
}

func expectAlive(t *testing.T, env *testEnv, name string, o sso.Outcome, want bool) {
	t.Helper()
	_, err := principalFor(env, o.Session.Token)
	if want && err != nil {
		t.Fatalf("%s: session ended: %v", name, err)
	}
	if !want && !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("%s: session still active (%v)", name, err)
	}
}

func auditCount(env *testEnv, action, via string) int {
	n := 0
	for _, e := range env.users.AuditEvents() {
		if e.Action == action && e.Details["via"] == via {
			n++
		}
	}
	return n
}

func TestOIDCBackchannelLogout(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	env.verifyDomain(t, "example.com")
	ctx := context.Background()
	a := oidcLoginSID(t, env, idp, "sid-a")
	b := oidcLoginSID(t, env, idp, "sid-b")
	if link, err := env.store.GetSSOSession(ctx, a.Session.Session.ID); err != nil || link.SessionIndex != "sid-a" || link.Subject != "user-1" {
		t.Fatalf("session link = %+v %v", link, err)
	}
	logout := func(token string) error { return env.sso.OIDCBackchannelLogout(ctx, c.ID, token, testMeta()) }
	rejected := func(err error) bool {
		var rej *sso.LogoutRejectedError
		return errors.As(err, &rej)
	}

	now := time.Now()
	full := map[string]any{"iss": idp.srv.URL, "aud": "openlog", "sub": "user-1", "iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
		"sid": "sid-a", "jti": "jti-foreign", "events": map[string]any{bclEvent: map[string]any{}}}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, token string }{
		{"empty", ""},
		{"not a JWT", "a.b.c"},
		{"ID token", idp.makeToken(map[string]any{"nonce": "n", "sid": "sid-a"})},
		{"no events", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"events": nil}))},
		{"other event", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"events": map[string]any{"http://example.com/event": map[string]any{}}}))},
		{"event not an object", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"events": map[string]any{bclEvent: "yes"}}))},
		{"nonce", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"nonce": "n"}))},
		{"no jti", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"jti": nil}))},
		{"neither sub nor sid", idp.makeToken(logoutTokenClaims("", map[string]any{"sub": nil}))},
		{"other audience", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"aud": "other-client"}))},
		{"other issuer", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"iss": "https://evil.example"}))},
		{"expired", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"exp": now.Add(-5 * time.Minute).Unix()}))},
		{"old", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"iat": now.Add(-time.Hour).Unix()}))},
		{"future", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"iat": now.Add(time.Hour).Unix()}))},
		{"no iat", idp.makeToken(logoutTokenClaims("sid-a", map[string]any{"iat": nil}))},
		{"key not in the JWKS", signJWT(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: otherKey, KeyID: "k1"}}, full)},
		{"HMAC", signJWT(t, jose.SigningKey{Algorithm: jose.HS256, Key: []byte("client-secret-client-secret-client-secret")}, full)},
	} {
		if err := logout(tc.token); !rejected(err) {
			t.Fatalf("%s: %v", tc.name, err)
		}
		expectAlive(t, env, tc.name, a, true)
	}
	if n := auditCount(env, "sso.logout_failed", "oidc_backchannel"); n == 0 {
		t.Fatal("refused logout tokens not audited")
	}

	token := idp.makeToken(logoutTokenClaims("sid-a", nil))
	if err := logout(token); err != nil {
		t.Fatalf("valid logout token: %v", err)
	}
	expectAlive(t, env, "sid-a", a, false)
	expectAlive(t, env, "sid-b", b, true)
	if err := logout(token); !rejected(err) {
		t.Fatalf("replayed logout token: %v", err)
	}
	// A sid with another subject, or an unknown sid, ends nothing (and is not an error).
	if err := logout(idp.makeToken(logoutTokenClaims("sid-b", map[string]any{"sub": "user-2"}))); err != nil {
		t.Fatalf("sid of another subject: %v", err)
	}
	if err := logout(idp.makeToken(logoutTokenClaims("sid-unknown", nil))); err != nil {
		t.Fatalf("unknown sid: %v", err)
	}
	expectAlive(t, env, "sid-b after foreign tokens", b, true)
	// sub only: every session of the subject on the connection.
	c3 := oidcLoginSID(t, env, idp, "sid-c")
	if err := logout(idp.makeToken(logoutTokenClaims("", nil))); err != nil {
		t.Fatalf("sub-only logout token: %v", err)
	}
	expectAlive(t, env, "sid-b after sub logout", b, false)
	expectAlive(t, env, "sid-c after sub logout", c3, false)
	if n := auditCount(env, "sso.logout", "oidc_backchannel"); n < 2 {
		t.Fatalf("sso.logout via oidc_backchannel = %d", n)
	}
	if err := env.sso.OIDCBackchannelLogout(ctx, "00000000-0000-0000-0000-000000000000", idp.makeToken(logoutTokenClaims("sid-a", nil)), testMeta()); !rejected(err) {
		t.Fatalf("unknown connection: %v", err)
	}

	// Invalid requests are limited per address; valid ones then wait too.
	meta := auth.ClientMeta{IP: "203.0.113.9"}
	for i := 0; i < 60; i++ {
		_ = env.sso.OIDCBackchannelLogout(ctx, c.ID, "a.b.c", meta)
	}
	var rej *sso.LogoutRejectedError
	if err := env.sso.OIDCBackchannelLogout(ctx, c.ID, idp.makeToken(logoutTokenClaims("sid-x", nil)), meta); !errors.As(err, &rej) || !strings.Contains(rej.Reason, "too many") {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestOIDCFrontchannelLogout(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	env.verifyDomain(t, "example.com")
	ctx := context.Background()
	a := oidcLoginSID(t, env, idp, "sid-f1")
	b := oidcLoginSID(t, env, idp, "sid-f2")
	frame := "frame-ancestors " + idp.srv.URL
	call := func(connID, iss, sid string) sso.FrontchannelPage {
		return env.sso.OIDCFrontchannelLogout(ctx, connID, iss, sid, testMeta())
	}
	for _, tc := range []struct{ name, iss, sid string }{
		{"no sid", idp.srv.URL, ""},
		{"no iss", "", "sid-f1"},
		{"other issuer", "https://evil.example", "sid-f1"},
		{"issuer with a trailing slash", idp.srv.URL + "/", "sid-f1"},
	} {
		if p := call(c.ID, tc.iss, tc.sid); p.Status != http.StatusBadRequest || !strings.HasSuffix(p.CSP, frame) {
			t.Fatalf("%s: %+v", tc.name, p)
		}
		expectAlive(t, env, tc.name, a, true)
	}
	p := call(c.ID, idp.srv.URL, "sid-f1")
	if p.Status != http.StatusOK || !strings.HasSuffix(p.CSP, frame) || !strings.Contains(p.CSP, "default-src 'none'") ||
		strings.Contains(string(p.HTML), "<script") {
		t.Fatalf("front-channel logout page: %+v %s", p, p.HTML)
	}
	expectAlive(t, env, "sid-f1", a, false)
	expectAlive(t, env, "sid-f2", b, true)
	if p := call(c.ID, idp.srv.URL, "sid-f1"); p.Status != http.StatusOK {
		t.Fatalf("repeated front-channel logout: %+v", p)
	}
	if p := call("00000000-0000-0000-0000-000000000000", idp.srv.URL, "sid-f2"); p.Status != http.StatusNotFound || !strings.HasSuffix(p.CSP, "frame-ancestors 'none'") {
		t.Fatalf("unknown connection: %+v", p)
	}
	expectAlive(t, env, "sid-f2 after unknown connection", b, true)
	if n := auditCount(env, "sso.logout", "oidc_frontchannel"); n != 1 {
		t.Fatalf("sso.logout via oidc_frontchannel = %d", n)
	}
	if info := env.sso.OIDCFrontchannelLogoutURL(c.ID); info != "https://openlog.example/api/v1/sso/oidc/"+c.ID+"/frontchannel-logout" {
		t.Fatalf("front-channel URL = %s", info)
	}
}
