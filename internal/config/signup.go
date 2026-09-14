package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KeyHash configures the hashing of stored license and API keys (D-044). Services that read or write keys
// (openlog-ingest, openlog-api, openlog-allinone, openlog-admin) must use the same values.
type KeyHash struct {
	Secret         string // OPENLOG_KEY_HASH_SECRET: HMAC-SHA256 secret; empty = plain SHA-256
	SecretPrevious string // OPENLOG_KEY_HASH_SECRET_PREVIOUS: accepted during a rotation
}

// Signup configures self-service sign-up protection and e-mail verification (openlog-api, D-045).
type Signup struct {
	// RequireVerification is OPENLOG_SIGNUP_REQUIRE_VERIFICATION; nil = automatic (true when e-mail can be sent:
	// OPENLOG_SMTP_HOST and OPENLOG_PUBLIC_URL are set).
	RequireVerification *bool
	VerificationTTL     time.Duration // OPENLOG_EMAIL_VERIFICATION_TTL
	MaxPerIP            int           // OPENLOG_SIGNUP_MAX_PER_IP
	MaxPerEmail         int           // OPENLOG_SIGNUP_MAX_PER_EMAIL
	RateWindow          time.Duration // OPENLOG_SIGNUP_RATE_WINDOW
	BlockedDomains      []string      // OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS
	BlockedDomainsFile  string        // OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS_FILE
	CaptchaProvider     string        // OPENLOG_SIGNUP_CAPTCHA_PROVIDER: "", turnstile, hcaptcha
	CaptchaSecret       string        // OPENLOG_SIGNUP_CAPTCHA_SECRET
	CaptchaSiteKey      string        // OPENLOG_SIGNUP_CAPTCHA_SITE_KEY
}

func loadKeyHash(p *parser) KeyHash {
	return KeyHash{Secret: p.getenv("OPENLOG_KEY_HASH_SECRET"), SecretPrevious: p.getenv("OPENLOG_KEY_HASH_SECRET_PREVIOUS")}
}

func loadSignup(p *parser) Signup {
	s := Signup{
		VerificationTTL:    p.duration("OPENLOG_EMAIL_VERIFICATION_TTL", 48*time.Hour),
		MaxPerIP:           int(p.int64("OPENLOG_SIGNUP_MAX_PER_IP", 10)),
		MaxPerEmail:        int(p.int64("OPENLOG_SIGNUP_MAX_PER_EMAIL", 5)),
		RateWindow:         p.duration("OPENLOG_SIGNUP_RATE_WINDOW", time.Hour),
		BlockedDomains:     p.list("OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS", ""),
		BlockedDomainsFile: p.str("OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS_FILE", ""),
		CaptchaProvider:    strings.ToLower(p.str("OPENLOG_SIGNUP_CAPTCHA_PROVIDER", "")),
		CaptchaSecret:      p.getenv("OPENLOG_SIGNUP_CAPTCHA_SECRET"),
		CaptchaSiteKey:     p.str("OPENLOG_SIGNUP_CAPTCHA_SITE_KEY", ""),
	}
	if v, ok := p.raw("OPENLOG_SIGNUP_REQUIRE_VERIFICATION"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_SIGNUP_REQUIRE_VERIFICATION: invalid boolean %q", v))
		} else {
			s.RequireVerification = &b
		}
	}
	return s
}

// EmailConfigured reports whether invitation and verification e-mails can be sent.
func (c Config) EmailConfigured() bool { return c.Alert.SMTP.Host != "" && c.Alert.PublicURL != "" }

// SignupRequireVerification is the effective OPENLOG_SIGNUP_REQUIRE_VERIFICATION.
func (c Config) SignupRequireVerification() bool {
	if v := c.API.Auth.Signup.RequireVerification; v != nil {
		return *v
	}
	return c.EmailConfigured()
}

func (c Config) validateSignup() []error {
	var errs []error
	if k := c.KeyHash; k.Secret != "" && len(k.Secret) < 32 {
		errs = append(errs, errors.New("OPENLOG_KEY_HASH_SECRET must be at least 32 bytes (e.g. openssl rand -base64 48)"))
	} else if k.Secret == "" && k.SecretPrevious != "" {
		errs = append(errs, errors.New("OPENLOG_KEY_HASH_SECRET_PREVIOUS requires OPENLOG_KEY_HASH_SECRET"))
	}
	s := c.API.Auth.Signup
	if s.RequireVerification != nil && *s.RequireVerification && !c.EmailConfigured() {
		errs = append(errs, errors.New("OPENLOG_SIGNUP_REQUIRE_VERIFICATION=true requires OPENLOG_SMTP_HOST and OPENLOG_PUBLIC_URL"))
	}
	if s.VerificationTTL <= 0 {
		errs = append(errs, errors.New("OPENLOG_EMAIL_VERIFICATION_TTL must be > 0"))
	}
	if s.MaxPerIP <= 0 || s.MaxPerEmail <= 0 {
		errs = append(errs, errors.New("OPENLOG_SIGNUP_MAX_PER_IP and OPENLOG_SIGNUP_MAX_PER_EMAIL must be > 0"))
	}
	if s.RateWindow <= 0 || s.RateWindow > 24*time.Hour {
		errs = append(errs, errors.New("OPENLOG_SIGNUP_RATE_WINDOW must be between 1s and 24h"))
	}
	switch s.CaptchaProvider {
	case "":
	case "turnstile", "hcaptcha":
		if s.CaptchaSecret == "" || s.CaptchaSiteKey == "" {
			errs = append(errs, errors.New("OPENLOG_SIGNUP_CAPTCHA_PROVIDER requires OPENLOG_SIGNUP_CAPTCHA_SECRET and OPENLOG_SIGNUP_CAPTCHA_SITE_KEY"))
		}
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_SIGNUP_CAPTCHA_PROVIDER: must be turnstile or hcaptcha, got %q", s.CaptchaProvider))
	}
	return errs
}
