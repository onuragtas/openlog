package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

// TLS holds client TLS settings for one dependency (OPENLOG_<DEP>_TLS_*).
// Files are read every time a *tls.Config is built, so a restart (or, for pools
// that dial lazily, a new connection pool) picks up renewed certificates.
type TLS struct {
	// Enabled turns TLS on (OPENLOG_<DEP>_TLS_ENABLED).
	Enabled bool
	// CAFile is a PEM bundle of trusted CAs; empty = system roots.
	CAFile string
	// CertFile and KeyFile are the PEM client certificate and key (mutual TLS); both or neither.
	CertFile string
	KeyFile  string
	// ServerName overrides the name verified in the server certificate; empty = the dialed host.
	ServerName string
	// InsecureSkipVerify disables server certificate verification (testing only).
	InsecureSkipVerify bool

	prefix string // variable prefix, e.g. OPENLOG_KAFKA_TLS, for error messages
}

// SASL mechanisms supported for Kafka (OPENLOG_KAFKA_SASL_MECHANISM).
const (
	SASLPlain       = "PLAIN"
	SASLScramSHA256 = "SCRAM-SHA-256"
	SASLScramSHA512 = "SCRAM-SHA-512"
)

// KafkaSASL holds Kafka SASL settings (OPENLOG_KAFKA_SASL_*).
type KafkaSASL struct {
	// Mechanism is "" (no SASL), PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512.
	Mechanism string
	Username  string
	Password  string
}

// PostgresTLS holds PostgreSQL client certificate files (OPENLOG_POSTGRES_TLS_*). They are
// passed to the driver as sslrootcert / sslcert / sslkey; sslmode stays in the DSN.
type PostgresTLS struct {
	CAFile   string
	CertFile string
	KeyFile  string
}

func (p *parser) tls(prefix string) TLS {
	return TLS{
		Enabled:            p.bool(prefix+"_ENABLED", false),
		CAFile:             p.str(prefix+"_CA_FILE", ""),
		CertFile:           p.str(prefix+"_CERT_FILE", ""),
		KeyFile:            p.str(prefix+"_KEY_FILE", ""),
		ServerName:         p.str(prefix+"_SERVER_NAME", ""),
		InsecureSkipVerify: p.bool(prefix+"_INSECURE_SKIP_VERIFY", false),
		prefix:             prefix,
	}
}

func (p *parser) kafkaSASL() KafkaSASL {
	return KafkaSASL{
		Mechanism: strings.ToUpper(p.str("OPENLOG_KAFKA_SASL_MECHANISM", "")),
		Username:  p.str("OPENLOG_KAFKA_SASL_USERNAME", ""),
		Password:  p.getenv("OPENLOG_KAFKA_SASL_PASSWORD"),
	}
}

func (p *parser) postgresTLS() PostgresTLS {
	return PostgresTLS{
		CAFile:   p.str("OPENLOG_POSTGRES_TLS_CA_FILE", ""),
		CertFile: p.str("OPENLOG_POSTGRES_TLS_CERT_FILE", ""),
		KeyFile:  p.str("OPENLOG_POSTGRES_TLS_KEY_FILE", ""),
	}
}

func (t TLS) name(suffix string) string {
	prefix := t.prefix
	if prefix == "" {
		prefix = "TLS"
	}
	return prefix + "_" + suffix
}

// validate checks consistency and that the files load.
func (t TLS) validate() []error {
	var errs []error
	if !t.Enabled {
		for suffix, v := range map[string]string{"CA_FILE": t.CAFile, "CERT_FILE": t.CertFile, "KEY_FILE": t.KeyFile, "SERVER_NAME": t.ServerName} {
			if v != "" {
				errs = append(errs, fmt.Errorf("%s is set but %s is not true", t.name(suffix), t.name("ENABLED")))
			}
		}
		if t.InsecureSkipVerify {
			errs = append(errs, fmt.Errorf("%s is set but %s is not true", t.name("INSECURE_SKIP_VERIFY"), t.name("ENABLED")))
		}
		return sortErrs(errs)
	}
	if _, err := t.Config(); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// Config builds the client *tls.Config, or returns nil when TLS is disabled.
func (t TLS) Config() (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}
	c := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         t.ServerName,
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // explicit opt-in, documented as testing only
	}
	if t.CAFile != "" {
		pool, err := LoadCertPool(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.name("CA_FILE"), err)
		}
		c.RootCAs = pool
	}
	switch {
	case t.CertFile != "" && t.KeyFile != "":
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("%s / %s: %w", t.name("CERT_FILE"), t.name("KEY_FILE"), err)
		}
		c.Certificates = []tls.Certificate{cert}
	case t.CertFile != "" || t.KeyFile != "":
		return nil, fmt.Errorf("%s and %s must be set together", t.name("CERT_FILE"), t.name("KEY_FILE"))
	}
	return c, nil
}

// LoadCertPool reads a PEM bundle into a new pool; it fails when the file holds no certificate.
func LoadCertPool(file string) (*x509.CertPool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s: no PEM certificate found", file)
	}
	return pool, nil
}

func (s KafkaSASL) validate() []error {
	var errs []error
	switch s.Mechanism {
	case "":
		if s.Username != "" || s.Password != "" {
			errs = append(errs, errors.New("OPENLOG_KAFKA_SASL_USERNAME/PASSWORD are set but OPENLOG_KAFKA_SASL_MECHANISM is empty"))
		}
	case SASLPlain, SASLScramSHA256, SASLScramSHA512:
		if s.Username == "" || s.Password == "" {
			errs = append(errs, fmt.Errorf("OPENLOG_KAFKA_SASL_MECHANISM=%s requires OPENLOG_KAFKA_SASL_USERNAME and OPENLOG_KAFKA_SASL_PASSWORD", s.Mechanism))
		}
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_KAFKA_SASL_MECHANISM: must be PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512, got %q", s.Mechanism))
	}
	return errs
}

func (t PostgresTLS) validate() []error {
	var errs []error
	if (t.CertFile == "") != (t.KeyFile == "") {
		errs = append(errs, errors.New("OPENLOG_POSTGRES_TLS_CERT_FILE and OPENLOG_POSTGRES_TLS_KEY_FILE must be set together"))
	}
	for name, f := range map[string]string{"OPENLOG_POSTGRES_TLS_CA_FILE": t.CAFile, "OPENLOG_POSTGRES_TLS_CERT_FILE": t.CertFile, "OPENLOG_POSTGRES_TLS_KEY_FILE": t.KeyFile} {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return sortErrs(errs)
}

// sortErrs orders errors by message so map iteration does not make output unstable.
func sortErrs(errs []error) []error {
	for i := 1; i < len(errs); i++ {
		for j := i; j > 0 && errs[j].Error() < errs[j-1].Error(); j-- {
			errs[j], errs[j-1] = errs[j-1], errs[j]
		}
	}
	return errs
}

func (c Config) validateTLS() []error {
	var errs []error
	errs = append(errs, c.KafkaTLS.validate()...)
	errs = append(errs, c.KafkaSASL.validate()...)
	errs = append(errs, c.ClickHouseTLS.validate()...)
	errs = append(errs, c.Postgres.TLS.validate()...)
	return errs
}
