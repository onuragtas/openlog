package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Integration ids implemented by the agent (semantic-conventions §6).
const (
	IntegrationNginx      = "nginx"
	IntegrationRedis      = "redis"
	IntegrationMySQL      = "mysql"
	IntegrationPostgreSQL = "postgresql"
	IntegrationDocker     = "docker"
)

// IntegrationIDs lists the implemented integrations in a stable order.
var IntegrationIDs = []string{IntegrationDocker, IntegrationMySQL, IntegrationNginx, IntegrationPostgreSQL, IntegrationRedis}

// IntegrationsConfig configures the metric integrations bound to discovered services.
type IntegrationsConfig struct {
	// Enabled is the master switch; integrations also need discovery.
	Enabled bool `yaml:"enabled"`
	// Interval is the default collection interval of every integration.
	Interval Duration `yaml:"interval"`
	// Timeout bounds one collection (connect + queries).
	Timeout Duration `yaml:"timeout"`
	// MaxConcurrent limits collections running at the same time.
	MaxConcurrent int `yaml:"max_concurrent"`
	// MaxInstances limits the number of integration instances.
	MaxInstances int `yaml:"max_instances"`
	// RemoteConfig applies integration settings configured in the openlog UI and
	// delivered by agent sync over this file (remote wins per field, D-039).
	RemoteConfig bool `yaml:"remote_config"`

	Nginx      IntegrationConfig `yaml:"nginx"`
	Redis      IntegrationConfig `yaml:"redis"`
	MySQL      IntegrationConfig `yaml:"mysql"`
	PostgreSQL IntegrationConfig `yaml:"postgresql"`
	Docker     IntegrationConfig `yaml:"docker"`
}

// IntegrationConfig configures one integration. Settings apply to every
// instance; Instances override them for matching discovered services.
type IntegrationConfig struct {
	Enabled          bool     `yaml:"enabled"`
	Interval         Duration `yaml:"interval"`
	InstanceSettings `yaml:",inline"`
	Instances        []InstanceConfig `yaml:"instances"`
}

// InstanceSettings are the connection settings of an integration instance.
type InstanceSettings struct {
	// Endpoint: host:port, unix:/path/to.sock, or (nginx) the stub_status URL.
	Endpoint string `yaml:"endpoint"`
	Username string `yaml:"username"`
	// Password: literal (discouraged), env:NAME or file:/absolute/path.
	Password Secret `yaml:"password"`
	// Database is the initial database (postgresql, default "postgres").
	Database string `yaml:"database"`
	// Databases limits the collected databases (postgresql); empty = all connectable.
	Databases        []string `yaml:"databases"`
	ExcludeDatabases []string `yaml:"exclude_databases"`
	// TopNTables bounds per-table/per-index series (mysql, postgresql); 0 = default.
	TopNTables int        `yaml:"top_n_tables"`
	TLS        *TLSConfig `yaml:"tls"`
	// QueryStats enables top statements from pg_stat_statements (postgresql).
	QueryStats *QueryStatsConfig `yaml:"query_stats"`
}

// QueryStatsConfig configures pg_stat_statements collection (postgresql, opt-in).
type QueryStatsConfig struct {
	Enabled bool `yaml:"enabled"`
	// TopN bounds the statements by total execution time (0 = 20, max 100).
	TopN int `yaml:"top_n"`
	// MinCalls skips statements executed fewer times (0 = 1).
	MinCalls int `yaml:"min_calls"`
}

// TLSConfig configures client TLS of an integration connection.
type TLSConfig struct {
	Enabled            bool   `yaml:"enabled"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	CAFile             string `yaml:"ca_file"`
	ServerName         string `yaml:"server_name"`
}

// InstanceConfig overrides settings for discovered services matching Match.
type InstanceConfig struct {
	Match            InstanceMatch `yaml:"match"`
	Enabled          *bool         `yaml:"enabled"`
	InstanceSettings `yaml:",inline"`
}

// InstanceMatch selects discovered services; all given fields must match.
type InstanceMatch struct {
	Port      int    `yaml:"port"`      // a listening port of the service
	Endpoint  string `yaml:"endpoint"`  // the derived endpoint, e.g. 127.0.0.1:6380
	Unit      string `yaml:"unit"`      // a systemd unit of the service
	Container string `yaml:"container"` // container name or id (prefix of at least 12 chars)
	Instance  string `yaml:"instance"`  // discovered_service instance (exe path, unit, container id)
}

// IsZero reports an empty matcher.
func (m InstanceMatch) IsZero() bool { return m == InstanceMatch{} }

// Integration returns the configuration of an integration id, or nil.
func (c *IntegrationsConfig) Integration(id string) *IntegrationConfig {
	switch id {
	case IntegrationNginx:
		return &c.Nginx
	case IntegrationRedis:
		return &c.Redis
	case IntegrationMySQL:
		return &c.MySQL
	case IntegrationPostgreSQL:
		return &c.PostgreSQL
	case IntegrationDocker:
		return &c.Docker
	}
	return nil
}

// EffectiveInterval returns the integration interval (falling back to the default).
func (c *IntegrationsConfig) EffectiveInterval(id string) time.Duration {
	if ic := c.Integration(id); ic != nil && ic.Interval > 0 {
		return ic.Interval.D()
	}
	return c.Interval.D()
}

// Merge overlays non-empty fields of o onto s.
func (s InstanceSettings) Merge(o InstanceSettings) InstanceSettings {
	if o.Endpoint != "" {
		s.Endpoint = o.Endpoint
	}
	if o.Username != "" {
		s.Username = o.Username
	}
	if o.Password != "" {
		s.Password = o.Password
	}
	if o.Database != "" {
		s.Database = o.Database
	}
	if o.Databases != nil {
		s.Databases = o.Databases
	}
	if o.ExcludeDatabases != nil {
		s.ExcludeDatabases = o.ExcludeDatabases
	}
	if o.TopNTables != 0 {
		s.TopNTables = o.TopNTables
	}
	if o.TLS != nil {
		s.TLS = o.TLS
	}
	if o.QueryStats != nil {
		s.QueryStats = o.QueryStats
	}
	return s
}

func defaultIntegrations() IntegrationsConfig {
	on := IntegrationConfig{Enabled: true}
	return IntegrationsConfig{
		Enabled: true, Interval: Duration(30 * time.Second), Timeout: Duration(10 * time.Second),
		MaxConcurrent: 4, MaxInstances: 32, RemoteConfig: true,
		Nginx: on, Redis: on, MySQL: on, PostgreSQL: on, Docker: on,
	}
}

// integrationKeys lists which settings each integration accepts.
var integrationKeys = map[string]map[string]bool{
	IntegrationNginx:      {"endpoint": true, "tls": true},
	IntegrationRedis:      {"endpoint": true, "username": true, "password": true, "tls": true},
	IntegrationMySQL:      {"endpoint": true, "username": true, "password": true, "tls": true, "top_n_tables": true},
	IntegrationPostgreSQL: {"endpoint": true, "username": true, "password": true, "tls": true, "top_n_tables": true, "database": true, "databases": true, "exclude_databases": true, "query_stats": true},
	IntegrationDocker:     {},
}

func (s InstanceSettings) usedKeys() []string {
	var k []string
	add := func(name string, set bool) {
		if set {
			k = append(k, name)
		}
	}
	add("endpoint", s.Endpoint != "")
	add("username", s.Username != "")
	add("password", s.Password != "")
	add("database", s.Database != "")
	add("databases", len(s.Databases) > 0)
	add("exclude_databases", len(s.ExcludeDatabases) > 0)
	add("top_n_tables", s.TopNTables != 0)
	add("tls", s.TLS != nil)
	add("query_stats", s.QueryStats != nil)
	return k
}

func (s InstanceSettings) validate(id, prefix string) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(prefix+format, a...)) }
	for _, k := range s.usedKeys() {
		if !integrationKeys[id][k] {
			add("%s is not supported by the %s integration", k, id)
		}
	}
	if err := s.Password.validate(); err != nil {
		add("password: %v", err)
	}
	if s.TopNTables < 0 {
		add("top_n_tables must be >= 0")
	}
	if q := s.QueryStats; q != nil && (q.TopN < 0 || q.TopN > 100 || q.MinCalls < 0) {
		add("query_stats: top_n must be 0..100 and min_calls >= 0")
	}
	if s.TLS != nil && s.TLS.CAFile != "" && !filepath.IsAbs(s.TLS.CAFile) {
		add("tls.ca_file must be an absolute path")
	}
	if e := s.Endpoint; e != "" {
		switch {
		case id == IntegrationNginx:
			u, err := url.Parse(e)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add("endpoint must be the http(s) URL of the stub_status page (got %q)", e)
			}
		case strings.HasPrefix(e, "unix:"):
			if !filepath.IsAbs(strings.TrimPrefix(e, "unix:")) {
				add("endpoint unix:<path> needs an absolute path")
			}
		default:
			if _, _, err := net.SplitHostPort(e); err != nil {
				add("endpoint must be host:port or unix:/path (got %q)", e)
			}
		}
	}
	return errs
}

func (c *IntegrationsConfig) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Interval.D() < 5*time.Second {
		add("integrations.interval must be at least 5s")
	}
	if c.Timeout.D() <= 0 || c.Timeout.D() > c.Interval.D() {
		add("integrations.timeout must be positive and not longer than integrations.interval")
	}
	if c.MaxConcurrent < 1 {
		add("integrations.max_concurrent must be >= 1")
	}
	if c.MaxInstances < 1 {
		add("integrations.max_instances must be >= 1")
	}
	for _, id := range IntegrationIDs {
		ic := c.Integration(id)
		p := "integrations." + id + "."
		if ic.Interval != 0 && ic.Interval.D() < 5*time.Second {
			add("%sinterval must be at least 5s", p)
		}
		errs = append(errs, ic.InstanceSettings.validate(id, p)...)
		for i, in := range ic.Instances {
			ip := fmt.Sprintf("%sinstances[%d].", p, i)
			if in.Match.IsZero() {
				add("%smatch: at least one of port, endpoint, unit, container, instance is required", ip)
			}
			if in.Match.Port < 0 || in.Match.Port > 65535 {
				add("%smatch.port must be 1..65535", ip)
			}
			errs = append(errs, in.InstanceSettings.validate(id, ip)...)
		}
	}
	return errs
}

// Warnings returns non-fatal configuration findings (e.g. literal passwords).
func (c *Config) Warnings() []string {
	var out []string
	for _, id := range IntegrationIDs {
		ic := c.Integrations.Integration(id)
		if ic.Password.IsLiteral() {
			out = append(out, "integrations."+id+".password is a literal value; prefer env:NAME or file:/path")
		}
		for i, in := range ic.Instances {
			if in.Password.IsLiteral() {
				out = append(out, fmt.Sprintf("integrations.%s.instances[%d].password is a literal value; prefer env:NAME or file:/path", id, i))
			}
		}
	}
	return out
}

// Secret is a credential reference: "env:NAME", "file:/path" or a literal.
// Its String/GoString/MarshalText never reveal the value.
type Secret string

// literalPrefix marks a value that is never interpreted as a reference. Remote
// integration config uses it so that a password from the backend cannot make
// the agent read an environment variable or a local file.
const literalPrefix = "literal:"

// LiteralSecret returns a Secret holding v verbatim ("" stays empty).
func LiteralSecret(v string) Secret {
	if v == "" {
		return ""
	}
	return Secret(literalPrefix + v)
}

// ErrSecretUnset means the referenced environment variable or file is empty/missing.
var ErrSecretUnset = errors.New("secret not set")

// String implements fmt.Stringer without revealing the value.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "***"
}

// GoString implements fmt.GoStringer.
func (s Secret) GoString() string { return s.String() }

// MarshalText keeps the value out of JSON/YAML dumps.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// IsLiteral reports a non-empty literal value.
func (s Secret) IsLiteral() bool {
	return s != "" && !strings.HasPrefix(string(s), "env:") && !strings.HasPrefix(string(s), "file:")
}

// Source describes the reference without the value (e.g. "env:REDIS_PASSWORD").
func (s Secret) Source() string {
	switch {
	case s == "":
		return ""
	case s.IsLiteral():
		return "literal"
	}
	return string(s)
}

func (s Secret) validate() error {
	v := string(s)
	switch {
	case strings.HasPrefix(v, "env:"):
		if strings.TrimPrefix(v, "env:") == "" {
			return errors.New("env: needs a variable name")
		}
	case strings.HasPrefix(v, "file:"):
		if !filepath.IsAbs(strings.TrimPrefix(v, "file:")) {
			return errors.New("file: needs an absolute path")
		}
	}
	return nil
}

// Resolve returns the secret value. env and file references are read on every
// call so that rotated credentials are picked up; a trailing newline of a file is removed.
func (s Secret) Resolve() (string, error) {
	v := string(s)
	switch {
	case v == "":
		return "", nil
	case strings.HasPrefix(v, literalPrefix):
		return strings.TrimPrefix(v, literalPrefix), nil
	case strings.HasPrefix(v, "env:"):
		name := strings.TrimPrefix(v, "env:")
		val := os.Getenv(name)
		if val == "" {
			return "", fmt.Errorf("environment variable %s: %w", name, ErrSecretUnset)
		}
		return val, nil
	case strings.HasPrefix(v, "file:"):
		p := strings.TrimPrefix(v, "file:")
		b, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("file %s: %w", p, ErrSecretUnset)
			}
			return "", fmt.Errorf("file %s: %w", p, errors.Unwrap(err))
		}
		val := strings.TrimRight(string(b), "\r\n")
		if val == "" {
			return "", fmt.Errorf("file %s is empty: %w", p, ErrSecretUnset)
		}
		return val, nil
	}
	return v, nil
}
