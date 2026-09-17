package config

import (
	"fmt"
	"time"
)

// CloudConnect configures the managed cloud service metrics (openlog-api, D-135): connections are stored per
// organization in PostgreSQL and the api leader polls the due ones. The limits below bound one process, not
// the cluster, and they exist because these provider APIs are billed per request and rate-limit hard.
type CloudConnect struct {
	// Enabled offers /api/v1/cloud/* and runs the poller on the api leader
	// (OPENLOG_CLOUD_ENABLED; postgres auth mode only).
	Enabled bool
	// MaxConcurrent bounds the polls of all organizations together (OPENLOG_CLOUD_MAX_CONCURRENT).
	MaxConcurrent int
	// TenantMaxConcurrent bounds the concurrent polls of one organization
	// (OPENLOG_CLOUD_TENANT_MAX_CONCURRENT).
	TenantMaxConcurrent int
	// ConnectionMaxConcurrent bounds the concurrent polls of one connection, so a connection covering many
	// regions does not burst requests at one cloud account (OPENLOG_CLOUD_CONNECTION_MAX_CONCURRENT).
	ConnectionMaxConcurrent int
	// RequestTimeout bounds one provider HTTP request (OPENLOG_CLOUD_REQUEST_TIMEOUT).
	RequestTimeout time.Duration
}

func loadCloudConnect(p *parser) CloudConnect {
	return CloudConnect{
		Enabled:                 p.bool("OPENLOG_CLOUD_ENABLED", true),
		MaxConcurrent:           int(p.int64("OPENLOG_CLOUD_MAX_CONCURRENT", 10)),
		TenantMaxConcurrent:     int(p.int64("OPENLOG_CLOUD_TENANT_MAX_CONCURRENT", 4)),
		ConnectionMaxConcurrent: int(p.int64("OPENLOG_CLOUD_CONNECTION_MAX_CONCURRENT", 2)),
		RequestTimeout:          p.duration("OPENLOG_CLOUD_REQUEST_TIMEOUT", 30*time.Second),
	}
}

func (c Config) validateCloudConnect() []error {
	var errs []error
	s := c.CloudConnect
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 1000 {
		errs = append(errs, fmt.Errorf("OPENLOG_CLOUD_MAX_CONCURRENT: must be between 1 and 1000, got %d", s.MaxConcurrent))
	}
	if s.TenantMaxConcurrent < 1 || s.TenantMaxConcurrent > s.MaxConcurrent {
		errs = append(errs, fmt.Errorf("OPENLOG_CLOUD_TENANT_MAX_CONCURRENT: must be between 1 and OPENLOG_CLOUD_MAX_CONCURRENT (%d), got %d",
			s.MaxConcurrent, s.TenantMaxConcurrent))
	}
	if s.ConnectionMaxConcurrent < 1 || s.ConnectionMaxConcurrent > s.TenantMaxConcurrent {
		errs = append(errs, fmt.Errorf("OPENLOG_CLOUD_CONNECTION_MAX_CONCURRENT: must be between 1 and OPENLOG_CLOUD_TENANT_MAX_CONCURRENT (%d), got %d",
			s.TenantMaxConcurrent, s.ConnectionMaxConcurrent))
	}
	if s.RequestTimeout < time.Second || s.RequestTimeout > 5*time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_CLOUD_REQUEST_TIMEOUT: must be between 1s and 5m, got %s", s.RequestTimeout))
	}
	return errs
}
