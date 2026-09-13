package config

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestTLSDefaultsDisabled(t *testing.T) {
	c, err := Load(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]TLS{"kafka": c.KafkaTLS, "clickhouse": c.ClickHouseTLS} {
		tc, err := tt.Config()
		if err != nil || tc != nil {
			t.Errorf("%s: default TLS config = %v, %v; want nil, nil", name, tc, err)
		}
	}
	if c.KafkaSASL.Mechanism != "" || c.Postgres.TLS != (PostgresTLS{}) {
		t.Errorf("defaults: sasl %+v postgres %+v", c.KafkaSASL, c.Postgres.TLS)
	}
}

func TestTLSConfigFromEnv(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "kafka", "clickhouse", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(envOf(map[string]string{
		"OPENLOG_KAFKA_TLS_ENABLED":   "true",
		"OPENLOG_KAFKA_TLS_CA_FILE":   b.CAFile,
		"OPENLOG_KAFKA_TLS_CERT_FILE": b.ClientCert,
		"OPENLOG_KAFKA_TLS_KEY_FILE":  b.ClientKey,

		"OPENLOG_KAFKA_SASL_MECHANISM": "scram-sha-512",
		"OPENLOG_KAFKA_SASL_USERNAME":  "openlog",
		"OPENLOG_KAFKA_SASL_PASSWORD":  "secret",

		"OPENLOG_CLICKHOUSE_TLS_ENABLED":              "1",
		"OPENLOG_CLICKHOUSE_TLS_CA_FILE":              b.CAFile,
		"OPENLOG_CLICKHOUSE_TLS_SERVER_NAME":          "clickhouse",
		"OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY": "false",

		"OPENLOG_POSTGRES_TLS_CA_FILE": b.CAFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	k, err := c.KafkaTLS.Config()
	if err != nil || k == nil {
		t.Fatalf("kafka tls: %v %v", k, err)
	}
	if k.RootCAs == nil || len(k.Certificates) != 1 || k.ServerName != "" || k.InsecureSkipVerify || k.MinVersion != tls.VersionTLS12 {
		t.Errorf("kafka tls config = %+v", k)
	}
	if c.KafkaSASL.Mechanism != SASLScramSHA512 || c.KafkaSASL.Username != "openlog" || c.KafkaSASL.Password != "secret" {
		t.Errorf("sasl = %+v", c.KafkaSASL)
	}
	ch, err := c.ClickHouseTLS.Config()
	if err != nil || ch == nil || ch.RootCAs == nil || len(ch.Certificates) != 0 || ch.ServerName != "clickhouse" {
		t.Errorf("clickhouse tls config = %+v %v", ch, err)
	}
	if c.Postgres.TLS.CAFile != b.CAFile {
		t.Errorf("postgres tls = %+v", c.Postgres.TLS)
	}

	// The CA really verifies the server certificate (and the wrong CA does not).
	srv, err := tls.LoadX509KeyPair(b.ServerCert, b.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(srv, k.RootCAs, "kafka"); err != nil {
		t.Errorf("server cert does not verify against the CA: %v", err)
	}
	wrong, err := LoadCertPool(b.WrongCA)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(srv, wrong, "kafka"); err == nil {
		t.Error("server cert verifies against the wrong CA")
	}
}

func TestTLSInsecureOptIn(t *testing.T) {
	c, err := Load(envOf(map[string]string{"OPENLOG_KAFKA_TLS_ENABLED": "true", "OPENLOG_KAFKA_TLS_INSECURE_SKIP_VERIFY": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := c.KafkaTLS.Config()
	if k == nil || !k.InsecureSkipVerify || k.RootCAs != nil {
		t.Errorf("insecure config = %+v", k)
	}
}

func TestTLSValidation(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "kafka")
	if err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	if err := os.WriteFile(notPEM, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"ca without enabled", map[string]string{"OPENLOG_KAFKA_TLS_CA_FILE": b.CAFile}, "OPENLOG_KAFKA_TLS_CA_FILE is set but OPENLOG_KAFKA_TLS_ENABLED is not true"},
		{"insecure without enabled", map[string]string{"OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY": "true"}, "OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY is set but OPENLOG_CLICKHOUSE_TLS_ENABLED"},
		{"cert without key", map[string]string{"OPENLOG_CLICKHOUSE_TLS_ENABLED": "true", "OPENLOG_CLICKHOUSE_TLS_CERT_FILE": b.ClientCert}, "OPENLOG_CLICKHOUSE_TLS_CERT_FILE and OPENLOG_CLICKHOUSE_TLS_KEY_FILE must be set together"},
		{"missing ca file", map[string]string{"OPENLOG_KAFKA_TLS_ENABLED": "true", "OPENLOG_KAFKA_TLS_CA_FILE": "/does/not/exist.crt"}, "OPENLOG_KAFKA_TLS_CA_FILE: open /does/not/exist.crt"},
		{"ca not pem", map[string]string{"OPENLOG_KAFKA_TLS_ENABLED": "true", "OPENLOG_KAFKA_TLS_CA_FILE": notPEM}, "no PEM certificate found"},
		{"key does not match", map[string]string{"OPENLOG_KAFKA_TLS_ENABLED": "true", "OPENLOG_KAFKA_TLS_CERT_FILE": b.ClientCert, "OPENLOG_KAFKA_TLS_KEY_FILE": b.ServerKey}, "private key does not match public key"},
		{"bad bool", map[string]string{"OPENLOG_KAFKA_TLS_ENABLED": "yes please"}, "OPENLOG_KAFKA_TLS_ENABLED: invalid boolean"},
		{"sasl bad mechanism", map[string]string{"OPENLOG_KAFKA_SASL_MECHANISM": "GSSAPI"}, "must be PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512"},
		{"sasl no password", map[string]string{"OPENLOG_KAFKA_SASL_MECHANISM": "PLAIN", "OPENLOG_KAFKA_SASL_USERNAME": "u"}, "requires OPENLOG_KAFKA_SASL_USERNAME and OPENLOG_KAFKA_SASL_PASSWORD"},
		{"sasl user without mechanism", map[string]string{"OPENLOG_KAFKA_SASL_USERNAME": "u"}, "OPENLOG_KAFKA_SASL_MECHANISM is empty"},
		{"postgres cert without key", map[string]string{"OPENLOG_POSTGRES_TLS_CERT_FILE": b.ClientCert}, "OPENLOG_POSTGRES_TLS_CERT_FILE and OPENLOG_POSTGRES_TLS_KEY_FILE must be set together"},
		{"postgres missing ca", map[string]string{"OPENLOG_POSTGRES_TLS_CA_FILE": "/nope/ca.crt"}, "OPENLOG_POSTGRES_TLS_CA_FILE: stat /nope/ca.crt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envOf(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	// SASL without TLS is allowed (SASL_PLAINTEXT on a trusted network).
	if _, err := Load(envOf(map[string]string{"OPENLOG_KAFKA_SASL_MECHANISM": "SCRAM-SHA-256", "OPENLOG_KAFKA_SASL_USERNAME": "u", "OPENLOG_KAFKA_SASL_PASSWORD": "p"})); err != nil {
		t.Errorf("SASL_PLAINTEXT rejected: %v", err)
	}
}

// verify checks cert's chain against roots for name, as a TLS client would.
func verify(cert tls.Certificate, roots *x509.CertPool, name string) error {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
	return err
}
