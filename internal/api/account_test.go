package api

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
)

const ownerPassword = "correct horse battery staple"

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type accountEnv struct {
	h    http.Handler
	svc  *auth.Service
	st   *memstore.Store
	conn *recordingConn
}

func newAccountEnv(t *testing.T) *accountEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-b", OrgName: "Org B",
		OwnerEmail: "other@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	return &accountEnv{h: s.srv.Handler, svc: svc, st: st, conn: conn}
}

type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
	csrf   string
	bearer string
	org    string
}

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set(auth.HeaderCSRF, c.csrf)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.org != "" {
		req.Header.Set(auth.HeaderOrg, c.org)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func (e *accountEnv) login(t *testing.T, email, password string) *client {
	t.Helper()
	c := &client{t: t, h: e.h}
	rec := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.DefaultCookieName {
			c.cookie = &http.Cookie{Name: ck.Name, Value: ck.Value}
		}
	}
	if c.cookie == nil {
		t.Fatal("no session cookie")
	}
	me := decode[meJSON](t, rec)
	if me.CSRFToken == nil || *me.CSRFToken == "" {
		t.Fatalf("login response without csrf_token: %s", rec.Body)
	}
	c.csrf = *me.CSRFToken
	return c
}

func TestLoginCookieAndMe(t *testing.T) {
	e := newAccountEnv(t)
	anon := &client{t: t, h: e.h}
	rec := anon.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "nope nope nope"})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid email or password") {
		t.Fatalf("bad login: %d %s", rec.Code, rec.Body)
	}
	// Login must be JSON (login CSRF: cross-site forms cannot send application/json).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("email=owner@example.com&password="+ownerPassword))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("form login: %d", rec.Code)
	}

	rec = anon.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "Owner@Example.com", "password": ownerPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, attr := range []string{"HttpOnly", "Secure", "SameSite=Strict", "Path=/api"} {
		if !strings.Contains(setCookie, attr) {
			t.Errorf("Set-Cookie %q lacks %s", setCookie, attr)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("login response cacheable")
	}
	me := decode[meJSON](t, rec)
	if me.Auth != "session" || me.User == nil || me.User.Email != "owner@example.com" || me.Organization == nil ||
		me.Organization.TenantID != "tenant-a" || me.Role == nil || *me.Role != "owner" || len(me.Organizations) != 1 {
		t.Fatalf("me = %s", rec.Body)
	}

	c := e.login(t, "owner@example.com", ownerPassword)
	if rec := c.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusOK {
		t.Fatalf("hosts with session: %d %s", rec.Code, rec.Body)
	}
	if rec := anon.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("hosts without session: %d", rec.Code)
	}
	// Ingest license keys are not API credentials in postgres mode.
	key, _ := e.svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", LicenseKey: "ingest-only-key-123"})
	_ = key
	lk := &client{t: t, h: e.h, bearer: "ingest-only-key-123"}
	if rec := lk.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("license key on query API: %d", rec.Code)
	}

	// Logout revokes the session and clears the cookie.
	noCSRF := *c
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPost, "/api/v1/auth/logout", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", rec.Code)
	}
	rec = c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	if rec := c.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", rec.Code)
	}
}

func TestManagementFlow(t *testing.T) {
	e := newAccountEnv(t)
	owner := e.login(t, "owner@example.com", ownerPassword)

	// CSRF is required for cookie-authenticated mutations.
	noCSRF := *owner
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "prod"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create key without CSRF: %d %s", rec.Code, rec.Body)
	}

	rec := owner.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "prod"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create license key: %d %s", rec.Code, rec.Body)
	}
	created := decode[struct {
		LicenseKey licenseKeyJSON `json:"license_key"`
		Key        string         `json:"key"`
	}](t, rec)
	if !strings.HasPrefix(created.Key, "olk_") || created.LicenseKey.Prefix != created.Key[:12] {
		t.Fatalf("created %+v", created)
	}
	rec = owner.do(http.MethodGet, "/api/v1/license-keys", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Key) || !strings.Contains(rec.Body.String(), created.LicenseKey.Prefix) {
		t.Fatalf("list must show the prefix but never the key: %s", rec.Body)
	}
	if rec := owner.do(http.MethodDelete, "/api/v1/license-keys/"+created.LicenseKey.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}

	// Imported (custom) key value: 201 with "key": null and "custom": true; 400 when invalid; 409 when reused.
	customValue := "0123456789abcdef0123456789abcdef"
	rec = owner.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "signoz", "key": " " + customValue + " "})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"key":null`) || !strings.Contains(rec.Body.String(), `"custom":true`) || strings.Contains(rec.Body.String(), customValue) {
		t.Fatalf("create custom key: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"", "   ", "too-short", "has space 0123456789", `quote"0123456789abc`} {
		if rec := owner.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "bad", "key": bad}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_argument") {
			t.Fatalf("invalid custom key %q: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := owner.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "again", "key": customValue}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already_exists") {
		t.Fatalf("duplicate custom key: %d %s", rec.Code, rec.Body)
	}
	if rec := owner.do(http.MethodGet, "/api/v1/license-keys", nil); !strings.Contains(rec.Body.String(), `"custom":false`) || !strings.Contains(rec.Body.String(), `"custom":true`) {
		t.Fatalf("list custom flags: %s", rec.Body)
	}

	// Invite a viewer and accept.
	rec = owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "viewer@example.com", "role": "viewer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	inv := decode[struct {
		Token string `json:"token"`
	}](t, rec)
	anon := &client{t: t, h: e.h}
	rec = anon.do(http.MethodPost, "/api/v1/invitations/lookup", map[string]string{"token": inv.Token})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"organization_name":"Org A"`) {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body)
	}
	rec = anon.do(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": inv.Token, "password": ownerPassword, "name": "Vic"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Set-Cookie"), auth.DefaultCookieName) {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	viewer := e.login(t, "viewer@example.com", ownerPassword)
	if rec := viewer.do(http.MethodPost, "/api/v1/license-keys", map[string]string{"name": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer creates license key: %d", rec.Code)
	}
	if rec := viewer.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer creates API key: %d", rec.Code)
	}
	if rec := viewer.do(http.MethodGet, "/api/v1/members", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "viewer@example.com") {
		t.Fatalf("viewer lists members: %d %s", rec.Code, rec.Body)
	}
	if rec := viewer.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer reads hosts: %d", rec.Code)
	}

	// API key: read-only bearer access.
	rec = owner.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "grafana"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create API key: %d %s", rec.Code, rec.Body)
	}
	apiKey := decode[struct {
		Key string `json:"key"`
	}](t, rec).Key
	bearer := &client{t: t, h: e.h, bearer: apiKey}
	if rec := bearer.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusOK {
		t.Fatalf("hosts with API key: %d %s", rec.Code, rec.Body)
	}
	if rec := bearer.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth":"api_key"`) {
		t.Fatalf("me with API key: %d %s", rec.Code, rec.Body)
	}
	if rec := bearer.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "escalate"}); rec.Code != http.StatusForbidden {
		t.Fatalf("API key creates API key: %d", rec.Code)
	}
	if rec := bearer.do(http.MethodGet, "/api/v1/sessions", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("API key lists sessions: %d", rec.Code)
	}

	// Sessions: list and revoke.
	rec = owner.do(http.MethodGet, "/api/v1/sessions", nil)
	sessions := decode[struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}](t, rec).Sessions
	if len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("sessions %s", rec.Body)
	}

	// Organization switching is explicit (header) and limited to memberships.
	orgB := e.login(t, "other@example.com", ownerPassword)
	me := decode[meJSON](t, orgB.do(http.MethodGet, "/api/v1/auth/me", nil))
	owner.org = me.Organization.ID
	if rec := owner.do(http.MethodGet, "/api/v1/hosts", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("hosts of a foreign org: %d %s", rec.Code, rec.Body)
	}

	// Errors keep the envelope; unknown API routes are 404 before authentication.
	if rec := anon.do(http.MethodGet, "/api/v1/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route: %d", rec.Code)
	}
	if rec := anon.do(http.MethodGet, "/api/v1/auth/config", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"postgres"`) {
		t.Fatalf("auth config: %d %s", rec.Code, rec.Body)
	}
}

func TestLoginRateLimitHTTP(t *testing.T) {
	e := newAccountEnv(t)
	anon := &client{t: t, h: e.h}
	for i := 0; i < 5; i++ {
		anon.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "wrong wrong wrong"})
	}
	rec := anon.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": ownerPassword})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limited login: %d %s", rec.Code, rec.Body)
	}
}

func TestStaticModeHasNoAccounts(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{"/api/v1/auth/login", "/api/v1/license-keys"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}")))
		if rec.Code != http.StatusNotFound {
			t.Errorf("static mode %s = %d, want 404", p, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tenant_id":"tenant-a"`) || !strings.Contains(rec.Body.String(), `"auth":"license_key"`) {
		t.Fatalf("static me: %d %s", rec.Code, rec.Body)
	}
}

type stubAuthn struct{ p *auth.Principal }

func (s stubAuthn) Authenticate(*http.Request) (*auth.Principal, error) { return s.p, nil }

// TestTenantComesOnlyFromPrincipal proves that nothing in a request other
// than the authenticated principal can choose the tenant scope.
func TestTenantComesOnlyFromPrincipal(t *testing.T) {
	p := &auth.Principal{Kind: auth.KindSession, UserID: "u", OrgID: "org-a", TenantID: "tenant-a", Role: auth.RoleViewer}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second), stubAuthn{p}, quietLog(), nil)
	var got []string
	h := s.wrap("test", func(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
		got = append(got, sc.Tenant())
		if pp, ok := auth.PrincipalFrom(r.Context()); !ok || pp != p {
			t.Error("principal not in request context")
		}
		sql, params, err := sc.From(query.Hosts).Columns("host_id").Build()
		if err != nil || params["tenant_id"] != "tenant-a" || !strings.Contains(sql, "tenant_id = {tenant_id:String}") {
			t.Errorf("query not scoped: %s %v %v", sql, params, err)
		}
		w.WriteHeader(http.StatusOK)
		return nil
	})
	forged := []func(r *http.Request){
		func(r *http.Request) { r.URL.RawQuery = "tenant_id=tenant-b" },
		func(r *http.Request) { r.Header.Set("X-Openlog-Tenant", "tenant-b") },
		func(r *http.Request) { r.Header.Set("X-Openlog-Tenant-Id", "tenant-b") },
		func(r *http.Request) { r.Header.Set(auth.HeaderOrg, "org-b") },
		func(r *http.Request) { r.Header.Set("openlog-license-key", "key-b") },
		func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "tenant_id", Value: "tenant-b"}) },
	}
	for _, f := range forged {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
		f(req)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(got) != len(forged) {
		t.Fatalf("handler ran %d times", len(got))
	}
	for _, tnt := range got {
		if tnt != "tenant-a" {
			t.Fatalf("tenant %q leaked into scope", tnt)
		}
	}

	// A principal without an organization never reaches a handler.
	s2 := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second),
		stubAuthn{&auth.Principal{Kind: auth.KindSession, UserID: "u"}}, quietLog(), nil)
	rec := httptest.NewRecorder()
	s2.wrap("test", func(http.ResponseWriter, *http.Request, *query.Scope) error {
		t.Error("handler ran without organization")
		return nil
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no org: %d", rec.Code)
	}
}

// TestScopeOnlyCreatedInWrap statically checks that query scopes are created
// only by wrap (from the principal), so no handler can build its own scope.
func TestScopeOnlyCreatedInWrap(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Scope" {
					calls++
					// wrap: tenant of the authenticated principal. shareScope (dashboard_public.go): tenant of a share link's
					// organization, resolved from the token hash in PostgreSQL (D-087). No other function creates scopes.
					if fn.Name.Name != "wrap" && fn.Name.Name != "shareScope" {
						t.Errorf("%s: %s calls .Scope(); scopes must only be created in wrap (or shareScope for share links)", fset.Position(call.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if calls != 2 {
		t.Errorf("found %d .Scope() calls, want exactly 2 (one in wrap, one in shareScope)", calls)
	}
}
