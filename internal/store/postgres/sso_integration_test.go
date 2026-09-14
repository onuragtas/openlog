//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/tenant"
)

// TestSSOStore covers the PostgreSQL sso.Store (0030_sso): constraints the in-memory store only imitates.
func TestSSOStore(t *testing.T) {
	migrate(t)
	ctx := context.Background()
	users := postgres.NewStore(pool)
	st := postgres.NewSSOStore(pool)
	e := authtest.NewEnv(t, users, auth.Config{})
	orgA, ownerA := e.Bootstrap("sso-a")
	orgB, _ := e.Bootstrap("sso-b")
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Connections: several per organization (the oldest is the default), settings round-trip, test result bookkeeping.
	c := sso.Connection{ID: uuid.NewString(), OrgID: orgA.ID, Protocol: sso.ProtocolOIDC, Name: "Okta", Enabled: true,
		OIDC:      &sso.OIDCConfig{Issuer: "https://idp.example", ClientID: "cid", Scopes: []string{"groups"}, RequireEmailVerified: true},
		SecretEnc: []byte{1, 2, 3}, JITEnabled: true, DefaultRole: auth.RoleMember, SessionMaxAge: time.Hour,
		BreakGlassUserIDs: []string{ownerA.P.UserID}, ConfigVersion: 1, CreatedBy: ownerA.P.UserID, CreatedAt: now}
	if err := st.CreateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	second := c
	second.ID, second.Name, second.CreatedAt, second.AllowExternalInvitations = uuid.NewString(), "Second", now.Add(time.Second), true
	second.LogoutRedirectAllowlist = []string{"/hosts"}
	if err := st.CreateConnection(ctx, &second); err != nil {
		t.Fatalf("second connection: %v", err)
	}
	if cs, err := st.ListConnections(ctx, orgA.ID); err != nil || len(cs) != 2 || cs[0].ID != c.ID || !cs[1].AllowExternalInvitations ||
		cs[1].LogoutRedirectAllowlist[0] != "/hosts" {
		t.Fatalf("list connections = %+v, %v", cs, err)
	}
	got, err := st.GetConnection(ctx, orgA.ID, "")
	if err != nil || got.OIDC == nil || got.OIDC.Scopes[0] != "groups" || got.SessionMaxAge != time.Hour || got.BreakGlassUserIDs[0] != ownerA.P.UserID ||
		string(got.SecretEnc) != string([]byte{1, 2, 3}) || got.DefaultRole != auth.RoleMember {
		t.Fatalf("get connection = %+v, %v", got, err)
	}
	if err := st.RecordTest(ctx, c.ID, 1, true, "", map[string]any{"email": "a@example.com"}, now); err != nil {
		t.Fatal(err)
	}
	got.ConfigVersion, got.Name = 2, "Okta 2"
	if err := st.UpdateConnection(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetConnectionByID(ctx, c.ID)
	if got.Name != "Okta 2" || got.TestedVersion != 1 || got.Tested() || got.LastTestDetails["email"] != "a@example.com" {
		t.Fatalf("after update = %+v", got)
	}
	// A stale test (version 1) does not mark version 2 tested.
	_ = st.RecordTest(ctx, c.ID, 1, true, "", nil, now)
	if got, _ = st.GetConnectionByID(ctx, c.ID); got.Tested() {
		t.Fatal("stale test marked the current version tested")
	}
	got.Enforce = true
	got.Enabled = false
	if err := st.UpdateConnection(ctx, &got); err == nil {
		t.Fatal("enforce without enabled accepted (CHECK)")
	}

	// Domains: verification is exclusive across organizations; e-mail tokens are single-use.
	dA := sso.Domain{OrgID: orgA.ID, Domain: "acme.example", DNSToken: "tok"}
	dB := sso.Domain{OrgID: orgB.ID, Domain: "acme.example", DNSToken: "tok2"}
	if err := st.CreateDomain(ctx, &dA); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDomain(ctx, &dB); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkDomainVerified(ctx, orgA.ID, dA.ID, "dns_txt", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkDomainVerified(ctx, orgB.ID, dB.ID, "dns_txt", now); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("second organization verified the same domain: %v", err)
	}
	if d, err := st.FindVerifiedDomain(ctx, "acme.example"); err != nil || d.OrgID != orgA.ID {
		t.Fatalf("find verified = %+v, %v", d, err)
	}
	dE := sso.Domain{OrgID: orgA.ID, Domain: "mail.example", DNSToken: "t"}
	_ = st.CreateDomain(ctx, &dE)
	hash := auth.HashSecret("oldv_x")
	if err := st.SetDomainEmailToken(ctx, orgA.ID, dE.ID, hash, "admin@mail.example", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d, err := st.ConsumeDomainEmailToken(ctx, hash, now); err != nil || d.VerifiedAt == nil || d.VerificationMethod != "email" {
		t.Fatalf("consume = %+v, %v", d, err)
	}
	if _, err := st.ConsumeDomainEmailToken(ctx, hash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("token reused: %v", err)
	}

	// Policy query: one statement with connection and domain.
	pol, err := st.GetOrgPolicy(ctx, orgA.ID, c.ID, "acme.example")
	if err != nil || pol.ConnectionID != c.ID || !pol.Enabled || !pol.DomainVerified || pol.SessionMaxAge != time.Hour {
		t.Fatalf("policy = %+v, %v", pol, err)
	}
	if pol, err := st.GetOrgPolicy(ctx, orgB.ID, c.ID, "acme.example"); err != nil || pol.ConnectionID != "" || pol.DomainVerified {
		t.Fatalf("policy of org without connection = %+v, %v", pol, err)
	}

	// Role mappings replace atomically.
	if err := st.ReplaceRoleMappings(ctx, orgA.ID, "", []sso.RoleMapping{{Group: "a", Role: auth.RoleAdmin}, {Group: "b", Role: auth.RoleViewer}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceRoleMappings(ctx, orgA.ID, "", []sso.RoleMapping{{Group: "c", Role: auth.RoleOwner}}); err == nil {
		t.Fatal("owner mapping accepted (CHECK)")
	}
	if ms, _ := st.ListRoleMappings(ctx, orgA.ID, ""); len(ms) != 2 {
		t.Fatalf("mappings after failed replace = %v", ms)
	}

	// Login states: result once, consumed once, expiry.
	ls := sso.LoginState{StateHash: auth.HashSecret("state-1"), BindingHash: auth.HashSecret("b"), OrgID: orgA.ID, ConnectionID: c.ID,
		Purpose: sso.PurposeLogin, Nonce: "n", PKCEVerifier: "v", RedirectTo: "/hosts", ConfigVersion: 2, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := st.CreateLoginState(ctx, &ls); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLoginResult(ctx, ls.ID, sso.Identity{Email: "a@acme.example", Groups: []string{"a"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLoginResult(ctx, ls.ID, sso.Identity{Email: "evil@acme.example"}, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("result overwritten: %v", err)
	}
	got1, err := st.ConsumeLoginState(ctx, ls.StateHash, now)
	if err != nil || got1.Result == nil || got1.Result.Email != "a@acme.example" || got1.PKCEVerifier != "v" {
		t.Fatalf("consume = %+v, %v", got1, err)
	}
	if _, err := st.ConsumeLoginState(ctx, ls.StateHash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("consumed twice: %v", err)
	}
	expired := ls
	expired.StateHash, expired.ExpiresAt = auth.HashSecret("state-2"), now.Add(-time.Second)
	_ = st.CreateLoginState(ctx, &expired)
	if _, err := st.GetLoginState(ctx, expired.StateHash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("expired state: %v", err)
	}

	// Replay cache.
	if fresh, err := st.RecordAssertion(ctx, c.ID, "id-1", now.Add(time.Hour)); err != nil || !fresh {
		t.Fatalf("first assertion: %v %v", fresh, err)
	}
	if fresh, err := st.RecordAssertion(ctx, c.ID, "id-1", now.Add(time.Hour)); err != nil || fresh {
		t.Fatalf("replayed assertion accepted: %v %v", fresh, err)
	}
	if err := st.Cleanup(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// SCIM tokens: hashed like API keys, rehashed after a secret is introduced.
	plain := tenant.NewKeyHasher("", "")
	keyed := tenant.NewKeyHasher("a key hash secret of at least 32 bytes!!", "")
	tok := sso.SCIMToken{OrgID: orgA.ID, Name: "okta", Prefix: "ols_12345678", Hash: plain.Hash("ols_secret"), CreatedBy: ownerA.P.UserID}
	if err := st.CreateSCIMToken(ctx, &tok); err != nil {
		t.Fatal(err)
	}
	found, org, err := st.LookupSCIMToken(ctx, keyed.Candidates("ols_secret"))
	if err != nil || found.ID != tok.ID || org.ID != orgA.ID {
		t.Fatalf("lookup = %+v %+v %v", found, org, err)
	}
	if found, _, err := st.LookupSCIMToken(ctx, [][]byte{keyed.Hash("ols_secret")}); err != nil || found.ID != tok.ID {
		t.Fatalf("token not rehashed: %v", err)
	}
	if ts, _ := st.ListSCIMTokens(ctx, orgA.ID); len(ts) != 1 || ts[0].CreatedByEmail == "" {
		t.Fatalf("list tokens = %+v", ts)
	}

	// SCIM users and groups.
	u := sso.SCIMUser{OrgID: orgA.ID, UserID: ownerA.P.UserID, UserName: "Owner@sso-a.example", ExternalID: "ext", Active: true}
	if err := st.CreateSCIMUser(ctx, &u); err != nil {
		t.Fatal(err)
	}
	other := e.AddUser(ownerA, "second", auth.RoleMember)
	u2 := sso.SCIMUser{OrgID: orgA.ID, UserID: other.P.UserID, UserName: "owner@SSO-A.example", Active: true}
	if err := st.CreateSCIMUser(ctx, &u2); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("case-insensitive user name duplicate: %v", err)
	}
	u2.UserName = "second@sso-a.example"
	if err := st.CreateSCIMUser(ctx, &u2); err != nil {
		t.Fatal(err)
	}
	if list, total, err := st.ListSCIMUsers(ctx, orgA.ID, sso.SCIMFilter{UserName: "OWNER@sso-a.example"}, 0, 10); err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("filter users = %v %d %v", list, total, err)
	}
	g := sso.SCIMGroup{OrgID: orgA.ID, DisplayName: "Admins", Members: []string{u.UserID, u2.UserID}}
	if err := st.CreateSCIMGroup(ctx, &g); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSCIMGroup(ctx, &sso.SCIMGroup{OrgID: orgA.ID, DisplayName: "admins"}); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("duplicate group: %v", err)
	}
	if err := st.RemoveSCIMGroupMembers(ctx, orgA.ID, g.ID, []string{u.UserID}); err != nil {
		t.Fatal(err)
	}
	if names, _ := st.SCIMGroupNames(ctx, orgA.ID, u2.UserID); len(names) != 1 || names[0] != "Admins" {
		t.Fatalf("group names = %v", names)
	}
	if err := st.AddSCIMGroupMembers(ctx, orgB.ID, g.ID, []string{u.UserID}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("add members through another organization: %v", err)
	}
	g.DisplayName = "Platform Admins"
	if err := st.UpdateSCIMGroup(ctx, &g, []string{u.UserID}); err != nil || len(g.Members) != 1 || g.Members[0] != u.UserID {
		t.Fatalf("update group = %+v, %v", g, err)
	}
	if err := st.DeleteSCIMUser(ctx, orgA.ID, u.UserID); err != nil {
		t.Fatal(err)
	}
	if g2, _ := st.GetSCIMGroup(ctx, orgA.ID, g.ID); len(g2.Members) != 0 {
		t.Fatalf("members after user delete = %v", g2.Members)
	}

	// SSO-bound sessions: columns round-trip; deleting the connection deletes them.
	sess := auth.Session{UserID: ownerA.P.UserID, TokenHash: auth.HashSecret("sso-session"), CSRFToken: "c", ExpiresAt: now.Add(time.Hour),
		AuthMethod: auth.MethodSAML, OrgID: orgA.ID, ConnectionID: c.ID}
	if err := users.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	gs, _, err := users.GetSessionByTokenHash(ctx, sess.TokenHash)
	if err != nil || gs.AuthMethod != auth.MethodSAML || gs.OrgID != orgA.ID || gs.ConnectionID != c.ID {
		t.Fatalf("session = %+v, %v", gs, err)
	}
	pw, _, _ := users.GetSessionByTokenHash(ctx, auth.HashSecret(ownerA.Token))
	if pw.AuthMethod != auth.MethodPassword || pw.OrgID != "" {
		t.Fatalf("password session = %+v", pw)
	}
	if _, err := st.DeleteConnection(ctx, orgA.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := users.GetSessionByTokenHash(ctx, sess.TokenHash); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("SSO session survived connection delete: %v", err)
	}
}
