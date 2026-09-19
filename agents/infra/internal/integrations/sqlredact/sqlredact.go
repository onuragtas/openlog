// Package sqlredact removes literal values from SQL text before it leaves the host (db-monitoring.md §3.4).
//
// Statement text read from a server's activity views (pg_stat_activity.query, MySQL QUERY_SAMPLE_TEXT, SQL Server
// sys.dm_exec_sql_text) carries the literal values an application sent — e-mail addresses, tokens, amounts. The
// agent replaces every string, number, hex/bit literal and dollar-quoted body with ? and drops comments (which carry
// sqlcommenter tags and sometimes secrets), keeps identifiers, keywords, operators and bind placeholders ($1, ?,
// :name, @p1) as they are, and collapses whitespace. It does not produce the canonical form: the backend normalizes
// every statement with the same function it applies to APM spans (IN lists, VALUES lists, booleans), so what the
// agent sends only has to be free of values and keep the statement's structure.
package sqlredact

import (
	"strings"
	"unicode/utf8"
)

// MaxBytes bounds the redacted text. The backend truncates the normalized form to 2048 bytes, so a statement longer
// than this may not match its APM counterpart; that is rare and documented.
const MaxBytes = 4096

// Redact returns text with literal values replaced by ?.
func Redact(text string) string {
	s := text
	var b strings.Builder
	b.Grow(min(len(s), MaxBytes))
	last := byte(' ')
	write := func(str string) {
		if str == "" {
			return
		}
		b.WriteString(str)
		last = str[len(str)-1]
	}
	space := func() {
		if b.Len() > 0 && last != ' ' {
			b.WriteByte(' ')
			last = ' '
		}
	}
	n := len(s)
	for i := 0; i < n && b.Len() < MaxBytes; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			space()
			i++
		case c == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
			space()
		case c == '#' && (i+1 >= n || s[i+1] == ' ' || s[i+1] == '\t'):
			for i < n && s[i] != '\n' {
				i++
			}
			space()
		case c == '/' && i+1 < n && s[i+1] == '*':
			if end := strings.Index(s[i+2:], "*/"); end < 0 {
				i = n
			} else {
				i += end + 4
			}
			space()
		case c == '\'' || (strings.IndexByte("EeNnXxBbUu", c) >= 0 && i+1 < n && s[i+1] == '\'' && !identChar(last)):
			if c != '\'' {
				i++
			}
			i = skipQuoted(s, i, '\'')
			write("?")
		case c == '"' || c == '`' || c == '[':
			// Quoted identifiers stay. MySQL in ANSI_QUOTES off mode uses "…" for strings too; those are values
			// only in a minority of applications and the backend treats "…" as an identifier as well.
			q := c
			if c == '[' {
				q = ']'
			}
			end := skipQuoted(s, i, q)
			write(s[i:end])
			i = end
		case c == '$':
			j := i + 1
			if j < n && s[j] >= '0' && s[j] <= '9' { // $1: a bind placeholder, kept
				for j < n && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				write(s[i:j])
				i = j
				continue
			}
			for j < n && (identStart(s[j]) || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			if j < n && s[j] == '$' && !identChar(last) { // $$…$$ or $tag$…$tag$
				tag := s[i : j+1]
				if end := strings.Index(s[j+1:], tag); end < 0 {
					i = n
				} else {
					i = j + 1 + end + len(tag)
				}
				write("?")
				continue
			}
			write("$")
			i++
		case c >= '0' && c <= '9' || (c == '.' && i+1 < n && s[i+1] >= '0' && s[i+1] <= '9' && !identChar(last)):
			if identChar(last) { // part of an identifier such as t1 or col2
				j := i
				for j < n && identChar(s[j]) {
					j++
				}
				write(s[i:j])
				i = j
				continue
			}
			i = skipNumber(s, i)
			write("?")
		case identStart(c):
			j := i + 1 // the start character itself may not be an identifier character (: of :name)
			for j < n && identChar(s[j]) {
				j++
			}
			write(s[i:j])
			i = j
		default:
			b.WriteByte(c)
			last = c
			i++
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > MaxBytes {
		cut := MaxBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

func identChar(c byte) bool {
	return c == '_' || c == '$' || c == '@' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func identStart(c byte) bool {
	return c == '_' || c == '@' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// skipQuoted returns the index after the closing quote of the literal starting at s[i]; doubled quotes and (for
// single-quoted strings) backslash escapes are part of the literal.
func skipQuoted(s string, i int, q byte) int {
	n := len(s)
	for j := i + 1; j < n; j++ {
		switch s[j] {
		case '\\':
			if q == '\'' {
				j++
			}
		case q:
			if j+1 < n && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return n
}

func skipNumber(s string, i int) int {
	n := len(s)
	j := i
	if s[j] == '0' && j+1 < n && (s[j+1] == 'x' || s[j+1] == 'X' || s[j+1] == 'b' || s[j+1] == 'B') {
		j += 2
		for j < n && (s[j] >= '0' && s[j] <= '9' || s[j] >= 'a' && s[j] <= 'f' || s[j] >= 'A' && s[j] <= 'F') {
			j++
		}
		return j
	}
	for j < n && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
		j++
	}
	if j < n && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < n && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if k < n && s[k] >= '0' && s[k] <= '9' {
			j = k
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
		}
	}
	return j
}
