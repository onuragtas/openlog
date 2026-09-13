// Package config parses service configuration from environment variables as
// specified in docs/contracts/config.md. Parsing takes a lookup function so it
// can be tested without touching the process environment.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Common holds variables shared by all services.
type Common struct {
	LogLevel           string
	AdminAddr          string
	KafkaBrokers       []string
	KafkaTopicPrefix   string
	ClickHouseAddr     []string
	ClickHouseDatabase string
	ClickHouseUser     string
	ClickHousePassword string
	ClickHouseCluster  string
	// KafkaTLS, KafkaSASL and ClickHouseTLS secure the dependency connections (tls.go).
	KafkaTLS      TLS
	KafkaSASL     KafkaSASL
	ClickHouseTLS TLS
	// LicenseKeys is the static key map for OPENLOG_AUTH_MODE=static (dev/tests).
	LicenseKeys string
	// AuthMode is "postgres" (default) or "static".
	AuthMode string
	Postgres Postgres
	// AuthCache configures ingest's license key cache (postgres mode).
	AuthCache AuthCache
}

// Postgres holds the PostgreSQL connection settings (postgres auth mode).
type Postgres struct {
	DSN      string
	Password string
	MaxConns int
	// TLS are client certificate files passed to the driver (sslmode stays in the DSN).
	TLS PostgresTLS
}

// AuthCache holds the license key cache TTLs.
type AuthCache struct {
	TTL         time.Duration
	NegativeTTL time.Duration
	MaxStale    time.Duration
}

// Auth holds user authentication settings used by openlog-api.
type Auth struct {
	SessionTTL         time.Duration
	SessionIdleTimeout time.Duration
	CookieSecure       bool
	CookieDomain       string
	SignupEnabled      bool
	LoginMaxFailures   int
	LoginWindow        time.Duration
	InvitationTTL      time.Duration
	TrustedProxies     []string
}

// Bootstrap describes the first organization created by `openlog-admin bootstrap`.
type Bootstrap struct {
	TenantID      string
	OrgName       string
	OwnerEmail    string
	OwnerPassword string
	OwnerName     string
	LicenseKey    string
	APIKey        string
}

// Ingest holds openlog-ingest variables.
type Ingest struct {
	HTTPAddr       string
	GRPCAddr       string
	MaxBodyBytes   int64
	ProduceTimeout time.Duration
}

// Processor holds openlog-processor variables.
type Processor struct {
	Group         string
	BatchRows     int
	FlushInterval time.Duration
	InsertTimeout time.Duration
	// BatchBytes cuts a per-partition chunk once its records reach this many bytes.
	BatchBytes int64
	// MaxBufferedBytes bounds record bytes held in memory before a forced flush.
	MaxBufferedBytes int64
	// InsertConcurrency is the number of chunks decoded and inserted in parallel.
	InsertConcurrency int
	// InsertMode is "direct" (shard *_local tables, D-018) or "distributed".
	InsertMode string
	// TopologyRefresh is how often direct mode re-reads system.clusters.
	TopologyRefresh time.Duration
	// InsertQuorum > 0 sets insert_quorum on direct inserts.
	InsertQuorum int
}

// Processor insert modes.
const (
	InsertModeDirect      = "direct"
	InsertModeDistributed = "distributed"
)

// API holds openlog-api variables.
type API struct {
	HTTPAddr     string
	QueryTimeout time.Duration
	MaxRows      int
	// UIEnabled serves the embedded web UI at / (OPENLOG_API_UI_ENABLED).
	UIEnabled bool
	Auth      Auth
}

// Migrate holds openlog-migrate variables.
type Migrate struct {
	KafkaPartitions        int
	KafkaReplicationFactor int
	// Topic configs applied when topics are created (existing topics are untouched).
	KafkaMinInsyncReplicas int
	KafkaRetentionMs       int64
	KafkaMaxMessageBytes   int64
	SkipKafka              bool
}

// Config is the union of all service configuration. Every binary parses the
// whole set; unused sections are simply ignored.
type Config struct {
	Common
	Ingest         Ingest
	Processor      Processor
	API            API
	Migrate        Migrate
	MigrateOnStart bool
	Bootstrap      Bootstrap
	UpdateCheck    UpdateCheck
	// Fleet configures agent fleet updates (fleet.go).
	Fleet Fleet
	// Alert configures alerting (alert.go).
	Alert Alert
	// APM configures the edge-linking job and Apdex default (docs/contracts/apm.md).
	APM APM
}

// APM holds openlog-api APM variables (docs/contracts/apm.md §4, §6).
type APM struct {
	LinkEnabled   bool          // OPENLOG_APM_LINK_ENABLED
	LinkInterval  time.Duration // OPENLOG_APM_LINK_INTERVAL
	LinkLookback  time.Duration // OPENLOG_APM_LINK_LOOKBACK
	LinkDelay     time.Duration // OPENLOG_APM_LINK_DELAY
	DefaultApdexT time.Duration // OPENLOG_APM_DEFAULT_APDEX_T
}

// UpdateCheck configures the release check of openlog-api (docs/contracts/releases-updates.md §5).
type UpdateCheck struct {
	Enabled         bool          // OPENLOG_UPDATE_CHECK=enabled|disabled
	Interval        time.Duration // OPENLOG_UPDATE_CHECK_INTERVAL (>= 1m)
	IndexURL        string        // OPENLOG_RELEASE_INDEX_URL
	Channel         string        // OPENLOG_UPDATE_CHANNEL=stable|beta
	TrustedKeysFile string        // OPENLOG_RELEASE_TRUSTED_KEYS_FILE
}

// FromEnv parses the configuration from the process environment.
func FromEnv() (Config, error) { return Load(os.Getenv) }

// Load parses configuration using getenv to look up variables. Unset or empty
// variables take their documented defaults.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	c := Config{
		Common: Common{
			LogLevel:           p.str("OPENLOG_LOG_LEVEL", "info"),
			AdminAddr:          p.str("OPENLOG_ADMIN_ADDR", ":9464"),
			KafkaBrokers:       p.list("OPENLOG_KAFKA_BROKERS", "localhost:9092"),
			KafkaTopicPrefix:   p.str("OPENLOG_KAFKA_TOPIC_PREFIX", "openlog"),
			ClickHouseAddr:     p.list("OPENLOG_CLICKHOUSE_ADDR", "localhost:9000"),
			ClickHouseDatabase: p.str("OPENLOG_CLICKHOUSE_DATABASE", "openlog"),
			ClickHouseUser:     p.str("OPENLOG_CLICKHOUSE_USER", "default"),
			ClickHousePassword: p.str("OPENLOG_CLICKHOUSE_PASSWORD", ""),
			ClickHouseCluster:  p.str("OPENLOG_CLICKHOUSE_CLUSTER", "openlog"),
			KafkaTLS:           p.tls("OPENLOG_KAFKA_TLS"),
			KafkaSASL:          p.kafkaSASL(),
			ClickHouseTLS:      p.tls("OPENLOG_CLICKHOUSE_TLS"),
			LicenseKeys:        p.str("OPENLOG_LICENSE_KEYS", ""),
			AuthMode:           p.str("OPENLOG_AUTH_MODE", "postgres"),
			Postgres: Postgres{
				DSN:      p.str("OPENLOG_POSTGRES_DSN", ""),
				Password: p.getenv("OPENLOG_POSTGRES_PASSWORD"),
				MaxConns: int(p.int64("OPENLOG_POSTGRES_MAX_CONNS", 10)),
				TLS:      p.postgresTLS(),
			},
			AuthCache: AuthCache{
				TTL:         p.duration("OPENLOG_AUTH_CACHE_TTL", 60*time.Second),
				NegativeTTL: p.duration("OPENLOG_AUTH_NEGATIVE_CACHE_TTL", 10*time.Second),
				MaxStale:    p.duration("OPENLOG_AUTH_CACHE_MAX_STALE", 15*time.Minute),
			},
		},
		Ingest: Ingest{
			HTTPAddr:       p.str("OPENLOG_INGEST_HTTP_ADDR", ":4318"),
			GRPCAddr:       p.str("OPENLOG_INGEST_GRPC_ADDR", ":4317"),
			MaxBodyBytes:   p.int64("OPENLOG_INGEST_MAX_BODY_BYTES", 10485760),
			ProduceTimeout: p.duration("OPENLOG_INGEST_PRODUCE_TIMEOUT", 10*time.Second),
		},
		Processor: Processor{
			Group:         p.str("OPENLOG_PROCESSOR_GROUP", "openlog-processor"),
			BatchRows:     int(p.int64("OPENLOG_PROCESSOR_BATCH_ROWS", 50000)),
			FlushInterval: p.duration("OPENLOG_PROCESSOR_FLUSH_INTERVAL", 2*time.Second),
			InsertTimeout: p.duration("OPENLOG_PROCESSOR_INSERT_TIMEOUT", 30*time.Second),

			BatchBytes:        p.int64("OPENLOG_PROCESSOR_BATCH_BYTES", 8<<20),
			MaxBufferedBytes:  p.int64("OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES", 128<<20),
			InsertConcurrency: int(p.int64("OPENLOG_PROCESSOR_INSERT_CONCURRENCY", 4)),
			InsertMode:        p.str("OPENLOG_PROCESSOR_INSERT_MODE", InsertModeDirect),
			TopologyRefresh:   p.duration("OPENLOG_PROCESSOR_TOPOLOGY_REFRESH", time.Minute),
			InsertQuorum:      int(p.int64("OPENLOG_PROCESSOR_INSERT_QUORUM", 0)),
		},
		API: API{
			HTTPAddr:     p.str("OPENLOG_API_HTTP_ADDR", ":8080"),
			QueryTimeout: p.duration("OPENLOG_API_QUERY_TIMEOUT", 30*time.Second),
			MaxRows:      int(p.int64("OPENLOG_API_MAX_ROWS", 10000)),
			UIEnabled:    p.bool("OPENLOG_API_UI_ENABLED", true),
			Auth: Auth{
				SessionTTL:         p.duration("OPENLOG_SESSION_TTL", 7*24*time.Hour),
				SessionIdleTimeout: p.duration("OPENLOG_SESSION_IDLE_TIMEOUT", 24*time.Hour),
				CookieSecure:       p.bool("OPENLOG_COOKIE_SECURE", true),
				CookieDomain:       p.str("OPENLOG_COOKIE_DOMAIN", ""),
				SignupEnabled:      p.bool("OPENLOG_SIGNUP_ENABLED", false),
				LoginMaxFailures:   int(p.int64("OPENLOG_LOGIN_MAX_FAILURES", 10)),
				LoginWindow:        p.duration("OPENLOG_LOGIN_WINDOW", 15*time.Minute),
				InvitationTTL:      p.duration("OPENLOG_INVITATION_TTL", 7*24*time.Hour),
				TrustedProxies:     p.list("OPENLOG_API_TRUSTED_PROXIES", ""),
			},
		},
		Migrate: Migrate{
			KafkaPartitions:        int(p.int64("OPENLOG_KAFKA_PARTITIONS", 6)),
			KafkaReplicationFactor: int(p.int64("OPENLOG_KAFKA_REPLICATION_FACTOR", 1)),
			KafkaMinInsyncReplicas: int(p.int64("OPENLOG_KAFKA_MIN_INSYNC_REPLICAS", 1)),
			KafkaRetentionMs:       p.int64("OPENLOG_KAFKA_RETENTION_MS", 86400000),
			KafkaMaxMessageBytes:   p.int64("OPENLOG_KAFKA_MAX_MESSAGE_BYTES", 12582912),
			SkipKafka:              p.bool("OPENLOG_MIGRATE_SKIP_KAFKA", false),
		},
		MigrateOnStart: p.bool("OPENLOG_MIGRATE_ON_START", true),
		UpdateCheck: UpdateCheck{
			Enabled:         p.str("OPENLOG_UPDATE_CHECK", "enabled") == "enabled",
			Interval:        p.duration("OPENLOG_UPDATE_CHECK_INTERVAL", 24*time.Hour),
			IndexURL:        p.str("OPENLOG_RELEASE_INDEX_URL", "https://github.com/onuragtas/openlog/releases/latest/download/index.json"),
			Channel:         p.str("OPENLOG_UPDATE_CHANNEL", "stable"),
			TrustedKeysFile: p.str("OPENLOG_RELEASE_TRUSTED_KEYS_FILE", ""),
		},
		Fleet: loadFleet(&p),
		Alert: loadAlert(&p),
		APM: APM{
			LinkEnabled:   p.bool("OPENLOG_APM_LINK_ENABLED", true),
			LinkInterval:  p.duration("OPENLOG_APM_LINK_INTERVAL", time.Minute),
			LinkLookback:  p.duration("OPENLOG_APM_LINK_LOOKBACK", 10*time.Minute),
			LinkDelay:     p.duration("OPENLOG_APM_LINK_DELAY", time.Minute),
			DefaultApdexT: p.duration("OPENLOG_APM_DEFAULT_APDEX_T", 500*time.Millisecond),
		},
		Bootstrap: Bootstrap{
			TenantID:      p.str("OPENLOG_BOOTSTRAP_TENANT_ID", "default"),
			OrgName:       p.str("OPENLOG_BOOTSTRAP_ORG_NAME", ""),
			OwnerEmail:    p.str("OPENLOG_BOOTSTRAP_OWNER_EMAIL", ""),
			OwnerPassword: p.getenv("OPENLOG_BOOTSTRAP_OWNER_PASSWORD"),
			OwnerName:     p.str("OPENLOG_BOOTSTRAP_OWNER_NAME", ""),
			LicenseKey:    p.str("OPENLOG_BOOTSTRAP_LICENSE_KEY", ""),
			APIKey:        p.str("OPENLOG_BOOTSTRAP_API_KEY", ""),
		},
	}
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	return c, c.validate(getenv)
}

func (c Config) validate(getenv func(string) string) error {
	var errs []error
	switch v := strings.TrimSpace(getenv("OPENLOG_UPDATE_CHECK")); v {
	case "", "enabled", "disabled":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATE_CHECK: must be enabled or disabled, got %q", v))
	}
	if c.UpdateCheck.Interval < time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATE_CHECK_INTERVAL: must be at least 1m, got %s", c.UpdateCheck.Interval))
	}
	switch c.UpdateCheck.Channel {
	case "stable", "beta":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATE_CHANNEL: must be stable or beta, got %q", c.UpdateCheck.Channel))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_LOG_LEVEL: invalid level %q", c.LogLevel))
	}
	errs = append(errs, c.validateTLS()...)
	if c.Ingest.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("OPENLOG_INGEST_MAX_BODY_BYTES must be > 0"))
	}
	if c.Processor.BatchRows <= 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_BATCH_ROWS must be > 0"))
	}
	if c.Processor.FlushInterval <= 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_FLUSH_INTERVAL must be > 0"))
	}
	if c.Processor.BatchBytes <= 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_BATCH_BYTES must be > 0"))
	}
	if c.Processor.MaxBufferedBytes < c.Processor.BatchBytes {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES must be >= OPENLOG_PROCESSOR_BATCH_BYTES"))
	}
	if c.Processor.InsertConcurrency <= 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_INSERT_CONCURRENCY must be > 0"))
	}
	switch c.Processor.InsertMode {
	case InsertModeDirect, InsertModeDistributed:
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_PROCESSOR_INSERT_MODE: invalid mode %q (direct or distributed)", c.Processor.InsertMode))
	}
	if c.Processor.TopologyRefresh <= 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_TOPOLOGY_REFRESH must be > 0"))
	}
	if c.Processor.InsertQuorum < 0 {
		errs = append(errs, errors.New("OPENLOG_PROCESSOR_INSERT_QUORUM must be >= 0"))
	}
	if c.API.MaxRows <= 0 {
		errs = append(errs, errors.New("OPENLOG_API_MAX_ROWS must be > 0"))
	}
	if c.APM.LinkInterval < 10*time.Second {
		errs = append(errs, fmt.Errorf("OPENLOG_APM_LINK_INTERVAL: must be at least 10s, got %s", c.APM.LinkInterval))
	}
	if c.APM.LinkLookback < time.Minute || c.APM.LinkLookback > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_APM_LINK_LOOKBACK: must be between 1m and 24h, got %s", c.APM.LinkLookback))
	}
	if c.APM.LinkDelay < 0 || c.APM.LinkDelay > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_APM_LINK_DELAY: must be between 0 and 1h, got %s", c.APM.LinkDelay))
	}
	if c.APM.DefaultApdexT < time.Millisecond || c.APM.DefaultApdexT > 10*time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_APM_DEFAULT_APDEX_T: must be between 1ms and 10m, got %s", c.APM.DefaultApdexT))
	}
	if c.Migrate.KafkaPartitions <= 0 || c.Migrate.KafkaReplicationFactor <= 0 {
		errs = append(errs, errors.New("OPENLOG_KAFKA_PARTITIONS and OPENLOG_KAFKA_REPLICATION_FACTOR must be > 0"))
	}
	if m := c.Migrate.KafkaMinInsyncReplicas; m < 1 || (c.Migrate.KafkaReplicationFactor > 0 && m > c.Migrate.KafkaReplicationFactor) {
		errs = append(errs, fmt.Errorf("OPENLOG_KAFKA_MIN_INSYNC_REPLICAS must be between 1 and OPENLOG_KAFKA_REPLICATION_FACTOR (%d), got %d",
			c.Migrate.KafkaReplicationFactor, m))
	}
	if c.Migrate.KafkaRetentionMs < -1 || c.Migrate.KafkaRetentionMs == 0 {
		errs = append(errs, errors.New("OPENLOG_KAFKA_RETENTION_MS must be > 0 or -1 (unlimited)"))
	}
	if c.Migrate.KafkaMaxMessageBytes <= 0 || c.Migrate.KafkaMaxMessageBytes > math.MaxInt32 {
		errs = append(errs, errors.New("OPENLOG_KAFKA_MAX_MESSAGE_BYTES must be between 1 and 2147483647"))
	}
	if len(c.KafkaBrokers) == 0 {
		errs = append(errs, errors.New("OPENLOG_KAFKA_BROKERS must not be empty"))
	}
	if len(c.ClickHouseAddr) == 0 {
		errs = append(errs, errors.New("OPENLOG_CLICKHOUSE_ADDR must not be empty"))
	}
	switch c.AuthMode {
	case "static", "postgres":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_AUTH_MODE: must be static or postgres, got %q", c.AuthMode))
	}
	if c.Postgres.MaxConns <= 0 {
		errs = append(errs, errors.New("OPENLOG_POSTGRES_MAX_CONNS must be > 0"))
	}
	if c.AuthCache.TTL <= 0 || c.AuthCache.NegativeTTL <= 0 || c.AuthCache.MaxStale < 0 {
		errs = append(errs, errors.New("OPENLOG_AUTH_CACHE_TTL and OPENLOG_AUTH_NEGATIVE_CACHE_TTL must be > 0, OPENLOG_AUTH_CACHE_MAX_STALE >= 0"))
	}
	a := c.API.Auth
	if a.SessionTTL <= 0 || a.SessionIdleTimeout < 0 || a.LoginWindow <= 0 || a.InvitationTTL <= 0 {
		errs = append(errs, errors.New("OPENLOG_SESSION_TTL, OPENLOG_LOGIN_WINDOW and OPENLOG_INVITATION_TTL must be > 0, OPENLOG_SESSION_IDLE_TIMEOUT >= 0"))
	}
	if a.LoginMaxFailures <= 0 {
		errs = append(errs, errors.New("OPENLOG_LOGIN_MAX_FAILURES must be > 0"))
	}
	errs = append(errs, c.Fleet.validate()...)
	errs = append(errs, c.Alert.validate()...)
	return errors.Join(errs...)
}

type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) raw(name string) (string, bool) {
	v := strings.TrimSpace(p.getenv(name))
	return v, v != ""
}

func (p *parser) str(name, def string) string {
	if v, ok := p.raw(name); ok {
		return v
	}
	return def
}

func (p *parser) list(name, def string) []string {
	v := p.str(name, def)
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *parser) int64(name string, def int64) int64 {
	v, ok := p.raw(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid integer %q", name, v))
		return def
	}
	return n
}

func (p *parser) duration(name string, def time.Duration) time.Duration {
	v, ok := p.raw(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid duration %q", name, v))
		return def
	}
	return d
}

func (p *parser) bool(name string, def bool) bool {
	v, ok := p.raw(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid boolean %q", name, v))
		return def
	}
	return b
}
