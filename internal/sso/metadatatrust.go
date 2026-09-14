package sso

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/onuragtas/openlog/internal/auth"
)

// Trust of IdP metadata fetched from a URL (D-098). Metadata is the root of trust of a SAML connection (signing
// certificates, sign-in and logout endpoints), so the background refresh must not accept whatever the metadata URL
// serves:
//
//   - With pinned metadata signing certificates the document must carry one enveloped XML signature on its root
//     (EntityDescriptor or EntitiesDescriptor, SHA-256+, one reference to the root ID) by a pinned certificate. Changes
//     inside verified metadata (IdP certificate rollover, new endpoints) apply automatically. Metadata signed by
//     another certificate is kept as a pending change until an administrator confirms it (the new certificate is
//     pinned then); an invalid or missing signature fails the refresh.
//   - Without pinned certificates (the administrator allowed unsigned metadata, or the connection predates D-098)
//     changed signing certificates or endpoints are never applied by the refresh: they wait for confirmation.
//
// Saving a connection with a metadata URL requires a trust decision: a pinned certificate, signed metadata (its
// signer is pinned — the administrator saw its fingerprint) or explicitly allowed unsigned metadata.

const (
	nsMetadata = "urn:oasis:names:tc:SAML:2.0:metadata"
	// maxMetadataSigningCerts bounds the pinned metadata signing certificates of a connection (rollover overlap).
	maxMetadataSigningCerts = 5
)

// CertificateFingerprint returns the upper-case hex SHA-256 fingerprint of a certificate.
func CertificateFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// CertificateFingerprints returns the fingerprints of PEM certificates (invalid entries are skipped).
func CertificateFingerprints(pems []string) []string {
	out := []string{}
	for _, p := range pems {
		if cs, err := parseCertificatesPEM(p); err == nil {
			for _, c := range cs {
				out = append(out, CertificateFingerprint(c))
			}
		}
	}
	return out
}

func certPEM(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

// parseCertificatesPEM parses one or more PEM certificates; a bare base64 DER certificate is accepted too.
func parseCertificatesPEM(text string) ([]*x509.Certificate, error) {
	rest := bytes.TrimSpace([]byte(text))
	if len(rest) == 0 {
		return nil, errors.New("no certificate")
	}
	if !bytes.HasPrefix(rest, []byte("-----")) {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(rest)), ""))
		if err != nil {
			return nil, errors.New("not a PEM or base64 certificate")
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate: %v", err)
		}
		return []*x509.Certificate{c}, nil
	}
	var out []*x509.Certificate
	for len(rest) > 0 {
		b, next := pem.Decode(rest)
		if b == nil {
			return nil, errors.New("invalid PEM data")
		}
		if b.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("unexpected PEM block %q", b.Type)
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate: %v", err)
		}
		out = append(out, c)
		rest = bytes.TrimSpace(next)
	}
	if len(out) > maxMetadataSigningCerts {
		return nil, fmt.Errorf("at most %d certificates", maxMetadataSigningCerts)
	}
	return out, nil
}

func parseStoredCerts(pems []string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for _, p := range pems {
		cs, err := parseCertificatesPEM(p)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

func certPEMs(cs []*x509.Certificate) []string {
	if len(cs) == 0 {
		return nil
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, certPEM(c))
	}
	return out
}

// metadataRoot parses metadata (already checked by idpMetadataInfo: size, round trip) and returns its root: no DTD,
// an EntityDescriptor or EntitiesDescriptor in the SAML metadata namespace.
func metadataRoot(data []byte) (*etree.Element, error) {
	d := etree.NewDocument()
	if err := d.ReadFromBytes(data); err != nil {
		return nil, fmt.Errorf("IdP metadata is not valid XML: %v", err)
	}
	for _, t := range d.Child {
		if _, ok := t.(*etree.Directive); ok {
			return nil, errors.New("DTDs are not allowed in IdP metadata")
		}
	}
	root := d.Root()
	if root == nil || (root.Tag != "EntityDescriptor" && root.Tag != "EntitiesDescriptor") || root.NamespaceURI() != nsMetadata {
		return nil, errors.New("the IdP metadata root is not an EntityDescriptor or EntitiesDescriptor")
	}
	return root, nil
}

// rootSigned reports whether the metadata root carries an XML signature.
func rootSigned(root *etree.Element) bool {
	for _, ch := range root.ChildElements() {
		if ch.Tag == "Signature" && ch.NamespaceURI() == nsDSig {
			return true
		}
	}
	return false
}

// keyInfoCert returns the first certificate of the root signature's KeyInfo (nil: none).
func keyInfoCert(root *etree.Element) *x509.Certificate {
	for _, sig := range root.ChildElements() {
		if sig.Tag != "Signature" || sig.NamespaceURI() != nsDSig {
			continue
		}
		el := sig.FindElement("./KeyInfo/X509Data/X509Certificate")
		if el == nil {
			return nil
		}
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(el.Text()), ""))
		if err != nil {
			return nil
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil
		}
		return c
	}
	return nil
}

// verifyMetadataSignature verifies the enveloped signature of the metadata root with certs at now: exactly one
// signature on the root referencing the root's ID (so the signed element is the whole document the metadata is read
// from — no wrapping), allowlisted algorithms, a certificate of certs (valid at now).
func verifyMetadataSignature(root *etree.Element, certs []*x509.Certificate, now time.Time) error {
	if len(certs) == 0 {
		return errors.New("no metadata signing certificate")
	}
	id := root.SelectAttrValue("ID", "")
	if id == "" {
		return errors.New("the signed IdP metadata has no ID attribute")
	}
	if ref := root.FindElement("./Signature/SignedInfo/Reference"); ref == nil || ref.SelectAttrValue("URI", "") != "#"+id {
		return errors.New("the metadata signature does not reference the metadata root")
	}
	store := dsig.MemoryX509CertificateStore{Roots: certs}
	vc := dsig.NewDefaultValidationContext(&store)
	vc.IdAttribute = "ID"
	vc.Clock = dsig.NewFakeClockAt(now)
	return (strictVerifier{}).VerifySignature(vc, root)
}

// metadataTrustOnSave checks the metadata fetched from a URL when a connection is saved and returns the certificates
// to pin: pinned (entered or kept), the signer of signed metadata, or none when unsigned metadata is allowed.
func metadataTrustOnSave(data []byte, pinned []*x509.Certificate, allowUnsigned bool, now time.Time) ([]*x509.Certificate, error) {
	root, err := metadataRoot(data)
	if err != nil {
		return nil, err
	}
	signed := rootSigned(root)
	switch {
	case len(pinned) > 0:
		if !signed {
			return nil, errors.New("the IdP metadata is not signed, but a metadata signing certificate is set")
		}
		if err := verifyMetadataSignature(root, pinned, now); err != nil {
			return nil, fmt.Errorf("the IdP metadata signature does not verify with the metadata signing certificate: %v", err)
		}
		return pinned, nil
	case signed:
		kc := keyInfoCert(root)
		if kc == nil {
			return nil, errors.New("the IdP metadata signature names no certificate; enter the metadata signing certificate")
		}
		if err := verifyMetadataSignature(root, []*x509.Certificate{kc}, now); err != nil {
			return nil, fmt.Errorf("the IdP metadata signature is not valid: %v", err)
		}
		return []*x509.Certificate{kc}, nil
	case !allowUnsigned:
		return nil, errors.New("the IdP metadata from idp_metadata_url is not signed: enter its metadata signing certificate, " +
			"or allow unsigned metadata (changed IdP certificates then wait for your confirmation)")
	}
	return nil, nil
}

// checkMetadataTimes refuses expired metadata or an expired IdP signing certificate.
func checkMetadataTimes(md *saml.EntityDescriptor, info SAMLConfig, now time.Time) error {
	if !md.ValidUntil.IsZero() && !md.ValidUntil.After(now) {
		return fmt.Errorf("the IdP metadata expired at %s", md.ValidUntil.UTC().Format(time.RFC3339))
	}
	if t, err := time.Parse(time.RFC3339, info.IdPCertNotAfter); err == nil && !t.After(now) {
		return fmt.Errorf("the IdP signing certificate expired at %s", info.IdPCertNotAfter)
	}
	return nil
}

func sortedCopy(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}

// metadataChanged reports whether refreshed metadata changes signing certificates or endpoints.
func metadataChanged(info SAMLConfig, cur *SAMLConfig) bool {
	return !slices.Equal(sortedCopy(info.IdPCertificates), sortedCopy(cur.IdPCertificates)) || info.IdPSSOURL != cur.IdPSSOURL ||
		info.IdPSLOURL != cur.IdPSLOURL || info.IdPSLOBinding != cur.IdPSLOBinding
}

func newPendingMetadata(reason string, info SAMLConfig, signer string, now time.Time) *PendingMetadata {
	b, _ := json.Marshal([]any{reason, info.IdPEntityID, sortedCopy(info.IdPCertificates), info.IdPSSOURL, info.IdPSLOURL, info.IdPSLOBinding, signer})
	sum := sha256.Sum256(b)
	return &PendingMetadata{Digest: hex.EncodeToString(sum[:]), Reason: reason, DetectedAt: now.UTC(), IdPCertificates: info.IdPCertificates,
		IdPSSOURL: info.IdPSSOURL, IdPSLOURL: info.IdPSLOURL, SignerCertificate: signer}
}

// refreshedMetadata is IdP metadata fetched from a connection's metadata URL and checked against its trust settings.
type refreshedMetadata struct {
	md      *saml.EntityDescriptor
	info    SAMLConfig
	signer  *x509.Certificate // pending signer_changed: the new signing certificate
	pending *PendingMetadata  // set: the change needs confirmation (the error says why)
}

// checkRefreshedMetadata validates fetched metadata of c. A nil result is a failed refresh; a result with pending is a
// change that waits for an administrator (returned together with the reason as error).
func (s *Service) checkRefreshedMetadata(c Connection, data []byte, now time.Time) (*refreshedMetadata, error) {
	md, info, err := idpMetadataInfo(data, now)
	if err != nil {
		return nil, err
	}
	if info.IdPEntityID != c.SAML.IdPEntityID {
		return nil, fmt.Errorf("the IdP entity ID changed to %q; save the connection to accept it", truncate(info.IdPEntityID, 200))
	}
	if err := checkMetadataTimes(md, info, now); err != nil {
		return nil, err
	}
	root, err := metadataRoot(data)
	if err != nil {
		return nil, err
	}
	out := &refreshedMetadata{md: md, info: info}
	pinned, err := parseStoredCerts(c.SAML.MetadataSigningCerts)
	if err != nil {
		return nil, fmt.Errorf("pinned metadata signing certificate: %v", err)
	}
	if len(pinned) == 0 {
		if metadataChanged(info, c.SAML) {
			out.pending = newPendingMetadata(PendingMetadataChanged, info, "", now)
			return out, fmt.Errorf("the unsigned IdP metadata changed (signing certificates %s, sign-in URL %s, logout URL %s); "+
				"an administrator must confirm the change", strings.Join(info.IdPCertificates, ", "), truncate(info.IdPSSOURL, 200), truncate(info.IdPSLOURL, 200))
		}
		return out, nil
	}
	if !rootSigned(root) {
		return nil, errors.New("the IdP metadata is not signed, but a metadata signing certificate is pinned")
	}
	verr := verifyMetadataSignature(root, pinned, now)
	if verr == nil {
		return out, nil
	}
	if kc := keyInfoCert(root); kc != nil && !slices.ContainsFunc(pinned, kc.Equal) {
		if err := verifyMetadataSignature(root, []*x509.Certificate{kc}, now); err == nil {
			fp := CertificateFingerprint(kc)
			out.signer = kc
			out.pending = newPendingMetadata(PendingMetadataSignerChanged, info, fp, now)
			return out, fmt.Errorf("the IdP metadata is signed by the certificate %s, which is not pinned; an administrator must confirm it", fp)
		}
	}
	return nil, fmt.Errorf("the IdP metadata signature is not valid: %v", verr)
}

// recordPendingMetadata stores a pending change on the connection (once per change).
func (s *Service) recordPendingMetadata(ctx context.Context, c Connection, p *PendingMetadata) {
	if cur := c.SAML.PendingMetadata; cur != nil && cur.Digest == p.Digest {
		return
	}
	cfg := *c.SAML
	cfg.PendingMetadata = p
	switch err := s.store.UpdateSAMLMetadata(ctx, c.ID, c.ConfigVersion, cfg); {
	case err == nil:
		s.audit(ctx, c.OrgID, "", "sso-refresh", "", "sso.connection.metadata_pending", "sso_connection", c.ID, map[string]any{
			"reason": p.Reason, "idp_certificates_before": c.SAML.IdPCertificates, "idp_certificates_after": p.IdPCertificates,
			"signer_certificate": p.SignerCertificate, "idp_sso_url": p.IdPSSOURL, "idp_slo_url": p.IdPSLOURL})
	case !errors.Is(err, auth.ErrNotFound):
		s.log.Warn("cannot record a pending IdP metadata change", "connection_id", c.ID, "err", err)
	}
}

// applyRefreshedMetadata stores verified metadata on the connection without changing its configuration version;
// pinSigner pins the new signing certificate of a confirmed signer change. auth.ErrNotFound: saved meanwhile.
func (s *Service) applyRefreshedMetadata(ctx context.Context, c Connection, data []byte, rm *refreshedMetadata, pinSigner bool) error {
	if string(data) == c.SAML.IdPMetadataXML && c.SAML.PendingMetadata == nil && !pinSigner {
		return nil
	}
	cfg := *c.SAML
	info := rm.info
	cfg.IdPMetadataXML, cfg.IdPSSOURL, cfg.IdPSLOURL, cfg.IdPSLOBinding = string(data), info.IdPSSOURL, info.IdPSLOURL, info.IdPSLOBinding
	cfg.IdPCertificates, cfg.IdPCertNotAfter = info.IdPCertificates, info.IdPCertNotAfter
	cfg.PendingMetadata = nil
	if pinSigner && rm.signer != nil {
		cfg.MetadataSigningCerts = []string{certPEM(rm.signer)}
	}
	if err := s.store.UpdateSAMLMetadata(ctx, c.ID, c.ConfigVersion, cfg); err != nil {
		return err
	}
	if !pinSigner && (!slices.Equal(cfg.IdPCertificates, c.SAML.IdPCertificates) || cfg.IdPSSOURL != c.SAML.IdPSSOURL || cfg.IdPSLOURL != c.SAML.IdPSLOURL) {
		s.audit(ctx, c.OrgID, "", "sso-refresh", "", "sso.connection.metadata_refresh", "sso_connection", c.ID, map[string]any{
			"idp_certificates_before": c.SAML.IdPCertificates, "idp_certificates_after": cfg.IdPCertificates,
			"idp_sso_url": cfg.IdPSSOURL, "idp_slo_url": cfg.IdPSLOURL, "signed": len(cfg.MetadataSigningCerts) > 0})
	}
	return nil
}

// samlSchedule returns the refresh cache and the delay until the next refresh of metadata md.
func samlSchedule(md *saml.EntityDescriptor, now time.Time) (*IdPCache, time.Duration) {
	next := samlRefreshDefault
	if md.CacheDuration > 0 {
		next = min(max(md.CacheDuration/2, samlRefreshMin), samlRefreshMax)
	}
	cache := &IdPCache{FetchedAt: &now}
	if !md.ValidUntil.IsZero() {
		vu := md.ValidUntil.UTC()
		cache.SAMLValidUntil = &vu
		if half := vu.Sub(now) / 2; half < next {
			next = max(half, samlRefreshMin)
		}
	}
	return cache, next
}

// AcceptMetadata applies the pending IdP metadata change of a SAML connection after an administrator compared it
// (admin+; POST /api/v1/sso/connections/{id}/metadata/accept). The metadata is fetched again and must still carry
// the change named by digest; a new metadata signing certificate is pinned.
func (s *Service) AcceptMetadata(ctx context.Context, p *auth.Principal, id, digest string, meta auth.ClientMeta) (Connection, error) {
	c, err := s.GetConnection(ctx, p, id)
	if err != nil {
		return Connection{}, err
	}
	if c.Protocol != ProtocolSAML || c.SAML == nil || c.SAML.IdPMetadataURL == "" {
		return Connection{}, precondition("only SAML connections with an IdP metadata URL have metadata changes to confirm")
	}
	pend := c.SAML.PendingMetadata
	if pend == nil {
		return Connection{}, precondition("no IdP metadata change awaits confirmation")
	}
	if digest == "" || digest != pend.Digest {
		return Connection{}, precondition("the pending IdP metadata change is a different one; reload the connection and compare it again")
	}
	if err := s.allowIP(ctx, "refresh-"+c.ID, "", 30); err != nil {
		return Connection{}, err
	}
	u, err := ParseIdPURL(c.SAML.IdPMetadataURL, s.cfg.AllowPrivateNetworks)
	if err != nil {
		return Connection{}, precondition("idp_metadata_url: %v", err)
	}
	fctx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
	defer cancel()
	data, err := fetch(fctx, s.client, u.String())
	if err != nil {
		return Connection{}, precondition("cannot fetch the IdP metadata: %v", err)
	}
	now := s.now()
	rm, cerr := s.checkRefreshedMetadata(c, data, now)
	if rm == nil || rm.pending == nil || rm.pending.Digest != digest {
		if cerr == nil {
			cerr = errors.New("the IdP metadata no longer contains this change")
		}
		return Connection{}, precondition("cannot confirm the IdP metadata change: %v", cerr)
	}
	if err := s.applyRefreshedMetadata(ctx, c, data, rm, pend.Reason == PendingMetadataSignerChanged); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return Connection{}, precondition("the connection was saved meanwhile; review it again")
		}
		return Connection{}, s.fail(err)
	}
	cache, next := samlSchedule(rm.md, now)
	if err := s.store.RecordRefresh(ctx, c.ID, true, "", cache, now.Add(next), now); err != nil && !errors.Is(err, auth.ErrNotFound) {
		return Connection{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.connection.metadata_accept", "sso_connection", c.ID, map[string]any{"reason": pend.Reason,
		"idp_certificates_before": c.SAML.IdPCertificates, "idp_certificates_after": rm.info.IdPCertificates,
		"signer_certificate": pend.SignerCertificate, "idp_sso_url": rm.info.IdPSSOURL, "idp_slo_url": rm.info.IdPSLOURL})
	out, err := s.store.GetConnectionByID(ctx, c.ID)
	if err != nil {
		return Connection{}, s.fail(err)
	}
	return out, nil
}
