package sso

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/onuragtas/openlog/internal/auth"
)

// OpenID Connect Back-Channel Logout 1.0 and Front-Channel Logout 1.0 (D-098): the provider ends the openlog sessions
// of an IdP session — server to server with a signed logout_token, or through a hidden iframe in the user's browser
// with iss and sid. Sessions are matched by the sub and sid recorded at sign-in (sso_sessions).

const (
	backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"
	maxLogoutTokenLen      = 16 << 10
	maxLogoutJTILen        = 256
	// logoutFailureLimit bounds invalid logout requests (and unknown front-channel sids) per client address per
	// publicRateSpan; valid requests do not count.
	logoutFailureLimit = 60
)

// OIDCBackchannelLogoutURL is the back-channel logout URI of an OIDC connection.
func (s *Service) OIDCBackchannelLogoutURL(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/oidc/" + connectionID + "/backchannel-logout"
}

// OIDCFrontchannelLogoutURL is the front-channel logout URI of an OIDC connection.
func (s *Service) OIDCFrontchannelLogoutURL(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/oidc/" + connectionID + "/frontchannel-logout"
}

// ErrLogoutUnavailable reports that a logout could not be completed (store unavailable); the IdP may retry.
var ErrLogoutUnavailable = errors.New("single logout is temporarily unavailable")

// LogoutRejectedError is a refused logout request; Reason is safe to return to the identity provider.
type LogoutRejectedError struct{ Reason string }

func (e *LogoutRejectedError) Error() string { return e.Reason }

func rateKey(purpose, ip string) []byte {
	h := sha256.Sum256([]byte("sso-" + purpose + "\x00" + ip))
	return h[:]
}

// tooManyFailures reports whether ip reached max recorded failures of purpose (shared login_failures counters).
func (s *Service) tooManyFailures(ctx context.Context, purpose, ip string, max int) (bool, error) {
	n, err := s.users.CountLoginFailures(ctx, rateKey(purpose, ip), s.now().Add(-publicRateSpan))
	return n >= max, err
}

func (s *Service) addFailure(ctx context.Context, purpose, ip string) {
	if err := s.users.AddLoginFailure(context.WithoutCancel(ctx), rateKey(purpose, ip), s.now()); err != nil {
		s.log.Warn("cannot record a failed single logout request", "err", err)
	}
}

// revokeSSOSessions revokes the sessions of links that keep accepts.
func (s *Service) revokeSSOSessions(ctx context.Context, links []SSOSession, keep func(SSOSession) bool, now time.Time) (revoked, failed int) {
	for _, l := range links {
		if keep != nil && !keep(l) {
			continue
		}
		if err := s.users.RevokeSession(ctx, l.UserID, l.SessionID, now); err != nil && !errors.Is(err, auth.ErrNotFound) {
			s.log.Error("cannot revoke session for single logout", "session_id", l.SessionID, "err", err)
			failed++
			continue
		}
		revoked++
	}
	return revoked, failed
}

// endOIDCSessions revokes the active sessions of an OIDC connection with sid (and sub, when both are given) or,
// without sid, all sessions with sub.
func (s *Service) endOIDCSessions(ctx context.Context, c Connection, sub, sid string, now time.Time) (revoked, failed int, err error) {
	var links []SSOSession
	if sid != "" {
		links, err = s.store.ListSSOSessionsByIndex(ctx, c.ID, sid, now)
	} else {
		links, err = s.store.ListSSOSessions(ctx, c.ID, sub, "", now)
	}
	if err != nil {
		s.log.Error("cannot list SSO sessions for single logout", "connection_id", c.ID, "err", err)
		return 0, 0, err
	}
	revoked, failed = s.revokeSSOSessions(ctx, links, func(l SSOSession) bool { return sub == "" || l.Subject == sub }, now)
	return revoked, failed, nil
}

type logoutClaims struct {
	sub, sid, jti string
	replayUntil   time.Time
}

func optionalStringClaim(claims map[string]json.RawMessage, name string) (string, error) {
	raw, ok := claims[name]
	if !ok {
		return "", nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("claim %s is not a string", name)
	}
	return strings.TrimSpace(v), nil
}

// verifyLogoutToken validates a logout token (Back-Channel Logout 1.0 §2.6): signature with the provider's JWKS
// (asymmetric allowlist), iss, aud, iat (and exp when present), the back-channel logout event, no nonce (an ID token is
// not a logout token), jti, and sub and/or sid.
func (s *Service) verifyLogoutToken(ctx context.Context, c Connection, raw string) (logoutClaims, error) {
	var out logoutClaims
	if raw == "" || len(raw) > maxLogoutTokenLen {
		return out, errors.New("logout_token is missing or too large")
	}
	oc, err := s.oidcClient(ctx, c)
	if err != nil {
		return out, err
	}
	v := oidc.NewVerifier(oc.meta.Issuer, oc.keys, &oidc.Config{ClientID: c.OIDC.ClientID, SupportedSigningAlgs: oc.algs, SkipExpiryCheck: true})
	cctx, cancel := context.WithTimeout(s.clientContext(ctx), s.cfg.HTTPTimeout)
	defer cancel()
	tok, err := v.Verify(cctx, raw)
	if err != nil {
		return out, fmt.Errorf("logout token: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := tok.Claims(&claims); err != nil {
		return out, fmt.Errorf("logout token: %w", err)
	}
	var events map[string]json.RawMessage
	if raw, ok := claims["events"]; !ok || json.Unmarshal(raw, &events) != nil {
		return out, errors.New("logout token: no events claim")
	}
	var event map[string]json.RawMessage
	if member, ok := events[backchannelLogoutEvent]; !ok || json.Unmarshal(member, &event) != nil || event == nil {
		return out, errors.New("logout token: events does not contain the back-channel logout event")
	}
	if _, ok := claims["nonce"]; ok {
		return out, errors.New("logout token: a nonce is not allowed")
	}
	if out.jti, err = optionalStringClaim(claims, "jti"); err != nil || out.jti == "" || len(out.jti) > maxLogoutJTILen {
		return out, errors.New("logout token: jti is missing or invalid")
	}
	if out.sub, err = optionalStringClaim(claims, "sub"); err != nil {
		return out, fmt.Errorf("logout token: %w", err)
	}
	if out.sid, err = optionalStringClaim(claims, "sid"); err != nil {
		return out, fmt.Errorf("logout token: %w", err)
	}
	if out.sub == "" && out.sid == "" {
		return out, errors.New("logout token: neither sub nor sid")
	}
	if len(out.sub) > 1024 || len(out.sid) > 1024 {
		return out, errors.New("logout token: sub or sid too long")
	}
	if len(tok.Audience) > 1 {
		if azp, err := optionalStringClaim(claims, "azp"); err != nil || (azp != "" && azp != c.OIDC.ClientID) {
			return out, errors.New("logout token: azp is not this client")
		}
	}
	now, skew := s.now(), s.cfg.ClockSkew
	switch {
	case tok.IssuedAt.IsZero():
		return out, errors.New("logout token: no iat")
	case tok.IssuedAt.After(now.Add(skew)):
		return out, errors.New("logout token: issued in the future")
	case tok.IssuedAt.Before(now.Add(-maxLogoutAge - skew)):
		return out, errors.New("logout token: too old")
	case !tok.Expiry.IsZero() && !now.Before(tok.Expiry.Add(skew)):
		return out, errors.New("logout token: expired")
	}
	out.replayUntil = now.Add(minAssertionTTL)
	for _, t := range []time.Time{tok.IssuedAt.Add(maxLogoutAge + 2*skew), tok.Expiry.Add(skew)} {
		if t.After(out.replayUntil) {
			out.replayUntil = t
		}
	}
	return out, nil
}

// OIDCBackchannelLogout validates a logout_token posted by the provider of an OIDC connection (POST
// /api/v1/sso/oidc/{connection_id}/backchannel-logout) and ends the matching sessions. It returns nil (200),
// a *LogoutRejectedError (400) or ErrLogoutUnavailable.
func (s *Service) OIDCBackchannelLogout(ctx context.Context, connectionID, rawToken string, meta auth.ClientMeta) error {
	const purpose = "oidc-backchannel"
	limited, err := s.tooManyFailures(ctx, purpose, meta.IP, logoutFailureLimit)
	if err != nil {
		return ErrLogoutUnavailable
	}
	if limited {
		return &LogoutRejectedError{Reason: "too many invalid logout requests"}
	}
	c, err := s.store.GetConnectionByID(ctx, connectionID)
	if err != nil || c.Protocol != ProtocolOIDC || c.OIDC == nil {
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return ErrLogoutUnavailable
		}
		s.addFailure(ctx, purpose, meta.IP)
		return &LogoutRejectedError{Reason: "unknown OIDC connection"}
	}
	reject := func(code string, err error) error {
		s.addFailure(ctx, purpose, meta.IP)
		s.log.Warn("OIDC back-channel logout refused", "connection_id", c.ID, "reason", code, "err", err)
		s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout_failed", "sso_connection", c.ID, map[string]any{"reason": code,
			"via": "oidc_backchannel", "error": truncate(err.Error(), 300)})
		return &LogoutRejectedError{Reason: "the logout token is not valid"}
	}
	claims, err := s.verifyLogoutToken(ctx, c, rawToken)
	if err != nil {
		return reject(ErrCodeInvalidRequest, err)
	}
	fresh, err := s.store.RecordAssertion(ctx, c.ID, "oidc-logout:"+claims.jti, claims.replayUntil)
	if err != nil {
		s.log.Error("cannot record a logout token", "connection_id", c.ID, "err", err)
		return ErrLogoutUnavailable
	}
	if !fresh {
		return reject(ErrCodeReplay, fmt.Errorf("logout token %s was already used", truncate(claims.jti, 80)))
	}
	revoked, failed, err := s.endOIDCSessions(ctx, c, claims.sub, claims.sid, s.now())
	s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "via": "oidc_backchannel",
		"subject": truncate(claims.sub, 256), "sid": claims.sid != "", "revoked_sessions": revoked, "failed": failed})
	if err != nil || failed > 0 {
		return ErrLogoutUnavailable
	}
	return nil
}

// FrontchannelPage is the answer of the front-channel logout URI: an empty page only the provider's origin may frame.
type FrontchannelPage struct {
	Status int
	HTML   []byte
	CSP    string
}

const frontchannelHTML = `<!doctype html><html><head><meta charset="utf-8"><title>openlog</title></head><body></body></html>`

// issuerOrigin returns the scheme://host[:port] of an issuer URL ("" when it is not a plain http(s) URL).
func issuerOrigin(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(u.Host, " ;,'\"*") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// OIDCFrontchannelLogout ends the sessions of an IdP session loaded by the provider in an iframe (GET
// /api/v1/sso/oidc/{connection_id}/frontchannel-logout?iss=…&sid=…). iss must be the connection's issuer and sid is
// required: openlog's session cookie is SameSite=Strict and never reaches a third-party iframe, so the IdP session
// identifies the sessions. The page may be framed only by the issuer's origin.
func (s *Service) OIDCFrontchannelLogout(ctx context.Context, connectionID, iss, sid string, meta auth.ClientMeta) FrontchannelPage {
	const purpose = "oidc-frontchannel"
	page := func(status int, frameOrigin string) FrontchannelPage {
		ancestors := "'none'"
		if frameOrigin != "" {
			ancestors = frameOrigin
		}
		return FrontchannelPage{Status: status, HTML: []byte(frontchannelHTML),
			CSP: "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors " + ancestors}
	}
	c, err := s.store.GetConnectionByID(ctx, connectionID)
	if err != nil || c.Protocol != ProtocolOIDC || c.OIDC == nil {
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return page(http.StatusServiceUnavailable, "")
		}
		s.addFailure(ctx, purpose, meta.IP)
		return page(http.StatusNotFound, "")
	}
	origin := issuerOrigin(c.OIDC.Issuer)
	limited, err := s.tooManyFailures(ctx, purpose, meta.IP, logoutFailureLimit)
	if err != nil {
		return page(http.StatusServiceUnavailable, origin)
	}
	if limited {
		return page(http.StatusTooManyRequests, origin)
	}
	if iss == "" || sid == "" || iss != c.OIDC.Issuer || len(sid) > 1024 {
		s.addFailure(ctx, purpose, meta.IP)
		reason := "iss and sid are required"
		if iss != "" && sid != "" {
			reason = fmt.Sprintf("iss %q is not the connection's issuer or sid is invalid", truncate(iss, 200))
		}
		s.log.Warn("OIDC front-channel logout refused", "connection_id", c.ID, "err", reason)
		s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout_failed", "sso_connection", c.ID, map[string]any{"reason": ErrCodeInvalidRequest,
			"via": "oidc_frontchannel", "error": reason})
		return page(http.StatusBadRequest, origin)
	}
	revoked, failed, err := s.endOIDCSessions(ctx, c, "", sid, s.now())
	if err != nil || failed > 0 {
		return page(http.StatusServiceUnavailable, origin)
	}
	if revoked == 0 {
		// Already ended (or a guessed sid): nothing to audit; guesses count against the address.
		s.addFailure(ctx, purpose, meta.IP)
		return page(http.StatusOK, origin)
	}
	s.audit(ctx, c.OrgID, "", "", meta.IP, "sso.logout", "sso_connection", c.ID, map[string]any{"protocol": c.Protocol, "via": "oidc_frontchannel",
		"sid": true, "revoked_sessions": revoked})
	return page(http.StatusOK, origin)
}
