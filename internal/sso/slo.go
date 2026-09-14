package sso

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"
	xrv "github.com/mattermost/xml-roundtrip-validator"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/onuragtas/openlog/internal/auth"
)

// Single logout (D-088): "Sign out everywhere" ends the openlog session(s) of an SSO sign-in and the IdP session —
// SAML 2.0 Single Logout (SP-initiated LogoutRequest, IdP-initiated LogoutRequest, LogoutResponse; HTTP-Redirect
// and HTTP-POST bindings, always signed) and OpenID Connect RP-initiated logout (end_session_endpoint with
// id_token_hint). The IdP session of every SSO session is recorded at sign-in (sso_sessions).

const (
	sigAlgRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	// maxLogoutAge bounds the IssueInstant of a logout message (browser round trip plus clock skew).
	maxLogoutAge = 5 * time.Minute
	// maxSLOIDLen bounds message ids kept in the replay cache.
	maxSLOIDLen     = 256
	statusResponder = "urn:oasis:names:tc:SAML:2.0:status:Responder"
)

// redirectSigAlgs are the signature algorithms accepted on HTTP-Redirect binding messages (no SHA-1).
var redirectSigAlgs = map[string]crypto.Hash{
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha256":   crypto.SHA256,
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha384":   crypto.SHA384,
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha512":   crypto.SHA512,
	"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256": crypto.SHA256,
	"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha384": crypto.SHA384,
	"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha512": crypto.SHA512,
}

// idpSLOEndpoint returns the IdP's SingleLogoutService: HTTP-Redirect preferred, else HTTP-POST.
func idpSLOEndpoint(md *saml.EntityDescriptor) (location, responseLocation, binding string) {
	if md == nil {
		return "", "", ""
	}
	for _, want := range []string{saml.HTTPRedirectBinding, saml.HTTPPostBinding} {
		for _, d := range md.IDPSSODescriptors {
			for _, e := range d.SingleLogoutServices {
				if e.Binding == want && e.Location != "" {
					resp := e.ResponseLocation
					if resp == "" {
						resp = e.Location
					}
					return e.Location, resp, want
				}
			}
		}
	}
	return "", "", ""
}

// idpSigningCerts returns the signing certificates of IdP metadata.
func idpSigningCerts(md *saml.EntityDescriptor) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for _, d := range md.IDPSSODescriptors {
		for _, kd := range d.KeyDescriptors {
			if kd.Use != "" && kd.Use != "signing" {
				continue
			}
			for _, c := range kd.KeyInfo.X509Data.X509Certificates {
				der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
				if err != nil {
					return nil, errors.New("invalid IdP certificate")
				}
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return nil, errors.New("invalid IdP certificate")
				}
				out = append(out, cert)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the IdP metadata has no signing certificate")
	}
	return out, nil
}

func newMessageID() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "id-" + hex.EncodeToString(b), nil
}

// ---- session info and SP-initiated logout ----

// SessionInfo describes the single sign-on behind a session (GET /api/v1/auth/sso/session).
type SessionInfo struct {
	SSO            bool
	Protocol       Protocol
	ConnectionID   string
	ConnectionName string
	// IdPLogout: "Sign out everywhere" also ends the IdP session (SAML SLO endpoint or OIDC end_session_endpoint).
	IdPLogout bool
}

// currentSession returns the auth session of p.
func (s *Service) currentSession(ctx context.Context, p *auth.Principal) (auth.Session, error) {
	ss, err := s.users.ListSessions(ctx, p.UserID, s.now())
	if err != nil {
		return auth.Session{}, s.fail(err)
	}
	for _, x := range ss {
		if x.ID == p.SessionID {
			return x, nil
		}
	}
	return auth.Session{}, &auth.Error{Code: auth.CodeUnauthenticated, Message: "session ended"}
}

func requireSessionPrincipal(p *auth.Principal) error {
	if p == nil || p.Kind != auth.KindSession {
		return denied("this operation requires a signed-in user")
	}
	return nil
}

// SessionInfo reports whether the caller's session was created by single sign-on and whether the IdP session can
// be ended.
func (s *Service) SessionInfo(ctx context.Context, p *auth.Principal) (SessionInfo, error) {
	if err := requireSessionPrincipal(p); err != nil {
		return SessionInfo{}, err
	}
	sess, err := s.currentSession(ctx, p)
	if err != nil || sess.ConnectionID == "" {
		return SessionInfo{}, err
	}
	c, err := s.store.GetConnectionByID(ctx, sess.ConnectionID)
	if errors.Is(err, auth.ErrNotFound) {
		return SessionInfo{}, nil
	}
	if err != nil {
		return SessionInfo{}, s.fail(err)
	}
	info := SessionInfo{SSO: true, Protocol: c.Protocol, ConnectionID: c.ID, ConnectionName: c.Name}
	switch c.Protocol {
	case ProtocolSAML:
		info.IdPLogout = c.SAML != nil && c.SAML.IdPSLOURL != ""
	case ProtocolOIDC:
		if oc, err := s.oidcClient(ctx, c); err == nil {
			info.IdPLogout = oc.meta.EndSessionURL != ""
		}
	}
	return info, nil
}

// LogoutResult is where the browser continues after "Sign out everywhere": RedirectURL (HTTP-Redirect binding or
// OIDC), or a form to post (HTTP-POST binding); both empty = only the openlog sessions ended.
type LogoutResult struct {
	Protocol    Protocol
	RedirectURL string
	PostURL     string
	PostFields  map[string]string
	Revoked     int
}

// logoutTarget returns the UI path a logout returns to: /login or an allowlisted path of the connection.
func logoutTarget(c Connection, redirect string) string {
	if r := validRedirect(redirect); r == "/login" || slices.Contains(c.LogoutRedirectAllowlist, r) {
		return r
	}
	return "/login"
}

func withQuery(path, kv string) string {
	if strings.Contains(path, "?") {
		return path + "&" + kv
	}
	return path + "?" + kv
}

// Logout ends the caller's session and, for an SSO session, the user's other sessions of the same connection and
// the IdP session (POST /api/v1/auth/sso/logout). The openlog sessions end before the browser is sent to the IdP,
// so a failing IdP never keeps them alive.
func (s *Service) Logout(ctx context.Context, p *auth.Principal, redirect string, meta auth.ClientMeta) (LogoutResult, error) {
	if err := requireSessionPrincipal(p); err != nil {
		return LogoutResult{}, err
	}
	sess, err := s.currentSession(ctx, p)
	if err != nil {
		return LogoutResult{}, err
	}
	if sess.ConnectionID == "" {
		return LogoutResult{Revoked: 1}, s.auth.Logout(ctx, p, meta)
	}
	c, cerr := s.store.GetConnectionByID(ctx, sess.ConnectionID)
	link, lerr := s.store.GetSSOSession(ctx, sess.ID)
	now := s.now()
	var others []SSOSession
	if cerr == nil {
		if others, err = s.store.ListSSOSessions(ctx, c.ID, "", p.UserID, now); err != nil {
			return LogoutResult{}, s.fail(err)
		}
	}
	if err := s.auth.Logout(ctx, p, meta); err != nil {
		return LogoutResult{}, err
	}
	res := LogoutResult{Revoked: 1}
	for _, o := range others {
		if o.SessionID == sess.ID {
			continue
		}
		if err := s.users.RevokeSession(ctx, o.UserID, o.SessionID, now); err != nil && !errors.Is(err, auth.ErrNotFound) {
			s.log.Warn("cannot revoke SSO session", "session_id", o.SessionID, "err", err)
			continue
		}
		res.Revoked++
	}
	if cerr != nil {
		return res, nil // connection deleted: nothing to end at the IdP
	}
	res.Protocol = c.Protocol
	target := logoutTarget(c, redirect)
	idpErr := error(nil)
	switch {
	case lerr != nil:
		idpErr = fmt.Errorf("no IdP session recorded: %w", lerr)
	case c.Protocol == ProtocolSAML:
		idpErr = s.samlLogoutStart(ctx, c, link, target, p.UserID, &res)
	case c.Protocol == ProtocolOIDC:
		idpErr = s.oidcLogoutStart(ctx, c, link, target, p.UserID, &res)
	}
	if idpErr != nil {
		s.log.Warn("single logout at the identity provider is not possible", "connection_id", c.ID, "err", idpErr)
	}
	s.audit(ctx, c.OrgID, p.UserID, p.Email, meta.IP, "sso.logout", "session", sess.ID, map[string]any{"protocol": c.Protocol,
		"connection_id": c.ID, "via": "user", "revoked_sessions": res.Revoked, "idp_logout": res.RedirectURL != "" || res.PostURL != ""})
	return res, nil
}

// newLogoutState stores a logout state and returns its token.
func (s *Service) newLogoutState(ctx context.Context, c Connection, requestID, target, actor string) (string, error) {
	state, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	st := LoginState{StateHash: hashToken(state), OrgID: c.OrgID, ConnectionID: c.ID, Purpose: PurposeLogout, SAMLRequestID: requestID,
		RedirectTo: target, ActorUserID: actor, ConfigVersion: c.ConfigVersion, CreatedAt: now, ExpiresAt: now.Add(s.cfg.LoginTTL)}
	if err := s.store.CreateLoginState(ctx, &st); err != nil {
		return "", err
	}
	return state, nil
}

// samlLogoutStart builds the signed LogoutRequest of an SP-initiated logout.
func (s *Service) samlLogoutStart(ctx context.Context, c Connection, link SSOSession, target, actor string, res *LogoutResult) error {
	if c.SAML == nil || c.SAML.IdPSLOURL == "" {
		return errors.New("the IdP metadata has no SingleLogoutService")
	}
	if link.Subject == "" {
		return errors.New("the sign-in carried no NameID")
	}
	sp, err := s.samlSP(c, false)
	if err != nil {
		return err
	}
	loc, _, binding := idpSLOEndpoint(sp.IDPMetadata)
	if loc == "" {
		return errors.New("the IdP metadata has no SingleLogoutService")
	}
	id, err := newMessageID()
	if err != nil {
		return err
	}
	req := saml.LogoutRequest{ID: id, Version: "2.0", IssueInstant: s.now().UTC(), Destination: loc,
		Issuer: &saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: sp.EntityID},
		NameID: &saml.NameID{Format: link.NameIDFormat, Value: link.Subject, NameQualifier: link.NameQualifier, SPNameQualifier: link.SPNameQualifier}}
	if link.SessionIndex != "" {
		req.SessionIndex = &saml.SessionIndex{Value: link.SessionIndex}
	}
	state, err := s.newLogoutState(ctx, c, id, target, actor)
	if err != nil {
		return err
	}
	if binding == saml.HTTPRedirectBinding {
		u, err := redirectBindingURL(sp.Key, loc, "SAMLRequest", req.Element(), state)
		if err != nil {
			return err
		}
		res.RedirectURL = u
		return nil
	}
	sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	if err := sp.SignLogoutRequest(&req); err != nil {
		return err
	}
	b, err := elementBytes(req.Element())
	if err != nil {
		return err
	}
	res.PostURL, res.PostFields = loc, map[string]string{"SAMLRequest": base64.StdEncoding.EncodeToString(b), "RelayState": state}
	return nil
}

func elementBytes(el *etree.Element) ([]byte, error) {
	doc := etree.NewDocument()
	doc.SetRoot(el)
	return doc.WriteToBytes()
}

// redirectBindingURL encodes a message for the HTTP-Redirect binding (DEFLATE, base64) and signs the query string
// (SAMLRequest|SAMLResponse, RelayState, SigAlg) with the SP key (RSA-SHA256).
func redirectBindingURL(key crypto.Signer, destination, param string, el *etree.Element, relayState string) (string, error) {
	raw, err := elementBytes(el)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(raw); err != nil {
		return "", err
	}
	if err := fw.Close(); err != nil {
		return "", err
	}
	q := param + "=" + url.QueryEscape(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if relayState != "" {
		q += "&RelayState=" + url.QueryEscape(relayState)
	}
	q += "&SigAlg=" + url.QueryEscape(sigAlgRSASHA256)
	sum := sha256.Sum256([]byte(q))
	sig, err := key.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		return "", err
	}
	q += "&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
	sep := "?"
	if strings.Contains(destination, "?") {
		sep = "&"
	}
	return destination + sep + q, nil
}

// ---- messages from the IdP ----

// SLOInput is a request to the SAML single logout service.
type SLOInput struct {
	Method   string // GET (HTTP-Redirect) or POST (HTTP-POST)
	RawQuery string
	Form     url.Values
}

// SLOOutcome is the answer of the single logout service: a redirect (a UI path, or the IdP when External) or an
// auto-submitting HTML form (HTTP-POST binding, with its Content-Security-Policy).
type SLOOutcome struct {
	Redirect string
	External bool
	HTML     []byte
	CSP      string
}

// samlMessage is a verified logout message.
type samlMessage struct {
	root  *etree.Element
	doc   []byte
	relay string
}

// parseRedirectQuery returns the raw (still URL-encoded) values of the HTTP-Redirect binding parameters.
func parseRedirectQuery(rawQuery string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "SAMLRequest", "SAMLResponse", "RelayState", "SigAlg", "Signature":
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("parameter %s repeated", k)
			}
			out[k] = v
		}
	}
	return out, nil
}

func decodeQueryValue(raw string) (string, error) {
	return url.QueryUnescape(raw)
}

// verifyRedirectSignature checks the query signature of an HTTP-Redirect binding message against the IdP
// certificates.
func verifyRedirectSignature(certs []*x509.Certificate, param string, vals map[string]string) error {
	sigAlg, err := decodeQueryValue(vals["SigAlg"])
	if err != nil || vals["Signature"] == "" || sigAlg == "" {
		return errors.New("the message is not signed")
	}
	h, ok := redirectSigAlgs[sigAlg]
	if !ok {
		return fmt.Errorf("signature algorithm %q is not allowed", sigAlg)
	}
	sigB64, err := decodeQueryValue(vals["Signature"])
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	signed := param + "=" + vals[param]
	if r, ok := vals["RelayState"]; ok {
		signed += "&RelayState=" + r
	}
	signed += "&SigAlg=" + vals["SigAlg"]
	hh := h.New()
	hh.Write([]byte(signed))
	sum := hh.Sum(nil)
	for _, c := range certs {
		switch pub := c.PublicKey.(type) {
		case *rsa.PublicKey:
			if strings.Contains(sigAlg, "#rsa-") && rsa.VerifyPKCS1v15(pub, h, sum, sig) == nil {
				return nil
			}
		case *ecdsa.PublicKey:
			if strings.Contains(sigAlg, "#ecdsa-") && ecdsa.VerifyASN1(pub, sum, sig) {
				return nil
			}
		}
	}
	return errors.New("the signature does not match an IdP certificate")
}

// checkLogoutDocument enforces the structure of a logout message: no DTD, the expected root in the SAML protocol
// namespace, no other SAML message or assertion in the document.
func checkLogoutDocument(doc []byte, want string) (*etree.Element, error) {
	if len(doc) > maxSAMLResponse {
		return nil, errors.New("the message is too large")
	}
	if err := xrv.Validate(bytes.NewReader(doc)); err != nil {
		return nil, fmt.Errorf("invalid xml: %v", err)
	}
	d := etree.NewDocument()
	if err := d.ReadFromBytes(doc); err != nil {
		return nil, fmt.Errorf("invalid xml: %v", err)
	}
	for _, t := range d.Child {
		if _, ok := t.(*etree.Directive); ok {
			return nil, errors.New("DTDs are not allowed")
		}
	}
	root := d.Root()
	if root == nil || root.Tag != want || root.NamespaceURI() != nsProtocol {
		return nil, fmt.Errorf("the document is not a SAML %s", want)
	}
	n := 0
	var walk func(e *etree.Element)
	walk = func(e *etree.Element) {
		switch e.Tag {
		case "LogoutRequest", "LogoutResponse", "Response", "Assertion", "AuthnRequest":
			n++
		}
		for _, ch := range e.ChildElements() {
			walk(ch)
		}
	}
	walk(root)
	if n != 1 {
		return nil, errors.New("nested SAML messages are not allowed")
	}
	return root, nil
}

// readLogoutMessage decodes and verifies a LogoutRequest ("SAMLRequest") or LogoutResponse ("SAMLResponse") of
// either binding. The signature is required: query signature (HTTP-Redirect) or one enveloped signature on the
// root (HTTP-POST), by a certificate of the IdP metadata.
func (s *Service) readLogoutMessage(sp *saml.ServiceProvider, in SLOInput, param, rootTag string) (samlMessage, error) {
	certs, err := idpSigningCerts(sp.IDPMetadata)
	if err != nil {
		return samlMessage{}, err
	}
	var msg samlMessage
	if in.Method == "GET" {
		vals, err := parseRedirectQuery(in.RawQuery)
		if err != nil {
			return msg, err
		}
		raw, err := decodeQueryValue(vals[param])
		if err != nil || raw == "" {
			return msg, fmt.Errorf("%s is missing", param)
		}
		if msg.relay, err = decodeQueryValue(vals["RelayState"]); err != nil {
			return msg, errors.New("invalid RelayState")
		}
		deflated, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(raw), ""))
		if err != nil {
			return msg, fmt.Errorf("%s is not base64", param)
		}
		doc, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(deflated)), maxSAMLResponse+1))
		if err != nil {
			return msg, fmt.Errorf("%s is not DEFLATE encoded", param)
		}
		if msg.root, err = checkLogoutDocument(doc, rootTag); err != nil {
			return msg, err
		}
		if err := verifyRedirectSignature(certs, param, vals); err != nil {
			return msg, err
		}
		msg.doc = doc
		return msg, nil
	}
	raw := in.Form.Get(param)
	doc, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(raw), ""))
	if err != nil || len(doc) == 0 {
		return msg, fmt.Errorf("%s is missing or not base64", param)
	}
	msg, err = verifyEnvelopedLogout(certs, doc, rootTag)
	msg.relay = in.Form.Get("RelayState")
	return msg, err
}

// verifyEnvelopedLogout checks the structure of a logout message document (HTTP-POST or SOAP binding) and its one
// enveloped signature by a certificate of certs.
func verifyEnvelopedLogout(certs []*x509.Certificate, doc []byte, rootTag string) (samlMessage, error) {
	var msg samlMessage
	var err error
	if msg.root, err = checkLogoutDocument(doc, rootTag); err != nil {
		return msg, err
	}
	store := dsig.MemoryX509CertificateStore{Roots: certs}
	vc := dsig.NewDefaultValidationContext(&store)
	vc.IdAttribute = "ID"
	if err := (strictVerifier{}).VerifySignature(vc, msg.root); err != nil {
		return msg, err
	}
	msg.doc = doc
	return msg, nil
}

// checkLogoutHeader validates issuer, destination (the SLO URL or one of alsoAllowed) and issue time of a verified
// logout message.
func (s *Service) checkLogoutHeader(sp *saml.ServiceProvider, issuer *saml.Issuer, destination string, issued time.Time, required bool, alsoAllowed ...string) error {
	if issuer == nil || issuer.Value != sp.IDPMetadata.EntityID {
		return errors.New("issuer does not match the IdP entity ID")
	}
	if (required || destination != "") && destination != sp.SloURL.String() && !slices.Contains(alsoAllowed, destination) {
		return fmt.Errorf("destination %q is not the SLO URL", truncate(destination, 200))
	}
	now := s.now()
	if issued.IsZero() || issued.After(now.Add(s.cfg.ClockSkew)) || issued.Before(now.Add(-maxLogoutAge-s.cfg.ClockSkew)) {
		return errors.New("IssueInstant is missing or not current")
	}
	return nil
}

func htmlPostCSP() string {
	sum := sha256.Sum256([]byte(postScript))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"
}

const postScript = `document.getElementById("saml").submit();`

var postTemplate = template.Must(template.New("saml-post").Parse(`<!doctype html><html><head><meta charset="utf-8">` +
	`<meta name="referrer" content="no-referrer"><title>openlog</title></head><body>` +
	`<form id="saml" method="post" action="{{.URL}}">{{range $k, $v := .Fields}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}` +
	`<noscript><button type="submit">Continue</button></noscript></form><script>` + postScript + `</script></body></html>`))

// PostForm renders an auto-submitting form (HTTP-POST binding).
func PostForm(action string, fields map[string]string) ([]byte, string, error) {
	var buf bytes.Buffer
	if err := postTemplate.Execute(&buf, struct {
		URL    string
		Fields map[string]string
	}{action, fields}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), htmlPostCSP(), nil
}

// SAMLSLO is the single logout service of a SAML connection (GET/POST /api/v1/sso/saml/{connection_id}/slo): an
// IdP-initiated LogoutRequest ends the matching SSO sessions and is answered with a signed LogoutResponse; a
// LogoutResponse completes an SP-initiated logout.
func (s *Service) SAMLSLO(ctx context.Context, connectionID string, in SLOInput, meta auth.ClientMeta) SLOOutcome {
	fail := func(c *Connection, code string, err error) SLOOutcome {
		orgID := ""
		if c != nil {
			orgID = c.OrgID
		}
		s.log.Warn("single logout failed", "connection_id", connectionID, "reason", code, "err", err)
		if orgID != "" {
			s.audit(ctx, orgID, "", "", meta.IP, "sso.logout_failed", "sso_connection", connectionID,
				map[string]any{"reason": code, "error": truncate(err.Error(), 300)})
		}
		return SLOOutcome{Redirect: "/login?sso_error=" + code}
	}
	c, err := s.store.GetConnectionByID(ctx, connectionID)
	if err != nil || c.Protocol != ProtocolSAML || c.SAML == nil {
		if err == nil || errors.Is(err, auth.ErrNotFound) {
			return fail(nil, ErrCodeDisabled, errors.New("unknown SAML connection"))
		}
		return fail(nil, ErrCodeUnavailable, err)
	}
	sp, err := s.samlSP(c, false)
	if err != nil {
		return fail(&c, ErrCodeUnavailable, err)
	}
	isResponse := in.Form.Get("SAMLResponse") != ""
	if in.Method == "GET" {
		vals, _ := parseRedirectQuery(in.RawQuery)
		_, isResponse = vals["SAMLResponse"]
	}
	if isResponse {
		return s.sloResponse(ctx, c, sp, in, meta)
	}
	msg, err := s.readLogoutMessage(sp, in, "SAMLRequest", "LogoutRequest")
	if err != nil {
		return fail(&c, ErrCodeInvalidRequest, err)
	}
	req, status, code, err := s.acceptLogoutRequest(ctx, c, sp, msg, "idp", meta)
	if err != nil {
		return fail(&c, code, err)
	}
	out, err := s.logoutResponse(sp, req.ID, status, msg.relay)
	if err != nil {
		return fail(&c, ErrCodeUnavailable, err)
	}
	return out
}

// acceptLogoutRequest validates a signature-verified IdP LogoutRequest — ID, version, issuer, destination (the SLO
// URL; via "idp_soap": optional, the SOAP or the SLO URL), IssueInstant, NotOnOrAfter, NameID or EncryptedID, replay
// cache — and ends the matching sessions. status is the LogoutResponse status; code names a rejection.
func (s *Service) acceptLogoutRequest(ctx context.Context, c Connection, sp *saml.ServiceProvider, msg samlMessage, via string,
	meta auth.ClientMeta) (req saml.LogoutRequest, status, code string, err error) {
	if err := xml.Unmarshal(msg.doc, &req); err != nil {
		return req, "", ErrCodeInvalidRequest, err
	}
	if req.ID == "" || len(req.ID) > maxSLOIDLen || req.Version != "2.0" {
		return req, "", ErrCodeInvalidRequest, errors.New("invalid LogoutRequest ID or version")
	}
	soap := via == "idp_soap"
	var alsoAllowed []string
	if soap {
		alsoAllowed = []string{s.SAMLSOAPLogoutURL(c.ID)}
	}
	if err := s.checkLogoutHeader(sp, req.Issuer, req.Destination, req.IssueInstant, !soap, alsoAllowed...); err != nil {
		return req, "", ErrCodeInvalidRequest, err
	}
	now := s.now()
	if req.NotOnOrAfter != nil && !now.Before(req.NotOnOrAfter.Add(s.cfg.ClockSkew)) {
		return req, "", ErrCodeExpired, errors.New("LogoutRequest expired")
	}
	nameID := req.NameID
	if nameID == nil {
		if nameID, err = s.decryptNameID(sp, msg.root); err != nil {
			return req, "", ErrCodeInvalidRequest, err
		}
	}
	if strings.TrimSpace(nameID.Value) == "" {
		return req, "", ErrCodeInvalidRequest, errors.New("LogoutRequest has no NameID")
	}
	fresh, err := s.store.RecordAssertion(ctx, c.ID, "slo:"+req.ID, now.Add(minAssertionTTL))
	if err != nil {
		return req, "", ErrCodeUnavailable, err
	}
	if !fresh {
		return req, "", ErrCodeReplay, errors.New("LogoutRequest " + truncate(req.ID, 80) + " was already used")
	}
	var indexes []string
	for _, ch := range msg.root.ChildElements() {
		if ch.Tag == "SessionIndex" && strings.TrimSpace(ch.Text()) != "" {
			indexes = append(indexes, strings.TrimSpace(ch.Text()))
		}
	}
	revoked, failed := s.endIdPSessions(ctx, c, strings.TrimSpace(nameID.Value), indexes, now)
	s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "via": via,
		"subject": truncate(nameID.Value, 256), "session_indexes": len(indexes), "revoked_sessions": revoked, "failed": failed})
	status = saml.StatusSuccess
	if failed > 0 {
		status = statusResponder
	}
	return req, status, "", nil
}

// endIdPSessions revokes the active sessions of a connection with the NameID (and one of the session indexes, when
// the IdP names them).
func (s *Service) endIdPSessions(ctx context.Context, c Connection, subject string, indexes []string, now time.Time) (revoked, failed int) {
	links, err := s.store.ListSSOSessions(ctx, c.ID, subject, "", now)
	if err != nil {
		s.log.Error("cannot list SSO sessions for single logout", "connection_id", c.ID, "err", err)
		return 0, 1
	}
	return s.revokeSSOSessions(ctx, links, func(l SSOSession) bool {
		return len(indexes) == 0 || l.SessionIndex == "" || slices.Contains(indexes, l.SessionIndex)
	}, now)
}

// decryptNameID decrypts the EncryptedID of a verified LogoutRequest with the SP key.
func (s *Service) decryptNameID(sp *saml.ServiceProvider, root *etree.Element) (*saml.NameID, error) {
	enc := root.FindElement("./EncryptedID")
	if enc == nil {
		return nil, errors.New("LogoutRequest has no NameID")
	}
	for _, m := range enc.FindElements(".//EncryptionMethod") {
		if alg := m.SelectAttrValue("Algorithm", ""); !samlEncAlgs[alg] {
			return nil, fmt.Errorf("encryption algorithm %q is not allowed", alg)
		}
	}
	data := enc.FindElement("./EncryptedData")
	if data == nil {
		return nil, errors.New("EncryptedID has no EncryptedData")
	}
	var key interface{} = sp.Key
	if ek := enc.FindElement("./EncryptedKey"); ek != nil {
		k, err := xmlenc.Decrypt(sp.Key, ek)
		if err != nil {
			return nil, fmt.Errorf("cannot decrypt the NameID key: %v", err)
		}
		key = k
	}
	plain, err := xmlenc.Decrypt(key, data)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt the NameID: %v", err)
	}
	if err := xrv.Validate(bytes.NewReader(plain)); err != nil {
		return nil, fmt.Errorf("decrypted NameID is not valid XML: %v", err)
	}
	var n saml.NameID
	if err := xml.Unmarshal(plain, &n); err != nil {
		return nil, fmt.Errorf("decrypted NameID: %v", err)
	}
	return &n, nil
}

// logoutResponse answers an IdP LogoutRequest through the IdP's SingleLogoutService (signed).
func (s *Service) logoutResponse(sp *saml.ServiceProvider, inResponseTo, status, relay string) (SLOOutcome, error) {
	_, loc, binding := idpSLOEndpoint(sp.IDPMetadata)
	if loc == "" {
		// Nothing to answer to: the sessions are ended; show the login page.
		return SLOOutcome{Redirect: "/login?sso_logout=ok"}, nil
	}
	id, err := newMessageID()
	if err != nil {
		return SLOOutcome{}, err
	}
	resp := saml.LogoutResponse{ID: id, InResponseTo: inResponseTo, Version: "2.0", IssueInstant: s.now().UTC(), Destination: loc,
		Issuer: &saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: sp.EntityID},
		Status: saml.Status{StatusCode: saml.StatusCode{Value: status}}}
	if binding == saml.HTTPRedirectBinding {
		u, err := redirectBindingURL(sp.Key, loc, "SAMLResponse", resp.Element(), relay)
		if err != nil {
			return SLOOutcome{}, err
		}
		return SLOOutcome{Redirect: u, External: true}, nil
	}
	sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	if err := sp.SignLogoutResponse(&resp); err != nil {
		return SLOOutcome{}, err
	}
	b, err := elementBytes(resp.Element())
	if err != nil {
		return SLOOutcome{}, err
	}
	fields := map[string]string{"SAMLResponse": base64.StdEncoding.EncodeToString(b)}
	if relay != "" {
		fields["RelayState"] = relay
	}
	page, csp, err := PostForm(loc, fields)
	if err != nil {
		return SLOOutcome{}, err
	}
	return SLOOutcome{HTML: page, CSP: csp}, nil
}

// sloResponse completes an SP-initiated SAML logout. The openlog sessions already ended; the result only tells the
// user whether the IdP session ended too.
func (s *Service) sloResponse(ctx context.Context, c Connection, sp *saml.ServiceProvider, in SLOInput, meta auth.ClientMeta) SLOOutcome {
	partial := func(target string, err error) SLOOutcome {
		s.log.Warn("SAML logout response not accepted", "connection_id", c.ID, "err", err)
		s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout_failed", "sso_connection", c.ID, map[string]any{"reason": ErrCodeInvalidResponse,
			"error": truncate(err.Error(), 300)})
		return SLOOutcome{Redirect: withQuery(target, "sso_logout=partial")}
	}
	msg, err := s.readLogoutMessage(sp, in, "SAMLResponse", "LogoutResponse")
	if err != nil {
		return partial("/login", err)
	}
	if msg.relay == "" || len(msg.relay) > 256 {
		return partial("/login", errors.New("RelayState is missing"))
	}
	st, err := s.store.ConsumeLoginState(ctx, hashToken(msg.relay), s.now())
	if err != nil || st.Purpose != PurposeLogout || st.ConnectionID != c.ID {
		if err == nil {
			err = errors.New("RelayState does not belong to a logout of this connection")
		}
		return partial("/login", err)
	}
	target := validRedirect(st.RedirectTo)
	var resp saml.LogoutResponse
	if err := xml.Unmarshal(msg.doc, &resp); err != nil {
		return partial(target, err)
	}
	if err := s.checkLogoutHeader(sp, resp.Issuer, resp.Destination, resp.IssueInstant, false); err != nil {
		return partial(target, err)
	}
	if resp.InResponseTo == "" || resp.InResponseTo != st.SAMLRequestID {
		return partial(target, errors.New("InResponseTo does not match the LogoutRequest"))
	}
	if resp.Status.StatusCode.Value != saml.StatusSuccess {
		return partial(target, fmt.Errorf("the IdP answered %s", truncate(resp.Status.StatusCode.Value, 120)))
	}
	s.audit(ctx, c.OrgID, st.ActorUserID, "", meta.IP, "sso.logout_complete", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol})
	return SLOOutcome{Redirect: withQuery(target, "sso_logout=ok")}
}

// ---- OIDC RP-initiated logout ----

func (s *Service) oidcLogoutStart(ctx context.Context, c Connection, link SSOSession, target, actor string, res *LogoutResult) error {
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return err
	}
	if oc.meta.EndSessionURL == "" {
		return errors.New("the provider has no end_session_endpoint")
	}
	u, err := url.Parse(oc.meta.EndSessionURL)
	if err != nil || checkIdPURL(u, s.cfg.AllowPrivateNetworks) != nil || u.Fragment != "" {
		return errors.New("invalid end_session_endpoint")
	}
	state, err := s.newLogoutState(ctx, c, "", target, actor)
	if err != nil {
		return err
	}
	q := u.Query()
	if len(link.IDTokenEnc) > 0 {
		if tok, err := s.cfg.SecretBox.Open(link.IDTokenEnc, idTokenAAD(link.SessionID)); err == nil {
			q.Set("id_token_hint", string(tok))
		}
	}
	q.Set("client_id", c.OIDC.ClientID)
	q.Set("post_logout_redirect_uri", s.OIDCPostLogoutRedirectURL())
	q.Set("state", state)
	u.RawQuery = q.Encode()
	res.RedirectURL = u.String()
	return nil
}

func idTokenAAD(sessionID string) string { return "oidc-id-token:" + sessionID }

// OIDCLogoutCallback is the post_logout_redirect_uri (GET /api/v1/sso/oidc/logout/callback): it sends the browser
// to the UI path stored with the logout state.
func (s *Service) OIDCLogoutCallback(ctx context.Context, state string) Outcome {
	if state == "" || len(state) > 256 {
		return Outcome{Redirect: "/login"}
	}
	st, err := s.store.ConsumeLoginState(ctx, hashToken(state), s.now())
	if err != nil || st.Purpose != PurposeLogout {
		return Outcome{Redirect: "/login"}
	}
	return Outcome{Redirect: withQuery(validRedirect(st.RedirectTo), "sso_logout=ok")}
}
