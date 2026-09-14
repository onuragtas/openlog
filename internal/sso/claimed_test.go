package sso_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/sso/ssomem"
)

func TestClaimedDomainRedirection(t *testing.T) {
	ctx := context.Background()
	idp := newFakeIdP(t)
	st := memstore.New()
	as := auth.NewService(st, auth.Config{CookieSecure: true, SignupEnabled: true}, nil)
	res, err := as.Bootstrap(ctx, auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Acme", OwnerEmail: "owner@example.com",
		OwnerPassword: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := st.GetUserByEmail(ctx, "owner@example.com")
	ss := ssomem.New(st)
	svc := sso.NewService(as, ss, sso.Config{PublicURL: "https://openlog.example", AllowPrivateNetworks: true, CookieSecure: true,
		SecretBox: sso.NewSecretBox("an sso secret key that is long enough", "", "", "")}, nil)
	as.SetSessionPolicy(svc.Policy())
	env := &testEnv{auth: as, users: st, store: ss, sso: svc, org: res.Org, owner: owner}
	c := env.oidcConnection(t, idp.srv.URL)
	env.verifyDomain(t, "example.com")

	// Self-service sign-up with a claimed address is refused; other addresses sign up.
	_, _, err = as.Signup(ctx, auth.SignupInput{Email: "new@example.com", Password: "a long enough password", OrgName: "Mine"}, testMeta())
	if !errors.Is(err, auth.ErrFailedPrecondition) || !strings.Contains(err.Error(), "single sign-on") || !strings.Contains(err.Error(), "Acme") {
		t.Fatalf("claimed sign-up: %v", err)
	}
	if r, err := svc.SignupRedirect(ctx, "New@Example.com"); err != nil || r == nil || r.OrganizationName != "Acme" || r.Protocol != sso.ProtocolOIDC {
		t.Fatalf("signup redirect = %+v, %v", r, err)
	}
	if _, other, err := as.Signup(ctx, auth.SignupInput{Email: "x@other.example", Password: "a long enough password", OrgName: "Other"}, testMeta()); err != nil {
		t.Fatalf("unclaimed sign-up: %v", err)
	} else {
		// An invitation of another organization to a claimed address is accepted with a password while the claiming
		// connection allows it, and redirected to SSO when it does not.
		otherOwner, _ := st.GetUserByEmail(ctx, "x@other.example")
		op := &auth.Principal{Kind: auth.KindSession, UserID: otherOwner.ID, Email: otherOwner.Email, EmailVerified: true, OrgID: other.ID,
			OrgName: other.Name, TenantID: other.TenantID, Role: auth.RoleOwner}
		inv, token, err := as.CreateInvitation(ctx, op, "partner@example.com", auth.RoleMember, auth.ClientMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if r, err := svc.InvitationRedirect(ctx, inv); err != nil || r != nil {
			t.Fatalf("external invitation allowed by default: %+v %v", r, err)
		}
		deny := false
		if _, err := svc.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Enabled: true,
			Issuer: idp.srv.URL, ClientID: "openlog", AllowExternalInvitations: &deny}, auth.ClientMeta{}); err != nil {
			t.Fatal(err)
		}
		if r, err := svc.InvitationRedirect(ctx, inv); err != nil || r == nil || r.SameOrganization {
			t.Fatalf("external invitation denied: %+v %v", r, err)
		}
		if _, _, err := as.AcceptInvitation(ctx, token, "a long enough password", "Partner", testMeta()); !errors.Is(err, auth.ErrFailedPrecondition) {
			t.Fatalf("password acceptance of a denied external invitation: %v", err)
		}
	}

	// An invitation of the claiming organization is accepted by signing in with SSO, with the invited role, even
	// with just-in-time provisioning off.
	inv, token, err := as.CreateInvitation(ctx, env.ownerPrincipal(), "dave@example.com", auth.RoleAdmin, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := svc.InvitationRedirect(ctx, inv); err != nil || r == nil || !r.SameOrganization {
		t.Fatalf("own invitation redirect = %+v, %v", r, err)
	}
	if _, _, err := as.AcceptInvitation(ctx, token, "a long enough password", "Dave", testMeta()); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("password acceptance of a claimed invitation: %v", err)
	}
	jit := false
	if _, err := svc.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Enabled: true,
		Issuer: idp.srv.URL, ClientID: "openlog", JITEnabled: &jit}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	idp.setClaims(map[string]any{"email": "dave@example.com"})
	out := runOIDCLogin(t, env, idp, "dave@example.com")
	if out.Session == nil {
		t.Fatalf("SSO acceptance: %+v", out)
	}
	dave, _ := st.GetUserByEmail(ctx, "dave@example.com")
	if m, err := st.GetMembership(ctx, env.org.ID, dave.ID); err != nil || m.Role != auth.RoleAdmin {
		t.Fatalf("membership from invitation = %+v, %v", m, err)
	}
	if invs, _ := st.ListInvitations(ctx, env.org.ID, dave.CreatedAt, false); len(invs) != 0 {
		t.Fatalf("invitation still pending: %+v", invs)
	}
	// Without an invitation JIT off still refuses unknown users.
	idp.setClaims(map[string]any{"email": "eve@example.com"})
	if out := runOIDCLogin(t, env, idp, "eve@example.com"); out.Redirect != "/login?sso_error=not_member" {
		t.Fatalf("eve: %+v", out)
	}

	// A disabled connection does not claim the domain.
	if _, err := svc.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Issuer: idp.srv.URL,
		ClientID: "openlog"}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if r, err := svc.SignupRedirect(ctx, "new@example.com"); err != nil || r != nil {
		t.Fatalf("disabled connection redirect = %+v, %v", r, err)
	}
}
