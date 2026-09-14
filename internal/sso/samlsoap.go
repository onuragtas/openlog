package sso

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	xrv "github.com/mattermost/xml-roundtrip-validator"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/russellhaering/goxmldsig/etreeutils"

	"github.com/onuragtas/openlog/internal/auth"
)

// SAML 2.0 SOAP binding of single logout (D-098): the IdP posts a signed LogoutRequest server to server (back-channel,
// no browser). The request passes the same checks as a front-channel LogoutRequest (enveloped signature by an IdP
// metadata certificate, issuer, destination — the SOAP or the SLO URL when present —, IssueInstant, NotOnOrAfter,
// replay cache) and is answered with a LogoutResponse signed with the SP key in the SOAP body; refused requests get a
// SOAP fault without details.

const nsSOAP11 = "http://schemas.xmlsoap.org/soap/envelope/"

// SAMLSOAPLogoutURL is the SOAP single logout service of a connection.
func (s *Service) SAMLSOAPLogoutURL(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/saml/" + connectionID + "/slo/soap"
}

// SOAPOutcome is the HTTP answer of the SOAP single logout service (text/xml).
type SOAPOutcome struct {
	Status int
	Body   []byte
}

func soapEnvelope(content *etree.Element) []byte {
	env := etree.NewElement("soap:Envelope")
	env.CreateAttr("xmlns:soap", nsSOAP11)
	env.CreateElement("soap:Body").AddChild(content)
	b, _ := elementBytes(env)
	return b
}

// SOAPFault returns a SOAP 1.1 fault (HTTP 500); code is Client or Server.
func SOAPFault(code, message string) SOAPOutcome {
	f := etree.NewElement("soap:Fault")
	f.CreateElement("faultcode").SetText("soap:" + code)
	f.CreateElement("faultstring").SetText(message)
	return SOAPOutcome{Status: http.StatusInternalServerError, Body: soapEnvelope(f)}
}

// soapLogoutRequest extracts the LogoutRequest of a SOAP 1.1 envelope as a self-contained document: no DTD, only
// Header and Body in the envelope, no header the service would have to understand, exactly one LogoutRequest in the
// body (namespace declarations of the envelope are copied onto it).
func soapLogoutRequest(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > maxSAMLResponse {
		return nil, errors.New("the SOAP message is empty or too large")
	}
	if err := xrv.Validate(bytes.NewReader(body)); err != nil {
		return nil, fmt.Errorf("invalid xml: %v", err)
	}
	d := etree.NewDocument()
	if err := d.ReadFromBytes(body); err != nil {
		return nil, fmt.Errorf("invalid xml: %v", err)
	}
	for _, t := range d.Child {
		if _, ok := t.(*etree.Directive); ok {
			return nil, errors.New("DTDs are not allowed")
		}
	}
	env := d.Root()
	if env == nil || env.Tag != "Envelope" || env.NamespaceURI() != nsSOAP11 {
		return nil, errors.New("the message is not a SOAP 1.1 envelope")
	}
	var soapBody *etree.Element
	for _, ch := range env.ChildElements() {
		if ch.NamespaceURI() != nsSOAP11 {
			return nil, fmt.Errorf("unexpected element %s in the SOAP envelope", ch.Tag)
		}
		switch ch.Tag {
		case "Header":
			for _, h := range ch.ChildElements() {
				for _, a := range h.Attr {
					if a.Key == "mustUnderstand" && (a.Value == "1" || a.Value == "true") {
						return nil, fmt.Errorf("SOAP header %s must be understood", h.Tag)
					}
				}
			}
		case "Body":
			if soapBody != nil {
				return nil, errors.New("several SOAP bodies")
			}
			soapBody = ch
		default:
			return nil, fmt.Errorf("unexpected element %s in the SOAP envelope", ch.Tag)
		}
	}
	if soapBody == nil {
		return nil, errors.New("the SOAP envelope has no body")
	}
	kids := soapBody.ChildElements()
	if len(kids) != 1 || kids[0].Tag != "LogoutRequest" || kids[0].NamespaceURI() != nsProtocol {
		return nil, errors.New("the SOAP body must contain exactly one LogoutRequest")
	}
	nsctx, err := etreeutils.NSBuildParentContext(kids[0])
	if err != nil {
		return nil, fmt.Errorf("invalid namespaces: %v", err)
	}
	detached, err := etreeutils.NSDetatch(nsctx, kids[0])
	if err != nil {
		return nil, fmt.Errorf("invalid namespaces: %v", err)
	}
	pruneInheritedNamespaces(kids[0], detached)
	return elementBytes(detached)
}

// pruneInheritedNamespaces drops namespace declarations detached copied from the envelope that orig neither declares
// nor uses (element and attribute prefixes, QName-like attribute values): a LogoutRequest signed on its own with
// inclusive canonicalization then canonicalizes like it did at the IdP (the SOAP envelope's namespace is not part of
// it).
func pruneInheritedNamespaces(orig, detached *etree.Element) {
	own := map[string]bool{}
	for _, a := range orig.Attr {
		switch {
		case a.Space == "xmlns":
			own[a.Key] = true
		case a.Space == "" && a.Key == "xmlns":
			own[""] = true
		}
	}
	used := map[string]bool{}
	var walk func(e *etree.Element)
	walk = func(e *etree.Element) {
		used[e.Space] = true
		for _, a := range e.Attr {
			if a.Space == "xmlns" || (a.Space == "" && a.Key == "xmlns") {
				continue
			}
			if a.Space != "" {
				used[a.Space] = true
			}
			if p, _, ok := strings.Cut(a.Value, ":"); ok && p != "" && !strings.ContainsAny(p, "/ ") {
				used[p] = true
			}
		}
		for _, ch := range e.ChildElements() {
			walk(ch)
		}
	}
	walk(orig)
	attrs := detached.Attr[:0]
	for _, a := range detached.Attr {
		prefix, isDecl := "", false
		switch {
		case a.Space == "xmlns":
			prefix, isDecl = a.Key, true
		case a.Space == "" && a.Key == "xmlns":
			isDecl = true
		}
		if isDecl && !own[prefix] && !used[prefix] {
			continue
		}
		attrs = append(attrs, a)
	}
	detached.Attr = attrs
}

// SAMLSOAPLogout is the SOAP single logout service of a SAML connection (POST /api/v1/sso/saml/{connection_id}/slo/soap).
func (s *Service) SAMLSOAPLogout(ctx context.Context, connectionID string, body []byte, meta auth.ClientMeta) SOAPOutcome {
	const purpose = "saml-soap"
	limited, err := s.tooManyFailures(ctx, purpose, meta.IP, logoutFailureLimit)
	if err != nil {
		return SOAPFault("Server", "temporarily unavailable")
	}
	if limited {
		return SOAPFault("Client", "too many invalid logout requests")
	}
	c, err := s.store.GetConnectionByID(ctx, connectionID)
	if err != nil || c.Protocol != ProtocolSAML || c.SAML == nil {
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return SOAPFault("Server", "temporarily unavailable")
		}
		s.addFailure(ctx, purpose, meta.IP)
		return SOAPFault("Client", "unknown SAML connection")
	}
	fail := func(code string, err error) SOAPOutcome {
		s.addFailure(ctx, purpose, meta.IP)
		s.log.Warn("SOAP single logout refused", "connection_id", c.ID, "reason", code, "err", err)
		s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout_failed", "sso_connection", c.ID, map[string]any{"reason": code, "via": "idp_soap",
			"error": truncate(err.Error(), 300)})
		return SOAPFault("Client", "the LogoutRequest was not accepted")
	}
	sp, err := s.samlSP(c, false)
	if err != nil {
		s.log.Error("SOAP single logout: cannot load the service provider", "connection_id", c.ID, "err", err)
		return SOAPFault("Server", "temporarily unavailable")
	}
	doc, err := soapLogoutRequest(body)
	if err != nil {
		return fail(ErrCodeInvalidRequest, err)
	}
	certs, err := idpSigningCerts(sp.IDPMetadata)
	if err != nil {
		return fail(ErrCodeInvalidRequest, err)
	}
	msg, err := verifyEnvelopedLogout(certs, doc, "LogoutRequest")
	if err != nil {
		return fail(ErrCodeInvalidRequest, err)
	}
	req, status, code, err := s.acceptLogoutRequest(ctx, c, sp, msg, "idp_soap", meta)
	if err != nil {
		if code == ErrCodeUnavailable {
			s.log.Error("SOAP single logout failed", "connection_id", c.ID, "err", err)
			return SOAPFault("Server", "temporarily unavailable")
		}
		return fail(code, err)
	}
	id, err := newMessageID()
	if err != nil {
		return SOAPFault("Server", "temporarily unavailable")
	}
	resp := saml.LogoutResponse{ID: id, InResponseTo: req.ID, Version: "2.0", IssueInstant: s.now().UTC(),
		Issuer: &saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: sp.EntityID},
		Status: saml.Status{StatusCode: saml.StatusCode{Value: status}}}
	sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	if err := sp.SignLogoutResponse(&resp); err != nil {
		s.log.Error("SOAP single logout: cannot sign the LogoutResponse", "connection_id", c.ID, "err", err)
		return SOAPFault("Server", "temporarily unavailable")
	}
	return SOAPOutcome{Status: http.StatusOK, Body: soapEnvelope(resp.Element())}
}
