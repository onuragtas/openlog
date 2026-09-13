package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Alert configures alerting: openlog-alert (evaluator + dispatcher), the alert API of openlog-api and
// openlog-allinone (docs/contracts/config.md "openlog-alert", docs/contracts/alerting.md).
type Alert struct {
	// Enabled runs the evaluator and dispatcher inside openlog-allinone (OPENLOG_ALERT_ENABLED).
	Enabled bool
	// SecretsKey (current) and SecretsKeyPrevious encrypt channel secrets (base64 32-byte keys).
	SecretsKey         string
	SecretsKeyPrevious string
	// PublicURL is the web UI base URL used in notification links.
	PublicURL string

	LeaseTTL                   time.Duration
	LeaseRenewInterval         time.Duration
	EvaluationDelay            time.Duration
	MaxConcurrentEvaluations   int
	TenantMaxConcurrent        int
	TenantEvaluationsPerMinute int
	MaxSeriesPerRule           int
	QueryTimeout               time.Duration
	MaxRulesPerOrg             int

	DispatchWorkers          int
	DeliveryTimeout          time.Duration
	DeliveryMaxAttempts      int
	BlockPrivateDestinations bool

	SMTP SMTP
}

// SMTP is the global e-mail server for e-mail channels without their own server.
type SMTP struct {
	Host               string
	Port               int
	Username           string
	Password           string
	From               string
	TLS                string
	InsecureSkipVerify bool
}

func loadAlert(p *parser) Alert {
	return Alert{
		Enabled:                    p.bool("OPENLOG_ALERT_ENABLED", true),
		SecretsKey:                 p.str("OPENLOG_SECRETS_KEY", ""),
		SecretsKeyPrevious:         p.str("OPENLOG_SECRETS_KEY_PREVIOUS", ""),
		PublicURL:                  p.str("OPENLOG_PUBLIC_URL", ""),
		LeaseTTL:                   p.duration("OPENLOG_ALERT_LEASE_TTL", 30*time.Second),
		LeaseRenewInterval:         p.duration("OPENLOG_ALERT_LEASE_RENEW_INTERVAL", 10*time.Second),
		EvaluationDelay:            p.duration("OPENLOG_ALERT_EVALUATION_DELAY", 15*time.Second),
		MaxConcurrentEvaluations:   int(p.int64("OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS", 16)),
		TenantMaxConcurrent:        int(p.int64("OPENLOG_ALERT_TENANT_MAX_CONCURRENT", 4)),
		TenantEvaluationsPerMinute: int(p.int64("OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE", 600)),
		MaxSeriesPerRule:           int(p.int64("OPENLOG_ALERT_MAX_SERIES_PER_RULE", 1000)),
		QueryTimeout:               p.duration("OPENLOG_ALERT_QUERY_TIMEOUT", 20*time.Second),
		MaxRulesPerOrg:             int(p.int64("OPENLOG_ALERT_MAX_RULES_PER_ORG", 1000)),
		DispatchWorkers:            int(p.int64("OPENLOG_ALERT_DISPATCH_WORKERS", 4)),
		DeliveryTimeout:            p.duration("OPENLOG_ALERT_DELIVERY_TIMEOUT", 10*time.Second),
		DeliveryMaxAttempts:        int(p.int64("OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS", 10)),
		BlockPrivateDestinations:   p.bool("OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS", false),
		SMTP: SMTP{
			Host:               p.str("OPENLOG_SMTP_HOST", ""),
			Port:               int(p.int64("OPENLOG_SMTP_PORT", 587)),
			Username:           p.str("OPENLOG_SMTP_USERNAME", ""),
			Password:           p.getenv("OPENLOG_SMTP_PASSWORD"),
			From:               p.str("OPENLOG_SMTP_FROM", ""),
			TLS:                p.str("OPENLOG_SMTP_TLS", "starttls"),
			InsecureSkipVerify: p.bool("OPENLOG_SMTP_INSECURE_SKIP_VERIFY", false),
		},
	}
}

func (a Alert) validate() []error {
	var errs []error
	if a.LeaseTTL < 5*time.Second {
		errs = append(errs, errors.New("OPENLOG_ALERT_LEASE_TTL must be >= 5s"))
	}
	if a.LeaseRenewInterval <= 0 || a.LeaseRenewInterval*2 > a.LeaseTTL {
		errs = append(errs, errors.New("OPENLOG_ALERT_LEASE_RENEW_INTERVAL must be > 0 and at most half of OPENLOG_ALERT_LEASE_TTL"))
	}
	if a.EvaluationDelay < 0 || a.EvaluationDelay > 10*time.Minute {
		errs = append(errs, errors.New("OPENLOG_ALERT_EVALUATION_DELAY must be between 0 and 10m"))
	}
	for name, v := range map[string]int{
		"OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS": a.MaxConcurrentEvaluations, "OPENLOG_ALERT_TENANT_MAX_CONCURRENT": a.TenantMaxConcurrent,
		"OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE": a.TenantEvaluationsPerMinute, "OPENLOG_ALERT_MAX_SERIES_PER_RULE": a.MaxSeriesPerRule,
		"OPENLOG_ALERT_MAX_RULES_PER_ORG": a.MaxRulesPerOrg, "OPENLOG_ALERT_DISPATCH_WORKERS": a.DispatchWorkers,
		"OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS": a.DeliveryMaxAttempts,
	} {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be > 0", name))
		}
	}
	if a.QueryTimeout < time.Second || a.DeliveryTimeout < time.Second {
		errs = append(errs, errors.New("OPENLOG_ALERT_QUERY_TIMEOUT and OPENLOG_ALERT_DELIVERY_TIMEOUT must be >= 1s"))
	}
	if a.PublicURL != "" {
		if u, err := url.Parse(a.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("OPENLOG_PUBLIC_URL: must be an http(s) URL, got %q", a.PublicURL))
		}
	}
	switch a.SMTP.TLS {
	case "starttls", "tls", "none":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_SMTP_TLS: must be starttls, tls or none, got %q", a.SMTP.TLS))
	}
	if a.SMTP.Port <= 0 || a.SMTP.Port > 65535 {
		errs = append(errs, errors.New("OPENLOG_SMTP_PORT must be 1-65535"))
	}
	if a.SMTP.Host != "" && a.SMTP.From == "" {
		errs = append(errs, errors.New("OPENLOG_SMTP_FROM is required with OPENLOG_SMTP_HOST"))
	}
	if a.SMTP.TLS == "none" && a.SMTP.Username != "" {
		errs = append(errs, errors.New("OPENLOG_SMTP_USERNAME requires OPENLOG_SMTP_TLS starttls or tls (credentials are never sent in plaintext)"))
	}
	return errs
}
