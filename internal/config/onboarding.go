package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Onboarding holds the public ingest addresses that openlog-api reports to the web UI's "Add data" page
// (GET /api/v1/onboarding, docs/contracts/config.md "openlog-api"). They only prefill install commands; ingest
// itself listens on OPENLOG_INGEST_HTTP_ADDR / OPENLOG_INGEST_GRPC_ADDR. Empty values are derived from
// OPENLOG_PUBLIC_URL (same scheme and host, ports 4318/4317) or, without it, from the request host.
type Onboarding struct {
	// IngestPublicURL is the OTLP/HTTP base URL agents use, e.g. https://ingest.openlog.example.com:4318
	// (OPENLOG_INGEST_PUBLIC_URL). Agents append /v1/traces, /v1/metrics and /v1/logs.
	IngestPublicURL string
	// IngestPublicGRPCURL is the OTLP/gRPC endpoint, e.g. https://ingest.openlog.example.com:4317
	// (OPENLOG_INGEST_PUBLIC_GRPC_URL). Empty: the host of IngestPublicURL (or its fallback) with port 4317.
	IngestPublicGRPCURL string
}

func loadOnboarding(p *parser) Onboarding {
	return Onboarding{
		IngestPublicURL:     strings.TrimRight(strings.TrimSpace(p.str("OPENLOG_INGEST_PUBLIC_URL", "")), "/"),
		IngestPublicGRPCURL: strings.TrimRight(strings.TrimSpace(p.str("OPENLOG_INGEST_PUBLIC_GRPC_URL", "")), "/"),
	}
}

func (o Onboarding) validate() []error {
	var errs []error
	for _, v := range []struct{ name, value string }{
		{"OPENLOG_INGEST_PUBLIC_URL", o.IngestPublicURL},
		{"OPENLOG_INGEST_PUBLIC_GRPC_URL", o.IngestPublicGRPCURL},
	} {
		if v.value == "" {
			continue
		}
		if err := checkPublicEndpoint(v.value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.name, err))
		}
	}
	return errs
}

// checkPublicEndpoint accepts an http(s) URL with a host and an optional path prefix. The value ends up in shell
// commands and YAML shown to users, so credentials, queries, fragments, quotes, backslashes and whitespace are refused.
func checkPublicEndpoint(raw string) error {
	if strings.ContainsAny(raw, " \t\r\n\"'`\\$") {
		return fmt.Errorf("must not contain whitespace, quotes, backslashes or $, got %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("must be an http(s) URL with a host, got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not contain credentials, a query or a fragment, got %q", raw)
	}
	return nil
}
