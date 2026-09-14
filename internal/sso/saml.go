package sso

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	xrv "github.com/mattermost/xml-roundtrip-validator"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	nsAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"
	nsProtocol  = "urn:oasis:names:tc:SAML:2.0:protocol"
	nsDSig      = "http://www.w3.org/2000/09/xmldsig#"
	bearer      = "urn:oasis:names:tc:SAML:2.0:cm:bearer"

	// maxSAMLResponse bounds the decoded SAMLResponse.
	maxSAMLResponse = 512 << 10
)

// samlSigAlgs is the XML signature allowlist (no SHA-1).
var samlSigAlgs = map[string]bool{
	dsig.RSASHA256SignatureMethod: true, dsig.RSASHA384SignatureMethod: true, dsig.RSASHA512SignatureMethod: true,
	dsig.ECDSASHA256SignatureMethod: true, dsig.ECDSASHA384SignatureMethod: true, dsig.ECDSASHA512SignatureMethod: true,
}

var samlDigestAlgs = map[string]bool{
	"http://www.w3.org/2001/04/xmlenc#sha256":       true,
	"http://www.w3.org/2001/04/xmldsig-more#sha384": true,
	"http://www.w3.org/2001/04/xmlenc#sha512":       true,
}

var samlOnce sync.Once

// configureSAML sets crewjam/saml's process-wide clock skew once (all connections share OPENLOG_SSO_CLOCK_SKEW).
func configureSAML(skew time.Duration) {
	samlOnce.Do(func() {
		saml.MaxClockSkew = skew
		saml.MaxIssueDelay = 90*time.Second + skew
	})
}

// strictVerifier restricts the signature and digest algorithms before crewjam/goxmldsig verify the enveloped
// signature against the IdP certificates.
type strictVerifier struct{}

func (strictVerifier) VerifySignature(vc *dsig.ValidationContext, el *etree.Element) error {
	sigs := 0
	for _, ch := range el.ChildElements() {
		if ch.Tag == "Signature" && ch.NamespaceURI() == nsDSig {
			sigs++
		}
	}
	if sigs != 1 {
		return fmt.Errorf("expected one Signature on %s, found %d", el.Tag, sigs)
	}
	sm := el.FindElement("./Signature/SignedInfo/SignatureMethod")
	if sm == nil || !samlSigAlgs[sm.SelectAttrValue("Algorithm", "")] {
		alg := ""
		if sm != nil {
			alg = sm.SelectAttrValue("Algorithm", "")
		}
		return fmt.Errorf("signature algorithm %q is not allowed", alg)
	}
	refs := el.FindElements("./Signature/SignedInfo/Reference")
	if len(refs) != 1 {
		return fmt.Errorf("expected one signature Reference, found %d", len(refs))
	}
	if dm := refs[0].FindElement("./DigestMethod"); dm == nil || !samlDigestAlgs[dm.SelectAttrValue("Algorithm", "")] {
		return errors.New("signature digest algorithm is not allowed")
	}
	if _, err := vc.Validate(el); err != nil {
		return fmt.Errorf("cannot validate signature on %s: %w", el.Tag, err)
	}
	return nil
}

// newSPKeyPair creates the SP key (RSA 2048, PKCS#8 DER) and a self-signed certificate valid for 10 years.
func newSPKeyPair(commonName string, now time.Time) ([]byte, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, "", err
	}
	if len(commonName) > 64 {
		commonName = commonName[:64]
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName, Organization: []string{"openlog"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", err
	}
	return keyDER, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

func spKeyAAD(connectionID string) string { return "saml-sp-key:" + connectionID }

// idpMetadataInfo validates IdP metadata and returns the display fields.
func idpMetadataInfo(xmlData []byte, now time.Time) (*saml.EntityDescriptor, SAMLConfig, error) {
	var out SAMLConfig
	if len(xmlData) == 0 {
		return nil, out, errors.New("IdP metadata is empty")
	}
	if len(xmlData) > maxIdPResponse {
		return nil, out, errors.New("IdP metadata is too large")
	}
	if err := xrv.Validate(bytes.NewReader(xmlData)); err != nil {
		return nil, out, fmt.Errorf("IdP metadata is not valid XML: %v", err)
	}
	md, err := parseMetadata(xmlData)
	if err != nil {
		return nil, out, fmt.Errorf("cannot parse IdP metadata: %v", err)
	}
	if md.EntityID == "" || len(md.IDPSSODescriptors) == 0 {
		return nil, out, errors.New("the metadata has no IDPSSODescriptor")
	}
	out.IdPEntityID = md.EntityID
	var notAfter time.Time
	for _, d := range md.IDPSSODescriptors {
		for _, sso := range d.SingleSignOnServices {
			if sso.Binding == saml.HTTPRedirectBinding && out.IdPSSOURL == "" {
				out.IdPSSOURL = sso.Location
			}
		}
		for _, kd := range d.KeyDescriptors {
			if kd.Use != "" && kd.Use != "signing" {
				continue
			}
			for _, c := range kd.KeyInfo.X509Data.X509Certificates {
				der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
				if err != nil {
					return nil, out, errors.New("the metadata contains an invalid certificate")
				}
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return nil, out, errors.New("the metadata contains an invalid certificate")
				}
				sum := sha256.Sum256(der)
				out.IdPCertificates = append(out.IdPCertificates, strings.ToUpper(hex.EncodeToString(sum[:])))
				if notAfter.IsZero() || cert.NotAfter.Before(notAfter) {
					notAfter = cert.NotAfter
				}
			}
		}
	}
	if out.IdPSSOURL == "" {
		return nil, out, errors.New("the IdP has no SingleSignOnService with the HTTP-Redirect binding")
	}
	if u, err := url.Parse(out.IdPSSOURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, out, errors.New("the IdP SingleSignOnService location is not an http(s) URL")
	}
	if len(out.IdPCertificates) == 0 {
		return nil, out, errors.New("the metadata has no signing certificate")
	}
	if !notAfter.IsZero() {
		out.IdPCertNotAfter = notAfter.UTC().Format(time.RFC3339)
	}
	return md, out, nil
}

// samlSP builds the crewjam service provider of c.
func (s *Service) samlSP(c Connection, allowIdPInitiated bool) (*saml.ServiceProvider, error) {
	if c.SAML == nil {
		return nil, errors.New("not a SAML connection")
	}
	md, err := parseMetadata([]byte(c.SAML.IdPMetadataXML))
	if err != nil {
		return nil, fmt.Errorf("stored IdP metadata: %w", err)
	}
	keyDER, err := s.cfg.SecretBox.Open(c.SPKeyEnc, spKeyAAD(c.ID))
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("SP key: %w", err)
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("SP key is not a signer")
	}
	block, _ := pem.Decode([]byte(c.SAML.SPCertificatePEM))
	if block == nil {
		return nil, errors.New("SP certificate missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("SP certificate: %w", err)
	}
	entityID := s.SAMLEntityID(c.ID)
	mdURL, err := url.Parse(entityID)
	if err != nil {
		return nil, err
	}
	acsURL, err := url.Parse(s.SAMLACSURL(c.ID))
	if err != nil {
		return nil, err
	}
	sp := &saml.ServiceProvider{
		EntityID: entityID, Key: signer, Certificate: cert, HTTPClient: s.client,
		MetadataURL: *mdURL, AcsURL: *acsURL, IDPMetadata: md,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat, AllowIDPInitiated: allowIdPInitiated,
		SignatureVerifier: strictVerifier{}, MetadataValidDuration: 48 * time.Hour,
		// crewjam accepts assertions without an AudienceRestriction; require one naming this SP.
		ValidateAudienceRestriction: func(a *saml.Assertion) error {
			if a.Conditions == nil {
				return errors.New("assertion has no Conditions")
			}
			for _, ar := range a.Conditions.AudienceRestrictions {
				if ar.Audience.Value == entityID {
					return nil
				}
			}
			return fmt.Errorf("assertion AudienceRestriction does not contain %q", entityID)
		},
	}
	if c.SAML.SignAuthnRequests {
		sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	}
	return sp, nil
}

// samlAuthURL returns the HTTP-Redirect binding URL of a new AuthnRequest and the request id.
func (s *Service) samlAuthURL(c Connection, relayState string) (string, string, error) {
	sp, err := s.samlSP(c, false)
	if err != nil {
		return "", "", err
	}
	loc := sp.GetSSOBindingLocation(saml.HTTPRedirectBinding)
	if loc == "" {
		return "", "", errors.New("the IdP has no HTTP-Redirect SingleSignOnService")
	}
	req, err := sp.MakeAuthenticationRequest(loc, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", "", err
	}
	u, err := req.Redirect(relayState, sp)
	if err != nil {
		return "", "", err
	}
	return u.String(), req.ID, nil
}

// SAMLMetadata returns the SP metadata XML of a connection.
func (s *Service) SAMLMetadata(c Connection) ([]byte, error) {
	sp, err := s.samlSP(c, false)
	if err != nil {
		return nil, err
	}
	md := sp.Metadata()
	for i := range md.SPSSODescriptors {
		wantSigned := true
		md.SPSSODescriptors[i].WantAssertionsSigned = &wantSigned
		signed := c.SAML.SignAuthnRequests
		md.SPSSODescriptors[i].AuthnRequestsSigned = &signed
	}
	return marshalMetadata(md)
}

// precheckSAMLResponse enforces the document structure before signature validation (defence against XML
// signature wrapping): no DTD, a single samlp:Response root and exactly one Assertion or EncryptedAssertion in
// the whole document. It returns the Response's InResponseTo.
func precheckSAMLResponse(doc []byte) (string, error) {
	if len(doc) > maxSAMLResponse {
		return "", errors.New("SAMLResponse is too large")
	}
	if err := xrv.Validate(bytes.NewReader(doc)); err != nil {
		return "", fmt.Errorf("invalid xml: %v", err)
	}
	d := etree.NewDocument()
	if err := d.ReadFromBytes(doc); err != nil {
		return "", fmt.Errorf("invalid xml: %v", err)
	}
	for _, t := range d.Child {
		if _, ok := t.(*etree.Directive); ok {
			return "", errors.New("DTDs are not allowed")
		}
	}
	root := d.Root()
	if root == nil || root.Tag != "Response" || root.NamespaceURI() != nsProtocol {
		return "", errors.New("the document is not a SAML Response")
	}
	assertions, responses := 0, 0
	var walk func(e *etree.Element)
	walk = func(e *etree.Element) {
		switch e.Tag {
		case "Assertion", "EncryptedAssertion":
			assertions++
		case "Response":
			responses++
		}
		for _, ch := range e.ChildElements() {
			walk(ch)
		}
	}
	walk(root)
	if responses != 1 {
		return "", errors.New("nested Response elements are not allowed")
	}
	if assertions != 1 {
		return "", fmt.Errorf("expected exactly one assertion, found %d", assertions)
	}
	return root.SelectAttrValue("InResponseTo", ""), nil
}

// samlAssertion validates a decoded SAMLResponse and returns the verified assertion. possibleIDs is the
// outstanding AuthnRequest id (SP-initiated) or nil (IdP-initiated, only when allowed).
func (s *Service) samlAssertion(c Connection, doc []byte, possibleIDs []string) (*saml.Assertion, error) {
	inResponseTo, err := precheckSAMLResponse(doc)
	if err != nil {
		return nil, err
	}
	idpInitiated := len(possibleIDs) == 0
	if idpInitiated && inResponseTo != "" {
		return nil, errors.New("unsolicited response carries InResponseTo")
	}
	sp, err := s.samlSP(c, idpInitiated)
	if err != nil {
		return nil, err
	}
	a, err := sp.ParseXMLResponse(doc, possibleIDs, sp.AcsURL)
	if err != nil {
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) && ire.PrivateErr != nil {
			return nil, ire.PrivateErr
		}
		return nil, err
	}
	if a.ID == "" {
		return nil, errors.New("assertion has no ID")
	}
	if a.Subject == nil {
		return nil, errors.New("assertion has no Subject")
	}
	confirmed := false
	for _, sc := range a.Subject.SubjectConfirmations {
		d := sc.SubjectConfirmationData
		if sc.Method != bearer || d == nil || d.Recipient != sp.AcsURL.String() || d.NotOnOrAfter.IsZero() {
			continue
		}
		if idpInitiated && d.InResponseTo != "" {
			return nil, errors.New("unsolicited assertion carries InResponseTo")
		}
		confirmed = true
	}
	if !confirmed {
		return nil, errors.New("assertion has no bearer SubjectConfirmation for this ACS")
	}
	if len(a.AuthnStatements) == 0 {
		return nil, errors.New("assertion has no AuthnStatement")
	}
	return a, nil
}

// assertionExpiry is how long the assertion id must stay in the replay cache.
func assertionExpiry(a *saml.Assertion, minTTL, skew time.Duration, at time.Time) time.Time {
	exp := at.Add(minTTL)
	if a.Conditions != nil && a.Conditions.NotOnOrAfter.After(exp) {
		exp = a.Conditions.NotOnOrAfter
	}
	for _, sc := range a.Subject.SubjectConfirmations {
		if d := sc.SubjectConfirmationData; d != nil && d.NotOnOrAfter.After(exp) {
			exp = d.NotOnOrAfter
		}
	}
	return exp.Add(skew)
}

// samlAttributes returns the values of an attribute matched by Name or FriendlyName.
func samlAttributes(a *saml.Assertion, name string) ([]string, bool) {
	var out []string
	found := false
	for _, st := range a.AttributeStatements {
		for _, attr := range st.Attributes {
			if attr.Name != name && attr.FriendlyName != name {
				continue
			}
			found = true
			for _, v := range attr.Values {
				if t := strings.TrimSpace(v.Value); t != "" {
					out = append(out, t)
				}
			}
		}
	}
	return out, found
}

func firstAttr(a *saml.Assertion, names ...string) string {
	for _, n := range names {
		if vs, _ := samlAttributes(a, n); len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// samlIdentity extracts the identity of a verified assertion.
func samlIdentity(c Connection, a *saml.Assertion) Identity {
	id := Identity{Issuer: a.Issuer.Value, EmailVerified: true}
	if a.Subject.NameID != nil {
		id.Subject = strings.TrimSpace(a.Subject.NameID.Value)
	}
	emailNames := []string{"email", "mail", "emailaddress", "urn:oid:0.9.2342.19200300.100.1.3",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"}
	if c.EmailAttribute != "" {
		emailNames = []string{c.EmailAttribute}
	}
	id.Email = strings.ToLower(firstAttr(a, emailNames...))
	if id.Email == "" && a.Subject.NameID != nil && strings.Contains(id.Subject, "@") &&
		(a.Subject.NameID.Format == string(saml.EmailAddressNameIDFormat) || a.Subject.NameID.Format == "" ||
			a.Subject.NameID.Format == string(saml.UnspecifiedNameIDFormat)) {
		id.Email = strings.ToLower(id.Subject)
	}
	nameNames := []string{"name", "displayName", "http://schemas.microsoft.com/identity/claims/displayname", "cn"}
	if c.NameAttribute != "" {
		nameNames = []string{c.NameAttribute}
	}
	id.Name = firstAttr(a, nameNames...)
	if id.Name == "" {
		id.Name = strings.TrimSpace(firstAttr(a, "firstName", "givenName", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname") +
			" " + firstAttr(a, "lastName", "surname", "sn", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname"))
	}
	groupsName := attrOr(c.GroupsAttribute, "groups")
	id.Groups, id.GroupsPresent = samlAttributes(a, groupsName)
	if !id.GroupsPresent && c.GroupsAttribute == "" {
		id.Groups, id.GroupsPresent = samlAttributes(a, "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups")
	}
	return id
}

// testSAML checks the stored metadata, certificates and SP key of c.
func (s *Service) testSAML(c Connection) []Check {
	var checks []Check
	_, info, err := idpMetadataInfo([]byte(c.SAML.IdPMetadataXML), s.now())
	if err != nil {
		return append(checks, Check{Name: "idp_metadata", OK: false, Message: err.Error()})
	}
	checks = append(checks, Check{Name: "idp_metadata", OK: true, Message: info.IdPEntityID})
	if t, err := time.Parse(time.RFC3339, info.IdPCertNotAfter); err == nil {
		switch {
		case !t.After(s.now()):
			checks = append(checks, Check{Name: "idp_certificate", OK: false, Message: "expired on " + info.IdPCertNotAfter})
		case t.Before(s.now().Add(30 * 24 * time.Hour)):
			checks = append(checks, Check{Name: "idp_certificate", OK: true, Message: "expires soon: " + info.IdPCertNotAfter})
		default:
			checks = append(checks, Check{Name: "idp_certificate", OK: true, Message: "valid until " + info.IdPCertNotAfter})
		}
	}
	if _, err := s.samlSP(c, false); err != nil {
		checks = append(checks, Check{Name: "sp_key", OK: false, Message: err.Error()})
	} else {
		checks = append(checks, Check{Name: "sp_key", OK: true, Message: "stored"})
	}
	return checks
}
