// Package renderer renders dashboard widgets to PNG images for scheduled report e-mails (D-097,
// docs/operations/reports.md).
//
// Parts:
//   - render tokens (this file): short-lived, HMAC-signed, bound to one scheduled report of one dashboard; minted by
//     the api leader, verified by every api pod (internal/api/dashboard_render.go). The signing key is derived from a
//     server secret the renderer never receives, so the renderer cannot mint tokens.
//   - Server (server.go): the internal HTTP API of openlog-renderer, authenticated with OPENLOG_RENDERER_TOKEN.
//   - Chrome (chrome.go): headless Chromium driven over the DevTools protocol (chromedp).
//   - Client (client.go) and ReportImages (reportimages.go): the api side.
package renderer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Render token limits.
const (
	TokenPrefix = "olrt_"
	// MaxTokenTTL bounds the lifetime of a render token (a render has a strict timeout well below it).
	MaxTokenTTL = 10 * time.Minute
	// maxTokenLen bounds accepted token strings (claims are ids and timestamps only).
	maxTokenLen = 1024
)

// ErrInvalidToken is returned for malformed, forged or expired render tokens.
var ErrInvalidToken = errors.New("invalid render token")

var tokenRe = regexp.MustCompile(`^olrt_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$`)

// Claims are the signed contents of a render token.
type Claims struct {
	OrgID       string `json:"o"`
	TenantID    string `json:"t"`
	DashboardID string `json:"d"`
	ReportID    string `json:"r"`
	// From and To are the report period (Unix milliseconds).
	From int64 `json:"f"`
	To   int64 `json:"u"`
	// Expires is the Unix time (seconds) after which the token is refused.
	Expires int64 `json:"e"`
	// Purpose separates render tokens from any other HMAC use of the key.
	Purpose string `json:"p"`
}

const purposeReport = "report-render"

// KeyFromSecret derives the render token signing key from a server secret (OPENLOG_KEY_HASH_SECRET or
// OPENLOG_SECRETS_KEY). Returns nil for an empty secret.
func KeyFromSecret(secret string) []byte {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("openlog render token signing key v1"))
	return m.Sum(nil)
}

// NewReportToken signs a render token for one report period valid for ttl (at most MaxTokenTTL).
func NewReportToken(key []byte, c Claims, now time.Time, ttl time.Duration) (string, error) {
	if len(key) < 32 {
		return "", errors.New("render token key is not configured")
	}
	if ttl <= 0 || ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}
	if c.OrgID == "" || c.TenantID == "" || c.DashboardID == "" || c.ReportID == "" || c.From >= c.To {
		return "", errors.New("render token claims are incomplete")
	}
	c.Expires = now.Add(ttl).Unix()
	c.Purpose = purposeReport
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return TokenPrefix + body + "." + base64.RawURLEncoding.EncodeToString(sign(key, body)), nil
}

// VerifyToken checks a render token's format, signature and expiry and returns its claims.
func VerifyToken(key []byte, token string, now time.Time) (*Claims, error) {
	if len(key) < 32 || len(token) > maxTokenLen || !tokenRe.MatchString(token) {
		return nil, ErrInvalidToken
	}
	body, sig, _ := strings.Cut(strings.TrimPrefix(token, TokenPrefix), ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, sign(key, body)) {
		return nil, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil || c.Purpose != purposeReport {
		return nil, ErrInvalidToken
	}
	exp := time.Unix(c.Expires, 0)
	if !now.Before(exp) || exp.Sub(now) > MaxTokenTTL+time.Minute || c.OrgID == "" || c.TenantID == "" ||
		c.DashboardID == "" || c.ReportID == "" || c.From >= c.To {
		return nil, ErrInvalidToken
	}
	return &c, nil
}

func sign(key []byte, body string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(TokenPrefix))
	m.Write([]byte(body))
	return m.Sum(nil)
}
