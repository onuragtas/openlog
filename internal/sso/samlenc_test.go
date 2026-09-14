package sso_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/beevik/etree"
	"github.com/crewjam/saml/xmlenc"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

// encryptForSP encrypts plain for the SP certificate like Keycloak does: AES-GCM content encryption and RSA-OAEP
// (xmlenc 1.1, SHA-256 digest, MGF1-SHA-256) key transport in KeyInfo/EncryptedKey.
func encryptForSP(t *testing.T, cert *x509.Certificate, plain []byte, dataAlg string, keySize int) *etree.Element {
	t.Helper()
	key := make([]byte, keySize)
	_, _ = rand.Read(key)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	ct := append(nonce, aead.Seal(nil, nonce, plain, nil)...)
	ek, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, cert.PublicKey.(*rsa.PublicKey), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	ed := etree.NewElement("xenc:EncryptedData")
	ed.CreateAttr("xmlns:xenc", "http://www.w3.org/2001/04/xmlenc#")
	ed.CreateAttr("Type", "http://www.w3.org/2001/04/xmlenc#Element")
	ed.CreateElement("xenc:EncryptionMethod").CreateAttr("Algorithm", dataAlg)
	ki := ed.CreateElement("ds:KeyInfo")
	ki.CreateAttr("xmlns:ds", "http://www.w3.org/2000/09/xmldsig#")
	encKey := ki.CreateElement("xenc:EncryptedKey")
	em := encKey.CreateElement("xenc:EncryptionMethod")
	em.CreateAttr("Algorithm", "http://www.w3.org/2009/xmlenc11#rsa-oaep")
	em.CreateElement("ds:DigestMethod").CreateAttr("Algorithm", "http://www.w3.org/2001/04/xmlenc#sha256")
	mgf := em.CreateElement("xenc11:MGF")
	mgf.CreateAttr("xmlns:xenc11", "http://www.w3.org/2009/xmlenc11#")
	mgf.CreateAttr("Algorithm", "http://www.w3.org/2009/xmlenc11#mgf1sha256")
	encKey.CreateElement("xenc:CipherData").CreateElement("xenc:CipherValue").SetText(base64.StdEncoding.EncodeToString(ek))
	ed.CreateElement("xenc:CipherData").CreateElement("xenc:CipherValue").SetText(base64.StdEncoding.EncodeToString(ct))
	return ed
}

// encryptAssertion replaces the signed assertion of a response with an EncryptedAssertion (built by enc) and
// re-signs the response with the IdP key.
func encryptAssertion(t *testing.T, idp *testIdP, doc []byte, enc func(plain []byte) *etree.Element) []byte {
	t.Helper()
	d := etree.NewDocument()
	if err := d.ReadFromBytes(doc); err != nil {
		t.Fatal(err)
	}
	root := d.Root()
	var assertion *etree.Element
	for _, ch := range root.ChildElements() {
		switch ch.Tag {
		case "Signature":
			root.RemoveChild(ch)
		case "Assertion":
			assertion = ch
		}
	}
	ad := etree.NewDocument()
	ad.SetRoot(assertion.Copy())
	plain, err := ad.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	ea := etree.NewElement("saml:EncryptedAssertion")
	ea.CreateAttr("xmlns:saml", "urn:oasis:names:tc:SAML:2.0:assertion")
	ea.AddChild(enc(plain))
	idx := assertion.Index()
	root.RemoveChild(assertion)
	root.InsertChildAt(idx, ea)
	out := etree.NewDocument()
	out.SetRoot(signEnveloped(t, idp, root))
	b, err := out.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncryptedAssertions(t *testing.T) {
	env := newTestEnv(t)
	idp := newTestIdP(t, idpEntityID)
	c := env.samlConnection(t, idp.metadataXML(t), false, nil)
	spCert := spCertificate(t, c)
	good := respOpts{requestID: "id-req-enc", audience: env.sso.SAMLEntityID(c.ID), recipient: env.sso.SAMLACSURL(c.ID), email: "alice@example.com"}

	accept := map[string]func(plain []byte) *etree.Element{
		"AES-256-GCM, RSA-OAEP (xmlenc 1.1, SHA-256)": func(p []byte) *etree.Element {
			return encryptForSP(t, spCert, p, "http://www.w3.org/2009/xmlenc11#aes256-gcm", 32)
		},
		"AES-128-GCM": func(p []byte) *etree.Element {
			return encryptForSP(t, spCert, p, "http://www.w3.org/2009/xmlenc11#aes128-gcm", 16)
		},
		"AES-256-CBC, RSA-OAEP-MGF1P (SHA-1)": func(p []byte) *etree.Element {
			e := xmlenc.OAEP()
			e.BlockCipher, e.DigestMethod = xmlenc.AES256CBC, &xmlenc.SHA1
			el, err := e.Encrypt(spCert, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			return el
		},
	}
	for name, enc := range accept {
		t.Run(name, func(t *testing.T) {
			doc := encryptAssertion(t, idp, idp.makeResponse(t, good), enc)
			if !strings.Contains(string(doc), "EncryptedAssertion") || strings.Contains(string(doc), "alice@example.com") {
				t.Fatal("assertion not encrypted")
			}
			id, _, err := env.sso.SAMLAssertionForTest(c, doc, []string{"id-req-enc"})
			if err != nil || id.Email != "alice@example.com" {
				t.Fatalf("encrypted assertion: %+v %v", id, err)
			}
		})
	}

	refuse := map[string][]byte{
		"RSA PKCS#1 v1.5 key transport": encryptAssertion(t, idp, idp.makeResponse(t, good), func(p []byte) *etree.Element {
			e := xmlenc.PKCS1v15()
			el, err := e.Encrypt(spCert, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			return el
		}),
		"another SP certificate": encryptAssertion(t, idp, idp.makeResponse(t, good), func(p []byte) *etree.Element {
			other, err := env.sso.CreateConnection(context.Background(), env.ownerPrincipal(), sso.ConnectionInput{Protocol: sso.ProtocolSAML,
				IdPMetadataXML: idp.metadataXML(t)}, auth.ClientMeta{}) // another connection has its own SP key
			if err != nil {
				t.Fatal(err)
			}
			return encryptForSP(t, spCertificate(t, other), p, "http://www.w3.org/2009/xmlenc11#aes256-gcm", 32)
		}),
		"tampered ciphertext": func() []byte {
			doc := encryptAssertion(t, idp, idp.makeResponse(t, good), func(p []byte) *etree.Element {
				el := encryptForSP(t, spCert, p, "http://www.w3.org/2009/xmlenc11#aes256-gcm", 32)
				cv := el.FindElement("./CipherData/CipherValue")
				raw, _ := base64.StdEncoding.DecodeString(cv.Text())
				raw[len(raw)/2] ^= 0xff
				cv.SetText(base64.StdEncoding.EncodeToString(raw))
				return el
			})
			return doc
		}(),
	}
	for name, doc := range refuse {
		t.Run(name, func(t *testing.T) {
			if id, _, err := env.sso.SAMLAssertionForTest(c, doc, []string{"id-req-enc"}); err == nil {
				t.Fatalf("accepted: %+v", id)
			}
		})
	}
}
