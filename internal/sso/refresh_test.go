package sso_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

func TestRefreshOIDC(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	_ = env.sso.RefreshJob(reg) // registers the metrics; RunRefresh is driven by the test
	if h := env.sso.ConnectionHealth(c); h.Status != "unknown" {
		t.Fatalf("health before refresh = %+v", h)
	}
	env.sso.RunRefresh(ctx)
	c, _ = env.store.GetConnectionByID(ctx, c.ID)
	if c.Refresh.OK == nil || !*c.Refresh.OK || c.Refresh.Cache.Issuer != idp.srv.URL || len(c.Refresh.Cache.OIDCJWKS) == 0 ||
		c.Refresh.NextAt == nil || c.Refresh.NextAt.Sub(*c.Refresh.RefreshedAt) != 30*time.Minute {
		t.Fatalf("refresh state = %+v", c.Refresh)
	}
	if h := env.sso.ConnectionHealth(c); h.Status != "ok" {
		t.Fatalf("health = %+v", h)
	}
	// Not due again: nothing is fetched.
	idp.mu.Lock()
	disco := idp.disco
	idp.mu.Unlock()
	env.sso.RunRefresh(ctx)
	// Sign-ins use the stored discovery document and JWKS (no discovery round trip).
	if err := env.sso.VerifyIDTokenForTest(ctx, c, idp.makeToken(map[string]any{"nonce": "n"}), "n"); err != nil {
		t.Fatalf("token with the cached JWKS: %v", err)
	}
	idp.mu.Lock()
	if idp.disco != disco {
		t.Fatalf("discovery fetched %d times after the refresh", idp.disco-disco)
	}
	idp.mu.Unlock()
	if got := testutil.ToFloat64(sso.RefreshRunsForTest(env.sso, "oidc", "ok")); got != 1 {
		t.Fatalf("refresh ok counter = %v", got)
	}

	// The IdP goes away: failures back off and the health degrades.
	idp.srv.Close()
	now := time.Now()
	for i := 1; i <= 3; i++ {
		now = now.Add(2 * time.Hour)
		at := now
		env.sso.SetClock(func() time.Time { return at })
		env.sso.RunRefresh(ctx)
		c, _ = env.store.GetConnectionByID(ctx, c.ID)
		if c.Refresh.OK == nil || *c.Refresh.OK || c.Refresh.Failures != i || c.Refresh.Error == "" || len(c.Refresh.Cache.OIDCJWKS) == 0 {
			t.Fatalf("failure %d: %+v", i, c.Refresh)
		}
		want := min(5*time.Minute<<(i-1), time.Hour)
		if c.Refresh.NextAt.Sub(at) != want {
			t.Fatalf("backoff after %d failures = %v, want %v", i, c.Refresh.NextAt.Sub(at), want)
		}
		h := env.sso.ConnectionHealth(c)
		if (i < 3 && h.Status != "warning") || (i == 3 && h.Status != "error") || h.Message == "" {
			t.Fatalf("health after %d failures = %+v", i, h)
		}
	}
	if got := testutil.ToFloat64(sso.RefreshFailingForTest(env.sso)); got != 1 {
		t.Fatalf("failing gauge = %v", got)
	}
}

func TestRefreshSAMLMetadata(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	idp := newTestIdP(t, idpEntityID)
	var mu sync.Mutex
	served := idp.metadataXML(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(served))
	}))
	defer srv.Close()
	// Unsigned metadata from a URL needs the administrator's explicit choice (D-098).
	if _, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataURL: srv.URL + "/metadata"}, auth.ClientMeta{}); !errors.Is(err, auth.ErrInvalidArgument) || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("unsigned metadata URL without a trust decision: %v", err)
	}
	c, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataURL: srv.URL + "/metadata", AllowUnsignedMetadata: true}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	before := c.SAML.IdPCertificates[0]
	env.sso.RunRefresh(ctx)
	c, _ = env.store.GetConnectionByID(ctx, c.ID)
	if c.Refresh.OK == nil || !*c.Refresh.OK || c.SAML.IdPCertificates[0] != before {
		t.Fatalf("first refresh = %+v", c.Refresh)
	}

	// Certificate rollover at the IdP: unsigned metadata is not trusted to change the signing certificate, the change
	// waits for an administrator.
	rolled := newTestIdP(t, idpEntityID)
	mu.Lock()
	served = rolled.metadataXML(t)
	mu.Unlock()
	version := c.ConfigVersion
	if next := c.Refresh.NextAt.Sub(*c.Refresh.RefreshedAt); next < 15*time.Minute || next > 24*time.Hour {
		t.Fatalf("SAML refresh interval = %v", next)
	}
	// Refresh now (admin) instead of waiting for the schedule; the test IdP certificates expire in a day.
	if c, err = env.sso.RefreshNow(ctx, env.ownerPrincipal(), c.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	pending := c.SAML.PendingMetadata
	if c.SAML.IdPCertificates[0] != before || *c.Refresh.OK || !strings.Contains(c.Refresh.Error, "confirm") || pending == nil ||
		pending.Reason != sso.PendingMetadataChanged || pending.IdPCertificates[0] == before {
		t.Fatalf("unconfirmed rollover: certs %v refresh %+v pending %+v", c.SAML.IdPCertificates, c.Refresh, pending)
	}
	resp := rolled.makeResponse(t, respOpts{requestID: "r1", audience: env.sso.SAMLEntityID(c.ID), recipient: env.sso.SAMLACSURL(c.ID), email: "a@example.com"})
	if _, _, err := env.sso.SAMLAssertionForTest(c, resp, []string{"r1"}); err == nil {
		t.Fatal("assertion signed with the unconfirmed key accepted")
	}
	if _, err := env.sso.AcceptMetadata(ctx, env.ownerPrincipal(), c.ID, "another-change", auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("accept with a wrong digest: %v", err)
	}
	// The administrator confirms: the refreshed metadata replaces the stored copy without a new config version.
	if c, err = env.sso.AcceptMetadata(ctx, env.ownerPrincipal(), c.ID, pending.Digest, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if c.SAML.IdPCertificates[0] == before || c.ConfigVersion != version || !*c.Refresh.OK || c.SAML.PendingMetadata != nil {
		t.Fatalf("after rollover: certs %v version %d/%d refresh %+v", c.SAML.IdPCertificates, c.ConfigVersion, version, c.Refresh)
	}
	audited := map[string]bool{}
	for _, e := range env.users.AuditEvents() {
		audited[e.Action] = true
	}
	if !audited["sso.connection.metadata_pending"] || !audited["sso.connection.metadata_accept"] {
		t.Fatalf("metadata change not audited: %v", audited)
	}
	if _, err := env.sso.AcceptMetadata(ctx, env.ownerPrincipal(), c.ID, pending.Digest, auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("accept without a pending change: %v", err)
	}
	// Assertions of the new key are accepted now.
	if _, _, err := env.sso.SAMLAssertionForTest(c, resp, []string{"r1"}); err != nil {
		t.Fatalf("assertion signed with the rolled key: %v", err)
	}

	// Another entity ID is not taken over by a refresh.
	mu.Lock()
	served = newTestIdP(t, "https://other-idp.example/metadata").metadataXML(t)
	mu.Unlock()
	if c, err = env.sso.RefreshNow(ctx, env.ownerPrincipal(), c.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if *c.Refresh.OK || !strings.Contains(c.Refresh.Error, "entity ID changed") || c.SAML.IdPEntityID != idpEntityID {
		t.Fatalf("entity ID change: %+v %s", c.Refresh, c.SAML.IdPEntityID)
	}
	refreshed := 0
	for _, e := range env.users.AuditEvents() {
		if e.Action == "sso.connection.refresh" {
			refreshed++
		}
	}
	if refreshed != 2 {
		t.Fatalf("sso.connection.refresh audit events = %d", refreshed)
	}
	// An expiring IdP certificate is a warning.
	c.SAML.IdPCertNotAfter = time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ok := true
	c.Refresh.OK, c.Refresh.Failures = &ok, 0
	if h := env.sso.ConnectionHealth(c); h.Status != "warning" || !strings.Contains(h.Message, "expires") {
		t.Fatalf("expiring certificate health = %+v", h)
	}
}
