package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// createKey creates an API key through the API and returns its plaintext.
func createKey(t *testing.T, c *client, name, role string) string {
	t.Helper()
	body := map[string]any{"name": name}
	if role != "" {
		body["role"] = role
	}
	rec := c.do(http.MethodPost, "/api/v1/api-keys", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q key: %d %s", role, rec.Code, rec.Body)
	}
	res := decode[struct {
		APIKey apiKeyJSON `json:"api_key"`
		Key    string     `json:"key"`
	}](t, rec)
	wantRole, wantScope := role, "write"
	if wantRole == "" {
		wantRole = "viewer"
	}
	if wantRole == "viewer" {
		wantScope = "read"
	}
	if res.APIKey.Role != wantRole || res.APIKey.Scope != wantScope {
		t.Fatalf("created key role/scope = %q/%q, want %q/%q", res.APIKey.Role, res.APIKey.Scope, wantRole, wantScope)
	}
	return res.Key
}

// addMember invites email with role and returns a signed-in client for it.
func (e *accountEnv) addMember(t *testing.T, owner *client, email, role string) *client {
	t.Helper()
	rec := owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": role})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite %s: %d %s", email, rec.Code, rec.Body)
	}
	inv := decode[struct {
		Token string `json:"token"`
	}](t, rec)
	anon := &client{t: t, h: e.h}
	if rec := anon.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": inv.Token, "password": ownerPassword, "name": "Mia"}); rec.Code != http.StatusOK {
		t.Fatalf("accept invitation: %d %s", rec.Code, rec.Body)
	}
	return e.login(t, email, ownerPassword)
}

// TestAPIKeyRoleGatesWrites: a read-only key is still refused on a write, and a key
// whose role allows it succeeds on the very same endpoint (D-133).
func TestAPIKeyRoleGatesWrites(t *testing.T) {
	e := newAccountEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)

	readOnly := createKey(t, owner, "reader", "") // no role given: the default stays read-only
	writer := createKey(t, owner, "terraform", "admin")
	rename := map[string]string{"name": "Renamed by automation"}

	rec := (&client{t: t, h: e.h, bearer: readOnly}).do(http.MethodPatch, "/api/v1/orgs/current", rename)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only key write: %d %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "this API key's role (viewer) does not allow this operation") {
		t.Errorf("read-only key message = %s", body)
	}

	if rec := (&client{t: t, h: e.h, bearer: writer}).do(http.MethodPatch, "/api/v1/orgs/current", rename); rec.Code != http.StatusOK {
		t.Fatalf("writing key on the same endpoint: %d %s", rec.Code, rec.Body)
	}

	// The boundary in both directions: a key reads what its role allows, but never acts
	// on the caller's own account or creates credentials, whatever its role.
	for _, tc := range []struct {
		method, path string
		body         any
		status       int
	}{
		{http.MethodGet, "/api/v1/license-keys", nil, http.StatusOK},
		{http.MethodGet, "/api/v1/api-keys", nil, http.StatusOK},
		{http.MethodGet, "/api/v1/sessions", nil, http.StatusForbidden},
		{http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "x"}, http.StatusForbidden},
		{http.MethodPost, "/api/v1/license-keys", map[string]any{"name": "x"}, http.StatusForbidden},
	} {
		rec := (&client{t: t, h: e.h, bearer: writer}).do(tc.method, tc.path, tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s %s with an admin key = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.status, rec.Body)
		}
	}
}

// TestAPIKeyRoleCreationRequiresAdmin: only an admin or owner may create a key that
// can write, and no key is more capable than the role that created it.
func TestAPIKeyRoleCreationRequiresAdmin(t *testing.T) {
	e := newAccountEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)
	member := e.addMember(t, owner, "member@example.com", "member")

	if rec := member.do(http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ok"}); rec.Code != http.StatusCreated {
		t.Fatalf("member creates a read-only key: %d %s", rec.Code, rec.Body)
	}
	rec := member.do(http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "nope", "role": "member"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member creates a writing key: %d %s", rec.Code, rec.Body)
	}

	createKey(t, owner, "adm", "admin") // an owner may
	for _, role := range []string{"owner", "root"} {
		if rec := owner.do(http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "bad", "role": role}); rec.Code != http.StatusBadRequest {
			t.Errorf("key with role %q: %d %s", role, rec.Code, rec.Body)
		}
	}
}

// TestAPIKeyAuditNamesTheKey: a change made with a key is attributed to the key and
// never to a user, and GET /auth/me says which key is calling.
func TestAPIKeyAuditNamesTheKey(t *testing.T) {
	e := newAccountEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)
	kc := &client{t: t, h: e.h, bearer: createKey(t, owner, "terraform", "admin")}

	me := decode[meJSON](t, kc.do(http.MethodGet, "/api/v1/auth/me", nil))
	if me.Auth != string(auth.KindAPIKey) || me.APIKey == nil || me.APIKey.Name != "terraform" || me.APIKey.Role != "admin" ||
		me.APIKey.ID == "" || me.User != nil || me.CSRFToken != nil || me.Role == nil || *me.Role != "admin" {
		t.Fatalf("me for an API key = %+v", me)
	}

	if rec := kc.do(http.MethodPatch, "/api/v1/orgs/current", map[string]string{"name": "Renamed"}); rec.Code != http.StatusOK {
		t.Fatalf("rename with key: %d %s", rec.Code, rec.Body)
	}

	type auditPage struct {
		Events []struct {
			ActorEmail  string `json:"actor_email"`
			ActorAPIKey *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"actor_api_key"`
			Action string `json:"action"`
		} `json:"events"`
	}
	page := decode[auditPage](t, owner.do(http.MethodGet, "/api/v1/audit-log?action=org.rename", nil))
	if len(page.Events) != 1 {
		t.Fatalf("org.rename events = %+v", page.Events)
	}
	switch ev := page.Events[0]; {
	case ev.ActorAPIKey == nil || ev.ActorAPIKey.Name != "terraform" || ev.ActorAPIKey.ID == "":
		t.Errorf("audit event does not name the key: %+v", ev)
	case ev.ActorEmail != "":
		t.Errorf("audit event claims a user made the change: actor_email = %q", ev.ActorEmail)
	}

	// An event a user made still names the user, and the actor filter finds a key by name.
	byKey := decode[auditPage](t, owner.do(http.MethodGet, "/api/v1/audit-log?actor=terraform", nil))
	if len(byKey.Events) != 1 || byKey.Events[0].Action != "org.rename" {
		t.Errorf("actor filter by key name = %+v", byKey.Events)
	}
	byUser := decode[auditPage](t, owner.do(http.MethodGet, "/api/v1/audit-log?action=api_key.create", nil))
	if len(byUser.Events) == 0 {
		t.Fatal("no api_key.create event")
	}
	if ev := byUser.Events[0]; ev.ActorEmail != "owner@example.com" || ev.ActorAPIKey != nil {
		t.Errorf("key creation by a user = %+v", ev)
	}
}
