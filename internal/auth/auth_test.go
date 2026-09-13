package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash format %q", h)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Fatalf("verify good = %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "correct horse batterY"); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h2 == h {
		t.Fatal("salt not random")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		strings.Replace(h, "m=19456", "m=4000000", 1), strings.Replace(h, "t=2", "t=0", 1), strings.Replace(h, "v=19", "v=16", 1)} {
		if ok, err := VerifyPassword(bad, "x"); ok || err == nil {
			t.Errorf("malformed hash %q accepted (%v %v)", bad, ok, err)
		}
	}
	if ok, _ := VerifyPassword(h, strings.Repeat("a", MaxPasswordLen+1)); ok {
		t.Error("over-long password accepted")
	}
}

func TestValidatePassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                               false,
		"elevenchars":                         false,
		"twelve chars":                        true,
		"çğıöşüçğıöşü":                        true, // 12 runes
		strings.Repeat("x", MaxPasswordLen):   true,
		strings.Repeat("x", MaxPasswordLen+1): false,
	} {
		if err := ValidatePassword(pw); (err == nil) != ok {
			t.Errorf("ValidatePassword(%q) = %v", pw, err)
		}
	}
}

func TestSecrets(t *testing.T) {
	a, _ := NewSecret(PrefixLicenseKey)
	b, _ := NewSecret(PrefixLicenseKey)
	if a == b || !strings.HasPrefix(a, "olk_") || len(a) != 52 {
		t.Fatalf("secret %q %q", a, b)
	}
	if p := DisplayPrefix(a); len(p) != 12 || !strings.HasPrefix(a, p) {
		t.Errorf("DisplayPrefix = %q", p)
	}
	if p := DisplayPrefix("dev-license-key"); p != "dev-lic" {
		t.Errorf("short key prefix = %q (must reveal at most half)", p)
	}
	if len(HashSecret(a)) != 32 || string(HashSecret(a)) == string(HashSecret(b)) {
		t.Error("HashSecret")
	}
}

func TestRoles(t *testing.T) {
	cases := []struct {
		role Role
		act  Action
		want bool
	}{
		{RoleViewer, ActReadTelemetry, true},
		{RoleViewer, ActListMembers, true},
		{RoleViewer, ActListLicenseKeys, false},
		{RoleViewer, ActCreateAPIKey, false},
		{RoleMember, ActCreateAPIKey, true},
		{RoleMember, ActListLicenseKeys, true},
		{RoleMember, ActManageLicenseKeys, false},
		{RoleMember, ActManageMembers, false},
		{RoleAdmin, ActManageLicenseKeys, true},
		{RoleAdmin, ActManageInvitations, true},
		{RoleAdmin, ActReadAudit, true},
		{RoleOwner, ActReadAudit, true},
		{Role("root"), ActReadTelemetry, false},
		{RoleOwner, Action("unknown"), false},
	}
	for _, c := range cases {
		if got := c.role.Can(c.act); got != c.want {
			t.Errorf("%s.Can(%s) = %v", c.role, c.act, got)
		}
	}
	assign := []struct {
		actor, from, to Role
		want            bool
	}{
		{RoleOwner, RoleMember, RoleOwner, true},
		{RoleOwner, RoleOwner, RoleViewer, true},
		{RoleAdmin, RoleMember, RoleAdmin, true},
		{RoleAdmin, RoleViewer, RoleOwner, false},
		{RoleAdmin, RoleOwner, RoleAdmin, false},
		{RoleMember, RoleViewer, RoleMember, false},
		{RoleOwner, RoleViewer, Role("x"), false},
	}
	for _, c := range assign {
		if got := CanAssign(c.actor, c.from, c.to); got != c.want {
			t.Errorf("CanAssign(%s, %s→%s) = %v", c.actor, c.from, c.to, got)
		}
	}
}

func TestSessionActive(t *testing.T) {
	now := time.Unix(10_000, 0)
	s := Session{LastSeenAt: now.Add(-30 * time.Minute), ExpiresAt: now.Add(time.Hour)}
	if !s.Active(now, time.Hour) {
		t.Error("active session rejected")
	}
	if s.Active(now, 29*time.Minute) {
		t.Error("idle session accepted")
	}
	if !s.Active(now, 0) {
		t.Error("idle timeout 0 must disable idle expiry")
	}
	if s.Active(now.Add(time.Hour), 0) {
		t.Error("session at absolute expiry accepted")
	}
	revoked := now
	s.RevokedAt = &revoked
	if s.Active(now, time.Hour) {
		t.Error("revoked session accepted")
	}
}

func TestClientIP(t *testing.T) {
	trusted, err := ParseCIDRs([]string{"10.0.0.0/8", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	cases := []struct {
		r    *http.Request
		want string
	}{
		{req("203.0.113.5:1234", "1.2.3.4"), "203.0.113.5"},             // untrusted peer: header ignored
		{req("10.1.2.3:1234", "1.2.3.4"), "1.2.3.4"},                    // trusted proxy
		{req("10.1.2.3:1234", "6.6.6.6, 1.2.3.4, 10.9.9.9"), "1.2.3.4"}, // right-most untrusted hop
		{req("10.1.2.3:1234", "6.6.6.6", "1.2.3.4"), "1.2.3.4"},         // several headers
		{req("10.1.2.3:1234"), "10.1.2.3"},                              // no header
		{req("127.0.0.1:1", "garbage, 1.2.3.4"), "1.2.3.4"},             //
		{req("[::ffff:10.0.0.1]:1", "2001:db8::1"), "2001:db8::1"},      // mapped IPv4 peer
	}
	for i, c := range cases {
		if got := ClientIP(c.r, trusted); got != c.want {
			t.Errorf("case %d: ClientIP = %q, want %q", i, got, c.want)
		}
	}
	if _, err := ParseCIDRs([]string{"nope"}); err == nil {
		t.Error("invalid CIDR accepted")
	}
}

func TestEmailAndNames(t *testing.T) {
	for e, ok := range map[string]bool{"a@example.com": true, "Bob <b@example.com>": false, "no-at": false, "a@b": true} {
		if validEmail(e) != ok {
			t.Errorf("validEmail(%q) != %v", e, ok)
		}
	}
	if NormalizeEmail("  A@Example.COM ") != "a@example.com" {
		t.Error("NormalizeEmail")
	}
	if _, err := cleanName("bad\x00name", "n", true); err == nil {
		t.Error("control characters accepted")
	}
	if v, err := cleanName("  Acme  ", "n", true); err != nil || v != "Acme" {
		t.Errorf("cleanName = %q %v", v, err)
	}
	if !ValidTenantID("e2e-tenant") || ValidTenantID("Bad") || ValidTenantID("") {
		t.Error("ValidTenantID")
	}
	id, _ := NewTenantID()
	if !ValidTenantID(id) {
		t.Errorf("generated tenant id %q invalid", id)
	}
}

func TestCookies(t *testing.T) {
	s := NewService(nil, Config{CookieSecure: true}, nil)
	c := s.SessionCookie("tok", time.Now().Add(time.Hour))
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api" || c.Name != DefaultCookieName || c.MaxAge <= 0 {
		t.Errorf("session cookie %+v", c)
	}
	if cl := s.ClearedCookie(); cl.MaxAge >= 0 || cl.Value != "" {
		t.Errorf("cleared cookie %+v", cl)
	}
}
