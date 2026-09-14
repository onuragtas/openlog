package config

import (
	"strings"
	"testing"
	"time"
)

func TestSignupConfig(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	s := c.API.Auth.Signup
	if s.RequireVerification != nil || c.SignupRequireVerification() || s.MaxPerIP != 10 || s.MaxPerEmail != 5 ||
		s.RateWindow != time.Hour || s.VerificationTTL != 48*time.Hour || c.KeyHash.Secret != "" {
		t.Fatalf("defaults %+v %+v", s, c.KeyHash)
	}

	// Automatic: verification is required once e-mail can be sent.
	c, err = Load(env(map[string]string{"OPENLOG_SMTP_HOST": "smtp.example.com", "OPENLOG_SMTP_FROM": "a@example.com", "OPENLOG_PUBLIC_URL": "https://ol.example"}))
	if err != nil || !c.SignupRequireVerification() || !c.EmailConfigured() {
		t.Fatalf("auto verification: %v", err)
	}
	c, err = Load(env(map[string]string{"OPENLOG_SMTP_HOST": "smtp.example.com", "OPENLOG_SMTP_FROM": "a@example.com", "OPENLOG_PUBLIC_URL": "https://ol.example",
		"OPENLOG_SIGNUP_REQUIRE_VERIFICATION": "false", "OPENLOG_SIGNUP_CAPTCHA_PROVIDER": "Turnstile", "OPENLOG_SIGNUP_CAPTCHA_SECRET": "s",
		"OPENLOG_SIGNUP_CAPTCHA_SITE_KEY": "k", "OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS": "a.com, b.com", "OPENLOG_KEY_HASH_SECRET": strings.Repeat("x", 32)}))
	if err != nil || c.SignupRequireVerification() || c.API.Auth.Signup.CaptchaProvider != "turnstile" || len(c.API.Auth.Signup.BlockedDomains) != 2 || c.KeyHash.Secret == "" {
		t.Fatalf("explicit: %v %+v", err, c.API.Auth.Signup)
	}

	for name, vars := range map[string]map[string]string{
		"verification without smtp": {"OPENLOG_SIGNUP_REQUIRE_VERIFICATION": "true"},
		"bad bool":                  {"OPENLOG_SIGNUP_REQUIRE_VERIFICATION": "maybe"},
		"short secret":              {"OPENLOG_KEY_HASH_SECRET": "short"},
		"previous without secret":   {"OPENLOG_KEY_HASH_SECRET_PREVIOUS": strings.Repeat("x", 32)},
		"unknown captcha":           {"OPENLOG_SIGNUP_CAPTCHA_PROVIDER": "recaptcha"},
		"captcha without secret":    {"OPENLOG_SIGNUP_CAPTCHA_PROVIDER": "hcaptcha"},
		"window too long":           {"OPENLOG_SIGNUP_RATE_WINDOW": "25h"},
		"zero per ip":               {"OPENLOG_SIGNUP_MAX_PER_IP": "0"},
	} {
		if _, err := Load(env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
