// Package captcha verifies sign-up CAPTCHA tokens with Cloudflare Turnstile or hCaptcha (server-side
// "siteverify"; OPENLOG_SIGNUP_CAPTCHA_*, docs/contracts/config.md).
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// Providers.
const (
	Turnstile = "turnstile"
	HCaptcha  = "hcaptcha"
)

var endpoints = map[string]string{
	Turnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	HCaptcha:  "https://api.hcaptcha.com/siteverify",
}

// Verifier implements auth.CaptchaVerifier.
type Verifier struct {
	provider string
	secret   string
	siteKey  string
	endpoint string
	client   *http.Client
}

var _ auth.CaptchaVerifier = (*Verifier)(nil)

// New returns a verifier. endpoint overrides the provider URL (tests); client may be nil.
func New(provider, secret, siteKey, endpoint string, client *http.Client) (*Verifier, error) {
	def, ok := endpoints[provider]
	if !ok {
		return nil, fmt.Errorf("unknown CAPTCHA provider %q (turnstile or hcaptcha)", provider)
	}
	if secret == "" || siteKey == "" {
		return nil, fmt.Errorf("CAPTCHA provider %s needs a secret and a site key", provider)
	}
	if endpoint == "" {
		endpoint = def
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Verifier{provider: provider, secret: secret, siteKey: siteKey, endpoint: endpoint, client: client}, nil
}

// Provider returns the provider name.
func (v *Verifier) Provider() string { return v.provider }

// SiteKey returns the public site key the UI widget needs.
func (v *Verifier) SiteKey() string { return v.siteKey }

// Verify asks the provider whether token is a valid, unused response for this site.
func (v *Verifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if len(token) > 4096 {
		return false, nil
	}
	form := url.Values{"secret": {v.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	if v.provider == HCaptcha {
		form.Set("sitekey", v.siteKey)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s siteverify: HTTP %d", v.provider, resp.StatusCode)
	}
	var out struct {
		Success bool     `json:"success"`
		Codes   []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, fmt.Errorf("%s siteverify: %w", v.provider, err)
	}
	for _, c := range out.Codes {
		// Our configuration is wrong, not the user's answer.
		if c == "missing-input-secret" || c == "invalid-input-secret" || c == "sitekey-secret-mismatch" {
			return false, fmt.Errorf("%s siteverify: %s", v.provider, c)
		}
	}
	return out.Success, nil
}
