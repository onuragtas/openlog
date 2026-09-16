package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/scim"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/sso/ssomem"
)

type env struct {
	t     *testing.T
	users *memstore.Store
	auth  *auth.Service
	sso   *sso.Service
	h     http.Handler
	org   auth.Organization
	owner *auth.Principal
	token string
}

func newEnv(t *testing.T, tenant, domain string, users *memstore.Store) *env {
	t.Helper()
	ctx := context.Background()
	if users == nil {
		users = memstore.New()
	}
	as := auth.NewService(users, auth.Config{}, nil)
	ownerEmail := "owner@" + domain
	res, err := as.Bootstrap(ctx, auth.BootstrapSpec{TenantID: tenant, OrgName: tenant, OwnerEmail: ownerEmail, OwnerPassword: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := users.GetUserByEmail(ctx, ownerEmail)
	store := ssomem.New(users)
	svc := sso.NewService(as, store, sso.Config{PublicURL: "https://openlog.example", SCIMEnabled: true}, nil)
	as.SetSessionPolicy(svc.Policy())
	p := &auth.Principal{Kind: auth.KindSession, UserID: owner.ID, Email: owner.Email, EmailVerified: true, OrgID: res.Org.ID,
		TenantID: res.Org.TenantID, Role: auth.RoleOwner}
	d := sso.Domain{OrgID: res.Org.ID, Domain: domain, DNSToken: "t"}
	if err := store.CreateDomain(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDomainVerified(ctx, res.Org.ID, d.ID, "dns_txt", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, secret, err := svc.CreateSCIMToken(ctx, p, "okta", nil, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, users: users, auth: as, sso: svc, h: scim.NewHandler(svc, as.Meta, nil), org: res.Org, owner: p, token: secret}
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	return e.doToken(e.token, method, path, body)
}

func (e *env) doToken(token, method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, scim.BasePath+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			e.t.Fatalf("%s %s: invalid JSON %q", method, path, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/scim+json" {
			e.t.Fatalf("Content-Type = %q", ct)
		}
	}
	return rec.Code, out
}

func (e *env) role(email string) auth.Role {
	e.t.Helper()
	u, err := e.users.GetUserByEmail(context.Background(), email)
	if err != nil {
		return ""
	}
	m, err := e.users.GetMembership(context.Background(), e.org.ID, u.ID)
	if err != nil {
		return ""
	}
	return m.Role
}

func patch(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": ops}
}

func TestSCIMProvisioning(t *testing.T) {
	e := newEnv(t, "tenant-a", "example.com", nil)
	ctx := context.Background()
	if _, err := e.sso.ReplaceRoleMappings(ctx, e.owner, "", []sso.RoleMapping{{Group: "openlog-admins", Role: auth.RoleAdmin}}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}

	// Authentication.
	if code, body := e.doToken("", http.MethodGet, "/Users", nil); code != http.StatusUnauthorized || body["status"] != "401" {
		t.Fatalf("no token: %d %v", code, body)
	}
	if code, _ := e.doToken("ols_wrong", http.MethodGet, "/Users", nil); code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", code)
	}
	if code, body := e.do(http.MethodGet, "/ServiceProviderConfig", nil); code != http.StatusOK || body["patch"].(map[string]any)["supported"] != true {
		t.Fatalf("ServiceProviderConfig: %d %v", code, body)
	}
	if code, _ := e.do(http.MethodGet, "/ResourceTypes", nil); code != http.StatusOK {
		t.Fatalf("ResourceTypes: %d", code)
	}

	// Create.
	code, alice := e.do(http.MethodPost, "/Users", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": "alice@example.com", "externalId": "00u1",
		"name": map[string]string{"givenName": "Alice", "familyName": "Liddell"}, "active": true,
		"emails": []map[string]any{{"value": "alice@example.com", "primary": true, "type": "work"}},
	})
	if code != http.StatusCreated || alice["id"] == "" || alice["active"] != true {
		t.Fatalf("create: %d %v", code, alice)
	}
	aliceID := alice["id"].(string)
	if r := e.role("alice@example.com"); r != auth.RoleViewer {
		t.Fatalf("role after create = %q", r)
	}
	if code, body := e.do(http.MethodPost, "/Users", map[string]any{"userName": "ALICE@example.com"}); code != http.StatusConflict || body["scimType"] != "uniqueness" {
		t.Fatalf("duplicate: %d %v", code, body)
	}
	if code, _ := e.do(http.MethodPost, "/Users", map[string]any{"userName": "eve@other.example"}); code != http.StatusBadRequest {
		t.Fatalf("unverified domain: %d", code)
	}

	// Filter and paging.
	code, list := e.do(http.MethodGet, "/Users?filter="+url.QueryEscape(`userName eq "alice@example.com"`), nil)
	if code != http.StatusOK || list["totalResults"].(float64) != 1 {
		t.Fatalf("filter: %d %v", code, list)
	}
	if code, list := e.do(http.MethodGet, "/Users?filter="+url.QueryEscape(`externalId eq "nope"`), nil); code != http.StatusOK || list["totalResults"].(float64) != 0 {
		t.Fatalf("filter none: %d %v", code, list)
	}
	if code, body := e.do(http.MethodGet, "/Users?filter="+url.QueryEscape(`userName sw "a"`), nil); code != http.StatusBadRequest || body["scimType"] != "invalidFilter" {
		t.Fatalf("unsupported filter: %d %v", code, body)
	}

	// Groups map to roles.
	code, group := e.do(http.MethodPost, "/Groups", map[string]any{"displayName": "openlog-admins", "members": []map[string]string{{"value": aliceID}}})
	if code != http.StatusCreated {
		t.Fatalf("group create: %d %v", code, group)
	}
	gid := group["id"].(string)
	if r := e.role("alice@example.com"); r != auth.RoleAdmin {
		t.Fatalf("role after group add = %q", r)
	}
	if code, _ := e.do(http.MethodPatch, "/Groups/"+gid, patch(map[string]any{"op": "remove", "path": `members[value eq "` + aliceID + `"]`})); code != http.StatusOK {
		t.Fatalf("remove member: %d", code)
	}
	if r := e.role("alice@example.com"); r != auth.RoleViewer {
		t.Fatalf("role after group remove = %q", r)
	}
	if code, _ := e.do(http.MethodPatch, "/Groups/"+gid, patch(map[string]any{"op": "add", "path": "members", "value": []map[string]string{{"value": aliceID}}})); code != http.StatusOK {
		t.Fatalf("add member: %d", code)
	}
	if code, g := e.do(http.MethodGet, "/Groups/"+gid+"?excludedAttributes=members", nil); code != http.StatusOK || g["members"] != nil {
		t.Fatalf("excludedAttributes: %d %v", code, g)
	}
	if code, _ := e.do(http.MethodPost, "/Groups", map[string]any{"displayName": "x", "members": []map[string]string{{"value": "unknown"}}}); code != http.StatusBadRequest {
		t.Fatalf("unknown member: %d", code)
	}

	// Deactivation (Azure AD style string boolean) removes the membership immediately and revokes the user's
	// SSO sessions of the organization and the API keys they created.
	u, _ := e.users.GetUserByEmail(ctx, "alice@example.com")
	res, err := e.auth.StartExternalSession(ctx, u, auth.SessionBinding{Method: auth.MethodSAML, OrgID: e.org.ID}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	alicePrincipal := &auth.Principal{Kind: auth.KindSession, UserID: u.ID, Email: u.Email, EmailVerified: true, OrgID: e.org.ID, TenantID: e.org.TenantID, Role: auth.RoleAdmin}
	if _, _, err := e.auth.CreateAPIKey(ctx, alicePrincipal, "grafana", auth.RoleViewer, nil, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(http.MethodPatch, "/Users/"+aliceID, patch(map[string]any{"op": "Replace", "path": "active", "value": "False"}))
	if code != http.StatusOK || body["active"] != false {
		t.Fatalf("deactivate: %d %v", code, body)
	}
	if r := e.role("alice@example.com"); r != "" {
		t.Fatalf("membership kept after deactivation: %q", r)
	}
	if ss, _ := e.users.ListSessions(ctx, u.ID, time.Now()); len(ss) != 0 {
		t.Fatalf("sessions not revoked: %d (%s)", len(ss), res.Session.ID)
	}
	keys, _ := e.users.ListAPIKeys(ctx, e.org.ID)
	for _, k := range keys {
		if k.CreatedBy == u.ID && k.RevokedAt == nil {
			t.Fatal("API key of the deprovisioned user not revoked")
		}
	}
	// Reactivation restores the membership with the group role (path-less replace, Okta style).
	if code, _ := e.do(http.MethodPatch, "/Users/"+aliceID, patch(map[string]any{"op": "replace", "value": map[string]any{"active": true, "displayName": "Alice L."}})); code != http.StatusOK {
		t.Fatalf("reactivate: %d", code)
	}
	if r := e.role("alice@example.com"); r != auth.RoleAdmin {
		t.Fatalf("role after reactivation = %q", r)
	}
	// PUT replace.
	if code, body := e.do(http.MethodPut, "/Users/"+aliceID, map[string]any{"userName": "alice@example.com", "externalId": "00u1", "active": true,
		"name": map[string]string{"givenName": "Alice", "familyName": "Smith"}}); code != http.StatusOK || body["name"].(map[string]any)["familyName"] != "Smith" {
		t.Fatalf("put: %d %v", code, body)
	}
	// E-mail change (D-089): a userName that was the account's address moves the account to the new address.
	if code, body := e.do(http.MethodPatch, "/Users/"+aliceID, patch(map[string]any{"op": "replace", "path": "userName", "value": "alice2@example.com"})); code != http.StatusOK ||
		body["emails"].([]any)[0].(map[string]any)["value"] != "alice2@example.com" {
		t.Fatalf("e-mail change: %d %v", code, body)
	}
	if _, err := e.users.GetUserByEmail(ctx, "alice@example.com"); err == nil {
		t.Fatal("old address still resolves")
	}

	// Owners cannot be deprovisioned through SCIM.
	code, owner := e.do(http.MethodPost, "/Users", map[string]any{"userName": "owner@example.com"})
	if code != http.StatusCreated {
		t.Fatalf("owner create: %d %v", code, owner)
	}
	if code, body := e.do(http.MethodPatch, "/Users/"+owner["id"].(string), patch(map[string]any{"op": "replace", "path": "active", "value": false})); code != http.StatusBadRequest || body["scimType"] != "mutability" {
		t.Fatalf("owner deactivate: %d %v", code, body)
	}
	if code, _ := e.do(http.MethodDelete, "/Users/"+owner["id"].(string), nil); code != http.StatusBadRequest {
		t.Fatalf("owner delete: %d", code)
	}
	if r := e.role("owner@example.com"); r != auth.RoleOwner {
		t.Fatalf("owner role = %q", r)
	}

	// Tenant isolation: another organization's token does not see these users.
	other := newEnv(t, "tenant-b", "other.example", e.users)
	if code, _ := other.do(http.MethodGet, "/Users/"+aliceID, nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: %d", code)
	}
	if code, list := other.do(http.MethodGet, "/Users", nil); code != http.StatusOK || list["totalResults"].(float64) != 0 {
		t.Fatalf("cross-tenant list: %d %v", code, list)
	}

	// Delete.
	if code, _ := e.do(http.MethodDelete, "/Users/"+aliceID, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(http.MethodGet, "/Users/"+aliceID, nil); code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", code)
	}
	if r := e.role("alice@example.com"); r != "" {
		t.Fatalf("membership after delete = %q", r)
	}
	if code, _ := e.do(http.MethodDelete, "/Groups/"+gid, nil); code != http.StatusNoContent {
		t.Fatalf("group delete: %d", code)
	}

	// Audit trail and token revocation.
	var created, deactivated bool
	for _, ev := range e.users.AuditEvents() {
		created = created || ev.Action == "scim.user.create"
		deactivated = deactivated || ev.Action == "scim.user.deactivate"
	}
	if !created || !deactivated {
		t.Fatal("SCIM audit events missing")
	}
	tokens, _ := e.sso.ListSCIMTokens(ctx, e.owner)
	if err := e.sso.RevokeSCIMToken(ctx, e.owner, tokens[0].ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(http.MethodGet, "/Users", nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
}

// TestSCIMEmailChange covers e-mail changes through PUT and PATCH (D-089).
func TestSCIMEmailChange(t *testing.T) {
	e := newEnv(t, "tenant-a", "example.com", nil)
	ctx := context.Background()
	code, alice := e.do(http.MethodPost, "/Users", map[string]any{"userName": "alice@example.com", "active": true})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, alice)
	}
	id := alice["id"].(string)
	accountEmail := func() string {
		u, err := e.users.GetUser(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u.Email
	}

	// PUT with a new primary e-mail (Entra style: userName stays the UPN).
	if code, body := e.do(http.MethodPut, "/Users/"+id, map[string]any{"userName": "alice@example.com", "active": true,
		"emails": []map[string]any{{"value": "Alice.Liddell@example.com", "primary": true, "type": "work"}}}); code != http.StatusOK || accountEmail() != "alice.liddell@example.com" {
		t.Fatalf("PUT e-mail change: %d %v (%s)", code, body, accountEmail())
	}
	// PATCH emails[type eq "work"].value.
	if code, body := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "path": `emails[type eq "work"].value`, "value": "alice3@example.com"})); code != http.StatusOK || accountEmail() != "alice3@example.com" {
		t.Fatalf("PATCH emails[type eq work]: %d %v", code, body)
	}
	// Path-less PATCH (Okta style).
	if code, _ := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "value": map[string]any{"emails": []map[string]any{{"value": "alice4@example.com", "primary": true}}}})); code != http.StatusOK || accountEmail() != "alice4@example.com" {
		t.Fatalf("path-less PATCH: %d %s", code, accountEmail())
	}

	// Another account already uses the address: 409 uniqueness, nothing changes.
	hash, _ := auth.HashPassword("bob's long password")
	now := time.Now()
	bob := auth.User{Email: "bob@example.com", PasswordHash: hash, EmailVerifiedAt: &now}
	if err := e.users.CreateUser(ctx, &bob); err != nil {
		t.Fatal(err)
	}
	if code, body := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "path": "emails", "value": []map[string]any{{"value": "bob@example.com", "primary": true}}})); code != http.StatusConflict || body["scimType"] != "uniqueness" || accountEmail() != "alice4@example.com" {
		t.Fatalf("conflicting e-mail: %d %v", code, body)
	}
	// A domain the organization has not verified.
	if code, body := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "path": `emails[primary eq true].value`, "value": "alice@gmail.com"})); code != http.StatusBadRequest || body["scimType"] != "invalidValue" {
		t.Fatalf("unverified domain: %d %v", code, body)
	}
	if code, _ := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "path": "emails", "value": []map[string]any{{"value": "not an address", "primary": true}}})); code != http.StatusBadRequest {
		t.Fatalf("invalid address: %d", code)
	}
	// Owners' addresses are managed in openlog.
	code, owner := e.do(http.MethodPost, "/Users", map[string]any{"userName": "owner@example.com"})
	if code != http.StatusCreated {
		t.Fatalf("owner create: %d", code)
	}
	if code, body := e.do(http.MethodPatch, "/Users/"+owner["id"].(string), patch(map[string]any{"op": "replace", "path": "userName", "value": "boss@example.com"})); code != http.StatusBadRequest || body["scimType"] != "mutability" {
		t.Fatalf("owner e-mail change: %d %v", code, body)
	}
	// A changed userName that was not the account's address does not change the address.
	if code, _ := e.do(http.MethodPatch, "/Users/"+id, patch(map[string]any{"op": "replace", "path": "userName", "value": "alice-upn@example.com"})); code != http.StatusOK || accountEmail() != "alice4@example.com" {
		t.Fatalf("userName change: %d %s", code, accountEmail())
	}
	changes := 0
	for _, ev := range e.users.AuditEvents() {
		if ev.Action == "scim.user.email_change" {
			changes++
		}
	}
	if changes != 3 {
		t.Fatalf("scim.user.email_change audit events = %d", changes)
	}
}
