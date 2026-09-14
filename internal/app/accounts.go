package app

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/captcha"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/mail"
	"github.com/onuragtas/openlog/internal/tenant"
)

// KeyHasher returns the license/API key hasher of OPENLOG_KEY_HASH_SECRET (D-044).
func KeyHasher(cfg config.Config) *tenant.KeyHasher {
	return tenant.NewKeyHasher(cfg.KeyHash.Secret, cfg.KeyHash.SecretPrevious)
}

// applyAccountOptions fills the e-mail, sign-up protection and key hashing settings of the api's auth
// service (D-044, D-045).
func applyAccountOptions(ac *auth.Config, cfg config.Config, log *slog.Logger) error {
	ac.KeyHasher = KeyHasher(cfg)
	if !ac.KeyHasher.Keyed() {
		log.Warn("OPENLOG_KEY_HASH_SECRET is not set: license and API keys are stored as plain SHA-256 hashes (set it for SaaS)")
	}
	s := cfg.API.Auth.Signup
	ac.PublicURL = cfg.Alert.PublicURL
	ac.VerificationTTL, ac.SignupMaxPerIP, ac.SignupMaxPerEmail, ac.SignupRateWindow = s.VerificationTTL, s.MaxPerIP, s.MaxPerEmail, s.RateWindow
	ac.RequireEmailVerification = cfg.SignupRequireVerification()

	switch smtp := cfg.Alert.SMTP; {
	case smtp.Host == "":
		log.Info("OPENLOG_SMTP_HOST is not set: invitations are shared as links, sign-ups are not e-mail verified")
	case cfg.Alert.PublicURL == "":
		log.Warn("OPENLOG_PUBLIC_URL is not set: invitation and verification e-mails are disabled (they need links)")
	default:
		m, err := mail.New(mail.Config{Host: smtp.Host, Port: smtp.Port, Username: smtp.Username, Password: smtp.Password,
			From: smtp.From, TLS: smtp.TLS, InsecureSkipVerify: smtp.InsecureSkipVerify})
		if err != nil {
			return fmt.Errorf("OPENLOG_SMTP_*: %w", err)
		}
		ac.Mailer = m
	}

	if !ac.SignupEnabled {
		return nil
	}
	if s.CaptchaProvider != "" {
		v, err := captcha.New(s.CaptchaProvider, s.CaptchaSecret, s.CaptchaSiteKey, "", nil)
		if err != nil {
			return fmt.Errorf("OPENLOG_SIGNUP_CAPTCHA_*: %w", err)
		}
		ac.Captcha = v
	}
	if len(s.BlockedDomains) > 0 || s.BlockedDomainsFile != "" {
		var list auth.DomainBlocklist
		var err error
		if s.BlockedDomainsFile != "" {
			f, ferr := os.Open(s.BlockedDomainsFile)
			if ferr != nil {
				return fmt.Errorf("OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS_FILE: %w", ferr)
			}
			list, err = auth.ParseDomainBlocklist(s.BlockedDomains, f)
			f.Close()
		} else {
			list, err = auth.ParseDomainBlocklist(s.BlockedDomains, nil)
		}
		if err != nil {
			return fmt.Errorf("OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS: %w", err)
		}
		ac.EmailDomainBlocked = list.Blocked
		log.Info("sign-up e-mail domain blocklist loaded", "domains", len(list))
	}
	if !ac.RequireEmailVerification {
		log.Warn("OPENLOG_SIGNUP_ENABLED=true without e-mail verification: anyone can create organizations with any address")
	}
	return nil
}
