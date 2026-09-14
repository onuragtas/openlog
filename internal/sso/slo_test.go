package sso_test

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

var loginIP atomic.Int64

// testMeta returns a client address per call (the public sign-in endpoints are rate limited per IP).
func testMeta() auth.ClientMeta {
	n := loginIP.Add(1)
	return auth.ClientMeta{IP: fmt.Sprintf("198.18.%d.%d", n/250, n%250+1)}
}

// samlLogin runs an SP-initiated SAML sign-in of email through connection c and returns the completed outcome.
func samlLogin(t *testing.T, env *testEnv, idp *testIdP, c sso.Connection, email string) sso.Outcome {
	t.Helper()
	ctx := context.Background()
	st, err := env.sso.StartLogin(ctx, email, "/hosts", testMeta())
	if err != nil {
		t.Fatal(err)
	}
	reqID, relay := authnRequestID(t, st.URL)
	resp := idp.makeResponse(t, respOpts{requestID: reqID, audience: env.sso.SAMLEntityID(c.ID), recipient: env.sso.SAMLACSURL(c.ID), email: email})
	out := env.sso.SAMLACS(ctx, c.ID, base64.StdEncoding.EncodeToString(resp), relay, testMeta())
	if !strings.HasPrefix(out.Redirect, "/api/v1/sso/saml/complete?state=") {
		t.Fatalf("ACS: %+v", out)
	}
	q, _ := url.ParseQuery(strings.SplitN(out.Redirect, "?", 2)[1])
	cookie := env.sso.BindingCookie(st)
	done := env.sso.SAMLComplete(ctx, q.Get("state"), func(n string) string {
		if n == cookie.Name {
			return cookie.Value
		}
		return ""
	}, testMeta())
	if done.Session == nil {
		t.Fatalf("complete: %+v", done)
	}
	return done
}

// principalFor authenticates a session token like a browser request.
func principalFor(env *testEnv, token string) (*auth.Principal, error) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
	return env.auth.Authenticate(r)
}

func deflateB64(t *testing.T, el *etree.Element) string {
	t.Helper()
	doc := etree.NewDocument()
	doc.SetRoot(el)
	raw, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = fw.Write(raw)
	_ = fw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// idpRedirectQuery encodes a message for the HTTP-Redirect binding signed by the IdP key with sigAlg ("" = unsigned).
func idpRedirectQuery(t *testing.T, key *rsa.PrivateKey, param string, el *etree.Element, relay, sigAlg string) string {
	t.Helper()
	q := param + "=" + url.QueryEscape(deflateB64(t, el))
	if relay != "" {
		q += "&RelayState=" + url.QueryEscape(relay)
	}
	if sigAlg == "" {
		return q
	}
	q += "&SigAlg=" + url.QueryEscape(sigAlg)
	var sig []byte
	var err error
	if strings.HasSuffix(sigAlg, "rsa-sha1") {
		sum := sha1.Sum([]byte(q))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, sum[:])
	} else {
		sum := sha256.Sum256([]byte(q))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return q + "&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
}

const sigRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"

// spMessage verifies the SP signature of an HTTP-Redirect binding URL and returns the decoded message.
func spMessage(t *testing.T, c sso.Connection, rawURL, param string) (*etree.Element, url.Values) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]string{}
	for _, part := range strings.Split(u.RawQuery, "&") {
		k, v, _ := strings.Cut(part, "=")
		raw[k] = v
	}
	signed := param + "=" + raw[param]
	if r, ok := raw["RelayState"]; ok {
		signed += "&RelayState=" + r
	}
	signed += "&SigAlg=" + raw["SigAlg"]
	block, _ := pem.Decode([]byte(c.SAML.SPCertificatePEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	sig, _ := base64.StdEncoding.DecodeString(q.Get("Signature"))
	sum := sha256.Sum256([]byte(signed))
	if q.Get("SigAlg") != sigRSASHA256 || rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, sum[:], sig) != nil {
		t.Fatalf("SP redirect signature invalid: %s", rawURL)
	}
	deflated, _ := base64.StdEncoding.DecodeString(q.Get(param))
	xmlDoc, err := io.ReadAll(flate.NewReader(bytes.NewReader(deflated)))
	if err != nil {
		t.Fatal(err)
	}
	d := etree.NewDocument()
	if err := d.ReadFromBytes(xmlDoc); err != nil {
		t.Fatal(err)
	}
	return d.Root(), q
}

// signEnveloped signs el (HTTP-POST binding) with the IdP key.
func signEnveloped(t *testing.T, idp *testIdP, el *etree.Element) *etree.Element {
	t.Helper()
	ks := dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{idp.cert.Raw}, PrivateKey: idp.key})
	sc := dsig.NewDefaultSigningContext(ks)
	if err := sc.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	signed, err := sc.SignEnveloped(el)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func postB64(t *testing.T, el *etree.Element) string {
	t.Helper()
	doc := etree.NewDocument()
	doc.SetRoot(el)
	b, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func sloIdP(t *testing.T) *testIdP {
	t.Helper()
	idp := newTestIdP(t, idpEntityID)
	slo, _ := url.Parse("https://idp.example/slo")
	idp.idp.LogoutURL = *slo
	return idp
}

func TestSAMLSingleLogout(t *testing.T) {
	env := newTestEnv(t)
	env.verifyDomain(t, "example.com")
	idp := sloIdP(t)
	c := env.samlConnection(t, idp.metadataXML(t), false, nil)
	ctx := context.Background()
	if c.SAML.IdPSLOURL != "https://idp.example/slo" || c.SAML.IdPSLOBinding != saml.HTTPRedirectBinding {
		t.Fatalf("IdP SLO = %q %q", c.SAML.IdPSLOURL, c.SAML.IdPSLOBinding)
	}
	md, err := env.sso.SAMLMetadata(c)
	if err != nil || !bytes.Contains(md, []byte(env.sso.SAMLSLOURL(c.ID))) || !bytes.Contains(md, []byte(`use="signing"`)) ||
		!bytes.Contains(md, []byte("xmlenc11#aes256-gcm")) {
		t.Fatalf("SP metadata lacks SLO, signing key or encryption methods:\n%s", md)
	}

	t.Run("SP-initiated with signed LogoutRequest and LogoutResponse", func(t *testing.T) {
		login := samlLogin(t, env, idp, c, "alice@example.com")
		link, err := env.store.GetSSOSession(ctx, login.Session.Session.ID)
		if err != nil || link.Subject != "alice@example.com" || link.SessionIndex != "idx" || link.NameIDFormat != string(saml.EmailAddressNameIDFormat) {
			t.Fatalf("SSO session link = %+v, %v", link, err)
		}
		other := samlLogin(t, env, idp, c, "alice@example.com") // a second browser of alice
		p, err := principalFor(env, login.Session.Token)
		if err != nil {
			t.Fatal(err)
		}
		info, err := env.sso.SessionInfo(ctx, p)
		if err != nil || !info.SSO || !info.IdPLogout || info.Protocol != sso.ProtocolSAML {
			t.Fatalf("session info = %+v, %v", info, err)
		}
		res, err := env.sso.Logout(ctx, p, "/nowhere", testMeta())
		if err != nil || !strings.HasPrefix(res.RedirectURL, "https://idp.example/slo?SAMLRequest=") || res.Revoked != 2 {
			t.Fatalf("logout = %+v, %v", res, err)
		}
		for _, tok := range []string{login.Session.Token, other.Session.Token} {
			if _, err := principalFor(env, tok); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("session after logout: %v", err)
			}
		}
		req, q := spMessage(t, c, res.RedirectURL, "SAMLRequest")
		if req.Tag != "LogoutRequest" || req.FindElement("./NameID").Text() != "alice@example.com" || req.FindElement("./SessionIndex").Text() != "idx" ||
			req.FindElement("./Issuer").Text() != env.sso.SAMLEntityID(c.ID) || req.SelectAttrValue("Destination", "") != "https://idp.example/slo" {
			t.Fatalf("LogoutRequest = %s", postB64(t, req))
		}
		reqID, relay := req.SelectAttrValue("ID", ""), q.Get("RelayState")

		response := func(inResponseTo, status string) *etree.Element {
			return (&saml.LogoutResponse{ID: "id-resp-" + inResponseTo, InResponseTo: inResponseTo, Version: "2.0", IssueInstant: time.Now().UTC(),
				Destination: env.sso.SAMLSLOURL(c.ID), Issuer: &saml.Issuer{Value: idpEntityID},
				Status: saml.Status{StatusCode: saml.StatusCode{Value: status}}}).Element()
		}
		// Wrong InResponseTo: the IdP logout is reported as partial (the openlog sessions are gone anyway).
		bad := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLResponse", response("id-other", saml.StatusSuccess), relay, sigRSASHA256)}, testMeta())
		if bad.Redirect != "/login?sso_logout=partial" {
			t.Fatalf("mismatched response: %+v", bad)
		}
		// The state was consumed by the failed attempt: start again for the successful response.
		p2, _ := principalFor(env, samlLogin(t, env, idp, c, "alice@example.com").Session.Token)
		res, _ = env.sso.Logout(ctx, p2, "/login", testMeta())
		req, q = spMessage(t, c, res.RedirectURL, "SAMLRequest")
		reqID, relay = req.SelectAttrValue("ID", ""), q.Get("RelayState")
		ok := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLResponse", response(reqID, saml.StatusSuccess), relay, sigRSASHA256)}, testMeta())
		if ok.Redirect != "/login?sso_logout=ok" {
			t.Fatalf("logout response: %+v", ok)
		}
		again := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLResponse", response(reqID, saml.StatusSuccess), relay, sigRSASHA256)}, testMeta())
		if again.Redirect != "/login?sso_logout=partial" {
			t.Fatalf("replayed logout response: %+v", again)
		}
	})

	newRequest := func(subject, index string, mutate func(r *saml.LogoutRequest)) *etree.Element {
		r := &saml.LogoutRequest{ID: fmt.Sprintf("id-lr-%d", time.Now().UnixNano()), Version: "2.0", IssueInstant: time.Now().UTC(),
			Destination: env.sso.SAMLSLOURL(c.ID), Issuer: &saml.Issuer{Value: idpEntityID},
			NameID: &saml.NameID{Format: string(saml.EmailAddressNameIDFormat), Value: subject}}
		if index != "" {
			r.SessionIndex = &saml.SessionIndex{Value: index}
		}
		if mutate != nil {
			mutate(r)
		}
		return r.Element()
	}

	t.Run("IdP-initiated LogoutRequest ends the matching sessions", func(t *testing.T) {
		alice := samlLogin(t, env, idp, c, "alice@example.com")
		bob := samlLogin(t, env, idp, c, "bob@example.com")
		bad := []struct {
			name string
			in   sso.SLOInput
			code string
		}{
			{"unsigned", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "", nil), "", "")}, "invalid_request"},
			{"SHA-1 signature", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "", nil), "", "http://www.w3.org/2000/09/xmldsig#rsa-sha1")}, "invalid_request"},
			{"unknown key", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, newTestIdP(t, idpEntityID).key, "SAMLRequest", newRequest("alice@example.com", "", nil), "", sigRSASHA256)}, "invalid_request"},
			{"wrong issuer", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "", func(r *saml.LogoutRequest) { r.Issuer.Value = "https://evil.example" }), "", sigRSASHA256)}, "invalid_request"},
			{"wrong destination", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "", func(r *saml.LogoutRequest) { r.Destination = "https://other.example/slo" }), "", sigRSASHA256)}, "invalid_request"},
			{"old", sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "", func(r *saml.LogoutRequest) { r.IssueInstant = time.Now().Add(-time.Hour) }), "", sigRSASHA256)}, "invalid_request"},
			{"POST without signature", sso.SLOInput{Method: http.MethodPost, Form: url.Values{"SAMLRequest": {postB64(t, newRequest("alice@example.com", "", nil))}}}, "invalid_request"},
		}
		for _, tc := range bad {
			if o := env.sso.SAMLSLO(ctx, c.ID, tc.in, testMeta()); o.Redirect != "/login?sso_error="+tc.code {
				t.Fatalf("%s: %+v", tc.name, o)
			}
			if _, err := principalFor(env, alice.Session.Token); err != nil {
				t.Fatalf("%s ended alice's session: %v", tc.name, err)
			}
		}
		// A tampered POST message: the signature no longer matches.
		signed := signEnveloped(t, idp, newRequest("bob@example.com", "", nil))
		signed.FindElement("./NameID").SetText("alice@example.com")
		if o := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodPost, Form: url.Values{"SAMLRequest": {postB64(t, signed)}}}, testMeta()); o.Redirect != "/login?sso_error=invalid_request" {
			t.Fatalf("tampered POST: %+v", o)
		}

		query := idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("alice@example.com", "idx", nil), "idp-relay", sigRSASHA256)
		o := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: query}, testMeta())
		if !o.External || !strings.HasPrefix(o.Redirect, "https://idp.example/slo?SAMLResponse=") {
			t.Fatalf("IdP-initiated logout: %+v", o)
		}
		resp, q := spMessage(t, c, o.Redirect, "SAMLResponse")
		if resp.Tag != "LogoutResponse" || !strings.HasPrefix(resp.SelectAttrValue("InResponseTo", ""), "id-lr-") || q.Get("RelayState") != "idp-relay" ||
			resp.FindElement("./Status/StatusCode").SelectAttrValue("Value", "") != saml.StatusSuccess {
			t.Fatalf("LogoutResponse = %s %v", postB64(t, resp), q)
		}
		if _, err := principalFor(env, alice.Session.Token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("alice after IdP logout: %v", err)
		}
		if _, err := principalFor(env, bob.Session.Token); err != nil {
			t.Fatalf("bob's session ended too: %v", err)
		}
		if o := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: query}, testMeta()); o.Redirect != "/login?sso_error=replay" {
			t.Fatalf("replayed LogoutRequest: %+v", o)
		}
		// A SessionIndex of another IdP session does not end bob's session.
		o = env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("bob@example.com", "other-index", nil), "", sigRSASHA256)}, testMeta())
		if !o.External {
			t.Fatalf("other session index: %+v", o)
		}
		if _, err := principalFor(env, bob.Session.Token); err != nil {
			t.Fatalf("bob's session ended by another session index: %v", err)
		}
		// HTTP-POST binding with an enveloped signature and an encrypted NameID.
		spCert := spCertificate(t, c)
		nameID := (&saml.NameID{Format: string(saml.EmailAddressNameIDFormat), Value: "bob@example.com"}).Element()
		plain := postBytes(t, nameID)
		enc := etree.NewElement("saml:EncryptedID")
		enc.CreateAttr("xmlns:saml", "urn:oasis:names:tc:SAML:2.0:assertion")
		enc.AddChild(encryptForSP(t, spCert, plain, "http://www.w3.org/2009/xmlenc11#aes256-gcm", 32))
		lr := newRequest("unused", "", nil)
		lr.RemoveChild(lr.FindElement("./NameID"))
		lr.AddChild(enc)
		o = env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodPost, Form: url.Values{"SAMLRequest": {postB64(t, signEnveloped(t, idp, lr))}}}, testMeta())
		if !o.External {
			t.Fatalf("POST LogoutRequest with EncryptedID: %+v", o)
		}
		if _, err := principalFor(env, bob.Session.Token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("bob after POST logout: %v", err)
		}
		var logouts int
		for _, e := range env.users.AuditEvents() {
			if e.Action == "sso.logout" && e.Details["via"] == "idp" {
				logouts++
			}
		}
		if logouts < 2 {
			t.Fatalf("sso.logout audit events = %d", logouts)
		}
	})

	t.Run("LogoutResponse through an HTTP-POST only IdP is an auto-submitting form", func(t *testing.T) {
		postMD := strings.Replace(idp.metadataXML(t), saml.HTTPRedirectBinding+`" Location="https://idp.example/slo`, saml.HTTPPostBinding+`" Location="https://idp.example/slo`, 1)
		c2, err := env.sso.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
			IdPMetadataXML: postMD, DefaultRole: auth.RoleMember}, auth.ClientMeta{})
		if err != nil || c2.SAML.IdPSLOBinding != saml.HTTPPostBinding {
			t.Fatalf("update: %+v %v", c2.SAML, err)
		}
		carol := samlLogin(t, env, idp, c2, "carol@example.com")
		o := env.sso.SAMLSLO(ctx, c.ID, sso.SLOInput{Method: http.MethodGet, RawQuery: idpRedirectQuery(t, idp.key, "SAMLRequest", newRequest("carol@example.com", "", nil), "rs", sigRSASHA256)}, testMeta())
		if o.HTML == nil || !bytes.Contains(o.HTML, []byte(`action="https://idp.example/slo"`)) || !bytes.Contains(o.HTML, []byte(`name="SAMLResponse"`)) ||
			!strings.Contains(o.CSP, "script-src 'sha256-") || !strings.Contains(o.CSP, "default-src 'none'") {
			t.Fatalf("POST response page: %+v %s", o, o.HTML)
		}
		if _, err := principalFor(env, carol.Session.Token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("carol after logout: %v", err)
		}
		// SP-initiated logout with the POST binding returns the form fields.
		dave := samlLogin(t, env, idp, c2, "dave@example.com")
		p, _ := principalFor(env, dave.Session.Token)
		res, err := env.sso.Logout(ctx, p, "", testMeta())
		if err != nil || res.PostURL != "https://idp.example/slo" || res.PostFields["SAMLRequest"] == "" || res.PostFields["RelayState"] == "" {
			t.Fatalf("POST logout = %+v, %v", res, err)
		}
		raw, _ := base64.StdEncoding.DecodeString(res.PostFields["SAMLRequest"])
		if !bytes.Contains(raw, []byte("SignatureValue")) {
			t.Fatalf("POST LogoutRequest is not signed: %s", raw)
		}
	})

	t.Run("password sessions sign out locally", func(t *testing.T) {
		r, err := env.auth.Login(ctx, "owner@example.com", "correct horse battery staple", auth.ClientMeta{})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := principalFor(env, r.Token)
		if info, err := env.sso.SessionInfo(ctx, p); err != nil || info.SSO {
			t.Fatalf("password session info = %+v %v", info, err)
		}
		res, err := env.sso.Logout(ctx, p, "", auth.ClientMeta{})
		if err != nil || res.RedirectURL != "" || res.PostURL != "" {
			t.Fatalf("local logout = %+v %v", res, err)
		}
		if _, err := principalFor(env, r.Token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("password session after logout: %v", err)
		}
	})
}

func postBytes(t *testing.T, el *etree.Element) []byte {
	t.Helper()
	b, _ := base64.StdEncoding.DecodeString(postB64(t, el))
	return b
}

func spCertificate(t *testing.T, c sso.Connection) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(c.SAML.SPCertificatePEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestOIDCLogout(t *testing.T) {
	idp := newFakeIdP(t)
	env := newTestEnv(t)
	c := env.oidcConnection(t, idp.srv.URL)
	env.verifyDomain(t, "example.com")
	ctx := context.Background()
	if c, err := env.sso.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Enabled: true,
		Issuer: idp.srv.URL, ClientID: "openlog", LogoutRedirectAllowlist: []string{"/goodbye"}}, auth.ClientMeta{}); err != nil || c.LogoutRedirectAllowlist[0] != "/goodbye" {
		t.Fatalf("update: %+v %v", c, err)
	}
	if _, err := env.sso.UpdateConnection(ctx, env.ownerPrincipal(), c.ID, sso.ConnectionInput{Protocol: sso.ProtocolOIDC, Issuer: idp.srv.URL,
		ClientID: "openlog", LogoutRedirectAllowlist: []string{"https://evil.example/"}}, auth.ClientMeta{}); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("absolute logout redirect accepted: %v", err)
	}

	out := runOIDCLogin(t, env, idp, "alice@example.com")
	if out.Session == nil {
		t.Fatalf("login: %+v", out)
	}
	link, err := env.store.GetSSOSession(ctx, out.Session.Session.ID)
	if err != nil || link.Subject != "user-1" || len(link.IDTokenEnc) == 0 || bytes.Contains(link.IDTokenEnc, []byte("eyJ")) {
		t.Fatalf("OIDC session link = %+v, %v", link, err)
	}
	p, err := principalFor(env, out.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := env.sso.SessionInfo(ctx, p); err != nil || !info.IdPLogout || info.Protocol != sso.ProtocolOIDC {
		t.Fatalf("session info = %+v %v", info, err)
	}
	res, err := env.sso.Logout(ctx, p, "/goodbye", auth.ClientMeta{})
	if err != nil || !strings.HasPrefix(res.RedirectURL, idp.srv.URL+"/logout?") {
		t.Fatalf("logout = %+v, %v", res, err)
	}
	u, _ := url.Parse(res.RedirectURL)
	q := u.Query()
	if strings.Count(q.Get("id_token_hint"), ".") != 2 || q.Get("client_id") != "openlog" || q.Get("state") == "" ||
		q.Get("post_logout_redirect_uri") != "https://openlog.example/api/v1/sso/oidc/logout/callback" {
		t.Fatalf("end session request = %v", q)
	}
	if _, err := principalFor(env, out.Session.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session after logout: %v", err)
	}
	if o := env.sso.OIDCLogoutCallback(ctx, q.Get("state")); o.Redirect != "/goodbye?sso_logout=ok" {
		t.Fatalf("callback = %+v", o)
	}
	if o := env.sso.OIDCLogoutCallback(ctx, q.Get("state")); o.Redirect != "/login" {
		t.Fatalf("reused state = %+v", o)
	}
	// A path outside the allowlist returns to /login.
	out = runOIDCLogin(t, env, idp, "alice@example.com")
	p, _ = principalFor(env, out.Session.Token)
	res, _ = env.sso.Logout(ctx, p, "//evil.example", auth.ClientMeta{})
	u, _ = url.Parse(res.RedirectURL)
	if o := env.sso.OIDCLogoutCallback(ctx, u.Query().Get("state")); o.Redirect != "/login?sso_logout=ok" {
		t.Fatalf("callback outside allowlist = %+v", o)
	}
}
