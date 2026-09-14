package sso_test

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

// signMetadataXML signs IdP metadata with an enveloped signature on the root (ID md-1) by key/cert.
func signMetadataXML(t *testing.T, doc string, key *rsa.PrivateKey, cert *x509.Certificate) string {
	t.Helper()
	d := etree.NewDocument()
	if err := d.ReadFromString(doc); err != nil {
		t.Fatal(err)
	}
	root := d.Root()
	root.RemoveAttr("ID")
	root.CreateAttr("ID", "md-1")
	ks := dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key})
	sc := dsig.NewDefaultSigningContext(ks)
	if err := sc.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	signed, err := sc.SignEnveloped(root)
	if err != nil {
		t.Fatal(err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed)
	s, err := out.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pemOf(c *x509.Certificate) *string {
	s := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	return &s
}

// metadataServer serves a replaceable metadata document.
type metadataServer struct {
	mu  sync.Mutex
	doc string
	srv *httptest.Server
}

func newMetadataServer(t *testing.T, doc string) *metadataServer {
	m := &metadataServer{doc: doc}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, _ = w.Write([]byte(m.doc))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *metadataServer) set(doc string) {
	m.mu.Lock()
	m.doc = doc
	m.mu.Unlock()
}

func (m *metadataServer) url() string { return m.srv.URL + "/metadata" }

func TestMetadataTrustOnSave(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	idp := newTestIdP(t, idpEntityID)
	fed := newTestIdP(t, "https://federation.example") // the metadata signing key
	unsigned := idp.metadataXML(t)
	signed := signMetadataXML(t, unsigned, fed.key, fed.cert)
	md := newMetadataServer(t, unsigned)
	save := func(in sso.ConnectionInput) (sso.Connection, error) {
		in.Protocol, in.Enabled, in.IdPMetadataURL, in.DefaultRole = sso.ProtocolSAML, true, md.url(), auth.RoleMember
		return env.sso.SaveConnection(ctx, env.ownerPrincipal(), in, auth.ClientMeta{})
	}
	invalid := func(name string, err error, want string) {
		t.Helper()
		if !errors.Is(err, auth.ErrInvalidArgument) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v (want %q)", name, err, want)
		}
	}

	_, err := save(sso.ConnectionInput{})
	invalid("unsigned without a decision", err, "not signed")
	c, err := save(sso.ConnectionInput{AllowUnsignedMetadata: true})
	if err != nil || !c.SAML.AllowUnsignedMetadata || len(c.SAML.MetadataSigningCerts) != 0 {
		t.Fatalf("unsigned allowed: %+v %v", c.SAML, err)
	}

	// Signed metadata: its signer is pinned when no certificate is entered, and kept on later saves.
	md.set(signed)
	for i := 0; i < 2; i++ {
		c, err = save(sso.ConnectionInput{})
		if fps := sso.CertificateFingerprints(c.SAML.MetadataSigningCerts); err != nil || len(fps) != 1 || fps[0] != sso.CertificateFingerprint(fed.cert) {
			t.Fatalf("save %d of signed metadata: pinned %v, %v", i, fps, err)
		}
	}
	_, err = save(sso.ConnectionInput{MetadataSigningCertificatePEM: pemOf(newTestIdP(t, "https://other.example").cert)})
	invalid("signed by another certificate", err, "does not verify")
	_, err = save(sso.ConnectionInput{MetadataSigningCertificatePEM: pemOf(fed.cert)})
	if err != nil {
		t.Fatalf("pinned certificate: %v", err)
	}
	bad := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	_, err = save(sso.ConnectionInput{MetadataSigningCertificatePEM: &bad})
	invalid("invalid PEM", err, "metadata_signing_certificate_pem")

	md.set(unsigned)
	_, err = save(sso.ConnectionInput{MetadataSigningCertificatePEM: pemOf(fed.cert), AllowUnsignedMetadata: true})
	invalid("unsigned with a pinned certificate", err, "not signed")
	md.set(strings.Replace(signed, "https://idp.example/sso", "https://evil.example/sso", 1))
	_, err = save(sso.ConnectionInput{})
	invalid("tampered", err, "signature")
}

// A SAML connection saved before metadata trust (no pinned certificate, no "allow unsigned" decision) can still be
// enabled/disabled without a trust decision, keeping its stored metadata; changing the URL needs a decision.
func TestLegacyMetadataConnectionSave(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	idp := newTestIdP(t, idpEntityID)
	unsigned := idp.metadataXML(t)
	md := newMetadataServer(t, unsigned)
	c, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataURL: md.url(), DefaultRole: auth.RoleMember, AllowUnsignedMetadata: true}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a connection from before D-098.
	stored, err := env.store.GetConnection(ctx, env.org.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.SAML.AllowUnsignedMetadata = false
	if err := env.store.UpdateConnection(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	// The IdP now serves different metadata: a toggle must not fetch it.
	md.set(strings.Replace(unsigned, "https://idp.example/sso", "https://changed.example/sso", 1))

	toggled, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: false,
		IdPMetadataURL: md.url(), DefaultRole: auth.RoleMember}, auth.ClientMeta{})
	if err != nil {
		t.Fatalf("disabling a legacy connection: %v", err)
	}
	if toggled.Enabled || toggled.SAML.IdPMetadataXML != stored.SAML.IdPMetadataXML || toggled.SAML.AllowUnsignedMetadata ||
		len(toggled.SAML.MetadataSigningCerts) != 0 {
		t.Fatalf("legacy toggle changed metadata or trust: enabled=%v allowUnsigned=%v pins=%d sameXML=%v", toggled.Enabled,
			toggled.SAML.AllowUnsignedMetadata, len(toggled.SAML.MetadataSigningCerts), toggled.SAML.IdPMetadataXML == stored.SAML.IdPMetadataXML)
	}

	other := newMetadataServer(t, unsigned)
	_, err = env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataURL: other.url(), DefaultRole: auth.RoleMember}, auth.ClientMeta{})
	if !errors.Is(err, auth.ErrInvalidArgument) || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("changed metadata URL without a trust decision: %v", err)
	}
	decided, err := env.sso.SaveConnection(ctx, env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true,
		IdPMetadataURL: md.url(), DefaultRole: auth.RoleMember, AllowUnsignedMetadata: true}, auth.ClientMeta{})
	if err != nil || !decided.SAML.AllowUnsignedMetadata || !strings.Contains(decided.SAML.IdPMetadataXML, "https://changed.example/sso") {
		t.Fatalf("explicit decision refetches: %v", err)
	}
}

func TestRefreshSignedMetadata(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	owner := env.ownerPrincipal()
	idp := newTestIdP(t, idpEntityID)
	fed := newTestIdP(t, "https://federation.example")
	md := newMetadataServer(t, signMetadataXML(t, idp.metadataXML(t), fed.key, fed.cert))
	c, err := env.sso.SaveConnection(ctx, owner, sso.ConnectionInput{Protocol: sso.ProtocolSAML, Enabled: true, IdPMetadataURL: md.url(),
		DefaultRole: auth.RoleMember, MetadataSigningCertificatePEM: pemOf(fed.cert)}, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	before, version := c.SAML.IdPCertificates[0], c.ConfigVersion
	refresh := func() sso.Connection {
		t.Helper()
		out, err := env.sso.RefreshNow(ctx, owner, c.ID, auth.ClientMeta{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// IdP certificate rollover inside metadata signed by the pinned certificate applies automatically.
	rolled := newTestIdP(t, idpEntityID)
	md.set(signMetadataXML(t, rolled.metadataXML(t), fed.key, fed.cert))
	c = refresh()
	if !*c.Refresh.OK || c.SAML.IdPCertificates[0] == before || c.SAML.PendingMetadata != nil || c.ConfigVersion != version {
		t.Fatalf("signed rollover: certs %v refresh %+v pending %+v", c.SAML.IdPCertificates, c.Refresh, c.SAML.PendingMetadata)
	}
	current := c.SAML.IdPCertificates[0]
	refused := func(name, want string) {
		t.Helper()
		c = refresh()
		if *c.Refresh.OK || !strings.Contains(c.Refresh.Error, want) || c.SAML.PendingMetadata != nil || c.SAML.IdPCertificates[0] != current {
			t.Fatalf("%s: refresh %+v pending %+v certs %v", name, c.Refresh, c.SAML.PendingMetadata, c.SAML.IdPCertificates)
		}
	}

	// Tampered after signing.
	md.set(strings.Replace(signMetadataXML(t, newTestIdP(t, idpEntityID).metadataXML(t), fed.key, fed.cert), "https://idp.example/sso", "https://evil.example/sso", 1))
	refused("tampered", "signature is not valid")

	// Signature wrapping: an attacker's root (same ID) with the copied signature, the signed original embedded.
	good := etree.NewDocument()
	if err := good.ReadFromString(signMetadataXML(t, rolled.metadataXML(t), fed.key, fed.cert)); err != nil {
		t.Fatal(err)
	}
	wrapped := etree.NewDocument()
	if err := wrapped.ReadFromString(newTestIdP(t, idpEntityID).metadataXML(t)); err != nil {
		t.Fatal(err)
	}
	root := wrapped.Root()
	root.RemoveAttr("ID")
	root.CreateAttr("ID", "md-1")
	root.CreateElement("Extensions").AddChild(good.Root().Copy())
	root.AddChild(good.Root().FindElement("./Signature").Copy())
	doc, _ := wrapped.WriteToString()
	md.set(doc)
	refused("wrapped", "signature is not valid")

	// Unsigned metadata while a certificate is pinned.
	md.set(newTestIdP(t, idpEntityID).metadataXML(t))
	refused("unsigned", "not signed")

	// A new metadata signing certificate waits for an administrator.
	fed2 := newTestIdP(t, "https://federation2.example")
	rolled2 := newTestIdP(t, idpEntityID)
	md.set(signMetadataXML(t, rolled2.metadataXML(t), fed2.key, fed2.cert))
	c = refresh()
	p := c.SAML.PendingMetadata
	if *c.Refresh.OK || !strings.Contains(c.Refresh.Error, "not pinned") || p == nil || p.Reason != sso.PendingMetadataSignerChanged ||
		p.SignerCertificate != sso.CertificateFingerprint(fed2.cert) || c.SAML.IdPCertificates[0] != current {
		t.Fatalf("signer change: refresh %+v pending %+v", c.Refresh, p)
	}
	// Four failed refreshes in a row by now: the health is an error naming the reason.
	if h := env.sso.ConnectionHealth(c); h.Status != "error" || !strings.Contains(h.Message, "not pinned") {
		t.Fatalf("health with a pending change = %+v", h)
	}
	resp := rolled2.makeResponse(t, respOpts{requestID: "r2", audience: env.sso.SAMLEntityID(c.ID), recipient: env.sso.SAMLACSURL(c.ID), email: "a@example.com"})
	if _, _, err := env.sso.SAMLAssertionForTest(c, resp, []string{"r2"}); err == nil {
		t.Fatal("assertion of the unconfirmed IdP key accepted")
	}
	if _, err := env.sso.AcceptMetadata(ctx, owner, c.ID, strings.Repeat("0", 64), auth.ClientMeta{}); !errors.Is(err, auth.ErrFailedPrecondition) {
		t.Fatalf("accept with a wrong digest: %v", err)
	}
	if c, err = env.sso.AcceptMetadata(ctx, owner, c.ID, p.Digest, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if fps := sso.CertificateFingerprints(c.SAML.MetadataSigningCerts); len(fps) != 1 || fps[0] != sso.CertificateFingerprint(fed2.cert) ||
		c.SAML.PendingMetadata != nil || !*c.Refresh.OK || c.SAML.IdPCertificates[0] == current || c.ConfigVersion != version {
		t.Fatalf("after confirmation: pinned %v %+v refresh %+v", fps, c.SAML, c.Refresh)
	}
	if _, _, err := env.sso.SAMLAssertionForTest(c, resp, []string{"r2"}); err != nil {
		t.Fatalf("assertion after confirmation: %v", err)
	}
	// The next refresh verifies with the newly pinned certificate.
	if c = refresh(); !*c.Refresh.OK {
		t.Fatalf("refresh after confirmation: %+v", c.Refresh)
	}
	// Metadata signed by the old certificate is a new pending change now.
	md.set(signMetadataXML(t, rolled2.metadataXML(t), fed.key, fed.cert))
	if c = refresh(); c.SAML.PendingMetadata == nil || c.SAML.PendingMetadata.SignerCertificate != sso.CertificateFingerprint(fed.cert) {
		t.Fatalf("old signer: %+v", c.SAML.PendingMetadata)
	}
}
