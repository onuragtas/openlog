//go:build ssoe2e

// End-to-end single sign-on test against a real Keycloak (OIDC and SAML), PostgreSQL and the api HTTP handlers
// (D-077, D-078, D-088, D-089): two connections of one organization routed by domain, encrypted SAML assertions,
// SAML single logout in both directions, OIDC RP-initiated logout, claimed-domain invitations and SCIM e-mail
// changes, and — with OPENLOG_TEST_CALLBACK_HOST (the host name under which the Keycloak container reaches the test
// server, e.g. host.docker.internal) — logout initiated by Keycloak through OIDC back-channel and front-channel logout
// and SAML SOAP back-channel logout (D-098). See test/integration/sso/docker-compose.yml:
//
//	docker compose -f test/integration/sso/docker-compose.yml up -d --wait
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:27440/openlog?sslmode=disable \
//	OPENLOG_TEST_KEYCLOAK_URL=http://127.0.0.1:18080 OPENLOG_TEST_CALLBACK_HOST=host.docker.internal \
//	go test -tags ssoe2e -count=1 -v ./test/sso
//
// OPENLOG_TEST_LISTEN_ADDR (default 127.0.0.1:0) sets the test server's listen address (0.0.0.0:0 where the container
// cannot reach the host's loopback, e.g. Linux).
package sso_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

const realm = "openlog"

type txtResolver map[string][]string

func (r txtResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := r[name]; ok {
		return v, nil
	}
	return nil, errors.New("NXDOMAIN")
}

type harness struct {
	t        *testing.T
	base     string // openlog public URL
	kc       string // Keycloak base URL
	resolver txtResolver
	orgID    string
	ownerID  string
	// callback: Keycloak reaches base (OPENLOG_TEST_CALLBACK_HOST), so logout initiated by Keycloak can be tested.
	callback bool
}

// frame is an iframe of Keycloak's front-channel logout page loaded by the browser.
type frame struct {
	url    string
	status int
	csp    string
}

// browser is an HTTP client with a cookie jar that follows redirects by hand, like a browser would.
type browser struct {
	t    *testing.T
	h    *harness
	c    *http.Client
	csrf string
	// lastSAMLResponse is the last SAMLResponse posted to the ACS (replay test).
	lastSAMLResponse, lastRelayState, lastACS string
	// lastStart is the IdP URL of the last "Sign in with SSO"; loginForms counts submitted Keycloak login forms.
	lastStart  string
	loginForms int
	// idpPage lets browse end on an identity provider page (after an IdP-initiated logout) and returns "idp-page".
	idpPage bool
	// frames are the front-channel logout iframes loaded on identity provider pages (idpPage).
	frames []frame
}

func (h *harness) newBrowser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: h.t, h: h, c: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) apiJSON(method, path string, body any, out any) int {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, b.h.base+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(auth.HeaderCSRF, b.csrf)
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			b.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
		}
	}
	return res.StatusCode
}

func (b *browser) passwordLogin(email, password string) int {
	b.t.Helper()
	var me struct {
		CSRFToken *string `json:"csrf_token"`
	}
	code := b.apiJSON(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password}, &me)
	if code == http.StatusOK && me.CSRFToken != nil {
		b.csrf = *me.CSRFToken
	}
	return code
}

type meJSON struct {
	User *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Organization *struct {
		ID string `json:"id"`
	} `json:"organization"`
	Role      *string `json:"role"`
	CSRFToken *string `json:"csrf_token"`
}

func (b *browser) me() (int, meJSON) {
	var me meJSON
	code := b.apiJSON(http.MethodGet, "/api/v1/auth/me", nil, &me)
	if me.CSRFToken != nil {
		b.csrf = *me.CSRFToken
	}
	return code, me
}

var (
	loginFormRe = regexp.MustCompile(`(?s)<form[^>]*?action="([^"]*login-actions/authenticate[^"]*)"`)
	postFormRe  = regexp.MustCompile(`(?s)<form[^>]*?action="([^"]+)"`)
	samlRespRe  = regexp.MustCompile(`name="(SAMLResponse|SAMLRequest)"\s+value="([^"]+)"`)
	relayRe     = regexp.MustCompile(`name="RelayState"\s+value="([^"]*)"`)
	// Keycloak's "Do you want to log out?" page of the OIDC logout endpoint.
	logoutFormRe  = regexp.MustCompile(`(?s)<form[^>]*?action="([^"]*logout-confirm[^"]*)"`)
	sessionCodeRe = regexp.MustCompile(`name="session_code"\s+value="([^"]*)"`)
	// Keycloak's front-channel logout page loads the clients' logout URIs in iframes.
	iframeRe = regexp.MustCompile(`(?s)<iframe[^>]*?src="([^"]+)"`)
)

// browse follows a navigation starting at target: redirects are followed; Keycloak's login form is submitted
// with the credentials; SAML auto-post forms are posted. It returns the first openlog UI location reached.
func (b *browser) browse(target, username, password string) string {
	b.t.Helper()
	method, body := http.MethodGet, url.Values(nil)
	submitted := false
	for i := 0; i < 20; i++ {
		var req *http.Request
		if method == http.MethodPost {
			req, _ = http.NewRequest(method, target, strings.NewReader(body.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req, _ = http.NewRequest(method, target, nil)
		}
		res, err := b.c.Do(req)
		if err != nil {
			b.t.Fatalf("%s %s: %v", method, target, err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			loc, err := res.Request.URL.Parse(res.Header.Get("Location"))
			if err != nil {
				b.t.Fatal(err)
			}
			if strings.HasPrefix(loc.String(), b.h.base) && !strings.HasPrefix(loc.Path, "/api/") {
				return strings.TrimPrefix(loc.String(), b.h.base)
			}
			target, method, body = loc.String(), http.MethodGet, nil
			continue
		}
		page := string(data)
		if m := samlRespRe.FindStringSubmatch(page); m != nil {
			action := html.UnescapeString(postFormRe.FindStringSubmatch(page)[1])
			relay := ""
			if r := relayRe.FindStringSubmatch(page); r != nil {
				relay = html.UnescapeString(r[1])
			}
			value := html.UnescapeString(m[2])
			if m[1] == "SAMLResponse" && strings.HasSuffix(action, "/acs") {
				b.lastSAMLResponse, b.lastRelayState, b.lastACS = value, relay, action
			}
			target, method, body = action, http.MethodPost, url.Values{m[1]: {value}, "RelayState": {relay}}
			continue
		}
		if m := logoutFormRe.FindStringSubmatch(page); m != nil {
			code := ""
			if c := sessionCodeRe.FindStringSubmatch(page); c != nil {
				code = html.UnescapeString(c[1])
			}
			action, _ := res.Request.URL.Parse(html.UnescapeString(m[1]))
			target, method, body = action.String(), http.MethodPost, url.Values{"session_code": {code}, "confirmLogout": {"Logout"}}
			continue
		}
		if m := loginFormRe.FindStringSubmatch(page); m != nil && !submitted {
			submitted = true
			b.loginForms++
			target, method = html.UnescapeString(m[1]), http.MethodPost
			body = url.Values{"username": {username}, "password": {password}, "credentialId": {""}}
			continue
		}
		if b.idpPage && strings.HasPrefix(target, b.h.kc) && res.StatusCode == http.StatusOK {
			for _, m := range iframeRe.FindAllStringSubmatch(page, -1) {
				src, err := res.Request.URL.Parse(html.UnescapeString(m[1]))
				if err != nil {
					b.t.Fatal(err)
				}
				fr, err := b.c.Get(src.String())
				if err != nil {
					b.t.Fatalf("front-channel logout iframe %s: %v", src, err)
				}
				fr.Body.Close()
				b.frames = append(b.frames, frame{url: src.String(), status: fr.StatusCode, csp: fr.Header.Get("Content-Security-Policy")})
			}
			return "idp-page"
		}
		b.t.Fatalf("unexpected page at %s (%d): %.1500s", target, res.StatusCode, page)
	}
	b.t.Fatal("too many redirects")
	return ""
}

// ssoLogin runs "Sign in with SSO" for email and signs in at Keycloak as username.
func (b *browser) ssoLogin(email, username, password, redirect string) string {
	b.t.Helper()
	var start struct {
		RedirectURL string `json:"redirect_url"`
	}
	if code := b.apiJSON(http.MethodPost, "/api/v1/auth/sso/start", map[string]string{"email": email, "redirect": redirect}, &start); code != http.StatusOK {
		b.t.Fatalf("sso start for %s: %d", email, code)
	}
	if !strings.HasPrefix(start.RedirectURL, b.h.kc) {
		b.t.Fatalf("redirect_url = %s", start.RedirectURL)
	}
	b.lastStart = start.RedirectURL
	return b.browse(start.RedirectURL, username, password)
}

// logoutEverywhere runs "Sign out everywhere (IdP)" and follows the browser through the identity provider.
func (b *browser) logoutEverywhere() string {
	b.t.Helper()
	b.me() // CSRF token
	var out struct {
		RedirectURL *string `json:"redirect_url"`
		Post        *struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"post"`
	}
	if code := b.apiJSON(http.MethodPost, "/api/v1/auth/sso/logout", map[string]string{"redirect": "/login"}, &out); code != http.StatusOK {
		b.t.Fatalf("sso logout: %d", code)
	}
	switch {
	case out.RedirectURL != nil:
		if !strings.HasPrefix(*out.RedirectURL, b.h.kc) {
			b.t.Fatalf("logout redirect_url = %s", *out.RedirectURL)
		}
		return b.browse(*out.RedirectURL, "", "")
	case out.Post != nil:
		res, err := b.c.PostForm(out.Post.URL, url.Values{"SAMLRequest": {out.Post.Fields["SAMLRequest"]}, "RelayState": {out.Post.Fields["RelayState"]}})
		if err != nil {
			b.t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := res.Request.URL.Parse(res.Header.Get("Location"))
		if loc == nil {
			b.t.Fatalf("IdP answered the POST LogoutRequest with %d", res.StatusCode)
		}
		return b.browse(loc.String(), "", "")
	}
	b.t.Fatal("the logout did not continue at the identity provider")
	return ""
}

func (h *harness) keycloakAdminToken() string {
	h.t.Helper()
	res, err := http.PostForm(h.kc+"/realms/master/protocol/openid-connect/token",
		url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {"admin"}})
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	if tok.AccessToken == "" {
		h.t.Fatalf("keycloak admin token: %d", res.StatusCode)
	}
	return tok.AccessToken
}

func (h *harness) keycloakAdmin(token, method, path, contentType string, body []byte) ([]byte, int) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.kc+"/admin/realms/"+realm+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return data, res.StatusCode
}

// registerSAMLClient imports openlog's SP metadata into Keycloak (as an administrator would) and adds the e-mail
// and group attribute mappers and IdP-initiated sign-in.
func (h *harness) registerSAMLClient(metadata []byte, spCertPEM, sloURL string) {
	h.t.Helper()
	token := h.keycloakAdminToken()
	list, _ := h.keycloakAdmin(token, http.MethodGet, "/clients", "", nil)
	var clients []map[string]any
	_ = json.Unmarshal(list, &clients)
	for _, c := range clients {
		if c["protocol"] == "saml" {
			h.keycloakAdmin(token, http.MethodDelete, "/clients/"+c["id"].(string), "", nil)
		}
	}
	rep, code := h.keycloakAdmin(token, http.MethodPost, "/client-description-converter", "text/plain", metadata)
	if code != http.StatusOK {
		h.t.Fatalf("client-description-converter: %d %s", code, rep)
	}
	var client map[string]any
	if err := json.Unmarshal(rep, &client); err != nil {
		h.t.Fatal(err)
	}
	attrs, _ := client["attributes"].(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["saml_name_id_format"] = "email"
	attrs["saml.force.name.id.format"] = "true"
	// Encrypted assertions with Keycloak's defaults (AES-256-GCM, RSA-OAEP with SHA-256), signed AuthnRequests and
	// LogoutRequests from openlog, front-channel single logout.
	certB64 := strings.Join(strings.Fields(strings.NewReplacer("-----BEGIN CERTIFICATE-----", "", "-----END CERTIFICATE-----", "").Replace(spCertPEM)), "")
	attrs["saml.encrypt"] = "true"
	attrs["saml.encryption.certificate"] = certB64
	attrs["saml.encryption.algorithm"] = "http://www.w3.org/2009/xmlenc11#aes256-gcm"
	attrs["saml.encryption.keyAlgorithm"] = "http://www.w3.org/2009/xmlenc11#rsa-oaep"
	attrs["saml.encryption.digestMethod"] = "http://www.w3.org/2001/04/xmlenc#sha256"
	attrs["saml.encryption.maskGenerationFunction"] = "http://www.w3.org/2009/xmlenc11#mgf1sha256"
	attrs["saml.client.signature"] = "true"
	attrs["saml.signing.certificate"] = certB64
	attrs["saml_single_logout_service_url_redirect"] = sloURL
	attrs["saml_single_logout_service_url_post"] = sloURL
	attrs["saml.assertion.signature"] = "true"
	attrs["saml.server.signature"] = "true"
	attrs["saml.signature.algorithm"] = "RSA_SHA256"
	attrs["saml_idp_initiated_sso_url_name"] = "openlog-saml"
	attrs["saml_idp_initiated_sso_relay_state"] = "/hosts"
	client["attributes"] = attrs
	client["enabled"] = true
	client["frontchannelLogout"] = true
	client["protocolMappers"] = []map[string]any{
		{"name": "email", "protocol": "saml", "protocolMapper": "saml-user-property-mapper",
			"config": map[string]string{"user.attribute": "email", "attribute.name": "email", "attribute.nameformat": "Basic", "friendly.name": "email"}},
		{"name": "groups", "protocol": "saml", "protocolMapper": "saml-group-membership-mapper",
			"config": map[string]string{"attribute.name": "groups", "full.path": "false", "single": "false", "attribute.nameformat": "Basic"}},
	}
	body, _ := json.Marshal(client)
	if out, code := h.keycloakAdmin(token, http.MethodPost, "/clients", "application/json", body); code != http.StatusCreated {
		h.t.Fatalf("create SAML client: %d %s", code, out)
	}
}

// updateClient changes the first Keycloak client match accepts.
func (h *harness) updateClient(match func(client map[string]any) bool, mutate func(client, attrs map[string]any)) {
	h.t.Helper()
	token := h.keycloakAdminToken()
	list, _ := h.keycloakAdmin(token, http.MethodGet, "/clients", "", nil)
	var clients []map[string]any
	_ = json.Unmarshal(list, &clients)
	for _, c := range clients {
		if !match(c) {
			continue
		}
		attrs, _ := c["attributes"].(map[string]any)
		if attrs == nil {
			attrs = map[string]any{}
		}
		mutate(c, attrs)
		c["attributes"] = attrs
		body, _ := json.Marshal(c)
		if out, code := h.keycloakAdmin(token, http.MethodPut, "/clients/"+c["id"].(string), "application/json", body); code != http.StatusNoContent {
			h.t.Fatalf("update client: %d %s", code, out)
		}
		return
	}
	h.t.Fatal("Keycloak client not found")
}

func oidcClient(c map[string]any) bool { return c["clientId"] == "openlog" }
func samlClient(c map[string]any) bool { return c["protocol"] == "saml" }

// logoutAtKeycloak ends every Keycloak session of a user, one session at a time over the admin API; Keycloak notifies
// the clients of each session through their back-channel logout URLs. POST /users/{id}/logout is not used: Keycloak
// 26.3 ends all sessions of the user there but sends the back-channel logout for only one of them, so with a session
// left over from an earlier subtest the session of this subtest's browser was never notified.
func (h *harness) logoutAtKeycloak(username string) {
	h.t.Helper()
	token := h.keycloakAdminToken()
	data, _ := h.keycloakAdmin(token, http.MethodGet, "/users?exact=true&username="+url.QueryEscape(username), "", nil)
	var users []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &users); err != nil || len(users) != 1 {
		h.t.Fatalf("keycloak user %s: %s", username, data)
	}
	data, code := h.keycloakAdmin(token, http.MethodGet, "/users/"+users[0].ID+"/sessions", "", nil)
	var sessions []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &sessions); err != nil || code != http.StatusOK || len(sessions) == 0 {
		h.t.Fatalf("keycloak sessions of %s: %d %s", username, code, data)
	}
	for _, s := range sessions {
		if out, code := h.keycloakAdmin(token, http.MethodDelete, "/sessions/"+url.PathEscape(s.ID), "", nil); code != http.StatusNoContent {
			h.t.Fatalf("keycloak logout of %s (session %s): %d %s", username, s.ID, code, out)
		}
	}
}

// waitSignedOut polls the browser's session until it is gone.
func (b *browser) waitSignedOut(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if code, _ := b.me(); code == http.StatusUnauthorized {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// logoutVias returns the via values of the organization's audit events of action.
func logoutVias(t *testing.T, owner *browser, action string) map[string]bool {
	t.Helper()
	var audit struct {
		Events []struct {
			Details map[string]any `json:"details"`
		} `json:"events"`
	}
	if code := owner.apiJSON(http.MethodGet, "/api/v1/audit-log?limit=500&action="+action, nil, &audit); code != http.StatusOK {
		t.Fatalf("audit: %d", code)
	}
	via := map[string]bool{}
	for _, e := range audit.Events {
		if v, _ := e.Details["via"].(string); v != "" {
			via[v] = true
		}
	}
	return via
}

func TestKeycloakSSO(t *testing.T) {
	dsn, kc := os.Getenv("OPENLOG_TEST_POSTGRES_DSN"), strings.TrimRight(os.Getenv("OPENLOG_TEST_KEYCLOAK_URL"), "/")
	if dsn == "" || kc == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN and OPENLOG_TEST_KEYCLOAK_URL are required")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, Application: "openlog-ssoe2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool, ms, log); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM organizations WHERE tenant_id LIKE 'ssoe2e%'`, `DELETE FROM users WHERE email LIKE '%@acme.test' OR email LIKE '%.acme.test' OR email LIKE '%@evil.test'`,
		`DELETE FROM login_failures`} { // login_failures: rate limit counters of earlier runs
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	listen := "127.0.0.1:0"
	if v := os.Getenv("OPENLOG_TEST_LISTEN_ADDR"); v != "" {
		listen = v
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	base := "http://127.0.0.1:" + port
	callbackHost := strings.TrimSpace(os.Getenv("OPENLOG_TEST_CALLBACK_HOST"))
	if callbackHost != "" {
		// openlog's public URL uses the host Keycloak's container reaches; the test's own clients dial the loopback.
		base = "http://" + net.JoinHostPort(callbackHost, port)
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		http.DefaultTransport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, p, err := net.SplitHostPort(addr); err == nil && host == callbackHost {
				addr = net.JoinHostPort("127.0.0.1", p)
			}
			return dialer.DialContext(ctx, network, addr)
		}
	}
	h := &harness{t: t, base: base, kc: kc, resolver: txtResolver{}, callback: callbackHost != ""}
	users := postgres.NewStore(pool)
	authSvc := auth.NewService(users, auth.Config{CookieSecure: false}, log)
	tenantID, _ := auth.NewTenantID()
	res, err := authSvc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: "ssoe2e" + tenantID[:8], OrgName: "Acme", OwnerEmail: "owner@acme.test",
		OwnerPassword: "owner password for e2e"})
	if err != nil {
		t.Fatal(err)
	}
	h.orgID = res.Org.ID
	ssoSvc := sso.NewService(authSvc, postgres.NewSSOStore(pool), sso.Config{PublicURL: h.base, CookieSecure: false, AllowPrivateNetworks: true,
		SecretBox: sso.NewSecretBox("end-to-end sso secret key, 32 bytes or more", "", "", ""), Resolver: h.resolver, SCIMEnabled: true}, log)
	authSvc.SetSessionPolicy(ssoSvc.Policy())
	srv := api.New(config.API{QueryTimeout: time.Second, MaxRows: 100}, query.New(nil, "openlog", time.Second), authSvc, log, nil)
	srv.SetAccounts(authSvc)
	srv.SetSSO(ssoSvc)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()

	// ---- administrator setup over the API ----
	owner := h.newBrowser()
	if code := owner.passwordLogin("owner@acme.test", "owner password for e2e"); code != http.StatusOK {
		t.Fatalf("owner login: %d", code)
	}
	_, ownerMe := owner.me()
	h.ownerID = ownerMe.User.ID

	// carol@acme.test joins with a password before the domain is claimed (enforcement test below).
	var inv struct {
		Token string `json:"token"`
	}
	if code := owner.apiJSON(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "carol@acme.test", "role": "member"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite: %d", code)
	}
	carol := h.newBrowser()
	if code := carol.apiJSON(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": inv.Token, "password": "carol password e2e", "name": "Carol"}, nil); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	var dom struct {
		ID        string            `json:"id"`
		Verified  bool              `json:"verified"`
		DNSRecord map[string]string `json:"dns_record"`
	}
	if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/domains", map[string]string{"domain": "acme.test"}, &dom); code != http.StatusCreated {
		t.Fatalf("add domain: %d", code)
	}
	h.resolver[dom.DNSRecord["name"]] = []string{dom.DNSRecord["value"]}
	if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/domains/"+dom.ID+"/verify", map[string]string{"method": "dns_txt"}, &dom); code != http.StatusOK || !dom.Verified {
		t.Fatalf("verify domain: %d %+v", code, dom)
	}
	acmeDomainID := dom.ID
	if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/domains", map[string]string{"domain": "sub.acme.test"}, &dom); code != http.StatusCreated {
		t.Fatalf("add second domain: %d", code)
	}
	h.resolver[dom.DNSRecord["name"]] = []string{dom.DNSRecord["value"]}
	if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/domains/"+dom.ID+"/verify", map[string]string{"method": "dns_txt"}, &dom); code != http.StatusOK || !dom.Verified {
		t.Fatalf("verify second domain: %d %+v", code, dom)
	}
	if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/role-mappings", map[string]any{"mappings": []map[string]string{{"group": "openlog-admins", "role": "admin"}}}, nil); code != http.StatusOK {
		t.Fatalf("role mappings: %d", code)
	}
	oidcBody := map[string]any{"protocol": "oidc", "name": "Keycloak", "enabled": true, "default_role": "viewer",
		"oidc": map[string]any{"issuer": kc + "/realms/" + realm, "client_id": "openlog", "client_secret": "openlog-oidc-secret"}}
	var state struct {
		Connection struct {
			ID       string `json:"id"`
			Tested   bool   `json:"tested"`
			LastTest *struct {
				OK      bool           `json:"ok"`
				Error   string         `json:"error"`
				Details map[string]any `json:"details"`
			} `json:"last_test"`
		} `json:"connection"`
		ServiceProvider map[string]any `json:"service_provider"`
	}
	if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/connection", oidcBody, &state); code != http.StatusOK {
		t.Fatalf("save OIDC connection: %d", code)
	}
	oidcID := state.Connection.ID
	if bcl, _ := state.ServiceProvider["oidc_backchannel_logout_uri"].(string); bcl != h.base+"/api/v1/sso/oidc/"+oidcID+"/backchannel-logout" {
		t.Fatalf("oidc_backchannel_logout_uri = %q", bcl)
	}
	var checks struct {
		OK     bool             `json:"ok"`
		Checks []map[string]any `json:"checks"`
	}
	if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/connection/test", nil, &checks); code != http.StatusOK || !checks.OK {
		t.Fatalf("connection checks: %d %+v", code, checks)
	}

	t.Run("OIDC sign-in with JIT provisioning and group roles", func(t *testing.T) {
		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("alice landed on %s", landing)
		}
		code, me := alice.me()
		if code != http.StatusOK || me.User.Email != "alice@acme.test" || me.Organization.ID != h.orgID || *me.Role != "admin" {
			t.Fatalf("alice me: %d %+v", code, me)
		}
		bob := h.newBrowser()
		bob.t = t
		if landing := bob.ssoLogin("bob@acme.test", "bob", "bob-password", "/apm"); landing != "/apm" {
			t.Fatalf("bob landed on %s", landing)
		}
		if _, me := bob.me(); *me.Role != "viewer" {
			t.Fatalf("bob role = %s", *me.Role)
		}
		// email_verified=false at the IdP.
		una := h.newBrowser()
		una.t = t
		if landing := una.ssoLogin("unverified@acme.test", "unverified", "unverified-password", "/"); landing != "/login?sso_error=email_not_verified" {
			t.Fatalf("unverified landed on %s", landing)
		}
		// A user of another domain signing in at the IdP after starting with an acme.test address.
		eve := h.newBrowser()
		eve.t = t
		if landing := eve.ssoLogin("anyone@acme.test", "eve", "eve-password", "/"); landing != "/login?sso_error=domain_not_verified" {
			t.Fatalf("eve landed on %s", landing)
		}
		if code, _ := eve.me(); code != http.StatusUnauthorized {
			t.Fatalf("eve has a session: %d", code)
		}
		frank := h.newBrowser()
		frank.t = t
		if landing := frank.ssoLogin("frank@sub.acme.test", "frank", "frank-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("frank landed on %s", landing)
		}
	})

	t.Run("test sign-in and enforcement", func(t *testing.T) {
		owner.t = t
		var start struct {
			RedirectURL string `json:"redirect_url"`
		}
		if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/connection/test/start", nil, &start); code != http.StatusOK {
			t.Fatalf("test start: %d", code)
		}
		if landing := owner.browse(start.RedirectURL, "alice", "alice-password"); landing != "/settings/sso?sso_test=ok" {
			t.Fatalf("test landed on %s", landing)
		}
		if code := owner.apiJSON(http.MethodGet, "/api/v1/sso/connection", nil, &state); code != http.StatusOK || !state.Connection.Tested || state.Connection.LastTest.Details["email"] != "alice@acme.test" {
			t.Fatalf("after test: %+v", state.Connection)
		}
		// The owner's password session still works after the test sign-in (no session was created for alice).
		if code, me := owner.me(); code != http.StatusOK || me.User.Email != "owner@acme.test" {
			t.Fatalf("owner session after test: %d %+v", code, me)
		}
		if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/enforcement", map[string]any{"enforce": true, "break_glass_user_ids": []string{}}, nil); code != http.StatusConflict {
			t.Fatalf("enforce without break-glass: %d", code)
		}
		if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/enforcement", map[string]any{"enforce": true, "break_glass_user_ids": []string{h.ownerID}}, nil); code != http.StatusOK {
			t.Fatalf("enforce: %d", code)
		}
		// carol@acme.test joined with a password: her password session and sign-in are refused.
		carol.t = t
		if code, me := carol.me(); code != http.StatusOK || me.Organization != nil {
			t.Fatalf("carol's password session acts in the enforcing organization: %d %+v", code, me)
		}
		var errBody struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if code := carol.apiJSON(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "carol@acme.test", "password": "carol password e2e"}, &errBody); code != http.StatusForbidden || !strings.Contains(errBody.Error.Message, "single sign-on") {
			t.Fatalf("carol password login: %d %+v", code, errBody)
		}
		// Claimed domain: a new invitation to acme.test must be accepted with SSO.
		var zoe struct {
			Token string `json:"token"`
		}
		if code := owner.apiJSON(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "zoe@acme.test", "role": "member"}, &zoe); code != http.StatusCreated {
			t.Fatalf("invite zoe: %d", code)
		}
		var lookup struct {
			SSO *struct {
				Required         bool `json:"required"`
				SameOrganization bool `json:"same_organization"`
			} `json:"sso"`
		}
		anon := h.newBrowser()
		anon.t = t
		if code := anon.apiJSON(http.MethodPost, "/api/v1/invitations/lookup", map[string]string{"token": zoe.Token}, &lookup); code != http.StatusOK ||
			lookup.SSO == nil || !lookup.SSO.Required || !lookup.SSO.SameOrganization {
			t.Fatalf("claimed invitation lookup: %d %+v", code, lookup)
		}
		if code := anon.apiJSON(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": zoe.Token, "password": "zoe password e2e"}, nil); code != http.StatusConflict {
			t.Fatalf("password acceptance of a claimed invitation: %d", code)
		}
		// The break-glass owner signs in with a password; alice with SSO.
		owner2 := h.newBrowser()
		if code := owner2.passwordLogin("owner@acme.test", "owner password for e2e"); code != http.StatusOK {
			t.Fatalf("break-glass login: %d", code)
		}
		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("alice under enforcement landed on %s", landing)
		}
		if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/enforcement", map[string]any{"enforce": false, "break_glass_user_ids": []string{h.ownerID}}, nil); code != http.StatusOK {
			t.Fatalf("enforcement off: %d", code)
		}
	})

	t.Run("OIDC RP-initiated logout ends the Keycloak session", func(t *testing.T) {
		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("alice landed on %s", landing)
		}
		alice.me()
		var info struct {
			SSO       bool   `json:"sso"`
			Protocol  string `json:"protocol"`
			IdPLogout bool   `json:"idp_logout"`
		}
		if code := alice.apiJSON(http.MethodGet, "/api/v1/auth/sso/session", nil, &info); code != http.StatusOK || !info.SSO || info.Protocol != "oidc" || !info.IdPLogout {
			t.Fatalf("session info: %d %+v", code, info)
		}
		if landing := alice.logoutEverywhere(); landing != "/login?sso_logout=ok" {
			t.Fatalf("OIDC logout landed on %s", landing)
		}
		if code, _ := alice.me(); code != http.StatusUnauthorized {
			t.Fatalf("alice after logout: %d", code)
		}
		// Keycloak asks for the password again: its session ended.
		alice.loginForms = 0
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" || alice.loginForms != 1 {
			t.Fatalf("sign-in after logout landed on %s with %d login forms", landing, alice.loginForms)
		}
	})

	t.Run("OIDC back-channel logout from Keycloak", func(t *testing.T) {
		if !h.callback {
			t.Skip("OPENLOG_TEST_CALLBACK_HOST is required: Keycloak calls openlog")
		}
		h.updateClient(oidcClient, func(c, attrs map[string]any) {
			c["frontchannelLogout"] = false
			attrs["backchannel.logout.url"] = h.base + "/api/v1/sso/oidc/" + oidcID + "/backchannel-logout"
			attrs["backchannel.logout.session.required"] = "true"
			attrs["frontchannel.logout.url"] = ""
		})
		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("alice landed on %s", landing)
		}
		if code, _ := alice.me(); code != http.StatusOK {
			t.Fatalf("alice before the Keycloak logout: %d", code)
		}
		h.logoutAtKeycloak("alice")
		if !alice.waitSignedOut(15 * time.Second) {
			t.Fatal("alice's openlog session survived the back-channel logout")
		}
		owner.t = t
		if via := logoutVias(t, owner, "sso.logout"); !via["oidc_backchannel"] {
			t.Fatalf("sso.logout audit events by via = %v", via)
		}
	})

	t.Run("OIDC front-channel logout from Keycloak", func(t *testing.T) {
		if !h.callback {
			t.Skip("OPENLOG_TEST_CALLBACK_HOST is required: the logout URI is registered at Keycloak")
		}
		h.updateClient(oidcClient, func(c, attrs map[string]any) {
			c["frontchannelLogout"] = true
			attrs["backchannel.logout.url"] = ""
			attrs["frontchannel.logout.url"] = h.base + "/api/v1/sso/oidc/" + oidcID + "/frontchannel-logout"
			attrs["frontchannel.logout.session.required"] = "true"
		})
		defer h.updateClient(oidcClient, func(_, attrs map[string]any) { attrs["frontchannel.logout.url"] = "" })
		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("alice landed on %s", landing)
		}
		if code, _ := alice.me(); code != http.StatusOK {
			t.Fatalf("alice before the Keycloak logout: %d", code)
		}
		// The user signs out at Keycloak; its logout page loads openlog's front-channel logout URI in an iframe.
		alice.idpPage = true
		if landing := alice.browse(kc+"/realms/"+realm+"/protocol/openid-connect/logout", "", ""); landing != "idp-page" {
			t.Fatalf("Keycloak logout landed on %s", landing)
		}
		var ours *frame
		for i := range alice.frames {
			if strings.HasPrefix(alice.frames[i].url, h.base+"/api/v1/sso/oidc/"+oidcID+"/frontchannel-logout?") {
				ours = &alice.frames[i]
			}
		}
		if ours == nil || ours.status != http.StatusOK || !strings.HasSuffix(ours.csp, "frame-ancestors "+kc) {
			t.Fatalf("front-channel logout iframes = %+v", alice.frames)
		}
		if code, _ := alice.me(); code != http.StatusUnauthorized {
			t.Fatalf("alice after the front-channel logout: %d", code)
		}
		owner.t = t
		if via := logoutVias(t, owner, "sso.logout"); !via["oidc_frontchannel"] {
			t.Fatalf("sso.logout audit events by via = %v", via)
		}
	})

	var samlID string
	t.Run("second SAML connection with encrypted assertions, IdP-initiated sign-in, replay and SCIM deprovisioning", func(t *testing.T) {
		owner.t = t
		samlBody := map[string]any{"protocol": "saml", "name": "Keycloak SAML", "enabled": true, "default_role": "member",
			// Keycloak's descriptor is unsigned: an explicit choice (D-098).
			"saml": map[string]any{"idp_metadata_url": kc + "/realms/" + realm + "/protocol/saml/descriptor", "allow_unsigned_metadata": true, "allow_idp_initiated": true,
				"relay_state_allowlist": []string{"/hosts"}, "sign_authn_requests": true}}
		if code := owner.apiJSON(http.MethodPost, "/api/v1/sso/connections", samlBody, &state); code != http.StatusCreated {
			t.Fatalf("create SAML connection: %d", code)
		}
		samlID = state.Connection.ID
		// acme.test signs in through SAML now; sub.acme.test stays on the default OIDC connection.
		if code := owner.apiJSON(http.MethodPut, "/api/v1/sso/domains/"+acmeDomainID, map[string]any{"connection_id": samlID}, nil); code != http.StatusOK {
			t.Fatalf("assign acme.test: %d", code)
		}
		mdURL, _ := state.ServiceProvider["saml_metadata_url"].(string)
		res, err := http.Get(mdURL)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("SP metadata: %v %v", err, res)
		}
		metadata, _ := io.ReadAll(res.Body)
		res.Body.Close()
		h.registerSAMLClient(metadata, state.ServiceProvider["saml_certificate_pem"].(string), state.ServiceProvider["saml_slo_url"].(string))
		var disc struct {
			Protocol       string `json:"protocol"`
			ConnectionName string `json:"connection_name"`
		}
		for email, want := range map[string]string{"dave@acme.test": "saml", "frank@sub.acme.test": "oidc"} {
			if code := owner.apiJSON(http.MethodPost, "/api/v1/auth/sso/discover", map[string]string{"email": email}, &disc); code != http.StatusOK || disc.Protocol != want {
				t.Fatalf("discover %s: %d %+v", email, code, disc)
			}
		}

		dave := h.newBrowser()
		dave.t = t
		if landing := dave.ssoLogin("dave@acme.test", "dave", "dave-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("dave landed on %s", landing)
		}
		if code, me := dave.me(); code != http.StatusOK || me.User.Email != "dave@acme.test" || *me.Role != "member" {
			t.Fatalf("dave me: %d %+v", code, me)
		}
		if raw, _ := base64.StdEncoding.DecodeString(dave.lastSAMLResponse); !bytes.Contains(raw, []byte("EncryptedAssertion")) || bytes.Contains(raw, []byte("dave@acme.test")) {
			t.Fatalf("the SAML assertion was not encrypted: %.600s", raw)
		}
		// frank (sub.acme.test) still signs in through the default OIDC connection.
		frank := h.newBrowser()
		frank.t = t
		if landing := frank.ssoLogin("frank@sub.acme.test", "frank", "frank-password", "/hosts"); landing != "/hosts" || !strings.Contains(frank.lastStart, "/protocol/openid-connect/") {
			t.Fatalf("frank landed on %s via %s", landing, frank.lastStart)
		}
		// Replaying the posted SAMLResponse is refused.
		replay := h.newBrowser()
		replay.t = t
		if landing := replay.browsePost(dave.lastACS, dave.lastSAMLResponse, dave.lastRelayState); !strings.HasPrefix(landing, "/login?sso_error=") {
			t.Fatalf("replay landed on %s", landing)
		}
		if code, _ := replay.me(); code != http.StatusUnauthorized {
			t.Fatalf("replay created a session: %d", code)
		}

		alice := h.newBrowser()
		alice.t = t
		if landing := alice.ssoLogin("alice@acme.test", "alice", "alice-password", "/dashboards"); landing != "/dashboards" {
			t.Fatalf("alice (SAML) landed on %s", landing)
		}
		if _, me := alice.me(); *me.Role != "admin" {
			t.Fatalf("alice SAML role = %s", *me.Role)
		}

		// IdP-initiated: Keycloak's IdP-initiated URL with the configured relay state.
		bob := h.newBrowser()
		bob.t = t
		if landing := bob.browse(kc+"/realms/"+realm+"/protocol/saml/clients/openlog-saml", "bob", "bob-password"); landing != "/hosts" {
			t.Fatalf("bob IdP-initiated landed on %s", landing)
		}
		if _, me := bob.me(); *me.Role != "member" { // groups present, no mapping matches → default role
			t.Fatalf("bob role after SAML = %s", *me.Role)
		}

		// SCIM: deactivating alice ends her SSO session immediately.
		var tok struct {
			Secret string `json:"secret"`
		}
		if code := owner.apiJSON(http.MethodPost, "/api/v1/scim/tokens", map[string]string{"name": "e2e"}, &tok); code != http.StatusCreated {
			t.Fatalf("scim token: %d", code)
		}
		scim := func(method, path string, body any) (int, map[string]any) {
			buf, _ := json.Marshal(body)
			req, _ := http.NewRequest(method, h.base+"/api/scim/v2"+path, bytes.NewReader(buf))
			req.Header.Set("Authorization", "Bearer "+tok.Secret)
			req.Header.Set("Content-Type", "application/scim+json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			out := map[string]any{}
			_ = json.NewDecoder(res.Body).Decode(&out)
			return res.StatusCode, out
		}
		// SCIM e-mail change of frank (sub.acme.test); an address of another account is a uniqueness conflict.
		code, fu := scim(http.MethodPost, "/Users", map[string]any{"userName": "frank@sub.acme.test", "active": true})
		if code != http.StatusCreated {
			t.Fatalf("scim create frank: %d %v", code, fu)
		}
		patchOp := func(path string, value any) map[string]any {
			return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []map[string]any{{"op": "replace", "path": path, "value": value}}}
		}
		if code, body := scim(http.MethodPatch, "/Users/"+fu["id"].(string), patchOp(`emails[type eq "work"].value`, "frank.renamed@sub.acme.test")); code != http.StatusOK {
			t.Fatalf("scim e-mail change: %d %v", code, body)
		}
		if code, body := scim(http.MethodPatch, "/Users/"+fu["id"].(string), patchOp(`emails[type eq "work"].value`, "dave@acme.test")); code != http.StatusConflict || body["scimType"] != "uniqueness" {
			t.Fatalf("scim conflicting e-mail: %d %v", code, body)
		}
		code, user := scim(http.MethodPost, "/Users", map[string]any{"userName": "alice@acme.test", "active": true})
		if code != http.StatusCreated {
			t.Fatalf("scim create: %d %v", code, user)
		}
		code, _ = scim(http.MethodPatch, "/Users/"+user["id"].(string), map[string]any{
			"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}}})
		if code != http.StatusOK {
			t.Fatalf("scim deactivate: %d", code)
		}
		if code, _ := alice.me(); code != http.StatusUnauthorized {
			t.Fatalf("alice's SSO session after SCIM deactivation: %d", code)
		}
		// … and JIT does not re-add a deprovisioned user.
		again := h.newBrowser()
		again.t = t
		if landing := again.ssoLogin("alice@acme.test", "alice", "alice-password", "/"); landing != "/login?sso_error=deprovisioned" {
			t.Fatalf("deprovisioned alice landed on %s", landing)
		}

		// Audit trail.
		var audit struct {
			Events []struct{ Action string } `json:"events"`
		}
		if code := owner.apiJSON(http.MethodGet, "/api/v1/audit-log?limit=500", nil, &audit); code != http.StatusOK {
			t.Fatalf("audit: %d", code)
		}
		seen := map[string]bool{}
		for _, e := range audit.Events {
			seen[e.Action] = true
		}
		for _, a := range []string{"sso.login", "sso.login_failed", "sso.connection.test_login", "sso.enforcement.update", "member.add", "scim.user.deactivate",
			"sso.connection.create", "sso.domain.assign", "sso.logout", "scim.user.email_change"} {
			if !seen[a] {
				t.Errorf("audit action %s missing (%v)", a, seen)
			}
		}
	})

	t.Run("SAML single logout, SP- and IdP-initiated", func(t *testing.T) {
		if samlID == "" {
			t.Skip("SAML connection not created")
		}
		dave := h.newBrowser()
		dave.t = t
		if landing := dave.ssoLogin("dave@acme.test", "dave", "dave-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("dave landed on %s", landing)
		}
		if landing := dave.logoutEverywhere(); landing != "/login?sso_logout=ok" {
			t.Fatalf("SP-initiated SAML logout landed on %s", landing)
		}
		if code, _ := dave.me(); code != http.StatusUnauthorized {
			t.Fatalf("dave after logout: %d", code)
		}
		dave.loginForms = 0
		if landing := dave.ssoLogin("dave@acme.test", "dave", "dave-password", "/hosts"); landing != "/hosts" || dave.loginForms != 1 {
			t.Fatalf("sign-in after SAML logout landed on %s with %d login forms", landing, dave.loginForms)
		}

		// IdP-initiated: the user signs out at Keycloak (its logout page); Keycloak sends a LogoutRequest to openlog through
		// the browser (front-channel logout of the SAML client).
		bob := h.newBrowser()
		bob.t = t
		if landing := bob.ssoLogin("bob@acme.test", "bob", "bob-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("bob landed on %s", landing)
		}
		if code, _ := bob.me(); code != http.StatusOK {
			t.Fatalf("bob before IdP logout: %d", code)
		}
		bob.idpPage = true
		if landing := bob.browse(kc+"/realms/"+realm+"/protocol/openid-connect/logout", "", ""); landing != "idp-page" && !strings.HasPrefix(landing, "/login") {
			t.Fatalf("IdP-initiated logout landed on %s", landing)
		}
		if code, _ := bob.me(); code != http.StatusUnauthorized {
			t.Fatalf("bob's openlog session after the IdP logout: %d", code)
		}
		var audit struct {
			Events []struct {
				Action  string         `json:"action"`
				Details map[string]any `json:"details"`
			} `json:"events"`
		}
		owner.t = t
		if code := owner.apiJSON(http.MethodGet, "/api/v1/audit-log?limit=500&action=sso.logout", nil, &audit); code != http.StatusOK {
			t.Fatalf("audit: %d", code)
		}
		via := map[string]bool{}
		for _, e := range audit.Events {
			if v, _ := e.Details["via"].(string); v != "" {
				via[v] = true
			}
		}
		if !via["idp"] || !via["user"] {
			t.Fatalf("sso.logout audit events by via = %v", via)
		}
	})

	t.Run("SAML SOAP back-channel logout from Keycloak", func(t *testing.T) {
		if samlID == "" || !h.callback {
			t.Skip("SAML connection and OPENLOG_TEST_CALLBACK_HOST are required")
		}
		h.updateClient(samlClient, func(c, attrs map[string]any) {
			c["frontchannelLogout"] = false
			attrs["saml_single_logout_service_url_soap"] = h.base + "/api/v1/sso/saml/" + samlID + "/slo/soap"
		})
		dave := h.newBrowser()
		dave.t = t
		if landing := dave.ssoLogin("dave@acme.test", "dave", "dave-password", "/hosts"); landing != "/hosts" {
			t.Fatalf("dave landed on %s", landing)
		}
		if code, _ := dave.me(); code != http.StatusOK {
			t.Fatalf("dave before the Keycloak logout: %d", code)
		}
		h.logoutAtKeycloak("dave")
		if !dave.waitSignedOut(15 * time.Second) {
			t.Fatal("dave's openlog session survived the SOAP back-channel logout")
		}
		owner.t = t
		if via := logoutVias(t, owner, "sso.logout"); !via["idp_soap"] {
			t.Fatalf("sso.logout audit events by via = %v", via)
		}
	})
}

// browsePost posts a SAMLResponse to the ACS and follows the redirects.
func (b *browser) browsePost(acs, samlResponse, relay string) string {
	b.t.Helper()
	res, err := b.c.PostForm(acs, url.Values{"SAMLResponse": {samlResponse}, "RelayState": {relay}})
	if err != nil {
		b.t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := res.Request.URL.Parse(res.Header.Get("Location"))
	if loc == nil {
		b.t.Fatal(fmt.Sprintf("ACS answered %d without Location", res.StatusCode))
	}
	if strings.HasPrefix(loc.Path, "/api/") {
		return b.browse(loc.String(), "", "")
	}
	return strings.TrimPrefix(loc.String(), b.h.base)
}
