package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// TestSSOConnectionsAndLogoutAPI covers several connections per organization, domain routing, per-connection role
// mappings, the refresh health, single logout endpoints and claimed-domain invitations (D-088, D-089).
func TestSSOConnectionsAndLogoutAPI(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.login(t, "owner@example.com")
	admin := e.login(t, "admin@example.com")
	member := e.login(t, "member@example.com")
	anon := &client{t: t, h: e.h}

	body := func(name string) map[string]any {
		return map[string]any{"protocol": "oidc", "name": name, "enabled": true, "default_role": "member",
			"logout_redirect_allowlist": []string{"/bye"}, "allow_external_invitations": false,
			"oidc": map[string]any{"issuer": "http://127.0.0.1:1", "client_id": "openlog"}}
	}
	type state struct {
		Connection *struct {
			ID                       string   `json:"id"`
			Name                     string   `json:"name"`
			Default                  bool     `json:"default"`
			AllowExternalInvitations bool     `json:"allow_external_invitations"`
			LogoutRedirectAllowlist  []string `json:"logout_redirect_allowlist"`
			Health                   struct {
				Status   string `json:"status"`
				Failures int    `json:"failures"`
			} `json:"health"`
		} `json:"connection"`
		Connections []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"connections"`
		ServiceProvider map[string]any `json:"service_provider"`
	}
	rec := admin.do(http.MethodPost, "/api/v1/sso/connections", body("Primary"))
	first := decode[state](t, rec)
	if rec.Code != http.StatusCreated || first.Connection == nil || !first.Connection.Default || first.Connection.AllowExternalInvitations ||
		first.Connection.LogoutRedirectAllowlist[0] != "/bye" || first.Connection.Health.Status != "unknown" ||
		first.ServiceProvider["oidc_post_logout_redirect_uri"] != "https://openlog.example/api/v1/sso/oidc/logout/callback" {
		t.Fatalf("create first: %d %s", rec.Code, rec.Body)
	}
	rec = admin.do(http.MethodPost, "/api/v1/sso/connections", body("Subsidiary"))
	second := decode[state](t, rec)
	if rec.Code != http.StatusCreated || second.Connection.Default || len(second.Connections) != 2 || second.Connections[0].ID != first.Connection.ID {
		t.Fatalf("create second: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodGet, "/api/v1/sso/connections", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member list: %d", rec.Code)
	}
	if got := decode[state](t, admin.do(http.MethodGet, "/api/v1/sso/connections", nil)); got.Connection != nil || len(got.Connections) != 2 {
		t.Fatalf("list = %+v", got)
	}
	secondID := second.Connection.ID
	if got := decode[state](t, admin.do(http.MethodGet, "/api/v1/sso/connections/"+secondID, nil)); got.Connection == nil || got.Connection.Name != "Subsidiary" {
		t.Fatalf("get by id = %+v", got)
	}
	upd := body("Subsidiary EU")
	if rec := admin.do(http.MethodPut, "/api/v1/sso/connections/"+secondID, upd); rec.Code != http.StatusOK || decode[state](t, rec).Connection.Name != "Subsidiary EU" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodGet, "/api/v1/sso/connections/7c1e2d9a-3b4f-4e5a-8b6c-000000000009", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connection: %d", rec.Code)
	}
	// The single-connection API addresses the default connection.
	if got := decode[state](t, admin.do(http.MethodGet, "/api/v1/sso/connection", nil)); got.Connection == nil || got.Connection.ID != first.Connection.ID {
		t.Fatalf("legacy get = %+v", got)
	}

	// Domain routing.
	dom := decode[domainJSON](t, admin.do(http.MethodPost, "/api/v1/sso/domains", map[string]string{"domain": "example.com"}))
	e.resolver[dom.DNSRecord["name"]] = []string{dom.DNSRecord["value"]}
	if rec := admin.do(http.MethodPost, "/api/v1/sso/domains/"+dom.ID+"/verify", map[string]string{"method": "dns_txt"}); rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	rec = admin.do(http.MethodPut, "/api/v1/sso/domains/"+dom.ID, map[string]any{"connection_id": secondID})
	if d := decode[domainJSON](t, rec); rec.Code != http.StatusOK || d.ConnectionID == nil || *d.ConnectionID != secondID {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body)
	}
	if d := decode[map[string]any](t, anon.do(http.MethodPost, "/api/v1/auth/sso/discover", map[string]string{"email": "x@example.com"})); d["connection_name"] != "Subsidiary EU" {
		t.Fatalf("discover = %v", d)
	}
	if rec := admin.do(http.MethodPut, "/api/v1/sso/domains/"+dom.ID, map[string]any{"connection_id": "7c1e2d9a-3b4f-4e5a-8b6c-000000000009"}); rec.Code != http.StatusNotFound {
		t.Fatalf("assign to unknown connection: %d", rec.Code)
	}
	rec = admin.do(http.MethodPut, "/api/v1/sso/domains/"+dom.ID, map[string]any{"connection_id": nil})
	if d := decode[domainJSON](t, rec); rec.Code != http.StatusOK || d.ConnectionID != nil {
		t.Fatalf("assign default: %d %s", rec.Code, rec.Body)
	}

	// Per-connection role mappings next to the organization-wide ones.
	rec = admin.do(http.MethodPut, "/api/v1/sso/connections/"+secondID+"/role-mappings", map[string]any{"mappings": []map[string]string{{"group": "eu-admins", "role": "admin"}}})
	if m := decode[map[string]any](t, rec); rec.Code != http.StatusOK || m["connection_id"] != secondID {
		t.Fatalf("connection mappings: %d %s", rec.Code, rec.Body)
	}
	if m := decode[map[string]any](t, admin.do(http.MethodGet, "/api/v1/sso/role-mappings", nil)); len(m["mappings"].([]any)) != 0 || m["connection_id"] != nil {
		t.Fatalf("organization-wide mappings = %v", m)
	}

	// Refresh now: the IdP is unreachable, the health shows it.
	rec = admin.do(http.MethodPost, "/api/v1/sso/connections/"+secondID+"/refresh", nil)
	if got := decode[state](t, rec); rec.Code != http.StatusOK || got.Connection.Health.Status != "warning" || got.Connection.Health.Failures != 1 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}

	// Claimed-domain invitations: example.com routes to the enabled default connection of this organization.
	rec = owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "newbie@example.com", "role": "member"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	token := decode[map[string]any](t, rec)["token"].(string)
	rec = anon.do(http.MethodPost, "/api/v1/invitations/lookup", map[string]string{"token": token})
	lookup := decode[map[string]any](t, rec)
	if sso, _ := lookup["sso"].(map[string]any); rec.Code != http.StatusOK || sso == nil || sso["required"] != true || sso["same_organization"] != true || sso["organization_name"] != "Org A" {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body)
	}
	if rec := anon.do(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": token, "password": "a long enough password"}); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "single sign-on") {
		t.Fatalf("password acceptance: %d %s", rec.Code, rec.Body)
	}

	// Delete the second connection.
	if rec := admin.do(http.MethodDelete, "/api/v1/sso/connections/"+secondID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodGet, "/api/v1/sso/connections/"+secondID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted connection: %d", rec.Code)
	}

	// Single logout endpoints.
	rec = member.do(http.MethodGet, "/api/v1/auth/sso/session", nil)
	if s := decode[map[string]any](t, rec); rec.Code != http.StatusOK || s["sso"] != false || s["idp_logout"] != false {
		t.Fatalf("session info: %d %s", rec.Code, rec.Body)
	}
	noCSRF := *member
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPost, "/api/v1/auth/sso/logout", map[string]string{}); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", rec.Code)
	}
	rec = member.do(http.MethodPost, "/api/v1/auth/sso/logout", map[string]string{"redirect": "/bye"})
	out := decode[map[string]any](t, rec)
	cleared := false
	for _, ck := range rec.Result().Cookies() {
		cleared = cleared || (ck.Name == auth.DefaultCookieName && ck.MaxAge < 0)
	}
	if rec.Code != http.StatusOK || out["redirect_url"] != nil || out["post"] != nil || !cleared {
		t.Fatalf("local logout: %d %s %v", rec.Code, rec.Body, rec.Result().Cookies())
	}
	if rec := member.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", rec.Code)
	}
	rec = anon.do(http.MethodGet, "/api/v1/sso/saml/7c1e2d9a-3b4f-4e5a-8b6c-000000000009/slo?SAMLRequest=x", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?sso_error=disabled" {
		t.Fatalf("SLO of an unknown connection: %d %v", rec.Code, rec.Header())
	}
	rec = anon.do(http.MethodGet, "/api/v1/sso/oidc/logout/callback?state=unknown", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout callback with unknown state: %d %v", rec.Code, rec.Header())
	}
	if rec := anon.do(http.MethodPost, "/api/v1/auth/sso/logout", map[string]string{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous logout: %d", rec.Code)
	}
}
