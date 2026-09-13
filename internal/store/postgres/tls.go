package postgres

import (
	"errors"
	"net/url"
	"strings"
)

// dsnWithTLSFiles sets sslrootcert / sslcert / sslkey from the OPENLOG_POSTGRES_TLS_* files,
// overriding values already in the DSN. Both DSN forms are supported: URL
// (postgres://…?sslmode=verify-full) and keyword/value (host=… sslmode=verify-full), where
// a later keyword wins. The driver then applies libpq semantics (with sslrootcert,
// sslmode=require also verifies the CA).
func dsnWithTLSFiles(dsn string, o Options) (string, error) {
	params := [][2]string{{"sslrootcert", o.TLSCAFile}, {"sslcert", o.TLSCertFile}, {"sslkey", o.TLSKeyFile}}
	set := false
	for _, p := range params {
		set = set || p[1] != ""
	}
	if !set {
		return dsn, nil
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			// url errors quote the input, which may contain a password.
			return "", errors.New("invalid URL")
		}
		q := u.Query()
		for _, p := range params {
			if p[1] != "" {
				q.Set(p[0], p[1])
			}
		}
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	var b strings.Builder
	b.WriteString(dsn)
	for _, p := range params {
		if p[1] != "" {
			b.WriteString(" " + p[0] + "='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(p[1]) + "'")
		}
	}
	return b.String(), nil
}
