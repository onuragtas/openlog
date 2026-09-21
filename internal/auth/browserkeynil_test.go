package auth_test

import (
	"context"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// Both allowlists are `text[] NOT NULL DEFAULT '{}'` in PostgreSQL, and a nil Go slice reaches the driver as
// NULL — which the column refuses. A browser key carries no app_ids and a mobile key carries no origins, so
// the unused one is exactly the field that was left nil: **every create and update of a browser key failed
// in production**, with a not_null_violation that surfaced as "authentication backend unavailable".
//
// Nothing here could catch that, because these tests run against the in-memory store, where nil and empty
// are the same thing, and the PostgreSQL suite is behind a build tag. So the invariant is tested rather than
// the database: whatever the kind, a key leaves the service with both lists present.
func TestNormalizeLeavesNoNilAllowlist(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()

	for name, in := range map[string]auth.BrowserKeyInput{
		"browser key": validInput(),
		"mobile key":  validMobileInput(),
	} {
		k, _, err := e.svc.CreateBrowserKey(ctx, e.admin, in, auth.ClientMeta{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if k.Origins == nil {
			t.Errorf("%s: origins is nil, which PostgreSQL stores as NULL and the column refuses", name)
		}
		if k.AppIDs == nil {
			t.Errorf("%s: app_ids is nil, which PostgreSQL stores as NULL and the column refuses", name)
		}

		// The update path builds its own row and broke the same way.
		up, err := e.svc.UpdateBrowserKey(ctx, e.admin, k.ID, in, auth.ClientMeta{})
		if err != nil {
			t.Fatalf("%s update: %v", name, err)
		}
		if up.Origins == nil || up.AppIDs == nil {
			t.Errorf("%s update: origins=%v app_ids=%v, both must be present", name, up.Origins, up.AppIDs)
		}
	}
}

// The empty list must stay empty, not become a scope. Filling the unused allowlist to avoid NULL would be a
// silent widening if it ever put something in it.
func TestTheUnusedAllowlistStaysEmpty(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()

	browser, _, err := e.svc.CreateBrowserKey(ctx, e.admin, validInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if len(browser.AppIDs) != 0 {
		t.Errorf("a browser key was given application ids: %v", browser.AppIDs)
	}
	if len(browser.Origins) == 0 {
		t.Error("a browser key lost its origins")
	}

	mobile, _, err := e.svc.CreateBrowserKey(ctx, e.admin, validMobileInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mobile.Origins) != 0 {
		t.Errorf("a mobile key was given origins: %v", mobile.Origins)
	}
	if len(mobile.AppIDs) == 0 {
		t.Error("a mobile key lost its application ids")
	}
}
