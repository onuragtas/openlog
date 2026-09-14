package sso

import (
	"context"
	"crypto"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

// oidcAlgs is the ID token signature allowlist (asymmetric only: no "none", no HMAC with the client secret).
var oidcAlgs = []string{oidc.RS256, oidc.RS384, oidc.RS512, oidc.PS256, oidc.PS384, oidc.PS512,
	oidc.ES256, oidc.ES384, oidc.ES512, oidc.EdDSA}

// providerTTL is how long a discovery document (and its JWKS cache) is reused. Unknown key ids refetch the JWKS
// immediately (key rotation).
const providerTTL = time.Hour

type oidcClient struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	algs     []string
	meta     oidcMetadata
	created  time.Time
}

type oidcMetadata struct {
	Issuer              string   `json:"issuer"`
	AuthURL             string   `json:"authorization_endpoint"`
	TokenURL            string   `json:"token_endpoint"`
	JWKSURL             string   `json:"jwks_uri"`
	UserInfoURL         string   `json:"userinfo_endpoint"`
	EndSessionURL       string   `json:"end_session_endpoint"`
	Algs                []string `json:"id_token_signing_alg_values_supported"`
	CodeChallengeMethod []string `json:"code_challenge_methods_supported"`
}

func (s *Service) clientContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.client)
}

// oidcClient returns the cached provider of c (discovery and verifier), refreshing it hourly or when the
// connection settings change.
func (s *Service) oidcClient(ctx context.Context, c Connection) (*oidcClient, error) {
	if c.OIDC == nil {
		return nil, errors.New("not an OIDC connection")
	}
	key := c.ID + "|" + strconv.Itoa(c.ConfigVersion) + "|" + c.OIDC.Issuer + "|" + c.OIDC.ClientID
	now := s.now()
	s.mu.Lock()
	oc := s.providers[key]
	s.mu.Unlock()
	if oc != nil && now.Sub(oc.created) < providerTTL {
		return oc, nil
	}
	if _, err := ParseIdPURL(c.OIDC.Issuer, s.cfg.AllowPrivateNetworks); err != nil {
		return nil, fmt.Errorf("issuer: %w", err)
	}
	// The background refresh (refresh.go) stores discovery and JWKS; a fresh copy avoids the round trips on the
	// sign-in path of every pod. Unknown key ids still fall back to the provider's JWKS.
	provider, meta, static := s.cachedProvider(c, now)
	if provider == nil {
		// Background context: the provider keeps it (only its HTTP client) for later JWKS refreshes.
		dctx, cancel := context.WithTimeout(s.clientContext(context.Background()), s.cfg.HTTPTimeout)
		defer cancel()
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-dctx.Done():
			}
		}()
		var err error
		provider, err = oidc.NewProvider(dctx, c.OIDC.Issuer)
		if err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
		if err := provider.Claims(&meta); err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
	}
	algs := []string{oidc.RS256} // the OpenID Connect default when the provider does not list any
	if len(meta.Algs) > 0 {
		algs = nil
		for _, a := range meta.Algs {
			if slices.Contains(oidcAlgs, a) {
				algs = append(algs, a)
			}
		}
		if len(algs) == 0 {
			return nil, fmt.Errorf("the provider signs ID tokens only with unsupported algorithms %v (allowed: %v)", meta.Algs, oidcAlgs)
		}
	}
	skew := s.cfg.ClockSkew
	vcfg := &oidc.Config{
		ClientID: c.OIDC.ClientID, SupportedSigningAlgs: algs,
		// exp is checked against now − skew (tolerates a slightly fast IdP clock); iat is checked below.
		Now: func() time.Time { return s.now().Add(-skew) },
	}
	var verifier *oidc.IDTokenVerifier
	if static != nil {
		remote := oidc.NewRemoteKeySet(s.clientContext(context.Background()), meta.JWKSURL)
		verifier = oidc.NewVerifier(meta.Issuer, cachedKeySet{static: static, remote: remote}, vcfg)
	} else {
		verifier = provider.VerifierContext(s.clientContext(context.Background()), vcfg)
	}
	oc = &oidcClient{provider: provider, verifier: verifier, algs: algs, meta: meta, created: now}
	s.mu.Lock()
	for k := range s.providers { // drop older versions of this connection
		if strings.HasPrefix(k, c.ID+"|") {
			delete(s.providers, k)
		}
	}
	s.providers[key] = oc
	s.mu.Unlock()
	return oc, nil
}

// forgetProvider drops the cached provider of a connection (deleted, or refreshed documents).
func (s *Service) forgetProvider(connectionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.providers {
		if strings.HasPrefix(k, connectionID+"|") {
			delete(s.providers, k)
		}
	}
}

// cachedKeySet verifies with the refreshed JWKS first and falls back to the provider's JWKS (key rotation).
type cachedKeySet struct {
	static *oidc.StaticKeySet
	remote oidc.KeySet
}

func (k cachedKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	if payload, err := k.static.VerifySignature(ctx, jwt); err == nil {
		return payload, nil
	}
	return k.remote.VerifySignature(ctx, jwt)
}

// cachedProvider builds the provider from the discovery document and JWKS stored by the background refresh when
// they belong to the connection's issuer and are younger than providerTTL; nil otherwise.
func (s *Service) cachedProvider(c Connection, now time.Time) (*oidc.Provider, oidcMetadata, *oidc.StaticKeySet) {
	var meta oidcMetadata
	cache := c.Refresh.Cache
	if cache.FetchedAt == nil || now.Sub(*cache.FetchedAt) >= providerTTL || cache.Issuer != c.OIDC.Issuer || len(cache.OIDCDiscovery) == 0 {
		return nil, meta, nil
	}
	if err := json.Unmarshal(cache.OIDCDiscovery, &meta); err != nil || meta.Issuer != c.OIDC.Issuer || meta.AuthURL == "" || meta.TokenURL == "" {
		return nil, oidcMetadata{}, nil
	}
	pc := oidc.ProviderConfig{IssuerURL: meta.Issuer, AuthURL: meta.AuthURL, TokenURL: meta.TokenURL, UserInfoURL: meta.UserInfoURL,
		JWKSURL: meta.JWKSURL, Algorithms: meta.Algs}
	provider := pc.NewProvider(s.clientContext(context.Background()))
	keys, err := parseJWKS(cache.OIDCJWKS)
	if err != nil || len(keys) == 0 {
		return provider, meta, nil
	}
	return provider, meta, &oidc.StaticKeySet{PublicKeys: keys}
}

// parseJWKS returns the signature public keys of a JWKS.
func parseJWKS(b []byte) ([]crypto.PublicKey, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, err
	}
	var out []crypto.PublicKey
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.IsPublic() && k.Valid() {
			out = append(out, k.Key)
		}
	}
	return out, nil
}

func oidcScopes(c Connection) []string {
	scopes := []string{oidc.ScopeOpenID, "email", "profile"}
	for _, sc := range c.OIDC.Scopes {
		if sc != "" && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	return scopes
}

func (s *Service) oauth2Config(oc *oidcClient, c Connection) (*oauth2.Config, error) {
	secret := ""
	if len(c.SecretEnc) > 0 {
		b, err := s.cfg.SecretBox.Open(c.SecretEnc, secretAAD(c.ID))
		if err != nil {
			return nil, err
		}
		secret = string(b)
	}
	return &oauth2.Config{ClientID: c.OIDC.ClientID, ClientSecret: secret, Endpoint: oc.provider.Endpoint(),
		RedirectURL: s.OIDCRedirectURL(), Scopes: oidcScopes(c)}, nil
}

func secretAAD(connectionID string) string { return "oidc-client-secret:" + connectionID }

// oidcAuthURL returns the authorization request URL (authorization code flow with PKCE S256 and a nonce).
func (s *Service) oidcAuthURL(ctx context.Context, c Connection, state, nonce, verifier string) (string, error) {
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return "", err
	}
	cfg, err := s.oauth2Config(oc, c)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), nil
}

// oidcIdentity exchanges the authorization code and returns the verified identity.
func (s *Service) oidcIdentity(ctx context.Context, c Connection, st LoginState, code string) (Identity, error) {
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return Identity{}, err
	}
	cfg, err := s.oauth2Config(oc, c)
	if err != nil {
		return Identity{}, err
	}
	cctx, cancel := context.WithTimeout(s.clientContext(ctx), 2*s.cfg.HTTPTimeout)
	defer cancel()
	tok, err := cfg.Exchange(cctx, code, oauth2.VerifierOption(st.PKCEVerifier))
	if err != nil {
		return Identity{}, fmt.Errorf("token exchange: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, errors.New("the token response has no id_token")
	}
	claims, err := s.verifyIDToken(cctx, oc, c.OIDC.ClientID, raw, st.Nonce)
	if err != nil {
		return Identity{}, err
	}
	id := claimsIdentity(c, claims)
	id.IDToken = raw
	// Some providers put e-mail or groups only into the UserInfo response.
	if (id.Email == "" || !id.GroupsPresent) && oc.meta.UserInfoURL != "" {
		if ui, err := oc.provider.UserInfo(cctx, oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == id.Subject {
			var extra map[string]any
			if ui.Claims(&extra) == nil {
				merged := map[string]any{}
				for k, v := range extra {
					merged[k] = v
				}
				for k, v := range claims { // ID token claims win
					merged[k] = v
				}
				id = claimsIdentity(c, merged)
			}
		}
	}
	return id, nil
}

// verifyIDToken validates signature (allowlisted algorithms, JWKS), issuer, audience, expiry, nonce, azp and iat.
func (s *Service) verifyIDToken(ctx context.Context, oc *oidcClient, clientID, raw, nonce string) (map[string]any, error) {
	idt, err := oc.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("id token: nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if len(idt.Audience) > 1 {
		if azp, _ := claims["azp"].(string); azp != clientID {
			return nil, errors.New("id token: several audiences without azp of this client")
		}
	}
	if !idt.IssuedAt.IsZero() && idt.IssuedAt.After(s.now().Add(s.cfg.ClockSkew)) {
		return nil, errors.New("id token: issued in the future")
	}
	if idt.Subject == "" {
		return nil, errors.New("id token: no subject")
	}
	return claims, nil
}

func claimString(claims map[string]any, name string) string {
	switch v := claims[name].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	}
	return ""
}

func claimStrings(claims map[string]any, name string) ([]string, bool) {
	v, ok := claims[name]
	if !ok {
		return nil, false
	}
	switch x := v.(type) {
	case string:
		return []string{x}, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, true
}

func attrOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func claimsIdentity(c Connection, claims map[string]any) Identity {
	id := Identity{Subject: claimString(claims, "sub"), Issuer: claimString(claims, "iss"),
		Email: strings.ToLower(claimString(claims, attrOr(c.EmailAttribute, "email")))}
	switch v := claims["email_verified"].(type) {
	case bool:
		id.EmailVerified = v
	case string:
		id.EmailVerified = strings.EqualFold(v, "true")
	}
	id.Name = claimString(claims, attrOr(c.NameAttribute, "name"))
	if id.Name == "" {
		id.Name = strings.TrimSpace(claimString(claims, "given_name") + " " + claimString(claims, "family_name"))
	}
	id.Groups, id.GroupsPresent = claimStrings(claims, attrOr(c.GroupsAttribute, "groups"))
	id.SessionIndex = claimString(claims, "sid")
	return id
}

// testOIDC checks discovery, JWKS and the client secret of c.
func (s *Service) testOIDC(ctx context.Context, c Connection) []Check {
	var checks []Check
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return append(checks, Check{Name: "discovery", OK: false, Message: err.Error()})
	}
	checks = append(checks, Check{Name: "discovery", OK: true, Message: oc.meta.Issuer})
	checks = append(checks, Check{Name: "signing_algorithms", OK: true, Message: strings.Join(oc.algs, ", ")})
	if oc.meta.JWKSURL == "" {
		checks = append(checks, Check{Name: "jwks", OK: false, Message: "discovery document has no jwks_uri"})
	} else if b, err := fetch(ctx, s.client, oc.meta.JWKSURL); err != nil {
		checks = append(checks, Check{Name: "jwks", OK: false, Message: err.Error()})
	} else {
		var set struct {
			Keys []json.RawMessage `json:"keys"`
		}
		if json.Unmarshal(b, &set) != nil || len(set.Keys) == 0 {
			checks = append(checks, Check{Name: "jwks", OK: false, Message: "the JWKS has no keys"})
		} else {
			checks = append(checks, Check{Name: "jwks", OK: true, Message: fmt.Sprintf("%d keys", len(set.Keys))})
		}
	}
	if len(oc.meta.CodeChallengeMethod) > 0 && !slices.Contains(oc.meta.CodeChallengeMethod, "S256") {
		checks = append(checks, Check{Name: "pkce", OK: false, Message: "the provider does not list PKCE S256"})
	} else {
		checks = append(checks, Check{Name: "pkce", OK: true, Message: "S256"})
	}
	if len(c.SecretEnc) > 0 {
		if _, err := s.cfg.SecretBox.Open(c.SecretEnc, secretAAD(c.ID)); err != nil {
			checks = append(checks, Check{Name: "client_secret", OK: false, Message: err.Error()})
		} else {
			checks = append(checks, Check{Name: "client_secret", OK: true, Message: "stored"})
		}
	}
	return checks
}
