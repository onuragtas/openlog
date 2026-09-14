package sso

import "context"

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
