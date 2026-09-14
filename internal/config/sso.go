package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SSO configures single sign-on and SCIM provisioning (openlog-api, D-077, D-078).
type SSO struct {
	Enabled bool // OPENLOG_SSO_ENABLED
	// SecretKey encrypts OIDC client secrets and SAML SP keys (OPENLOG_SSO_SECRET_KEY); SecretKeyPrevious is
	// accepted for decryption during a rotation. Without it a key derived from OPENLOG_KEY_HASH_SECRET is used.
	SecretKey         string
	SecretKeyPrevious string
	// AllowPrivateNetworks is OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS; nil = automatic (true unless
	// OPENLOG_SIGNUP_ENABLED=true, where organization admins are not operators).
	AllowPrivateNetworks *bool
	LoginTTL             time.Duration // OPENLOG_SSO_LOGIN_TTL
	ClockSkew            time.Duration // OPENLOG_SSO_CLOCK_SKEW
	HTTPTimeout          time.Duration // OPENLOG_SSO_HTTP_TIMEOUT
	SCIMEnabled          bool          // OPENLOG_SCIM_ENABLED
}

func loadSSO(p *parser) SSO {
	s := SSO{
		Enabled:           p.bool("OPENLOG_SSO_ENABLED", true),
		SecretKey:         p.getenv("OPENLOG_SSO_SECRET_KEY"),
		SecretKeyPrevious: p.getenv("OPENLOG_SSO_SECRET_KEY_PREVIOUS"),
		LoginTTL:          p.duration("OPENLOG_SSO_LOGIN_TTL", 10*time.Minute),
		ClockSkew:         p.duration("OPENLOG_SSO_CLOCK_SKEW", 2*time.Minute),
		HTTPTimeout:       p.duration("OPENLOG_SSO_HTTP_TIMEOUT", 10*time.Second),
		SCIMEnabled:       p.bool("OPENLOG_SCIM_ENABLED", true),
	}
	if v, ok := p.raw("OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS: invalid boolean %q", v))
		} else {
			s.AllowPrivateNetworks = &b
		}
	}
	return s
}

// SSOAllowPrivateNetworks is the effective OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS.
func (c Config) SSOAllowPrivateNetworks() bool {
	if v := c.API.Auth.SSO.AllowPrivateNetworks; v != nil {
		return *v
	}
	return !c.API.Auth.SignupEnabled
}

func (c Config) validateSSO() []error {
	var errs []error
	s := c.API.Auth.SSO
	if s.SecretKey != "" && len(s.SecretKey) < 32 {
		errs = append(errs, errors.New("OPENLOG_SSO_SECRET_KEY must be at least 32 bytes (e.g. openssl rand -base64 48)"))
	} else if s.SecretKey == "" && s.SecretKeyPrevious != "" {
		errs = append(errs, errors.New("OPENLOG_SSO_SECRET_KEY_PREVIOUS requires OPENLOG_SSO_SECRET_KEY"))
	}
	if s.LoginTTL < time.Minute || s.LoginTTL > time.Hour {
		errs = append(errs, errors.New("OPENLOG_SSO_LOGIN_TTL must be between 1m and 1h"))
	}
	if s.ClockSkew < 0 || s.ClockSkew > 10*time.Minute {
		errs = append(errs, errors.New("OPENLOG_SSO_CLOCK_SKEW must be between 0 and 10m"))
	}
	if s.HTTPTimeout < time.Second || s.HTTPTimeout > time.Minute {
		errs = append(errs, errors.New("OPENLOG_SSO_HTTP_TIMEOUT must be between 1s and 1m"))
	}
	return errs
}
