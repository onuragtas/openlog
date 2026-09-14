package sso_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/russellhaering/goxmldsig/etreeutils"

	"github.com/onuragtas/openlog/internal/sso"
)

const soapNS = "http://schemas.xmlsoap.org/soap/envelope/"

// soapMessage wraps elements into a SOAP 1.1 envelope (ns: envelope namespace, header: add a mustUnderstand header).
func soapMessage(t *testing.T, ns string, header bool, content ...*etree.Element) []byte {
	t.Helper()
	env := etree.NewElement("soapenv:Envelope")
	env.CreateAttr("xmlns:soapenv", ns)
	if header {
		h := env.CreateElement("soapenv:Header").CreateElement("wsse:Security")
		h.CreateAttr("xmlns:wsse", "urn:example:security")
		h.CreateAttr("soapenv:mustUnderstand", "1")
	}
	body := env.CreateElement("soapenv:Body")
	for _, c := range content {
		body.AddChild(c)
	}
	doc := etree.NewDocument()
	doc.SetRoot(env)
	b, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// signExclusive signs el like most IdPs do for SOAP messages (exclusive canonicalization).
func signExclusive(t *testing.T, idp *testIdP, el *etree.Element) *etree.Element {
	t.Helper()
	ks := dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{idp.cert.Raw}, PrivateKey: idp.key})
	sc := dsig.NewDefaultSigningContext(ks)
	sc.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if err := sc.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	signed, err := sc.SignEnveloped(el)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSAMLSOAPLogout(t *testing.T) {
	env := newTestEnv(t)
	env.verifyDomain(t, "example.com")
	idp := sloIdP(t)
	c := env.samlConnection(t, idp.metadataXML(t), false, nil)
	ctx := context.Background()
	soapURL := env.sso.SAMLSOAPLogoutURL(c.ID)
	if md, err := env.sso.SAMLMetadata(c); err != nil || !bytes.Contains(md, []byte(`Binding="`+saml.SOAPBinding+`" Location="`+soapURL+`"`)) {
		t.Fatalf("SP metadata lacks the SOAP single logout service: %s %v", md, err)
	}
	request := func(subject, index string, mutate func(r *saml.LogoutRequest)) *etree.Element {
		r := &saml.LogoutRequest{ID: fmt.Sprintf("id-soap-%d", time.Now().UnixNano()), Version: "2.0", IssueInstant: time.Now().UTC(),
			Destination: soapURL, Issuer: &saml.Issuer{Value: idpEntityID},
			NameID: &saml.NameID{Format: string(saml.EmailAddressNameIDFormat), Value: subject}}
		if index != "" {
			r.SessionIndex = &saml.SessionIndex{Value: index}
		}
		if mutate != nil {
			mutate(r)
		}
		return r.Element()
	}
	call := func(body []byte) sso.SOAPOutcome { return env.sso.SAMLSOAPLogout(ctx, c.ID, body, testMeta()) }
	fault := func(o sso.SOAPOutcome) bool {
		return o.Status == http.StatusInternalServerError && bytes.Contains(o.Body, []byte("Fault>")) && !bytes.Contains(o.Body, []byte("LogoutResponse"))
	}
	alice := samlLogin(t, env, idp, c, "alice@example.com")
	bob := samlLogin(t, env, idp, c, "bob@example.com")

	tampered := signEnveloped(t, idp, request("bob@example.com", "", nil))
	tampered.FindElement("./NameID").SetText("alice@example.com")
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"unsigned", soapMessage(t, soapNS, false, request("alice@example.com", "", nil))},
		{"other destination", soapMessage(t, soapNS, false, signEnveloped(t, idp, request("alice@example.com", "", func(r *saml.LogoutRequest) { r.Destination = "https://other.example/soap" })))},
		{"other issuer", soapMessage(t, soapNS, false, signEnveloped(t, idp, request("alice@example.com", "", func(r *saml.LogoutRequest) { r.Issuer.Value = "https://evil.example" })))},
		{"old", soapMessage(t, soapNS, false, signEnveloped(t, idp, request("alice@example.com", "", func(r *saml.LogoutRequest) { r.IssueInstant = time.Now().Add(-time.Hour) })))},
		{"unknown key", soapMessage(t, soapNS, false, signEnveloped(t, newTestIdP(t, idpEntityID), request("alice@example.com", "", nil)))},
		{"tampered", soapMessage(t, soapNS, false, tampered)},
		{"two requests", soapMessage(t, soapNS, false, signEnveloped(t, idp, request("alice@example.com", "", nil)), signEnveloped(t, idp, request("bob@example.com", "", nil)))},
		{"no envelope", postBytes(t, signEnveloped(t, idp, request("alice@example.com", "", nil)))},
		{"SOAP 1.2", soapMessage(t, "http://www.w3.org/2003/05/soap-envelope", false, signEnveloped(t, idp, request("alice@example.com", "", nil)))},
		{"mustUnderstand header", soapMessage(t, soapNS, true, signEnveloped(t, idp, request("alice@example.com", "", nil)))},
		{"DTD", append([]byte(`<!DOCTYPE x [<!ENTITY a "b">]>`), soapMessage(t, soapNS, false, signEnveloped(t, idp, request("alice@example.com", "", nil)))...)},
		{"empty", nil},
	} {
		if o := call(tc.body); !fault(o) {
			t.Fatalf("%s: %d %s", tc.name, o.Status, o.Body)
		}
		expectAlive(t, env, tc.name, alice, true)
	}

	// Valid, exclusively canonicalized, with the SAML namespaces declared on the envelope instead of the request.
	lr := signExclusive(t, idp, request("alice@example.com", "idx", nil))
	reqID := lr.SelectAttrValue("ID", "")
	d := etree.NewDocument()
	if err := d.ReadFromBytes(soapMessage(t, soapNS, false, lr)); err != nil {
		t.Fatal(err)
	}
	envEl := d.Root()
	inner := envEl.FindElement("./Body/LogoutRequest")
	for _, a := range slices.Clone(inner.Attr) {
		if a.Space == "xmlns" {
			inner.RemoveAttr("xmlns:" + a.Key)
			envEl.CreateAttr("xmlns:"+a.Key, a.Value)
		}
	}
	body, _ := d.WriteToBytes()
	o := call(body)
	if o.Status != http.StatusOK {
		t.Fatalf("valid SOAP LogoutRequest: %d %s", o.Status, o.Body)
	}
	rd := etree.NewDocument()
	if err := rd.ReadFromBytes(o.Body); err != nil {
		t.Fatal(err)
	}
	resp := rd.FindElement("//LogoutResponse")
	if resp == nil || rd.Root().Tag != "Envelope" || resp.SelectAttrValue("InResponseTo", "") != reqID || resp.SelectAttrValue("Destination", "") != "" ||
		resp.FindElement("./Status/StatusCode").SelectAttrValue("Value", "") != saml.StatusSuccess || resp.FindElement("./Issuer").Text() != env.sso.SAMLEntityID(c.ID) {
		t.Fatalf("SOAP LogoutResponse: %s", o.Body)
	}
	nsctx, _ := etreeutils.NSBuildParentContext(resp)
	detached, _ := etreeutils.NSDetatch(nsctx, resp)
	store := dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{spCertificate(t, c)}}
	vc := dsig.NewDefaultValidationContext(&store)
	vc.IdAttribute = "ID"
	if _, err := vc.Validate(detached); err != nil {
		t.Fatalf("LogoutResponse signature: %v", err)
	}
	expectAlive(t, env, "alice after SOAP logout", alice, false)
	expectAlive(t, env, "bob after alice's SOAP logout", bob, true)
	if o := call(body); !fault(o) {
		t.Fatalf("replayed SOAP LogoutRequest: %d %s", o.Status, o.Body)
	}

	// Without Destination, or addressed to the front-channel SLO URL, with inclusive canonicalization.
	if o := call(soapMessage(t, soapNS, false, signEnveloped(t, idp, request("bob@example.com", "", func(r *saml.LogoutRequest) { r.Destination = "" })))); o.Status != http.StatusOK {
		t.Fatalf("without Destination: %d %s", o.Status, o.Body)
	}
	expectAlive(t, env, "bob after SOAP logout", bob, false)
	carol := samlLogin(t, env, idp, c, "carol@example.com")
	if o := call(soapMessage(t, soapNS, false, signEnveloped(t, idp, request("carol@example.com", "", func(r *saml.LogoutRequest) { r.Destination = env.sso.SAMLSLOURL(c.ID) })))); o.Status != http.StatusOK {
		t.Fatalf("SLO URL as Destination: %d %s", o.Status, o.Body)
	}
	expectAlive(t, env, "carol after SOAP logout", carol, false)
	if n := auditCount(env, "sso.logout", "idp_soap"); n != 3 {
		t.Fatalf("sso.logout via idp_soap = %d", n)
	}
	if n := auditCount(env, "sso.logout_failed", "idp_soap"); n == 0 {
		t.Fatal("refused SOAP requests not audited")
	}
	if o := env.sso.SAMLSOAPLogout(ctx, "00000000-0000-0000-0000-000000000000", body, testMeta()); !fault(o) {
		t.Fatalf("unknown connection: %d %s", o.Status, o.Body)
	}
}
