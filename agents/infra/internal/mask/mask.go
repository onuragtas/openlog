// Package mask removes secrets from command lines before they leave the host
// (semantic-conventions §3.5). Masking is mandatory for process cmdline and
// systemd ExecStart values.
package mask

import (
	"path"
	"regexp"
	"strings"
)

// Redacted replaces every masked value.
const Redacted = "***"

var (
	// scheme://user:pass@ → scheme://user:***@
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://[^\s:/@]*:)[^\s@]+@`)
	// --password=x, --password x, -token x, --api-key=x, ...
	secretFlag     = regexp.MustCompile(`(?i)(--?(?:password|passwd|pwd|secret|token|api[-_]?key|auth|credentials?))(=|\s+)\S+`)
	secretFlagName = regexp.MustCompile(`(?i)^--?(?:password|passwd|pwd|secret|token|api[-_]?key|auth|credentials?)$`)
	// KEY=value token (optionally prefixed by dashes, e.g. -Dx.keyStorePassword=v).
	keyValue = regexp.MustCompile(`^(-{0,2}[A-Za-z_][A-Za-z0-9_.\-]*)=(.+)$`)
	// Keys naming a secret, unless they merely point at a file/path/dir.
	secretKey    = regexp.MustCompile(`(?i)(pass|secret|token|key|auth)`)
	nonSecretKey = regexp.MustCompile(`(?i)(file|path|dir)$`)
	// Executables whose attached -p<value> is a password (MySQL client style).
	mysqlExe = regexp.MustCompile(`^(mysql|mysqldump|mysqladmin|mysqlimport|mysqlcheck|mysqlpump|mariadb.*)$`)
	token    = regexp.MustCompile(`\S+`)
)

// Cmdline masks secrets in a space-joined command line. exe is the resolved
// executable path if known (may be empty); the attached -p<value> rule applies
// when the basename of exe or of argv[0] is a MySQL/MariaDB client.
func Cmdline(exe, s string) string {
	if s == "" {
		return s
	}
	mysqlStyle := isMySQLExe(exe)
	if argv0, _, _ := strings.Cut(strings.TrimSpace(s), " "); !mysqlStyle && argv0 != "" {
		mysqlStyle = isMySQLExe(argv0)
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}"+Redacted+"@")
	s = secretFlag.ReplaceAllString(s, "${1}${2}"+Redacted)
	return token.ReplaceAllStringFunc(s, func(tok string) string { return maskToken(tok, mysqlStyle) })
}

// Text masks secrets in free text such as log lines, using the same patterns
// as Cmdline except the MySQL-specific -p<value> rule (the program is unknown).
func Text(s string) string {
	if s == "" {
		return s
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}"+Redacted+"@")
	s = secretFlag.ReplaceAllString(s, "${1}${2}"+Redacted)
	return token.ReplaceAllStringFunc(s, func(tok string) string { return maskToken(tok, false) })
}

func isMySQLExe(p string) bool {
	if p == "" {
		return false
	}
	// systemd ExecStart prefixes such as "-", "@", "+", "!" are not part of the path.
	p = strings.TrimLeft(p, "-@+!:")
	return mysqlExe.MatchString(path.Base(p))
}

func maskToken(tok string, mysqlStyle bool) string {
	if strings.Contains(tok, Redacted) {
		return tok
	}
	if m := keyValue.FindStringSubmatch(tok); m != nil && secretKey.MatchString(m[1]) && !nonSecretKey.MatchString(m[1]) {
		return m[1] + "=" + Redacted
	}
	// MySQL style -p<password>: single dash, value directly attached. A bare
	// secret flag name (e.g. "-password" whose value was masked) is kept.
	if mysqlStyle && len(tok) > 2 && strings.HasPrefix(tok, "-p") && !secretFlagName.MatchString(tok) {
		return "-p" + Redacted
	}
	return tok
}

// Truncate shortens s to at most n bytes without splitting a UTF-8 sequence.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
