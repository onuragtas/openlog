package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// deviceSessionsJSON is the GET /api/v1/sessions body, with the two fields 0102 added.
type deviceSessionsJSON struct {
	Sessions []struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		DeviceName string `json:"device_name"`
		Current    bool   `json:"current"`
	} `json:"sessions"`
}

// deviceLoginJSON is the POST /api/v1/auth/device body.
type deviceLoginJSON struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	ExpiresAt string `json:"expires_at"`
	Me        meJSON `json:"me"`
}

func deviceLogin(t *testing.T, e *accountEnv, name string) (*client, deviceLoginJSON) {
	t.Helper()
	anon := &client{t: t, h: e.h}
	rec := anon.do(http.MethodPost, "/api/v1/auth/device",
		map[string]string{"email": "owner@example.com", "password": ownerPassword, "device_name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("device login: %d %s", rec.Code, rec.Body)
	}
	out := decode[deviceLoginJSON](t, rec)
	if !strings.HasPrefix(out.Token, auth.PrefixDeviceSession) {
		t.Fatalf("token %q does not carry the device prefix", out.Token)
	}
	return &client{t: t, h: e.h, bearer: out.Token}, out
}

func TestDeviceSessionSignsInAndReadsItsOwnAccount(t *testing.T) {
	e := newAccountEnv(t)
	dev, out := deviceLogin(t, e, "Onur's iPhone")

	// The sign-in answer already carries the /auth/me body, so the app needs no second call to know who it is.
	if out.Me.User == nil || out.Me.User.Email != "owner@example.com" {
		t.Fatalf("device login me: %+v", out.Me)
	}
	if out.Me.Auth != string(auth.KindDevice) {
		t.Fatalf("auth kind %q, want %q", out.Me.Auth, auth.KindDevice)
	}
	if len(out.Me.Organizations) == 0 {
		t.Fatal("device login listed no organizations: the app would have nothing to select")
	}
	// A bearer token needs no CSRF token, and handing one out would invite a client to send it.
	if out.Me.CSRFToken != nil {
		t.Fatalf("device login returned a csrf_token: %v", *out.Me.CSRFToken)
	}

	rec := dev.do(http.MethodGet, "/api/v1/auth/me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me with the device token: %d %s", rec.Code, rec.Body)
	}
	me := decode[meJSON](t, rec)
	if me.User == nil || me.User.Email != "owner@example.com" || me.Auth != string(auth.KindDevice) {
		t.Fatalf("me: %+v", me)
	}
}

// The restriction is the point of a device session: the owner of this token is an organization owner, and is
// still refused everything that creates a credential or changes who may sign in.
//
// Reading is a different question and is deliberately not listed here. Listing members or the names and
// prefixes of keys is guarded by a role, not by UserOnly, so an API key of the same role may do it too and a
// phone is no more privileged for being allowed it. What a phone must never do is mint or revoke the things
// in those lists. TestADeviceSessionIsRefusedEveryUserOnlyAction proves the whole rule over the matrix; this
// one proves the wiring actually reaches it.
func TestDeviceSessionCannotManageTheInstallation(t *testing.T) {
	e := newAccountEnv(t)
	dev, _ := deviceLogin(t, e, "Onur's iPhone")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "from a phone"}},
		{http.MethodPost, "/api/v1/invitations", map[string]string{"email": "x@example.com", "role": "member"}},
		{http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "from a phone"}},
		// Own-account management: a phone that is unlocked in the wrong hands must not be able to take the
		// account over, so changing the password stays a browser operation.
		{http.MethodPost, "/api/v1/auth/password", map[string]string{"current_password": ownerPassword, "new_password": "another long password"}},
		// Listing sessions shows where the person is signed in, and revoking them is how an attacker would
		// lock them out; both stay with the browser.
		{http.MethodGet, "/api/v1/sessions", nil},
	} {
		rec := dev.do(c.method, c.path, c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d %s, want 403", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

// A device token sent as the session cookie would authenticate as a browser session and walk past the
// restriction above, so the cookie path refuses it.
func TestDeviceTokenIsRefusedAsACookie(t *testing.T) {
	e := newAccountEnv(t)
	_, out := deviceLogin(t, e, "Onur's iPhone")

	asCookie := &client{t: t, h: e.h, cookie: &http.Cookie{Name: auth.DefaultCookieName, Value: out.Token}}
	if rec := asCookie.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("device token as a cookie: %d %s, want 401", rec.Code, rec.Body)
	}
}

// Signing out only ever removes access, so a device may do it even though it may not manage the account.
func TestDeviceSessionSignsItselfOut(t *testing.T) {
	e := newAccountEnv(t)
	dev, _ := deviceLogin(t, e, "Onur's iPhone")

	if rec := dev.do(http.MethodPost, "/api/v1/auth/logout", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("device logout: %d %s", rec.Code, rec.Body)
	}
	if rec := dev.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d %s, want 401", rec.Code, rec.Body)
	}
}

// The phone shows up in the person's own session list as a phone, not as another user agent string, because
// "sign this one out" depends on telling them apart.
func TestDeviceSessionIsListedAsADeviceForItsOwner(t *testing.T) {
	e := newAccountEnv(t)
	if _, out := deviceLogin(t, e, "Onur's iPhone"); out.SessionID == "" {
		t.Fatal("device login returned no session id")
	}
	owner := e.login(t, "owner@example.com", ownerPassword)

	rec := owner.do(http.MethodGet, "/api/v1/sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions: %d %s", rec.Code, rec.Body)
	}
	out := decode[deviceSessionsJSON](t, rec)
	var devices, browsers int
	for _, x := range out.Sessions {
		switch x.Kind {
		case "device":
			devices++
			if x.DeviceName != "Onur's iPhone" {
				t.Errorf("device name %q", x.DeviceName)
			}
			if x.Current {
				t.Error("the phone is listed as the current session of a browser")
			}
		case "browser":
			browsers++
			if x.DeviceName != "" {
				t.Errorf("browser session carries a device name %q", x.DeviceName)
			}
		default:
			t.Errorf("session of unknown kind %q", x.Kind)
		}
	}
	if devices != 1 || browsers != 1 {
		t.Fatalf("want one phone and one browser, got %d and %d: %+v", devices, browsers, out.Sessions)
	}
}

// A list where every row reads the same is a list you cannot act on, so the name is required rather than
// defaulted to something like "device".
func TestDeviceNameIsRequiredAndBounded(t *testing.T) {
	e := newAccountEnv(t)
	anon := &client{t: t, h: e.h}
	for _, name := range []string{"", "   ", strings.Repeat("x", auth.MaxDeviceNameLen+1)} {
		rec := anon.do(http.MethodPost, "/api/v1/auth/device",
			map[string]string{"email": "owner@example.com", "password": ownerPassword, "device_name": name})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("device_name %q: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	// Wrong credentials must fail the same way they do for a browser, not leak that the device name was fine.
	rec := anon.do(http.MethodPost, "/api/v1/auth/device",
		map[string]string{"email": "owner@example.com", "password": "nope nope nope", "device_name": "phone"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: %d %s, want 401", rec.Code, rec.Body)
	}
}
