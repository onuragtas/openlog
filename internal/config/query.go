package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ClickHouseRead holds the read-only ClickHouse credentials of the api and alert query paths
// (OPENLOG_CLICKHOUSE_READ_*, D-047). Address, database and TLS are shared with the writer.
type ClickHouseRead struct {
	// User is empty when not configured: queries then use OPENLOG_CLICKHOUSE_USER (with a warning).
	User     string
	Password string
}

// QueryLimits are the ClickHouse limits applied to every api/alert query of a tenant. 0 = not set by
// openlog (the server profile's value applies).
type QueryLimits struct {
	MaxMemoryUsage int64 // max_memory_usage (bytes)
	MaxRowsToRead  int64 // max_rows_to_read
	MaxBytesToRead int64 // max_bytes_to_read (uncompressed bytes)
}

// Query configures per-tenant query limits (OPENLOG_QUERY_*).
type Query struct {
	Defaults QueryLimits
	// Tenants overrides fields of Defaults per tenant id (OPENLOG_QUERY_TENANT_LIMITS).
	Tenants map[string]QueryLimits
}

// Limits returns the effective limits of tenant.
func (q Query) Limits(tenant string) QueryLimits {
	if l, ok := q.Tenants[tenant]; ok {
		return l
	}
	return q.Defaults
}

func (p *parser) clickHouseRead() ClickHouseRead {
	return ClickHouseRead{
		User:     p.str("OPENLOG_CLICKHOUSE_READ_USER", ""),
		Password: p.getenv("OPENLOG_CLICKHOUSE_READ_PASSWORD"),
	}
}

func loadQuery(p *parser) Query {
	q := Query{Defaults: QueryLimits{
		MaxMemoryUsage: p.int64("OPENLOG_QUERY_MAX_MEMORY_USAGE", 2<<30),
		MaxRowsToRead:  p.int64("OPENLOG_QUERY_MAX_ROWS_TO_READ", 2_000_000_000),
		MaxBytesToRead: p.int64("OPENLOG_QUERY_MAX_BYTES_TO_READ", 0),
	}}
	if v, ok := p.raw("OPENLOG_QUERY_TENANT_LIMITS"); ok {
		t, err := parseTenantLimits(v, q.Defaults)
		if err != nil {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_QUERY_TENANT_LIMITS: %w", err))
		}
		q.Tenants = t
	}
	return q
}

var tenantIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// parseTenantLimits parses "tenant:key=value;key=value,tenant2:key=value". Keys: max_memory_usage,
// max_rows_to_read, max_bytes_to_read. Unset keys inherit defaults.
func parseTenantLimits(v string, defaults QueryLimits) (map[string]QueryLimits, error) {
	out := map[string]QueryLimits{}
	for _, entry := range strings.Split(v, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		tenant, spec, ok := strings.Cut(entry, ":")
		tenant = strings.TrimSpace(tenant)
		if !ok || !tenantIDPattern.MatchString(tenant) {
			return nil, fmt.Errorf("entry %q: want tenant:key=value[;key=value]", entry)
		}
		if _, dup := out[tenant]; dup {
			return nil, fmt.Errorf("tenant %q listed twice", tenant)
		}
		l := defaults
		for _, kv := range strings.Split(spec, ";") {
			k, val, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok {
				return nil, fmt.Errorf("tenant %q: invalid %q (want key=value)", tenant, kv)
			}
			n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("tenant %q: %s must be an integer >= 0, got %q", tenant, k, val)
			}
			switch strings.TrimSpace(k) {
			case "max_memory_usage":
				l.MaxMemoryUsage = n
			case "max_rows_to_read":
				l.MaxRowsToRead = n
			case "max_bytes_to_read":
				l.MaxBytesToRead = n
			default:
				return nil, fmt.Errorf("tenant %q: unknown key %q (max_memory_usage, max_rows_to_read, max_bytes_to_read)", tenant, k)
			}
		}
		out[tenant] = l
	}
	return out, nil
}

func (c Config) validateQuery() []error {
	var errs []error
	d := c.Query.Defaults
	if d.MaxMemoryUsage < 0 || d.MaxRowsToRead < 0 || d.MaxBytesToRead < 0 {
		errs = append(errs, errors.New("OPENLOG_QUERY_MAX_MEMORY_USAGE, OPENLOG_QUERY_MAX_ROWS_TO_READ and OPENLOG_QUERY_MAX_BYTES_TO_READ must be >= 0"))
	}
	if c.ClickHouseRead.User == "" && c.ClickHouseRead.Password != "" {
		errs = append(errs, errors.New("OPENLOG_CLICKHOUSE_READ_PASSWORD is set but OPENLOG_CLICKHOUSE_READ_USER is empty"))
	}
	return errs
}

// TenantIDs returns the tenants with overrides, sorted (logging).
func (q Query) TenantIDs() []string {
	ids := make([]string, 0, len(q.Tenants))
	for id := range q.Tenants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
