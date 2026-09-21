package auth_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/rum"
)

// Browser keys had no test of any kind. They are the one credential openlog hands to the open internet —
// public by construction, bounded only by the application name forced onto every payload, the origin
// allowlist, the per-key rate limit and revocation (rum.md §3.5). Every one of those bounds lived in code
// nothing exercised.
//
// These characterize the behaviour as it stands, and they are written to fail loudly at the two places a
// change is silent: the fields carried from a stored key into rum.Key (ingest authorizes and rewrites
// payloads from exactly those, and store column order is positional), and the defaults the normalizer
// applies (a zero rate limit that stayed zero would mean "no limit", not "the default").
//
// The test package is auth_test rather than auth because memstore imports auth.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type keyEnv struct {
	svc   *auth.Service
	st    *memstore.Store
	admin *auth.Principal
}

func newKeyEnv(t *testing.T) *keyEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	res, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	return &keyEnv{svc: svc, st: st, admin: &auth.Principal{
		Kind: auth.KindSession, UserID: "u-1", Email: "owner@example.com", EmailVerified: true,
		OrgID: res.Org.ID, OrgName: "Org A", TenantID: "tenant-a", Role: auth.RoleAdmin,
	}}
}

func validInput() auth.BrowserKeyInput {
	return auth.BrowserKeyInput{
		Name: "shop web", ServiceName: "shop-web", Environment: "prod",
		Origins: []string{"https://shop.example.com"}, RateLimitPerMinute: 120, SampleRate: 0.5,
	}
}

func TestBrowserKeyLifecycle(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()

	k, secret, err := e.svc.CreateBrowserKey(ctx, e.admin, validInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || k.ID == "" || k.Prefix == "" {
		t.Fatalf("create returned id=%q prefix=%q secret empty=%v", k.ID, k.Prefix, secret == "")
	}
	// The plaintext is public, but it must still not be readable back from the store: it is shown once.
	if len(k.Hash) == 0 {
		t.Error("key stored without a hash")
	}
	if string(k.Hash) == secret {
		t.Error("the plaintext itself is stored as the hash")
	}

	got, err := e.svc.GetBrowserKey(ctx, e.admin, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServiceName != "shop-web" || got.Environment != "prod" || got.RateLimitPerMinute != 120 ||
		got.SampleRate != 0.5 || len(got.Origins) != 1 || got.Origins[0] != "https://shop.example.com" {
		t.Errorf("stored key does not round-trip: %+v", got)
	}

	list, err := e.svc.ListBrowserKeys(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != k.ID {
		t.Fatalf("list = %d keys", len(list))
	}

	in := validInput()
	in.Name, in.Origins, in.RateLimitPerMinute = "renamed", []string{"https://other.example.com"}, 600
	up, err := e.svc.UpdateBrowserKey(ctx, e.admin, k.ID, in, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "renamed" || up.RateLimitPerMinute != 600 || len(up.Origins) != 1 ||
		up.Origins[0] != "https://other.example.com" {
		t.Errorf("update did not apply: %+v", up)
	}

	rev, err := e.svc.RevokeBrowserKey(ctx, e.admin, k.ID, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if rev.RevokedAt == nil {
		t.Fatal("revoke left revoked_at unset")
	}
	// Revocation is a soft delete and must be final: the plaintext is still cached in browsers that loaded
	// the old page, so editing a revoked key back into service would silently re-arm a value in the wild.
	if _, err := e.svc.UpdateBrowserKey(ctx, e.admin, k.ID, validInput(), auth.ClientMeta{}); err == nil {
		t.Error("a revoked key was edited back into service")
	}
	again, err := e.svc.RevokeBrowserKey(ctx, e.admin, k.ID, auth.ClientMeta{})
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*rev.RevokedAt) {
		t.Errorf("revoke is not idempotent: %v %v", err, again.RevokedAt)
	}
}

// TestBrowserKeyDefaults pins what a zero field means. Both matter in the unsafe direction: a rate limit
// left at zero is "no limit" to a token bucket, and a sample rate left at zero would weight every stored
// span by division.
func TestBrowserKeyDefaults(t *testing.T) {
	e := newKeyEnv(t)
	in := validInput()
	in.RateLimitPerMinute, in.SampleRate = 0, 0
	k, _, err := e.svc.CreateBrowserKey(context.Background(), e.admin, in, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if k.RateLimitPerMinute != auth.DefaultBrowserRateLimit {
		t.Errorf("rate limit defaulted to %d, want %d", k.RateLimitPerMinute, auth.DefaultBrowserRateLimit)
	}
	if k.SampleRate != 1 {
		t.Errorf("sample rate defaulted to %v, want 1", k.SampleRate)
	}
}

func TestBrowserKeyInputIsValidated(t *testing.T) {
	e := newKeyEnv(t)
	many := make([]string, auth.MaxBrowserOrigins+1)
	for i := range many {
		many[i] = "https://a.example.com"
	}
	for name, mutate := range map[string]func(*auth.BrowserKeyInput){
		"no origins":       func(in *auth.BrowserKeyInput) { in.Origins = nil },
		"too many origins": func(in *auth.BrowserKeyInput) { in.Origins = many },
		"no service name":  func(in *auth.BrowserKeyInput) { in.ServiceName = "  " },
		"no name":          func(in *auth.BrowserKeyInput) { in.Name = "" },
		"rate too low":     func(in *auth.BrowserKeyInput) { in.RateLimitPerMinute = auth.MinBrowserRateLimit - 1 },
		"rate too high":    func(in *auth.BrowserKeyInput) { in.RateLimitPerMinute = auth.MaxBrowserRateLimit + 1 },
		"sample above one": func(in *auth.BrowserKeyInput) { in.SampleRate = 1.5 },
		"sample negative":  func(in *auth.BrowserKeyInput) { in.SampleRate = -0.1 },
		"control in service name": func(in *auth.BrowserKeyInput) {
			in.ServiceName = "shop\x00web"
		},
	} {
		in := validInput()
		mutate(&in)
		if _, _, err := e.svc.CreateBrowserKey(context.Background(), e.admin, in, auth.ClientMeta{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestBrowserKeyManagementNeedsAnAdminUser holds the gate in place: managing keys is RoleAdmin and
// UserOnly, so an API key may not mint a credential for the open internet even when its role would allow
// the change (D-133).
func TestBrowserKeyManagementNeedsAnAdminUser(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()
	for name, p := range map[string]*auth.Principal{
		"viewer": {Kind: auth.KindSession, UserID: "u-2", OrgID: e.admin.OrgID, TenantID: "tenant-a", Role: auth.RoleViewer},
		"member": {Kind: auth.KindSession, UserID: "u-3", OrgID: e.admin.OrgID, TenantID: "tenant-a", Role: auth.RoleMember},
		"api key with admin role": {Kind: auth.KindAPIKey, APIKeyID: "k-1", APIKeyName: "ci",
			OrgID: e.admin.OrgID, TenantID: "tenant-a", Role: auth.RoleAdmin},
	} {
		if _, _, err := e.svc.CreateBrowserKey(ctx, p, validInput(), auth.ClientMeta{}); err == nil {
			t.Errorf("%s created a browser key", name)
		}
	}
}

// TestLookupCarriesWhatIngestEnforces is the field-mapping canary. Ingest authorizes the request and then
// rewrites the payload from these fields alone: a key that arrived with the wrong service name would write
// telemetry under a backend service's identity, and one with the wrong origins would be usable from any
// site. Store reads are positional, so a column added in the wrong place corrupts them silently.
func TestLookupCarriesWhatIngestEnforces(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()
	k, _, err := e.svc.CreateBrowserKey(ctx, e.admin, validInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	hash := e.st.BrowserKeyHash(k.ID)
	if len(hash) == 0 {
		t.Fatal("no stored hash for the created key")
	}

	got, err := e.st.LookupBrowserKey(ctx, [][]byte{hash})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID != k.ID || got.TenantID != "tenant-a" || got.ServiceName != "shop-web" ||
		got.Environment != "prod" || got.RateLimitPerMinute != 120 || got.SampleRate != 0.5 {
		t.Errorf("lookup carried the wrong fields: %+v", got)
	}
	if len(got.Origins) != 1 || got.Origins[0] != "https://shop.example.com" {
		t.Errorf("lookup carried origins %v", got.Origins)
	}
	// The allowlist is only a bound if it is actually consulted with what the key carries.
	if !rum.OriginAllowed(got.Origins, "https://shop.example.com") {
		t.Error("the key's own origin is not allowed by its allowlist")
	}
	if rum.OriginAllowed(got.Origins, "https://evil.example.com") {
		t.Error("an origin outside the allowlist was allowed")
	}

	// Revocation must reach the ingest path, not only the management screens.
	if _, err := e.svc.RevokeBrowserKey(ctx, e.admin, k.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.LookupBrowserKey(ctx, [][]byte{hash}); err == nil {
		t.Error("a revoked key still resolves on the ingest path")
	}
}
