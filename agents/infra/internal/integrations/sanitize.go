package integrations

import (
	"regexp"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/mask"
)

// MaxErrorBytes bounds integration.error strings.
const MaxErrorBytes = 256

var (
	// scheme://user:pass@ → scheme://user:***@
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://[^\s:/@]*:)[^\s@]+@`)
	// user:password@tcp(host:3306)/db and user:password@unix(/path) (go-sql-driver DSN).
	dsnUserinfo = regexp.MustCompile(`([^\s:@/(]+):[^\s@]+@(tcp|unix)?\(`)
	// password=secret inside libpq/ODBC-style connection strings and JSON "password":"x".
	kvPassword   = regexp.MustCompile(`(?i)\b(password|passwd|pwd|sslpassword)=('[^']*'|"[^"]*"|[^\s;&]+)`)
	jsonPassword = regexp.MustCompile(`(?i)("(?:password|passwd|pwd)"\s*:\s*)"[^"]*"`)
	whitespace   = regexp.MustCompile(`\s+`)
)

// Sanitize turns an error into a status message that is safe to send: configured
// secret values are replaced, URL/DSN userinfo and password fields are masked,
// whitespace is collapsed and the result is truncated to MaxErrorBytes. Server
// messages such as "(using password: YES)" stay readable.
func Sanitize(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	return SanitizeString(err.Error(), secrets...)
}

// SanitizeString is Sanitize for a message.
func SanitizeString(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, mask.Redacted)
		}
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}"+mask.Redacted+"@")
	s = dsnUserinfo.ReplaceAllString(s, "${1}:"+mask.Redacted+"@${2}(")
	s = kvPassword.ReplaceAllString(s, "${1}="+mask.Redacted)
	s = jsonPassword.ReplaceAllString(s, `${1}"`+mask.Redacted+`"`)
	s = strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
	return mask.Truncate(s, MaxErrorBytes)
}
