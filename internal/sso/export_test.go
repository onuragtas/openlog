package sso

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
)

// RefreshRunsForTest returns the refresh counter of protocol and result (RefreshJob must have been called).
func RefreshRunsForTest(s *Service, protocol, result string) prometheus.Counter {
	return s.metrics.runs.WithLabelValues(protocol, result)
}

// RefreshFailingForTest returns the failing connections gauge.
func RefreshFailingForTest(s *Service) prometheus.Gauge { return s.metrics.failing }

// VerifyIDTokenForTest validates raw like the OIDC callback does (discovery, JWKS, claims, nonce).
func (s *Service) VerifyIDTokenForTest(ctx context.Context, c Connection, raw, nonce string) error {
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return err
	}
	_, err = s.verifyIDToken(s.clientContext(ctx), oc, c.OIDC.ClientID, raw, nonce)
	return err
}

// DiscoverOIDCForTest loads the provider of c.
func (s *Service) DiscoverOIDCForTest(ctx context.Context, c Connection) error {
	_, err := s.oidcClient(ctx, c)
	return err
}

// SAMLAssertionForTest validates a decoded SAMLResponse like the ACS does (without the replay cache).
func (s *Service) SAMLAssertionForTest(c Connection, doc []byte, possibleIDs []string) (Identity, string, error) {
	a, err := s.samlAssertion(c, doc, possibleIDs)
	if err != nil {
		return Identity{}, "", err
	}
	return samlIdentity(c, a), a.ID, nil
}
