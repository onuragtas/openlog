package sso_test

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

const idpEntityID = "https://idp.example/metadata"

type testIdP struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	idp  *saml.IdentityProvider
}

func newTestIdP(t *testing.T, entityID string) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	mdURL, _ := url.Parse(entityID)
	ssoURL, _ := url.Parse("https://idp.example/sso")
	// crewjam's IdP signs with RSA-SHA1 by default, which openlog refuses; real IdPs default to SHA-256.
	return &testIdP{key: key, cert: cert, idp: &saml.IdentityProvider{Key: key, Certificate: cert, MetadataURL: *mdURL, SSOURL: *ssoURL,
		SignatureMethod: dsig.RSASHA256SignatureMethod}}
}

func (p *testIdP) metadataXML(t *testing.T) string {
	t.Helper()
	b, err := xml.Marshal(p.idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// response options
type respOpts struct {
	requestID string
	audience  string
	recipient string
	email     string
	groups    []string
	now       time.Time
	mutate    func(a *saml.Assertion)
}

// makeResponse returns a signed SAML Response XML document.
func (p *testIdP) makeResponse(t *testing.T, o respOpts) []byte {
	t.Helper()
	if o.now.IsZero() {
		o.now = time.Now()
	}
	req := &saml.IdpAuthnRequest{
		IDP: p.idp, HTTPRequest: httptest.NewRequest(http.MethodPost, "/sso", nil), Now: o.now,
		Request:                 saml.AuthnRequest{ID: o.requestID, IssueInstant: o.now},
		ACSEndpoint:             &saml.IndexedEndpoint{Binding: saml.HTTPPostBinding, Location: o.recipient},
		SPSSODescriptor:         &saml.SPSSODescriptor{},
		ServiceProviderMetadata: &saml.EntityDescriptor{EntityID: o.audience},
	}
	session := &saml.Session{ID: "s1", NameID: o.email, NameIDFormat: string(saml.EmailAddressNameIDFormat), UserEmail: o.email,
		CreateTime: o.now, ExpireTime: o.now.Add(time.Hour), Index: "idx"}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, session); err != nil {
		t.Fatal(err)
	}
	req.Assertion.IssueInstant = o.now
	if len(o.groups) > 0 {
		vals := []saml.AttributeValue{}
		for _, g := range o.groups {
			vals = append(vals, saml.AttributeValue{Type: "xs:string", Value: g})
		}
		req.Assertion.AttributeStatements[0].Attributes = append(req.Assertion.AttributeStatements[0].Attributes,
			saml.Attribute{Name: "groups", NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic", Values: vals})
	}
	if o.mutate != nil {
		o.mutate(req.Assertion)
	}
	if err := req.MakeAssertionEl(); err != nil {
		t.Fatal(err)
	}
	if err := req.MakeResponse(); err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.ResponseEl)
	b, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *testEnv) samlConnection(t *testing.T, metadata string, allowIdPInitiated bool, relay []string) sso.Connection {
	t.Helper()
	c, err := e.sso.SaveConnection(context.Background(), e.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataXML: metadata, AllowIdPInitiated: allowIdPInitiated, RelayStateAllowlist: relay, DefaultRole: auth.RoleMember}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var idRe = regexp.MustCompile(`ID="([^"]+)"`)

// authnRequestID decodes the HTTP-Redirect binding AuthnRequest of a sign-in URL.
func authnRequestID(t *testing.T, redirect string) (string, string) {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	xmlReq, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	m := idRe.FindSubmatch(xmlReq)
	if m == nil {
		t.Fatalf("no ID in %s", xmlReq)
	}
	return string(m[1]), u.Query().Get("RelayState")
}

func TestSAMLAssertionValidation(t *testing.T) {
	env := newTestEnv(t)
	idp := newTestIdP(t, idpEntityID)
	c := env.samlConnection(t, idp.metadataXML(t), false, nil)
	entity, acs := env.sso.SAMLEntityID(c.ID), env.sso.SAMLACSURL(c.ID)
	good := respOpts{requestID: "id-req-1", audience: entity, recipient: acs, email: "Alice@Example.com", groups: []string{"admins"}}

	id, assertionID, err := env.sso.SAMLAssertionForTest(c, idp.makeResponse(t, good), []string{"id-req-1"})
	if err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	if id.Email != "alice@example.com" || !id.GroupsPresent || len(id.Groups) != 1 || id.Groups[0] != "admins" || assertionID == "" {
		t.Fatalf("identity = %+v", id)
	}

	evil := newTestIdP(t, idpEntityID) // same entity id, different key
	sha1IdP := newTestIdP(t, idpEntityID)
	sha1IdP.key, sha1IdP.cert = idp.key, idp.cert
	sha1IdP.idp.Key, sha1IdP.idp.Certificate = idp.key, idp.cert
	sha1IdP.idp.SignatureMethod = dsig.RSASHA1SignatureMethod

	with := func(f func(o *respOpts)) respOpts {
		o := good
		f(&o)
		return o
	}
	cases := []struct {
		name     string
		doc      []byte
		possible []string
	}{
		{"expired", idp.makeResponse(t, with(func(o *respOpts) { o.now = time.Now().Add(-time.Hour) })), []string{"id-req-1"}},
		{"not yet valid", idp.makeResponse(t, with(func(o *respOpts) { o.now = time.Now().Add(time.Hour) })), []string{"id-req-1"}},
		{"wrong audience", idp.makeResponse(t, with(func(o *respOpts) { o.audience = "https://other-sp.example" })), []string{"id-req-1"}},
		{"no audience restriction", idp.makeResponse(t, with(func(o *respOpts) {
			o.mutate = func(a *saml.Assertion) { a.Conditions.AudienceRestrictions = nil }
		})), []string{"id-req-1"}},
		{"wrong recipient", idp.makeResponse(t, with(func(o *respOpts) { o.recipient = "https://evil.example/acs" })), []string{"id-req-1"}},
		{"no bearer confirmation", idp.makeResponse(t, with(func(o *respOpts) {
			o.mutate = func(a *saml.Assertion) { a.Subject.SubjectConfirmations = nil }
		})), []string{"id-req-1"}},
		{"InResponseTo mismatch", idp.makeResponse(t, good), []string{"id-req-2"}},
		{"unsolicited response with InResponseTo", idp.makeResponse(t, good), nil},
		{"signed by an unknown key", evil.makeResponse(t, good), []string{"id-req-1"}},
		{"SHA-1 signature", sha1IdP.makeResponse(t, good), []string{"id-req-1"}},
		{"tampered attribute", bytes.Replace(idp.makeResponse(t, good), []byte("Alice@Example.com"), []byte("mallory@example.com"), -1), []string{"id-req-1"}},
		{"wrapped: forged assertion before the signed one", wrapAssertion(t, idp.makeResponse(t, good), "before"), []string{"id-req-1"}},
		{"wrapped: signed assertion nested in the forged one", wrapAssertion(t, idp.makeResponse(t, good), "nested"), []string{"id-req-1"}},
		{"wrapped: forged assertion carrying the original signature", wrapAssertion(t, idp.makeResponse(t, good), "replace"), []string{"id-req-1"}},
		{"DTD", append([]byte(`<?xml version="1.0"?><!DOCTYPE r [<!ENTITY x "y">]>`), idp.makeResponse(t, good)...), []string{"id-req-1"}},
		{"not a response", []byte(`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"/>`), []string{"id-req-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if id, _, err := env.sso.SAMLAssertionForTest(c, tc.doc, tc.possible); err == nil {
				t.Fatalf("accepted: %+v", id)
			}
		})
	}
}

// wrapAssertion builds XML signature wrapping variants of a signed response: a forged copy of the assertion (other
// ID, attacker's e-mail) is placed before the signed original, around it, or instead of it with its signature.
func wrapAssertion(t *testing.T, doc []byte, mode string) []byte {
	t.Helper()
	d := etree.NewDocument()
	if err := d.ReadFromBytes(doc); err != nil {
		t.Fatal(err)
	}
	root := d.Root()
	var orig *etree.Element
	for _, ch := range root.ChildElements() {
		if ch.Tag == "Assertion" {
			orig = ch
		}
	}
	if orig == nil {
		t.Fatal("no assertion")
	}
	forged := orig.Copy()
	forged.CreateAttr("ID", "id-forged")
	for _, el := range forged.FindElements(".//AttributeValue") {
		el.SetText("mallory@example.com")
	}
	if nameID := forged.FindElement(".//NameID"); nameID != nil {
		nameID.SetText("mallory@example.com")
	}
	switch mode {
	case "before":
		if sig := forged.FindElement("./Signature"); sig != nil {
			forged.RemoveChild(sig)
		}
		root.InsertChildAt(orig.Index(), forged)
	case "nested":
		if sig := forged.FindElement("./Signature"); sig != nil {
			forged.RemoveChild(sig)
		}
		root.RemoveChild(orig)
		forged.FindElement("./Subject").AddChild(orig)
		root.AddChild(forged)
	case "replace":
		root.RemoveChild(orig)
		root.AddChild(forged) // keeps the copied Signature, which references the original ID
	}
	b, err := d.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSAMLLoginFlow(t *testing.T) {
	env := newTestEnv(t)
	env.verifyDomain(t, "example.com")
	idp := newTestIdP(t, idpEntityID)
	c := env.samlConnection(t, idp.metadataXML(t), true, []string{"/dashboards"})
	ctx := context.Background()
	entity, acs := env.sso.SAMLEntityID(c.ID), env.sso.SAMLACSURL(c.ID)
	if _, err := env.sso.ReplaceRoleMappings(ctx, env.ownerPrincipal(), "", []sso.RoleMapping{{Group: "openlog-admins", Role: auth.RoleAdmin}}, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}

	md, err := env.sso.SAMLMetadata(c)
	if err != nil || !bytes.Contains(md, []byte(acs)) || !bytes.Contains(md, []byte(`entityID="`+entity+`"`)) {
		t.Fatalf("metadata: %v\n%s", err, md)
	}

	// SP-initiated: start → IdP → ACS → complete (with the binding cookie).
	st, err := env.sso.StartLogin(ctx, "alice@example.com", "/hosts", auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.URL, "https://idp.example/sso?") {
		t.Fatalf("redirect = %s", st.URL)
	}
	reqID, relay := authnRequestID(t, st.URL)
	if relay != st.State {
		t.Fatalf("RelayState = %q", relay)
	}
	resp := idp.makeResponse(t, respOpts{requestID: reqID, audience: entity, recipient: acs, email: "alice@example.com", groups: []string{"openlog-admins"}})
	b64 := base64.StdEncoding.EncodeToString(resp)
	out := env.sso.SAMLACS(ctx, c.ID, b64, relay, auth.ClientMeta{})
	if out.Session != nil || !strings.HasPrefix(out.Redirect, "/api/v1/sso/saml/complete?state=") {
		t.Fatalf("ACS outcome = %+v", out)
	}
	stateParam, _ := url.ParseQuery(strings.SplitN(out.Redirect, "?", 2)[1])
	// Without the browser binding cookie the sign-in does not complete (login CSRF with a posted response).
	if o := env.sso.SAMLComplete(ctx, stateParam.Get("state"), func(string) string { return "" }, auth.ClientMeta{}); o.Session != nil {
		t.Fatalf("completed without binding: %+v", o)
	}

	// A second, clean sign-in completes.
	st, _ = env.sso.StartLogin(ctx, "alice@example.com", "/hosts", auth.ClientMeta{})
	reqID, relay = authnRequestID(t, st.URL)
	resp = idp.makeResponse(t, respOpts{requestID: reqID, audience: entity, recipient: acs, email: "alice@example.com", groups: []string{"openlog-admins"}})
	out = env.sso.SAMLACS(ctx, c.ID, base64.StdEncoding.EncodeToString(resp), relay, auth.ClientMeta{})
	stateParam, _ = url.ParseQuery(strings.SplitN(out.Redirect, "?", 2)[1])
	cookie := env.sso.BindingCookie(st)
	done := env.sso.SAMLComplete(ctx, stateParam.Get("state"), func(n string) string {
		if n == cookie.Name {
			return cookie.Value
		}
		return ""
	}, auth.ClientMeta{})
	if done.Session == nil || done.Redirect != "/hosts" || done.Session.Session.AuthMethod != auth.MethodSAML {
		t.Fatalf("complete = %+v", done)
	}
	alice, _ := env.users.GetUserByEmail(ctx, "alice@example.com")
	if m, err := env.users.GetMembership(ctx, env.org.ID, alice.ID); err != nil || m.Role != auth.RoleAdmin {
		t.Fatalf("membership = %+v, %v", m, err)
	}

	// The same response posted again is refused (state consumed; assertion id in the replay cache).
	if o := env.sso.SAMLACS(ctx, c.ID, base64.StdEncoding.EncodeToString(resp), relay, auth.ClientMeta{}); o.Session != nil || !strings.HasPrefix(o.Redirect, "/login?sso_error=") {
		t.Fatalf("reposted: %+v", o)
	}

	// IdP-initiated (allowed on this connection): RelayState must be in the allowlist; replays are refused.
	unsolicited := idp.makeResponse(t, respOpts{audience: entity, recipient: acs, email: "alice@example.com"})
	enc := base64.StdEncoding.EncodeToString(unsolicited)
	if o := env.sso.SAMLACS(ctx, c.ID, enc, "https://evil.example/", auth.ClientMeta{}); o.Redirect != "/login?sso_error=invalid_request" {
		t.Fatalf("relay state not in allowlist: %+v", o)
	}
	unsolicited = idp.makeResponse(t, respOpts{audience: entity, recipient: acs, email: "alice@example.com"})
	enc = base64.StdEncoding.EncodeToString(unsolicited)
	o := env.sso.SAMLACS(ctx, c.ID, enc, "/dashboards", auth.ClientMeta{})
	if !strings.HasPrefix(o.Redirect, "/api/v1/sso/saml/complete?state=") {
		t.Fatalf("idp-initiated ACS: %+v", o)
	}
	q, _ := url.ParseQuery(strings.SplitN(o.Redirect, "?", 2)[1])
	if d := env.sso.SAMLComplete(ctx, q.Get("state"), func(string) string { return "" }, auth.ClientMeta{}); d.Session == nil || d.Redirect != "/dashboards" {
		t.Fatalf("idp-initiated complete: %+v", d)
	}
	if o := env.sso.SAMLACS(ctx, c.ID, enc, "/dashboards", auth.ClientMeta{}); o.Redirect != "/login?sso_error=replay" {
		t.Fatalf("replay: %+v", o)
	}
}
