package synthetics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The network checks (D-140): a TCP connection, a TLS handshake read for what the certificate says, and a
// DNS resolution compared with what the check expects. They share the HTTP check's address guard — a check
// must not become a port scanner of the network openlog itself runs in — except the DNS check, which
// resolves a name without connecting anywhere.

// maxAnswerBytes bounds the stored evidence of a run (resolved records, the certificate subject).
const maxAnswerBytes = 512

// dialer returns the guarded dialer used by the tcp and tls checks.
func (c *Checker) dialer() *net.Dialer {
	d := &net.Dialer{}
	if !c.opts.AllowPrivateNetworks {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || !publicAddr(a) {
				return errBlockedAddress
			}
			return nil
		}
	}
	return d
}

// RootCAs returns the certificate pool a checker trusts: the system roots plus the PEM bundle at path
// (OPENLOG_SYNTHETICS_CA_FILE). An empty path means the system roots alone, which is the common case.
func RootCAs(path string) (*x509.CertPool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("no PEM certificate in " + path)
	}
	return pool, nil
}

// runTCP opens a connection and closes it again: the check is that the port accepts one, and how long the
// handshake took. Nothing is written to the socket — a protocol the check does not know must not be sent
// bytes it cannot interpret.
func (c *Checker) runTCP(ctx context.Context, chk Check, r Result) Result {
	start := time.Now()
	conn, err := c.dialer().DialContext(ctx, "tcp", chk.Target)
	end := time.Now()
	r.ConnectMs = ms(start, end)
	r.DurationMs = r.ConnectMs
	if err != nil {
		kind, msg := classifyError(ctx, err)
		return r.fail(kind, msg)
	}
	defer conn.Close()
	if addr := conn.RemoteAddr(); addr != nil {
		r.Answer = addr.String()
	}
	r.Success = true
	return r
}

// runTLS completes the handshake and then reads the certificate the server presented. A handshake that
// fails is an ErrorTLS; a handshake that succeeds while the certificate expires within the warning window is
// an ErrorCertificate, which is the whole point of the check: the alert has to arrive before the outage.
func (c *Checker) runTLS(ctx context.Context, chk Check, r Result) Result {
	host, _, err := net.SplitHostPort(chk.Target)
	if err != nil {
		return r.fail(ErrorRequest, "the target must be host:port")
	}
	start := time.Now()
	conn, err := c.dialer().DialContext(ctx, "tcp", chk.Target)
	connected := time.Now()
	r.ConnectMs = ms(start, connected)
	if err != nil {
		r.DurationMs = r.ConnectMs
		kind, msg := classifyError(ctx, err)
		return r.fail(kind, msg)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// The handshake is completed without the library's verification, and the chain is then verified here
	// (verifyChain below) — the run still fails on anything that does not verify. Doing it in this order is
	// what lets the check say "the certificate expired on 3 March" instead of one opaque handshake error,
	// which is the entire reason the check exists.
	serverName := hostForSNI(host)
	tc := tls.Client(conn, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}) //nolint:gosec // verified below
	err = tc.HandshakeContext(ctx)
	done := time.Now()
	r.TLSMs = ms(connected, done)
	r.DurationMs = ms(start, done)
	if err != nil {
		kind, msg := classifyError(ctx, err)
		if kind == ErrorRequest || kind == ErrorConnect {
			kind = ErrorTLS
		}
		return r.fail(kind, msg)
	}
	defer tc.Close()
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return r.fail(ErrorCertificate, "the server presented no certificate")
	}
	leaf := certs[0]
	r.CertExpiresAt = leaf.NotAfter.UTC()
	r.Answer = truncate(certificateAnswer(leaf), maxAnswerBytes)

	// Dates before trust: an expired certificate on an otherwise fine chain is the common failure, and
	// naming it is more useful than reporting that the chain does not verify (which it no longer does).
	now := r.At
	switch {
	case now.Before(leaf.NotBefore):
		return r.fail(ErrorCertificate, "the certificate is not valid before "+leaf.NotBefore.UTC().Format(time.RFC3339))
	case !now.Before(leaf.NotAfter):
		return r.fail(ErrorCertificate, "the certificate expired on "+leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if err := verifyChain(certs, serverName, c.opts.RootCAs, now); err != nil {
		return r.fail(ErrorTLS, err.Error())
	}
	if days := int(leaf.NotAfter.Sub(now).Hours() / 24); days < chk.TLSWarningDays {
		return r.fail(ErrorCertificate, fmt.Sprintf("the certificate expires in %d days, on %s",
			days, leaf.NotAfter.UTC().Format(time.RFC3339)))
	}
	r.Success = true
	return r
}

// verifyChain is the verification tls.Client would have done: the chain up to a trusted root (the system
// roots plus OPENLOG_SYNTHETICS_CA_FILE) and the name the check asked for. Certificates after the leaf are
// the intermediates the server sent.
func verifyChain(certs []*x509.Certificate, serverName string, roots *x509.CertPool, now time.Time) error {
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now}
	if _, err := certs[0].Verify(opts); err != nil {
		return fmt.Errorf("the certificate is not trusted: %w", err)
	}
	// An IP target verifies against the certificate's IP SANs, a name against its DNS SANs; VerifyHostname
	// does both, and a certificate for another name on the same address is a failure, not a pass.
	if err := certs[0].VerifyHostname(serverName); err != nil {
		return fmt.Errorf("the certificate is not valid for %s: %w", serverName, err)
	}
	return nil
}

// certificateAnswer describes the certificate a run saw: who it is for, who issued it and until when.
func certificateAnswer(leaf *x509.Certificate) string {
	subject := leaf.Subject.CommonName
	if subject == "" && len(leaf.DNSNames) > 0 {
		subject = leaf.DNSNames[0]
	}
	return fmt.Sprintf("%s, issued by %s, valid until %s", subject, leaf.Issuer.CommonName,
		leaf.NotAfter.UTC().Format(time.RFC3339))
}

// hostForSNI strips the trailing dot of an absolute name; an IP address is passed through, which makes the
// handshake verify the certificate's IP SANs.
func hostForSNI(host string) string { return strings.TrimSuffix(host, ".") }

// runDNS resolves the name and compares the answer with what the check expects. It connects to nothing, so
// the address guard does not apply: resolving an internal name tells the person their internal DNS works,
// which is exactly what the check is for.
func (c *Checker) runDNS(ctx context.Context, chk Check, r Result) Result {
	start := time.Now()
	records, err := resolve(ctx, chk.DNSRecordType, strings.TrimSuffix(chk.Target, "."))
	end := time.Now()
	r.DNSMs = ms(start, end)
	r.DurationMs = r.DNSMs
	if err != nil {
		var de *net.DNSError
		switch {
		case ctx.Err() != nil:
			return r.fail(ErrorTimeout, "the lookup timed out")
		case errors.As(err, &de) && de.IsNotFound:
			return r.fail(ErrorRecord, "the name has no "+chk.DNSRecordType+" record")
		}
		return r.fail(ErrorDNS, err.Error())
	}
	sort.Strings(records)
	r.Answer = truncate(strings.Join(records, ", "), maxAnswerBytes)
	if len(records) == 0 {
		return r.fail(ErrorRecord, "the name has no "+chk.DNSRecordType+" record")
	}
	if missing := missingRecords(chk.DNSExpected, records); len(missing) > 0 {
		return r.fail(ErrorRecord, fmt.Sprintf("expected %s, resolved %s",
			strings.Join(missing, ", "), strings.Join(records, ", ")))
	}
	r.Success = true
	return r
}

// missingRecords returns the expected answers the resolution did not produce. An expected answer matches a
// record case-insensitively and ignoring a trailing dot, because "example.com" and "example.com." are the
// same name and nobody types the dot.
func missingRecords(expected, got []string) []string {
	if len(expected) == 0 {
		return nil
	}
	have := make(map[string]bool, len(got))
	for _, g := range got {
		have[normalizeRecord(g)] = true
	}
	var missing []string
	for _, e := range expected {
		if !have[normalizeRecord(e)] {
			missing = append(missing, e)
		}
	}
	return missing
}

func normalizeRecord(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// resolve asks the system resolver for one record type and returns the answers as text.
func resolve(ctx context.Context, recordType, name string) ([]string, error) {
	var res net.Resolver
	switch recordType {
	case "A", "AAAA":
		network := "ip4"
		if recordType == "AAAA" {
			network = "ip6"
		}
		addrs, err := res.LookupIP(ctx, network, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, a.String())
		}
		return out, nil
	case "CNAME":
		cname, err := res.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		if normalizeRecord(cname) == normalizeRecord(name) {
			return nil, nil // the resolver answers the name itself when there is no CNAME
		}
		return []string{cname}, nil
	case "MX":
		mxs, err := res.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(mxs))
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
		return out, nil
	case "NS":
		nss, err := res.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(nss))
		for _, ns := range nss {
			out = append(out, ns.Host)
		}
		return out, nil
	case "TXT":
		return res.LookupTXT(ctx, name)
	}
	return nil, errors.New("unsupported record type " + recordType)
}
