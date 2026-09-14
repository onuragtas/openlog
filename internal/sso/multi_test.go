package sso_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

func domainID(t *testing.T, env *testEnv, name string) string {
	t.Helper()
	ds, err := env.sso.ListDomains(context.Background(), env.ownerPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Domain == name {
			return d.ID
		}
	}
	t.Fatalf("domain %s not found", name)
	return ""
}

func addPasswordMember(t *testing.T, env *testEnv, email string, role auth.Role) {
	t.Helper()
	ctx := context.Background()
	hash, _ := auth.HashPassword("a long member password")
	now := time.Now()
	u := auth.User{Email: email, PasswordHash: hash, EmailVerifiedAt: &now}
	if err := env.users.CreateUser(ctx, &u); err != nil {
		t.Fatal(err)
	}
	if err := env.users.AddMember(ctx, env.org.ID, u.ID, role); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleConnections(t *testing.T) {
	idp1, idp2 := newFakeIdP(t), newFakeIdP(t)
	env := newTestEnv(t)
	ctx := context.Background()
	owner := env.ownerPrincipal()
	c1 := env.oidcConnection(t, idp1.srv.URL)
	secret := "client-secret"
	c2, err := env.sso.CreateConnection(ctx, owner, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Name: "Subsidiary", Enabled: true,
		Issuer: idp2.srv.URL, ClientID: "openlog", ClientSecret: &secret, DefaultRole: auth.RoleMember}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if cs, err := env.sso.ListConnections(ctx, owner); err != nil || len(cs) != 2 || cs[0].ID != c1.ID || !cs[1].AllowExternalInvitations {
		t.Fatalf("connections = %+v, %v", cs, err)
	}
	env.verifyDomain(t, "example.com")
	env.verifyDomain(t, "sub.example.com")
	sub := domainID(t, env, "sub.example.com")
	if d, err := env.sso.AssignDomain(ctx, owner, sub, c2.ID, auth.ClientMeta{}); err != nil || d.ConnectionID != c2.ID {
		t.Fatalf("assign = %+v, %v", d, err)
	}
	if _, err := env.sso.AssignDomain(ctx, owner, sub, "7c1e2d9a-3b4f-4e5a-8b6c-000000000009", auth.ClientMeta{}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("assign to an unknown connection: %v", err)
	}

	// Discovery and sign-in route by domain.
	if d, err := env.sso.Discover(ctx, "x@sub.example.com", testMeta()); err != nil || !d.SSO || d.ConnectionName != "Subsidiary" {
		t.Fatalf("discover sub = %+v, %v", d, err)
	}
	idp2.setClaims(map[string]any{"email": "carol@sub.example.com"})
	carol := runOIDCLogin(t, env, idp2, "carol@sub.example.com")
	if carol.Session == nil || carol.Session.Session.ConnectionID != c2.ID {
		t.Fatalf("carol through the subsidiary IdP: %+v", carol)
	}
	// The default connection's IdP cannot assert an address of a domain routed to another connection.
	idp1.setClaims(map[string]any{"email": "mallory@sub.example.com"})
	if out := runOIDCLogin(t, env, idp1, "alice@example.com"); out.Session != nil || out.Redirect != "/login?sso_error=domain_not_verified" {
		t.Fatalf("cross-connection identity: %+v", out)
	}
	idp1.setClaims(nil)

	// Role mappings per connection; a connection without own mappings uses the organization-wide ones.
	if _, err := env.sso.ReplaceRoleMappings(ctx, owner, c2.ID, []sso.RoleMapping{{Group: "admins", Role: auth.RoleAdmin}}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.sso.ReplaceRoleMappings(ctx, owner, "", []sso.RoleMapping{{Group: "eng", Role: auth.RoleMember}}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	idp2.setClaims(map[string]any{"email": "dan@sub.example.com"})
	if out := runOIDCLogin(t, env, idp2, "dan@sub.example.com"); out.Session == nil {
		t.Fatalf("dan: %+v", out)
	}
	if out := runOIDCLogin(t, env, idp1, "alice@example.com"); out.Session == nil {
		t.Fatalf("alice: %+v", out)
	}
	for email, want := range map[string]auth.Role{"dan@sub.example.com": auth.RoleAdmin, "alice@example.com": auth.RoleMember} {
		u, _ := env.users.GetUserByEmail(ctx, email)
		if m, err := env.users.GetMembership(ctx, env.org.ID, u.ID); err != nil || m.Role != want {
			t.Fatalf("%s role = %+v, %v (want %s)", email, m, err, want)
		}
	}

	// Enforcement applies to the domains of the enforcing connection only.
	addPasswordMember(t, env, "bob@example.com", auth.RoleMember)
	addPasswordMember(t, env, "erin@sub.example.com", auth.RoleMember)
	c2, _ = env.store.GetConnection(ctx, env.org.ID, c2.ID)
	if err := env.store.RecordTest(ctx, c2.ID, c2.ConfigVersion, true, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.sso.UpdateEnforcement(ctx, owner, c2.ID, true, []string{env.owner.ID}, auth.ClientMeta{}); err != nil {
		t.Fatalf("enforce subsidiary: %v", err)
	}
	if _, err := env.auth.Login(ctx, "bob@example.com", "a long member password", auth.ClientMeta{}); err != nil {
		t.Fatalf("bob (default connection, not enforced): %v", err)
	}
	if _, err := env.auth.Login(ctx, "erin@sub.example.com", "a long member password", auth.ClientMeta{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("erin (enforced subsidiary domain): %v", err)
	}
	// The last verified domain of the enforcing connection can be neither moved nor removed.
	if _, err := env.sso.AssignDomain(ctx, owner, sub, "", auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("move the enforced domain: %v", err)
	}
	if err := env.sso.DeleteDomain(ctx, owner, sub, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("remove the enforced domain: %v", err)
	}
	// Admins cannot change which members an enforcing connection covers.
	adminP := *owner
	adminP.Role = auth.RoleAdmin
	if _, err := env.sso.AssignDomain(ctx, &adminP, domainID(t, env, "example.com"), c2.ID, auth.ClientMeta{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("admin moves a domain into an enforcing connection: %v", err)
	}
	// The default connection cannot be deleted while verified domains depend on it and another connection exists.
	if err := env.sso.DeleteConnection(ctx, owner, c1.ID, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("delete the default connection with unassigned domains: %v", err)
	}
	// The default connection cannot be enforced through a domain it does not serve once all domains moved away.
	c1, _ = env.store.GetConnection(ctx, env.org.ID, c1.ID)
	_ = env.store.RecordTest(ctx, c1.ID, c1.ConfigVersion, true, "", nil, time.Now())
	if _, err := env.sso.AssignDomain(ctx, owner, domainID(t, env, "example.com"), c2.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.sso.UpdateEnforcement(ctx, owner, c1.ID, true, []string{env.owner.ID}, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("enforce a connection without routed domains: %v", err)
	}

	// Deleting a connection ends its sessions and returns its domains to the default connection.
	if _, err := env.sso.UpdateEnforcement(ctx, owner, c2.ID, false, []string{env.owner.ID}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := env.sso.DeleteConnection(ctx, owner, c2.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := principalFor(env, carol.Session.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("carol's session after connection delete: %v", err)
	}
	if d, err := env.sso.Discover(ctx, "x@sub.example.com", testMeta()); err != nil || !d.SSO || d.ConnectionName != "" {
		t.Fatalf("discover after delete = %+v, %v", d, err)
	}
	// The single-connection API updates the default connection.
	if c, err := env.sso.SaveConnection(ctx, owner, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Name: "Renamed", Enabled: true, Issuer: idp1.srv.URL,
		ClientID: "openlog"}, auth.ClientMeta{}); err != nil || c.ID != c1.ID {
		t.Fatalf("legacy save = %+v, %v", c, err)
	}
	for i := 1; i < 10; i++ {
		if _, err := env.sso.CreateConnection(ctx, owner, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Issuer: idp1.srv.URL, ClientID: "x"}, auth.ClientMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.sso.CreateConnection(ctx, owner, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Issuer: idp1.srv.URL, ClientID: "x"}, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("11th connection: %v", err)
	}
}
