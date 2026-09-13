package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

func TestDSNWithTLSFiles(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "db.example")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{TLSCAFile: b.CAFile, TLSCertFile: b.ClientCert, TLSKeyFile: b.ClientKey}
	for _, dsn := range []string{
		"postgres://openlog:pw@db.example:5432/openlog?sslmode=verify-full&sslrootcert=/old/ca.crt",
		"host=db.example port=5432 user=openlog password=pw dbname=openlog sslmode=verify-full sslrootcert=/old/ca.crt",
	} {
		got, err := dsnWithTLSFiles(dsn, o)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := pgxpool.ParseConfig(got)
		if err != nil {
			t.Fatalf("%s: %v", got, err)
		}
		tc := cfg.ConnConfig.TLSConfig
		if tc == nil || tc.RootCAs == nil || tc.ServerName != "db.example" || tc.InsecureSkipVerify || len(tc.Certificates) != 1 {
			t.Errorf("%s: TLS config %+v", dsn, tc)
		}
		if len(cfg.ConnConfig.Fallbacks) != 0 {
			t.Errorf("%s: verify-full must not fall back to plaintext", dsn)
		}
	}

	// Nothing set: DSN unchanged (sslmode, sslrootcert from the DSN apply as before).
	dsn := "postgres://openlog@db/openlog?sslmode=require"
	if got, _ := dsnWithTLSFiles(dsn, Options{}); got != dsn {
		t.Errorf("unchanged DSN rewritten: %s", got)
	}
	// Quoting in keyword/value form.
	got, _ := dsnWithTLSFiles("host=db", Options{TLSCAFile: `/certs/it's.crt`})
	if !strings.HasSuffix(got, ` sslrootcert='/certs/it\'s.crt'`) {
		t.Errorf("quoted: %s", got)
	}
	// Errors never contain the password.
	if _, err := dsnWithTLSFiles("postgres://u:hunter2@%zz/db", o); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("bad URL error leaks or is nil: %v", err)
	}
}
