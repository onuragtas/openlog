// Package mcp serves openlog's read-only Model Context Protocol server
// (docs/operations/mcp.md, D-126): it exposes OQL, APM, alerting, log search and SLO reads as MCP
// tools for AI tools such as Claude Code and Cursor.
//
// The server is a front end of the query API, never of the databases: every tool call is one or
// more requests to `/api/v1` carrying the caller's API key, so authentication, the role gate, the
// tenant scope and the read-only ClickHouse user of openlog-api apply unchanged (docs/contracts/api.md
// "Authentication"). It holds no credentials of its own and has no ClickHouse or PostgreSQL client.
//
// Layout: config.go (OPENLOG_MCP_* variables), client.go (API client and error mapping),
// tools.go (tool schemas and handlers), server.go (MCP server, stdio and streamable HTTP).
package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Transports (OPENLOG_MCP_TRANSPORT).
const (
	// TransportStdio speaks MCP over stdin/stdout, for a local CLI (Claude Code, Cursor).
	TransportStdio = "stdio"
	// TransportHTTP serves streamable HTTP, for remote clients.
	TransportHTTP = "http"
)

// DefaultAPIURL is the openlog API a local server talks to.
const DefaultAPIURL = "http://localhost:8080"

// Row limits of the list tools (OPENLOG_MCP_MAX_ROWS bounds what a model can pull into its context).
const (
	minMaxRows     = 1
	maxMaxRows     = 1000
	defaultMaxRows = 100
)

// Config is parsed from OPENLOG_MCP_* (docs/contracts/config.md "openlog-mcp").
type Config struct {
	// Transport is stdio (default) or http.
	Transport string
	// HTTPAddr is the listen address of the streamable HTTP transport.
	HTTPAddr string
	// AdminAddr serves /healthz, /readyz and /metrics in http transport (stdio starts no listener).
	AdminAddr string
	// APIURL is the base URL of openlog-api, e.g. https://openlog.example.com (no /api/v1 suffix).
	APIURL string
	// APIKey is the openlog API key (ola_…) tool calls act as. Required for stdio; in http it is the
	// fallback for requests that carry no Authorization header of their own.
	APIKey string
	// OrgID selects the organization for keys of users in several organizations (X-Openlog-Org-Id);
	// empty = the key's organization.
	OrgID string
	// Timeout bounds one API request (a tool call may make two).
	Timeout time.Duration
	// MaxRows caps the row limit a tool argument may ask for.
	MaxRows int
}

// LoadConfig parses the server configuration. Unset variables take their documented defaults.
func LoadConfig(getenv func(string) string) (Config, error) {
	var errs []error
	str := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	dur := func(name string, def time.Duration) time.Duration {
		v := str(name, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid duration %q", name, v))
			return def
		}
		return d
	}
	c := Config{
		Transport: strings.ToLower(str("OPENLOG_MCP_TRANSPORT", TransportStdio)),
		HTTPAddr:  str("OPENLOG_MCP_HTTP_ADDR", ":8092"),
		AdminAddr: str("OPENLOG_ADMIN_ADDR", ":9464"),
		APIURL:    strings.TrimRight(str("OPENLOG_MCP_API_URL", DefaultAPIURL), "/"),
		APIKey:    str("OPENLOG_MCP_API_KEY", ""),
		OrgID:     str("OPENLOG_MCP_ORG_ID", ""),
		Timeout:   dur("OPENLOG_MCP_TIMEOUT", 60*time.Second),
		MaxRows:   defaultMaxRows,
	}
	if v := str("OPENLOG_MCP_MAX_ROWS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minMaxRows || n > maxMaxRows {
			errs = append(errs, fmt.Errorf("OPENLOG_MCP_MAX_ROWS: must be an integer between %d and %d, got %q", minMaxRows, maxMaxRows, v))
		} else {
			c.MaxRows = n
		}
	}
	if c.Transport != TransportStdio && c.Transport != TransportHTTP {
		errs = append(errs, fmt.Errorf("OPENLOG_MCP_TRANSPORT: must be %s or %s, got %q", TransportStdio, TransportHTTP, c.Transport))
	}
	if err := checkAPIURL(c.APIURL); err != nil {
		errs = append(errs, fmt.Errorf("OPENLOG_MCP_API_URL: %w", err))
	}
	// A stdio server serves exactly one caller and has no way to ask for a key, so it needs one up front.
	// The http transport takes the key from each request and only falls back to this one.
	if c.Transport == TransportStdio && c.APIKey == "" {
		errs = append(errs, errors.New("OPENLOG_MCP_API_KEY: required with the stdio transport (an openlog API key, ola_…)"))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}

// checkAPIURL accepts an http(s) URL with a host and an optional path prefix. Credentials, queries and
// fragments would end up in every request, so they are refused.
func checkAPIURL(raw string) error {
	if raw == "" {
		return errors.New("must not be empty")
	}
	if strings.ContainsAny(raw, " \t\r\n\"'`\\") {
		return fmt.Errorf("must not contain whitespace or quotes, got %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("must be an http(s) URL with a host, got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("must not contain credentials, a query or a fragment, got %q", raw)
	}
	return nil
}
