package config

import (
	"fmt"
	"strconv"
)

// Synthetics configures the scheduled outside-in checks (openlog-api, D-132).
type Synthetics struct {
	// Enabled offers /api/v1/synthetics/* and runs the scheduler on the api leader
	// (OPENLOG_SYNTHETICS_ENABLED; postgres auth mode only).
	Enabled bool
	// AllowPrivateNetworks lets checks reach private, loopback and link-local addresses
	// (OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS); nil = automatic (true unless OPENLOG_SIGNUP_ENABLED=true,
	// where organization members are not operators). Same rule as OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS.
	AllowPrivateNetworks *bool
	// MaxConcurrent bounds the runs of all organizations together (OPENLOG_SYNTHETICS_MAX_CONCURRENT).
	MaxConcurrent int
	// TenantMaxConcurrent bounds the concurrent runs of one organization
	// (OPENLOG_SYNTHETICS_TENANT_MAX_CONCURRENT).
	TenantMaxConcurrent int
	// MaxResponseBytes caps the response body one run reads (OPENLOG_SYNTHETICS_MAX_RESPONSE_BYTES).
	MaxResponseBytes int64
	// MaxRedirects caps the redirects one run follows (OPENLOG_SYNTHETICS_MAX_REDIRECTS).
	MaxRedirects int
	// CAFile is a PEM bundle trusted in addition to the system roots (OPENLOG_SYNTHETICS_CA_FILE). An
	// installation whose internal endpoints carry certificates of its own CA needs it; there is deliberately
	// no per-check "skip verification", because a tls check that does not verify checks nothing (D-140).
	CAFile string
}

func loadSynthetics(p *parser) Synthetics {
	s := Synthetics{
		Enabled:             p.bool("OPENLOG_SYNTHETICS_ENABLED", true),
		MaxConcurrent:       int(p.int64("OPENLOG_SYNTHETICS_MAX_CONCURRENT", 20)),
		TenantMaxConcurrent: int(p.int64("OPENLOG_SYNTHETICS_TENANT_MAX_CONCURRENT", 5)),
		MaxResponseBytes:    p.int64("OPENLOG_SYNTHETICS_MAX_RESPONSE_BYTES", 1<<20),
		MaxRedirects:        int(p.int64("OPENLOG_SYNTHETICS_MAX_REDIRECTS", 5)),
		CAFile:              p.str("OPENLOG_SYNTHETICS_CA_FILE", ""),
	}
	if v, ok := p.raw("OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS: invalid boolean %q", v))
		} else {
			s.AllowPrivateNetworks = &b
		}
	}
	return s
}

// SyntheticsAllowPrivateNetworks is the effective OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS: a self-hosted
// installation checks its own internal services by default, a sign-up installation does not (its members are
// not operators and a check is a request the server makes for them).
func (c Config) SyntheticsAllowPrivateNetworks() bool {
	if v := c.Synthetics.AllowPrivateNetworks; v != nil {
		return *v
	}
	return !c.API.Auth.SignupEnabled
}

func (c Config) validateSynthetics() []error {
	var errs []error
	s := c.Synthetics
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 1000 {
		errs = append(errs, fmt.Errorf("OPENLOG_SYNTHETICS_MAX_CONCURRENT: must be between 1 and 1000, got %d", s.MaxConcurrent))
	}
	if s.TenantMaxConcurrent < 1 || s.TenantMaxConcurrent > s.MaxConcurrent {
		errs = append(errs, fmt.Errorf("OPENLOG_SYNTHETICS_TENANT_MAX_CONCURRENT: must be between 1 and OPENLOG_SYNTHETICS_MAX_CONCURRENT (%d), got %d",
			s.MaxConcurrent, s.TenantMaxConcurrent))
	}
	if s.MaxResponseBytes < 1024 || s.MaxResponseBytes > 64<<20 {
		errs = append(errs, fmt.Errorf("OPENLOG_SYNTHETICS_MAX_RESPONSE_BYTES: must be between 1024 and 67108864, got %d", s.MaxResponseBytes))
	}
	if s.MaxRedirects < 0 || s.MaxRedirects > 10 {
		errs = append(errs, fmt.Errorf("OPENLOG_SYNTHETICS_MAX_REDIRECTS: must be between 0 and 10, got %d", s.MaxRedirects))
	}
	return errs
}
