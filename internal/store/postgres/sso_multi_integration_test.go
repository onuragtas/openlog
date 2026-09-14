//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// TestSSOStoreConnectionsAndLogout covers 0056_sso_connections_slo: several connections per organization, domain
// routing, per-connection role mappings, the refresh state, SSO session links and the e-mail change.
func TestSSOStoreConnectionsAndLogout(t *testing.T) {
	migrate(t)
	ctx := context.Background()
	users := postgres.NewStore(pool)
	st := postgres.NewSSOStore(pool)
	e := authtest.NewEnv(t, users, auth.Config{})
	orgA, ownerA := e.Bootstrap("ssom-a")
	orgB, _ := e.Bootstrap("ssom-b")
	now := time.Now().UTC().Truncate(time.Microsecond)

	newConn := func(org string, at time.Time, protocol sso.Protocol) sso.Connection {
		c := sso.Connection{ID: uuid.NewString(), OrgID: org, Protocol: protocol, Enabled: true, DefaultRole: auth.RoleViewer, ConfigVersion: 1,
			AllowExternalInvitations: true, CreatedAt: at}
		if protocol == sso.ProtocolOIDC {
			c.OIDC = &sso.OIDCConfig{Issuer: "https://idp.example", ClientID: "cid"}
		} else {
			c.SAML = &sso.SAMLConfig{IdPMetadataXML: "<md/>", IdPEntityID: "https://idp.example/md", IdPCertificates: []string{"AA"}}
		}
		if err := st.CreateConnection(ctx, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1 := newConn(orgA.ID, now, sso.ProtocolOIDC)
	c2 := newConn(orgA.ID, now.Add(time.Second), sso.ProtocolOIDC)
	cB := newConn(orgB.ID, now, sso.ProtocolOIDC)
	if got, err := st.GetConnection(ctx, orgA.ID, ""); err != nil || got.ID != c1.ID {
		t.Fatalf("default connection = %+v, %v", got, err)
	}
	if _, err := st.GetConnection(ctx, orgA.ID, cB.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("connection of another organization: %v", err)
	}

	// Domain routing: default connection, explicit connection, never another organization's connection.
	domain := "ssom-" + uuid.NewString()[:8] + ".example" // verified domains are unique across runs on one database
	d := sso.Domain{OrgID: orgA.ID, Domain: domain, DNSToken: "t"}
	if err := st.CreateDomain(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkDomainVerified(ctx, orgA.ID, d.ID, "dns_txt", now); err != nil {
		t.Fatal(err)
	}
	if c, dd, err := st.ConnectionForDomain(ctx, domain); err != nil || c.ID != c1.ID || dd.ConnectionID != "" {
		t.Fatalf("routed to default = %v %+v %v", c.ID, dd, err)
	}
	if _, err := st.SetDomainConnection(ctx, orgA.ID, d.ID, cB.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("route to another organization's connection: %v", err)
	}
	if dd, err := st.SetDomainConnection(ctx, orgA.ID, d.ID, c2.ID); err != nil || dd.ConnectionID != c2.ID {
		t.Fatalf("route to c2 = %+v, %v", dd, err)
	}
	if c, _, err := st.ConnectionForDomain(ctx, domain); err != nil || c.ID != c2.ID {
		t.Fatalf("routed to c2 = %v %v", c.ID, err)
	}

	// Policy: session connection and the enforcement of the routed connection, in one statement.
	c2.Enforce, c2.BreakGlassUserIDs = true, []string{ownerA.P.UserID}
	if err := st.UpdateConnection(ctx, &c2); err != nil {
		t.Fatal(err)
	}
	pol, err := st.GetOrgPolicy(ctx, orgA.ID, c1.ID, domain)
	if err != nil || pol.ConnectionID != c1.ID || !pol.Enabled || !pol.DomainVerified || !pol.Enforce || len(pol.BreakGlassUserIDs) != 1 {
		t.Fatalf("policy = %+v, %v", pol, err)
	}
	if pol, err := st.GetOrgPolicy(ctx, orgA.ID, "", "other.example"); err != nil || pol.ConnectionID != "" || pol.DomainVerified || pol.Enforce {
		t.Fatalf("policy without session connection and domain = %+v, %v", pol, err)
	}
	if pol, err := st.GetOrgPolicy(ctx, orgA.ID, "not-a-uuid", ""); err != nil || pol.ConnectionID != "" {
		t.Fatalf("policy with an invalid connection id = %+v, %v", pol, err)
	}

	// Role mappings per connection.
	if err := st.ReplaceRoleMappings(ctx, orgA.ID, "", []sso.RoleMapping{{Group: "g", Role: auth.RoleMember}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceRoleMappings(ctx, orgA.ID, c2.ID, []sso.RoleMapping{{Group: "g", Role: auth.RoleAdmin}, {Group: "h", Role: auth.RoleViewer}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceRoleMappings(ctx, orgB.ID, c2.ID, []sso.RoleMapping{{Group: "x", Role: auth.RoleAdmin}}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("mappings of another organization's connection: %v", err)
	}
	if ms, _ := st.ListRoleMappings(ctx, orgA.ID, ""); len(ms) != 1 || ms[0].Role != auth.RoleMember {
		t.Fatalf("organization-wide mappings = %v", ms)
	}
	if ms, _ := st.ListRoleMappings(ctx, orgA.ID, c2.ID); len(ms) != 2 || ms[0].Role != auth.RoleAdmin {
		t.Fatalf("connection mappings = %v", ms)
	}

	// Refresh state.
	issued := now
	cache := &sso.IdPCache{FetchedAt: &issued, Issuer: "https://idp.example", OIDCDiscovery: json.RawMessage(`{"issuer":"https://idp.example"}`), OIDCJWKS: json.RawMessage(`{"keys":[]}`)}
	if err := st.RecordRefresh(ctx, c1.ID, true, "", cache, now.Add(30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRefresh(ctx, c1.ID, false, "boom", nil, now.Add(5*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetConnectionByID(ctx, c1.ID)
	if got.Refresh.OK == nil || *got.Refresh.OK || got.Refresh.Failures != 1 || got.Refresh.Error != "boom" || got.Refresh.Cache.Issuer != "https://idp.example" {
		t.Fatalf("refresh state = %+v", got.Refresh)
	}
	if n, err := st.CountRefreshFailing(ctx); err != nil || n < 1 {
		t.Fatalf("failing = %d, %v", n, err)
	}
	due, err := st.ListRefreshDue(ctx, now.Add(time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var dueIDs []string
	for _, c := range due {
		dueIDs = append(dueIDs, c.ID)
	}
	if !contains(dueIDs, c2.ID) || contains(dueIDs, c1.ID) {
		t.Fatalf("due = %v (c1 %s, c2 %s)", dueIDs, c1.ID, c2.ID)
	}
	// Refreshed SAML metadata: only while the version is unchanged, and without incrementing it.
	c3 := newConn(orgA.ID, now.Add(2*time.Second), sso.ProtocolSAML)
	next := *c3.SAML
	next.IdPCertificates = []string{"BB"}
	if err := st.UpdateSAMLMetadata(ctx, c3.ID, 2, next); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("stale version: %v", err)
	}
	if err := st.UpdateSAMLMetadata(ctx, c1.ID, 1, next); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("OIDC connection: %v", err)
	}
	if err := st.UpdateSAMLMetadata(ctx, c3.ID, 1, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetConnectionByID(ctx, c3.ID); got.SAML.IdPCertificates[0] != "BB" || got.ConfigVersion != 1 {
		t.Fatalf("metadata update = %+v", got.SAML)
	}

	// SSO session links: active sessions only; deleted with the session and with the connection.
	sess := auth.Session{UserID: ownerA.P.UserID, TokenHash: auth.HashSecret("ssom-session-" + domain), CSRFToken: "c", ExpiresAt: now.Add(time.Hour),
		AuthMethod: auth.MethodSAML, OrgID: orgA.ID, ConnectionID: c2.ID}
	if err := users.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	link := sso.SSOSession{SessionID: sess.ID, ConnectionID: c2.ID, OrgID: orgA.ID, UserID: ownerA.P.UserID, Subject: "owner@ssom.example",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", SessionIndex: "idx-1", IDTokenEnc: []byte{1, 2}}
	if err := st.CreateSSOSession(ctx, &link); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetSSOSession(ctx, sess.ID); err != nil || got.SessionIndex != "idx-1" || string(got.IDTokenEnc) != string([]byte{1, 2}) {
		t.Fatalf("link = %+v, %v", got, err)
	}
	if ls, err := st.ListSSOSessions(ctx, c2.ID, "owner@ssom.example", "", now); err != nil || len(ls) != 1 {
		t.Fatalf("by subject = %v, %v", ls, err)
	}
	if ls, _ := st.ListSSOSessions(ctx, c2.ID, "", ownerA.P.UserID, now); len(ls) != 1 {
		t.Fatalf("by user = %v", ls)
	}
	if ls, _ := st.ListSSOSessions(ctx, c1.ID, "owner@ssom.example", "", now); len(ls) != 0 {
		t.Fatalf("other connection = %v", ls)
	}
	// By session index (OIDC sid, 0063_sso_sessions_index).
	if ls, err := st.ListSSOSessionsByIndex(ctx, c2.ID, "idx-1", now); err != nil || len(ls) != 1 || ls[0].SessionID != sess.ID {
		t.Fatalf("by session index = %v, %v", ls, err)
	}
	for _, x := range []struct{ conn, index string }{{c2.ID, "idx-2"}, {c1.ID, "idx-1"}, {c2.ID, ""}, {"not-a-uuid", "idx-1"}} {
		if ls, err := st.ListSSOSessionsByIndex(ctx, x.conn, x.index, now); err != nil || len(ls) != 0 {
			t.Fatalf("by session index %v = %v, %v", x, ls, err)
		}
	}
	if err := users.RevokeSession(ctx, ownerA.P.UserID, sess.ID, now); err != nil {
		t.Fatal(err)
	}
	if ls, _ := st.ListSSOSessions(ctx, c2.ID, "owner@ssom.example", "", now); len(ls) != 0 {
		t.Fatalf("revoked session listed = %v", ls)
	}
	if ls, _ := st.ListSSOSessionsByIndex(ctx, c2.ID, "idx-1", now); len(ls) != 0 {
		t.Fatalf("revoked session listed by index = %v", ls)
	}
	// Logout states.
	ls := sso.LoginState{StateHash: auth.HashSecret("ssom-logout-" + domain), OrgID: orgA.ID, ConnectionID: c2.ID, Purpose: sso.PurposeLogout,
		SAMLRequestID: "id-lr", RedirectTo: "/login", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := st.CreateLoginState(ctx, &ls); err != nil {
		t.Fatalf("logout state: %v", err)
	}
	c2.Enforce = false
	_ = st.UpdateConnection(ctx, &c2)
	if _, err := st.DeleteConnection(ctx, orgA.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSSOSession(ctx, sess.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("link after connection delete: %v", err)
	}
	if dd, err := st.GetDomain(ctx, orgA.ID, d.ID); err != nil || dd.ConnectionID != "" {
		t.Fatalf("domain after connection delete = %+v, %v", dd, err)
	}
	if ms, _ := st.ListRoleMappings(ctx, orgA.ID, ""); len(ms) != 1 {
		t.Fatalf("organization-wide mappings after connection delete = %v", ms)
	}

	// E-mail change (SCIM).
	second := e.AddUser(ownerA, "ssom-second", auth.RoleMember)
	if err := users.SetUserEmail(ctx, second.P.UserID, ownerA.P.Email); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("e-mail of another account: %v", err)
	}
	if err := users.SetUserEmail(ctx, second.P.UserID, "renamed-"+domain+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if u, err := users.GetUserByEmail(ctx, "renamed-"+domain+"@example.com"); err != nil || u.ID != second.P.UserID {
		t.Fatalf("renamed user = %+v, %v", u, err)
	}
}

func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
