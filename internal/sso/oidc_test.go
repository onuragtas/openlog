package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/sso/ssomem"
)

// fakeIdP is an OpenID provider for tests: discovery, rotating JWKS, a token endpoint that checks PKCE and
// returns an ID token, and UserInfo.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	keys   map[string]*rsa.PrivateKey // kid → key published in the JWKS
	kid    string                     // signing key
	algs   []string
	codes  map[string]fakeCode // code → pending authorization
	claims map[string]any      // overriding ID token claims for the next tokens
	jwks   int                 // JWKS fetches
	disco  int                 // discovery fetches
}

type fakeCode struct {
	challenge string
	nonce     string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{t: t, keys: map[string]*rsa.PrivateKey{}, codes: map[string]fakeCode{}, algs: []string{"RS256"}}
	f.rotate("k1")
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		algs := f.algs
		f.disco++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/auth", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "userinfo_endpoint": f.srv.URL + "/userinfo", "end_session_endpoint": f.srv.URL + "/logout",
			"id_token_signing_alg_values_supported": algs, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.jwks++
		set := jose.JSONWebKeySet{}
		for kid, k := range f.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		pc, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != pc.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		tok := f.makeToken(map[string]any{"nonce": pc.nonce})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60, "id_token": tok})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-1"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) rotate(kid string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.keys[kid], f.kid = k, kid
	f.mu.Unlock()
}

func (f *fakeIdP) setClaims(c map[string]any) {
	f.mu.Lock()
	f.claims = c
	f.mu.Unlock()
}

// makeToken signs the base claims with overrides (a nil value deletes the claim).
func (f *fakeIdP) makeToken(overrides ...map[string]any) string {
	now := time.Now()
	c := map[string]any{"iss": f.srv.URL, "aud": "openlog", "sub": "user-1", "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "email": "alice@example.com", "email_verified": true, "name": "Alice", "groups": []string{"eng", "admins"}}
	f.mu.Lock()
	extra := f.claims
	key, kid := f.keys[f.kid], f.kid
	f.mu.Unlock()
	for _, o := range append([]map[string]any{extra}, overrides...) {
		for k, v := range o {
			if v == nil {
				delete(c, k)
			} else {
				c[k] = v
			}
		}
	}
	return signJWT(f.t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, c)
}

func signJWT(t *testing.T, key jose.SigningKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(key, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := signer.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// authorize emulates the user signing in at the IdP: it registers a code for the authorization URL.
func (f *fakeIdP) authorize(t *testing.T, authURL string) url.Values {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request lacks PKCE/nonce/state: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") || q.Get("redirect_uri") != "https://openlog.example/api/v1/sso/oidc/callback" {
		t.Fatalf("scope/redirect_uri = %q %q", q.Get("scope"), q.Get("redirect_uri"))
	}
	code := "code-" + q.Get("state")[:8]
	f.mu.Lock()
	f.codes[code] = fakeCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce")}
	f.mu.Unlock()
	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

// testEnv is an auth service with one organization, an SSO service and an in-memory SSO store.
type testEnv struct {
	auth  *auth.Service
	users *memstore.Store
	store sso.Store
	sso   *sso.Service
	org   auth.Organization
	owner auth.User
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st := memstore.New()
	as := auth.NewService(st, auth.Config{CookieSecure: true}, nil)
	res, err := as.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Acme",
		OwnerEmail: "owner@example.com", OwnerPassword: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := st.GetUserByEmail(context.Background(), "owner@example.com")
	ss := ssomem.New(st)
	svc := sso.NewService(as, ss, sso.Config{PublicURL: "https://openlog.example", AllowPrivateNetworks: true, CookieSecure: true,
		SecretBox: sso.NewSecretBox("an sso secret key that is long enough", "", "", ""), SCIMEnabled: true}, nil)
	as.SetSessionPolicy(svc.Policy())
	return &testEnv{auth: as, users: st, store: ss, sso: svc, org: res.Org, owner: owner}
}

func (e *testEnv) ownerPrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.KindSession, UserID: e.owner.ID, Email: e.owner.Email, EmailVerified: true,
		OrgID: e.org.ID, OrgName: e.org.Name, TenantID: e.org.TenantID, Role: auth.RoleOwner, SessionID: "none"}
}

func (e *testEnv) verifyDomain(t *testing.T, domain string) {
	t.Helper()
	d := sso.Domain{OrgID: e.org.ID, Domain: domain, DNSToken: "x"}
	if err := e.store.CreateDomain(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.MarkDomainVerified(context.Background(), e.org.ID, d.ID, "dns_txt", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) oidcConnection(t *testing.T, issuer string) sso.Connection {
	t.Helper()
	secret := "client-secret"
	c, err := e.sso.SaveConnection(context.Background(), e.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Enabled: true,
		Issuer: issuer, ClientID: "openlog", ClientSecret: &secret, DefaultRole: auth.RoleViewer}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVerifyIDToken(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	ctx := context.Background()
	verify := func(raw, nonce string) error { return env.sso.VerifyIDTokenForTest(ctx, c, raw, nonce) }
	now := time.Now()
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	if err := verify(idp.makeToken(map[string]any{"nonce": "n1"}), "n1"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	cases := []struct {
		name  string
		token string
	}{
		{"expired", idp.makeToken(map[string]any{"nonce": "n1", "exp": now.Add(-10 * time.Minute).Unix()})},
		{"wrong audience", idp.makeToken(map[string]any{"nonce": "n1", "aud": "someone-else"})},
		{"wrong issuer", idp.makeToken(map[string]any{"nonce": "n1", "iss": "https://evil.example"})},
		{"nonce mismatch", idp.makeToken(map[string]any{"nonce": "n2"})},
		{"missing nonce", idp.makeToken()},
		{"several audiences without azp", idp.makeToken(map[string]any{"nonce": "n1", "aud": []string{"openlog", "other"}})},
		{"issued in the future", idp.makeToken(map[string]any{"nonce": "n1", "iat": now.Add(time.Hour).Unix()})},
		{"bad signature (unknown key, same kid)", signJWT(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: otherKey, KeyID: "k1"}},
			map[string]any{"iss": idp.srv.URL, "aud": "openlog", "sub": "u", "exp": now.Add(time.Minute).Unix(), "nonce": "n1"})},
		{"HS256 with the client secret", signJWT(t, jose.SigningKey{Algorithm: jose.HS256, Key: []byte("client-secret-client-secret-32by")},
			map[string]any{"iss": idp.srv.URL, "aud": "openlog", "sub": "u", "exp": now.Add(time.Minute).Unix(), "nonce": "n1"})},
		{"alg none", unsignedJWT(map[string]any{"iss": idp.srv.URL, "aud": "openlog", "sub": "u", "exp": now.Add(time.Minute).Unix(), "nonce": "n1"})},
		{"tampered payload", tamper(idp.makeToken(map[string]any{"nonce": "n1"}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := verify(tc.token, "n1"); err == nil {
				t.Fatal("token accepted")
			}
		})
	}
	if err := verify(idp.makeToken(map[string]any{"nonce": "n1", "aud": []string{"openlog", "other"}, "azp": "openlog"}), "n1"); err != nil {
		t.Fatalf("azp token rejected: %v", err)
	}
	// Clock skew: a token that expired a minute ago is still accepted (default skew 2m).
	if err := verify(idp.makeToken(map[string]any{"nonce": "n1", "exp": now.Add(-time.Minute).Unix()}), "n1"); err != nil {
		t.Fatalf("token within clock skew rejected: %v", err)
	}

	// Key rotation: the IdP publishes a new key and signs with it; the JWKS is refetched for the unknown kid.
	idp.mu.Lock()
	before := idp.jwks
	idp.mu.Unlock()
	idp.rotate("k2")
	if err := verify(idp.makeToken(map[string]any{"nonce": "n1"}), "n1"); err != nil {
		t.Fatalf("token signed with a rotated key rejected: %v", err)
	}
	idp.mu.Lock()
	after := idp.jwks
	idp.mu.Unlock()
	if after <= before {
		t.Fatal("JWKS was not refetched after rotation")
	}
}

func unsignedJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + "."
}

func tamper(raw string) string {
	parts := strings.Split(raw, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	payload = []byte(strings.Replace(string(payload), "alice@example.com", "mallory@example.com", 1))
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	return strings.Join(parts, ".")
}

func TestOIDCProviderWithOnlySymmetricAlgorithms(t *testing.T) {
	idp := newFakeIdP(t)
	idp.algs = []string{"HS256", "none"}
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	if err := env.sso.DiscoverOIDCForTest(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unsupported algorithms") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdPAddressGuard(t *testing.T) {
	idp := newFakeIdP(t) // listens on 127.0.0.1
	st := memstore.New()
	as := auth.NewService(st, auth.Config{}, nil)
	res, err := as.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OwnerEmail: "o@example.com", OwnerPassword: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	svc := sso.NewService(as, ssomem.New(st), sso.Config{PublicURL: "https://openlog.example", AllowPrivateNetworks: false}, nil)
	owner, _ := st.GetUserByEmail(context.Background(), "o@example.com")
	p := &auth.Principal{Kind: auth.KindSession, UserID: owner.ID, OrgID: res.Org.ID, TenantID: res.Org.TenantID, Role: auth.RoleOwner}
	// http issuers are refused without private networks …
	_, err = svc.SaveConnection(context.Background(), p, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Issuer: idp.srv.URL, ClientID: "x"}, auth.ClientMeta{})
	if !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("http issuer accepted: %v", err)
	}
	// … and loopback/private addresses are never dialled, even over https.
	c := sso.Connection{ID: "c", OrgID: res.Org.ID, Protocol: sso.ProtocolOIDC, OIDC: &sso.OIDCConfig{Issuer: "https://127.0.0.1:1", ClientID: "x"}}
	if err := svc.DiscoverOIDCForTest(context.Background(), c); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("loopback issuer: %v", err)
	}
}

// runOIDCLogin starts a sign-in for email and completes the callback with the fake IdP.
func runOIDCLogin(t *testing.T, env *testEnv, idp *fakeIdP, email string) sso.Outcome {
	t.Helper()
	ctx := context.Background()
	st, err := env.sso.StartLogin(ctx, email, "/apm?x=1", auth.ClientMeta{IP: "198.51.100.7"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := env.sso.BindingCookie(st)
	if cookie.SameSite != http.SameSiteLaxMode || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/api/v1/sso" {
		t.Fatalf("binding cookie = %+v", cookie)
	}
	q := idp.authorize(t, st.URL)
	return env.sso.OIDCCallback(ctx, q, func(name string) string {
		if name == cookie.Name {
			return cookie.Value
		}
		return ""
	}, auth.ClientMeta{IP: "198.51.100.7"})
}

func TestOIDCLoginFlow(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	ctx := context.Background()

	// Domain not verified yet: refused, nothing is provisioned.
	if _, err := env.sso.StartLogin(ctx, "alice@example.com", "", auth.ClientMeta{}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("start without verified domain: %v", err)
	}
	env.verifyDomain(t, "example.com")
	if _, err := env.sso.ReplaceRoleMappings(ctx, env.ownerPrincipal(), "", []sso.RoleMapping{{Group: "admins", Role: auth.RoleAdmin}}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	d, err := env.sso.Discover(ctx, "Alice@Example.com", auth.ClientMeta{})
	if err != nil || !d.SSO || d.OrganizationName != "Acme" || d.Protocol != sso.ProtocolOIDC {
		t.Fatalf("discover = %+v, %v", d, err)
	}

	out := runOIDCLogin(t, env, idp, "alice@example.com")
	if out.Session == nil || out.Redirect != "/apm?x=1" {
		t.Fatalf("outcome = %+v", out)
	}
	sess := out.Session.Session
	if sess.OrgID != env.org.ID || sess.ConnectionID != c.ID || sess.AuthMethod != auth.MethodOIDC {
		t.Fatalf("session not bound: %+v", sess)
	}
	alice, err := env.users.GetUserByEmail(ctx, "alice@example.com")
	if err != nil || alice.PasswordHash != "" || alice.EmailVerifiedAt == nil {
		t.Fatalf("JIT user = %+v, %v", alice, err)
	}
	m, err := env.users.GetMembership(ctx, env.org.ID, alice.ID)
	if err != nil || m.Role != auth.RoleAdmin {
		t.Fatalf("JIT membership = %+v, %v", m, err)
	}

	// Groups change at the IdP: the role follows on the next sign-in.
	idp.setClaims(map[string]any{"groups": []string{"eng"}})
	if out = runOIDCLogin(t, env, idp, "alice@example.com"); out.Session == nil {
		t.Fatalf("second login: %+v", out)
	}
	if m, _ = env.users.GetMembership(ctx, env.org.ID, alice.ID); m.Role != auth.RoleViewer {
		t.Fatalf("role after group change = %s", m.Role)
	}

	// email_verified=false is refused by default.
	idp.setClaims(map[string]any{"email_verified": false})
	if out = runOIDCLogin(t, env, idp, "alice@example.com"); out.Session != nil || out.Redirect != "/login?sso_error=email_not_verified" {
		t.Fatalf("unverified email: %+v", out)
	}
	// An address outside the verified domains (an IdP asserting another company's user) is refused.
	idp.setClaims(map[string]any{"email": "ceo@other.example"})
	if out = runOIDCLogin(t, env, idp, "alice@example.com"); out.Session != nil || out.Redirect != "/login?sso_error=domain_not_verified" {
		t.Fatalf("foreign domain: %+v", out)
	}
	idp.setClaims(nil)

	// JIT off: an unknown user is refused.
	jit := false
	secret := "client-secret"
	if _, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Enabled: true, Issuer: idp.srv.URL,
		ClientID: "openlog", ClientSecret: &secret, JITEnabled: &jit}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	idp.setClaims(map[string]any{"email": "carol@example.com"})
	if out = runOIDCLogin(t, env, idp, "carol@example.com"); out.Session != nil || out.Redirect != "/login?sso_error=not_member" {
		t.Fatalf("JIT off: %+v", out)
	}
	idp.setClaims(nil)

	// Missing or wrong binding cookie (login CSRF), and state reuse.
	st, err := env.sso.StartLogin(ctx, "alice@example.com", "", auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	q := idp.authorize(t, st.URL)
	if out := env.sso.OIDCCallback(ctx, q, func(string) string { return "wrong" }, auth.ClientMeta{}); out.Session != nil || out.Redirect != "/login?sso_error=expired" {
		t.Fatalf("wrong binding: %+v", out)
	}
	if out := env.sso.OIDCCallback(ctx, q, func(string) string { return st.Binding }, auth.ClientMeta{}); out.Session != nil {
		t.Fatalf("state reused: %+v", out)
	}
	// IdP error responses.
	st, _ = env.sso.StartLogin(ctx, "alice@example.com", "", auth.ClientMeta{})
	cookie := env.sso.BindingCookie(st)
	q = url.Values{"state": {st.State}, "error": {"access_denied"}}
	if out := env.sso.OIDCCallback(ctx, q, func(string) string { return cookie.Value }, auth.ClientMeta{}); out.Redirect != "/login?sso_error=idp_error" {
		t.Fatalf("idp error: %+v", out)
	}

	var login, failed bool
	for _, e := range env.users.AuditEvents() {
		login = login || e.Action == "sso.login"
		failed = failed || e.Action == "sso.login_failed"
	}
	if !login || !failed {
		t.Fatalf("audit events missing (login=%v failed=%v)", login, failed)
	}
}

func TestSessionPolicyAndEnforcement(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	env.verifyDomain(t, "example.com")
	ctx := context.Background()

	out := runOIDCLogin(t, env, idp, "alice@example.com")
	if out.Session == nil {
		t.Fatalf("login: %+v", out)
	}
	authReq := func(token, orgHeader string) (*auth.Principal, error) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		r.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
		if orgHeader != "" {
			r.Header.Set(auth.HeaderOrg, orgHeader)
		}
		return env.auth.Authenticate(r)
	}
	if p, err := authReq(out.Session.Token, ""); err != nil || p.OrgID != env.org.ID {
		t.Fatalf("SSO session: %+v, %v", p, err)
	}

	// The SSO session cannot act in another organization of the same user.
	other, err := env.auth.Bootstrap(ctx, auth.BootstrapSpec{TenantID: "tenant-b", OrgName: "Other", OwnerEmail: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authReq(out.Session.Token, other.Org.ID); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("other org with SSO session: %v", err)
	}

	// Disabling the connection ends SSO sessions.
	c, _ = env.store.GetConnection(ctx, env.org.ID, "")
	c.Enabled = false
	if err := env.store.UpdateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if _, err := authReq(out.Session.Token, ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled connection: %v", err)
	}
	// Re-authentication after the maximum session age.
	c.Enabled, c.SessionMaxAge = true, 10*time.Minute
	if err := env.store.UpdateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	env.auth.SetClock(func() time.Time { return time.Now().Add(11 * time.Minute) })
	if _, err := authReq(out.Session.Token, ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("max age: %v", err)
	}
	env.auth.SetClock(time.Now)
	c.SessionMaxAge = 0
	_ = env.store.UpdateConnection(ctx, &c)

	// Enforcement: lockout safeguards.
	owner := env.ownerPrincipal()
	if _, err := env.sso.UpdateEnforcement(ctx, owner, "", true, []string{env.owner.ID}, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("enforce without test: %v", err)
	}
	if err := env.store.RecordTest(ctx, c.ID, c.ConfigVersion, true, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.sso.UpdateEnforcement(ctx, owner, "", true, nil, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("enforce without break-glass: %v", err)
	}
	if _, err := env.sso.UpdateEnforcement(ctx, owner, "", true, []string{"7c1e2d9a-3b4f-4e5a-8b6c-000000000009"}, auth.ClientMeta{}); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("enforce with a non-owner break-glass: %v", err)
	}
	adminP := *owner
	adminP.Role = auth.RoleAdmin
	if _, err := env.sso.UpdateEnforcement(ctx, &adminP, "", true, []string{env.owner.ID}, auth.ClientMeta{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("admin changes enforcement: %v", err)
	}
	if _, err := env.sso.UpdateEnforcement(ctx, owner, "", true, []string{env.owner.ID}, auth.ClientMeta{}); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	// A config change while enforced keeps enforcement, but disabling/deleting is refused.
	if err := env.sso.DeleteConnection(ctx, owner, "", auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("delete while enforced: %v", err)
	}

	// bob@example.com has a password and is a member: password sign-in is refused.
	hash, _ := auth.HashPassword("bob's long password")
	now := time.Now()
	bob := auth.User{Email: "bob@example.com", Name: "Bob", PasswordHash: hash, EmailVerifiedAt: &now}
	if err := env.users.CreateUser(ctx, &bob); err != nil {
		t.Fatal(err)
	}
	if err := env.users.AddMember(ctx, env.org.ID, bob.ID, auth.RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := env.auth.Login(ctx, "bob@example.com", "bob's long password", auth.ClientMeta{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("password login under enforcement: %v", err)
	}
	// The break-glass owner still signs in with a password.
	res, err := env.auth.Login(ctx, "owner@example.com", "correct horse battery staple", auth.ClientMeta{})
	if err != nil {
		t.Fatalf("break-glass login: %v", err)
	}
	if p, err := authReq(res.Token, env.org.ID); err != nil || p.Role != auth.RoleOwner {
		t.Fatalf("break-glass session: %+v %v", p, err)
	}
	// Without the break-glass entry the owner's password session loses the organization immediately.
	c, _ = env.store.GetConnection(ctx, env.org.ID, "")
	c.BreakGlassUserIDs = []string{}
	_ = env.store.UpdateConnection(ctx, &c)
	if _, err := authReq(res.Token, env.org.ID); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("owner without break-glass: %v", err)
	}
	// SSO sessions are unaffected by enforcement.
	if _, err := authReq(out.Session.Token, ""); err != nil {
		t.Fatalf("SSO session under enforcement: %v", err)
	}
}
