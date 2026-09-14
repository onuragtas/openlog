package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/sso/ssomem"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

type ssoEnv struct {
	h        http.Handler
	accounts *auth.Service
	users    *memstore.Store
	sso      *sso.Service
	resolver fakeResolver
	org      auth.Organization
}

func newSSOEnv(t *testing.T) *ssoEnv {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	res, err := svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A", OwnerEmail: "owner@example.com",
		OwnerPassword: ownerPassword, APIKey: "ola_0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		email string
		role  auth.Role
	}{{"admin@example.com", auth.RoleAdmin}, {"member@example.com", auth.RoleMember}} {
		hash, _ := auth.HashPassword(ownerPassword)
		now := time.Now()
		user := auth.User{Email: u.email, PasswordHash: hash, EmailVerifiedAt: &now}
		if err := st.CreateUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
		if err := st.AddMember(ctx, res.Org.ID, user.ID, u.role); err != nil {
			t.Fatal(err)
		}
	}
	resolver := fakeResolver{}
	ssoSvc := sso.NewService(svc, ssomem.New(st), sso.Config{PublicURL: "https://openlog.example", CookieSecure: true,
		AllowPrivateNetworks: true, HTTPTimeout: time.Second, Resolver: resolver, SCIMEnabled: true,
		SecretBox: sso.NewSecretBox("a test sso secret key that is long enough", "", "", "")}, quietLog())
	svc.SetSessionPolicy(ssoSvc.Policy())
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(&recordingConn{}, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	s.SetSSO(ssoSvc)
	return &ssoEnv{h: s.srv.Handler, accounts: svc, users: st, sso: ssoSvc, resolver: resolver, org: res.Org}
}

func (e *ssoEnv) login(t *testing.T, email string) *client {
	return (&accountEnv{h: e.h}).login(t, email, ownerPassword)
}

func TestSSOAdminEndpoints(t *testing.T) {
	e := newSSOEnv(t)
	anon := &client{t: t, h: e.h}
	if cfg := decode[map[string]any](t, anon.do(http.MethodGet, "/api/v1/auth/config", nil)); cfg["sso_enabled"] != true {
		t.Fatalf("auth config: %v", cfg)
	}

	owner := e.login(t, "owner@example.com")
	admin := e.login(t, "admin@example.com")
	member := e.login(t, "member@example.com")
	apiKey := &client{t: t, h: e.h, bearer: "ola_0123456789abcdef0123456789abcdef0123456789abcdef"}

	rec := owner.do(http.MethodGet, "/api/v1/sso/connection", nil)
	state := decode[map[string]any](t, rec)
	if rec.Code != http.StatusOK || state["connection"] != nil || state["available"] != true ||
		state["service_provider"].(map[string]any)["oidc_redirect_uri"] != "https://openlog.example/api/v1/sso/oidc/callback" {
		t.Fatalf("get connection: %d %v", rec.Code, state)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("SSO settings must not be cached")
	}
	for name, c := range map[string]*client{"member": member, "api key": apiKey} {
		if rec := c.do(http.MethodGet, "/api/v1/sso/connection", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%s GET connection: %d", name, rec.Code)
		}
	}
	body := map[string]any{"protocol": "oidc", "enabled": true, "default_role": "member",
		"oidc": map[string]any{"issuer": "http://127.0.0.1:1", "client_id": "openlog", "client_secret": "s3cret-value"}}
	noCSRF := *admin
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPut, "/api/v1/sso/connection", body); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF: %d", rec.Code)
	}
	if rec := member.do(http.MethodPut, "/api/v1/sso/connection", body); rec.Code != http.StatusForbidden {
		t.Fatalf("member PUT: %d", rec.Code)
	}
	body["default_role"] = "owner"
	if rec := admin.do(http.MethodPut, "/api/v1/sso/connection", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("owner default role: %d %s", rec.Code, rec.Body)
	}
	body["default_role"] = "member"
	rec = admin.do(http.MethodPut, "/api/v1/sso/connection", body)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-value") {
		t.Fatalf("admin PUT: %d %s", rec.Code, rec.Body)
	}
	conn := decode[map[string]any](t, rec)["connection"].(map[string]any)
	if conn["oidc"].(map[string]any)["client_secret_set"] != true || conn["tested"] != false {
		t.Fatalf("connection = %v", conn)
	}

	// Enforcement is owner-only and refused until tested.
	if rec := admin.do(http.MethodPut, "/api/v1/sso/enforcement", map[string]any{"enforce": true}); rec.Code != http.StatusForbidden {
		t.Fatalf("admin enforcement: %d", rec.Code)
	}
	if rec := owner.do(http.MethodPut, "/api/v1/sso/enforcement", map[string]any{"enforce": true}); rec.Code != http.StatusConflict {
		t.Fatalf("untested enforcement: %d %s", rec.Code, rec.Body)
	}

	// Domains: DNS TXT verification.
	rec = admin.do(http.MethodPost, "/api/v1/sso/domains", map[string]string{"domain": "Example.COM."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add domain: %d %s", rec.Code, rec.Body)
	}
	dom := decode[domainJSON](t, rec)
	if dom.Domain != "example.com" || dom.Verified || dom.DNSRecord["name"] != "_openlog-verification.example.com" {
		t.Fatalf("domain = %+v", dom)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/sso/domains", map[string]string{"domain": "localhost"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid domain: %d", rec.Code)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/sso/domains/"+dom.ID+"/verify", map[string]string{"method": "dns_txt"}); rec.Code != http.StatusConflict {
		t.Fatalf("verify without record: %d %s", rec.Code, rec.Body)
	}
	e.resolver[dom.DNSRecord["name"]] = []string{"v=spf1 -all", dom.DNSRecord["value"]}
	rec = admin.do(http.MethodPost, "/api/v1/sso/domains/"+dom.ID+"/verify", map[string]string{"method": "dns_txt"})
	if rec.Code != http.StatusOK || !decode[domainJSON](t, rec).Verified {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodGet, "/api/v1/sso/domains", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member domains: %d", rec.Code)
	}

	// Role mappings.
	if rec := admin.do(http.MethodPut, "/api/v1/sso/role-mappings", map[string]any{"mappings": []map[string]string{{"group": "g", "role": "owner"}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("owner mapping: %d", rec.Code)
	}
	rec = admin.do(http.MethodPut, "/api/v1/sso/role-mappings", map[string]any{"mappings": []map[string]string{{"group": "openlog-admins", "role": "admin"}}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "openlog-admins") {
		t.Fatalf("mappings: %d %s", rec.Code, rec.Body)
	}

	// SCIM tokens are shown once and authenticate the SCIM API.
	rec = admin.do(http.MethodPost, "/api/v1/scim/tokens", map[string]string{"name": "okta"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("scim token: %d %s", rec.Code, rec.Body)
	}
	created := decode[struct {
		Token  scimTokenJSON `json:"token"`
		Secret string        `json:"secret"`
	}](t, rec)
	if !strings.HasPrefix(created.Secret, "ols_") || !strings.HasPrefix(created.Secret, created.Token.Prefix) {
		t.Fatalf("token = %+v", created)
	}
	if rec := admin.do(http.MethodGet, "/api/v1/scim/tokens", nil); strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatal("token list reveals the secret")
	}
	scimClient := &client{t: t, h: e.h, bearer: created.Secret}
	if rec := scimClient.do(http.MethodGet, "/api/scim/v2/Users", nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/scim+json" {
		t.Fatalf("scim users: %d %s", rec.Code, rec.Body)
	}
	// A SCIM token is not an API credential, and an API key is not a SCIM credential.
	if rec := scimClient.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("scim token on the query API: %d", rec.Code)
	}
	if rec := apiKey.do(http.MethodGet, "/api/scim/v2/Users", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("API key on SCIM: %d", rec.Code)
	}
	if rec := admin.do(http.MethodDelete, "/api/v1/scim/tokens/"+created.Token.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke token: %d", rec.Code)
	}
	if rec := scimClient.do(http.MethodGet, "/api/scim/v2/Users", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked scim token: %d", rec.Code)
	}

	// Public sign-in endpoints.
	rec = anon.do(http.MethodPost, "/api/v1/auth/sso/discover", map[string]string{"email": "someone@example.com"})
	if d := decode[map[string]any](t, rec); rec.Code != http.StatusOK || d["sso"] != true || d["organization_name"] != "Org A" {
		t.Fatalf("discover: %d %v", rec.Code, d)
	}
	if d := decode[map[string]any](t, anon.do(http.MethodPost, "/api/v1/auth/sso/discover", map[string]string{"email": "someone@gmail.com"})); d["sso"] != false {
		t.Fatalf("discover other domain: %v", d)
	}
	form := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sso/start", strings.NewReader("email=someone@example.com"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	frec := httptest.NewRecorder()
	e.h.ServeHTTP(frec, form)
	if frec.Code != http.StatusBadRequest {
		t.Fatalf("form post to start: %d", frec.Code)
	}
	// The issuer (127.0.0.1:1) is unreachable: 503 without details.
	if rec := anon.do(http.MethodPost, "/api/v1/auth/sso/start", map[string]string{"email": "someone@example.com"}); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Fatalf("start with unreachable IdP: %d %s", rec.Code, rec.Body)
	}
	rec = anon.do(http.MethodGet, "/api/v1/sso/oidc/callback?state=bogus&code=x", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?sso_error=expired" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback with unknown state: %d %v", rec.Code, rec.Header())
	}
	if rec := anon.do(http.MethodGet, "/api/v1/sso/saml/"+conn["id"].(string)+"/metadata", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("metadata of an OIDC connection: %d", rec.Code)
	}
	if rec := anon.do(http.MethodGet, "/api/v1/sso/connection", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous connection: %d", rec.Code)
	}

	// Deleting the connection (admin) is audited.
	if rec := admin.do(http.MethodDelete, "/api/v1/sso/connection", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete connection: %d %s", rec.Code, rec.Body)
	}
	actions := map[string]bool{}
	for _, ev := range e.users.AuditEvents() {
		actions[ev.Action] = true
	}
	for _, a := range []string{"sso.connection.create", "sso.domain.add", "sso.domain.verify", "sso.role_mappings.update", "scim.token.create", "scim.token.revoke", "sso.connection.delete"} {
		if !actions[a] {
			t.Errorf("audit action %s missing", a)
		}
	}
}
