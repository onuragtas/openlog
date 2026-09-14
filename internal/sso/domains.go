package sso

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

const (
	// DNSRecordPrefix is prepended to the domain for the TXT record name.
	DNSRecordPrefix = "_openlog-verification."
	// DNSValuePrefix precedes the token in the TXT record value.
	DNSValuePrefix = "openlog-domain-verification="
	// PrefixDomainToken is the visible prefix of domain verification e-mail tokens.
	PrefixDomainToken = "oldv_"

	maxDomainsPerOrg   = 20
	domainEmailTTL     = 24 * time.Hour
	domainMailsPerHour = 5
)

// DomainEmailLocalParts are the addresses a verification e-mail can be sent to (administrative mailboxes, as
// used by certificate authorities).
var DomainEmailLocalParts = []string{"admin", "administrator", "hostmaster", "postmaster", "webmaster"}

// normalizeDomain validates a DNS domain name (at least two labels, LDH labels, IDNs in punycode).
func normalizeDomain(v string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
	d = strings.TrimPrefix(d, "@")
	if len(d) < 3 || len(d) > 253 {
		return "", invalid("domain must be a DNS name such as example.com")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", invalid("domain must have at least two labels, e.g. example.com")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", invalid("domain must be a DNS name such as example.com")
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", invalid("domain must be a DNS name such as example.com (internationalized names in punycode)")
			}
		}
	}
	if tld := labels[len(labels)-1]; strings.Trim(tld, "0123456789") == "" {
		return "", invalid("domain must not be an IP address")
	}
	return d, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func requireVerified(p *auth.Principal) error {
	if !p.EmailVerified {
		return denied("confirm your e-mail address first")
	}
	return nil
}

// AddDomain claims an e-mail domain for the organization (admin+). It must be verified before SSO sign-ins
// from it are accepted.
func (s *Service) AddDomain(ctx context.Context, p *auth.Principal, domain string, meta auth.ClientMeta) (Domain, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Domain{}, err
	}
	if err := requireVerified(p); err != nil {
		return Domain{}, err
	}
	d, err := normalizeDomain(domain)
	if err != nil {
		return Domain{}, err
	}
	existing, err := s.store.ListDomains(ctx, p.OrgID)
	if err != nil {
		return Domain{}, s.fail(err)
	}
	if len(existing) >= maxDomainsPerOrg {
		return Domain{}, precondition("an organization can claim at most %d domains", maxDomainsPerOrg)
	}
	token, err := randomHex(20)
	if err != nil {
		return Domain{}, err
	}
	rec := Domain{OrgID: p.OrgID, Domain: d, DNSToken: token, CreatedBy: p.UserID, CreatedAt: s.now()}
	if err := s.store.CreateDomain(ctx, &rec); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return Domain{}, &auth.Error{Code: auth.CodeAlreadyExists, Message: "the organization already claimed this domain"}
		}
		return Domain{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.domain.add", "domain", rec.ID, map[string]any{"domain": d})
	return rec, nil
}

// ListDomains lists the organization's domains (admin+).
func (s *Service) ListDomains(ctx context.Context, p *auth.Principal) ([]Domain, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return nil, err
	}
	ds, err := s.store.ListDomains(ctx, p.OrgID)
	if err != nil {
		return nil, s.fail(err)
	}
	return ds, nil
}

func (s *Service) getDomain(ctx context.Context, p *auth.Principal, id string) (Domain, error) {
	d, err := s.store.GetDomain(ctx, p.OrgID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return Domain{}, notFound("domain not found")
	}
	if err != nil {
		return Domain{}, s.fail(err)
	}
	return d, nil
}

// DeleteDomain removes a claim (admin+). The last verified domain of an enforcing connection cannot be removed.
func (s *Service) DeleteDomain(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) error {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return err
	}
	d, err := s.getDomain(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.keepEnforcedDomain(ctx, p.OrgID, d, "removing"); err != nil {
		return err
	}
	if _, err := s.store.DeleteDomain(ctx, p.OrgID, id); err != nil {
		return s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.domain.remove", "domain", d.ID, map[string]any{"domain": d.Domain, "verified": d.VerifiedAt != nil})
	return nil
}

// keepEnforcedDomain refuses to take verified domain d away from an enforcing connection when it is the last
// verified domain routed to it.
func (s *Service) keepEnforcedDomain(ctx context.Context, orgID string, d Domain, verb string) error {
	if d.VerifiedAt == nil {
		return nil
	}
	conns, err := s.store.ListConnections(ctx, orgID)
	if err != nil {
		return s.fail(err)
	}
	ds, err := s.store.ListDomains(ctx, orgID)
	if err != nil {
		return s.fail(err)
	}
	for _, c := range conns {
		if !c.Enforce || !routesTo(d, conns, c.ID) {
			continue
		}
		verified := 0
		for _, x := range ds {
			if x.VerifiedAt != nil && routesTo(x, conns, c.ID) {
				verified++
			}
		}
		if verified <= 1 {
			return precondition("turn off single sign-on enforcement of %q before %s its last verified domain", connectionLabel(c), verb)
		}
	}
	return nil
}

func connectionLabel(c Connection) string {
	if c.Name != "" {
		return c.Name
	}
	return string(c.Protocol) + " " + c.ID
}

// AssignDomain routes a domain's sign-ins to a connection of the organization; connectionID "" = the default
// connection (admin+).
func (s *Service) AssignDomain(ctx context.Context, p *auth.Principal, id, connectionID string, meta auth.ClientMeta) (Domain, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Domain{}, err
	}
	d, err := s.getDomain(ctx, p, id)
	if err != nil {
		return Domain{}, err
	}
	if d.ConnectionID == connectionID {
		return d, nil
	}
	if connectionID != "" {
		if _, err := s.GetConnection(ctx, p, connectionID); err != nil {
			return Domain{}, err
		}
	}
	conns, err := s.store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return Domain{}, s.fail(err)
	}
	moved := d
	moved.ConnectionID = connectionID
	for _, c := range conns {
		changes := routesTo(d, conns, c.ID) != routesTo(moved, conns, c.ID)
		// Moving a verified domain into or out of an enforcing connection changes who must use SSO: owner only,
		// like the enforcement setting itself.
		if c.Enforce && changes && d.VerifiedAt != nil && p.Role != auth.RoleOwner {
			return Domain{}, denied("only owners can move a verified domain into or out of a connection that enforces single sign-on")
		}
		if c.Enforce && routesTo(d, conns, c.ID) && !routesTo(moved, conns, c.ID) {
			if err := s.keepEnforcedDomain(ctx, p.OrgID, d, "moving"); err != nil {
				return Domain{}, err
			}
		}
	}
	out, err := s.store.SetDomainConnection(ctx, p.OrgID, id, connectionID)
	if errors.Is(err, auth.ErrNotFound) {
		return Domain{}, notFound("domain or connection not found")
	}
	if err != nil {
		return Domain{}, s.fail(err)
	}
	s.auth.Audit(ctx, p, meta, "sso.domain.assign", "domain", d.ID, map[string]any{"domain": d.Domain, "from": d.ConnectionID, "to": connectionID})
	return out, nil
}

// DNSRecord returns the TXT record name and value that verifies d.
func DNSRecord(d Domain) (name, value string) {
	return DNSRecordPrefix + d.Domain, DNSValuePrefix + d.DNSToken
}

// VerifyDomainDNS looks up the TXT record now (admin+).
func (s *Service) VerifyDomainDNS(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) (Domain, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Domain{}, err
	}
	d, err := s.getDomain(ctx, p, id)
	if err != nil {
		return Domain{}, err
	}
	if d.VerifiedAt != nil {
		return d, nil
	}
	if err := s.allowIP(ctx, "domain-dns-"+p.OrgID, "", 60); err != nil {
		return Domain{}, err
	}
	name, want := DNSRecord(d)
	lctx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
	defer cancel()
	values, lerr := s.cfg.Resolver.LookupTXT(lctx, name)
	now := s.now()
	if err := s.store.SetDomainChecked(ctx, d.ID, now); err != nil {
		s.log.Warn("cannot record domain check", "err", err)
	}
	if lerr != nil || !slices.Contains(values, want) {
		return Domain{}, precondition("TXT record %s with the value %s was not found (DNS changes can take a while)", name, want)
	}
	return s.markVerified(ctx, p, d, "dns_txt", now, meta)
}

func (s *Service) markVerified(ctx context.Context, p *auth.Principal, d Domain, method string, now time.Time, meta auth.ClientMeta) (Domain, error) {
	v, err := s.store.MarkDomainVerified(ctx, d.OrgID, d.ID, method, now)
	if errors.Is(err, auth.ErrAlreadyExists) {
		return Domain{}, &auth.Error{Code: auth.CodeAlreadyExists, Message: "another organization already verified this domain"}
	}
	if err != nil {
		return Domain{}, s.fail(err)
	}
	details := map[string]any{"domain": d.Domain, "method": method}
	if p != nil {
		s.auth.Audit(ctx, p, meta, "sso.domain.verify", "domain", d.ID, details)
	} else {
		s.audit(ctx, d.OrgID, "", v.EmailAddress, meta.IP, "sso.domain.verify", "domain", d.ID, details)
	}
	return v, nil
}

// SendDomainVerificationEmail sends a verification link to an administrative mailbox of the domain (admin+).
func (s *Service) SendDomainVerificationEmail(ctx context.Context, p *auth.Principal, id, localPart string, meta auth.ClientMeta) (Domain, error) {
	if err := s.gate(p, auth.RoleAdmin); err != nil {
		return Domain{}, err
	}
	if err := requireVerified(p); err != nil {
		return Domain{}, err
	}
	if s.cfg.Mailer == nil || s.cfg.PublicURL == "" {
		return Domain{}, precondition("e-mail verification needs OPENLOG_SMTP_HOST and OPENLOG_PUBLIC_URL; use the DNS TXT record")
	}
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if !slices.Contains(DomainEmailLocalParts, localPart) {
		return Domain{}, invalid("the verification e-mail can be sent to %s at the domain", strings.Join(DomainEmailLocalParts, ", "))
	}
	d, err := s.getDomain(ctx, p, id)
	if err != nil {
		return Domain{}, err
	}
	if d.VerifiedAt != nil {
		return d, nil
	}
	if err := s.allowIP(ctx, "domain-mail-"+d.ID, "", domainMailsPerHour); err != nil {
		return Domain{}, err
	}
	token, err := randomHex(24)
	if err != nil {
		return Domain{}, err
	}
	token = PrefixDomainToken + token
	address := localPart + "@" + d.Domain
	expires := s.now().Add(domainEmailTTL)
	if err := s.store.SetDomainEmailToken(ctx, p.OrgID, d.ID, hashToken(token), address, expires); err != nil {
		return Domain{}, s.fail(err)
	}
	org, err := s.users.GetOrganization(ctx, p.OrgID)
	if err != nil {
		return Domain{}, s.fail(err)
	}
	link := s.cfg.PublicURL + "/sso/verify-domain#token=" + token
	// The organization's default language, else the requesting admin's browser language (internal/mail/templates, D-095).
	msg := mailtemplates.DomainVerification(mailtemplates.Resolve(org.Locale, meta.Locale), mailtemplates.DomainVerificationData{Requester: p.Email, Domain: d.Domain,
		OrgName: org.Name, Link: link})
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := s.cfg.Mailer.Send(mctx, auth.Mail{To: address, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}); err != nil {
		s.log.Warn("cannot send domain verification e-mail", "domain", d.Domain, "err", err)
		return Domain{}, &auth.Error{Code: auth.CodeUnavailable, Message: "the verification e-mail could not be sent; try again later"}
	}
	s.auth.Audit(ctx, p, meta, "sso.domain.verification_email", "domain", d.ID, map[string]any{"domain": d.Domain, "to": address})
	d.EmailAddress, d.EmailExpiresAt = address, &expires
	return d, nil
}

// ConsumeDomainEmailToken verifies a domain from the e-mailed link (public).
func (s *Service) ConsumeDomainEmailToken(ctx context.Context, token string, meta auth.ClientMeta) (Domain, error) {
	if err := s.allowIP(ctx, "domain-verify", meta.IP, 30); err != nil {
		return Domain{}, err
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, PrefixDomainToken) || len(token) > 128 {
		return Domain{}, invalid("the verification link is invalid or expired")
	}
	d, err := s.store.ConsumeDomainEmailToken(ctx, hashToken(token), s.now())
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return Domain{}, invalid("the verification link is invalid or expired")
	case errors.Is(err, auth.ErrAlreadyExists):
		return Domain{}, &auth.Error{Code: auth.CodeAlreadyExists, Message: "another organization already verified this domain"}
	case err != nil:
		return Domain{}, s.fail(err)
	}
	s.audit(ctx, d.OrgID, "", d.EmailAddress, meta.IP, "sso.domain.verify", "domain", d.ID, map[string]any{"domain": d.Domain, "method": "email"})
	return d, nil
}
