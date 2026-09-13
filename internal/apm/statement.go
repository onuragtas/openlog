package apm

import (
	"regexp"
	"strings"
)

// MaxStatement bounds db_statement_normalized (bytes).
const MaxStatement = 2048

var kvSystems = map[string]bool{"redis": true, "memcached": true, "valkey": true, "keydb": true, "dragonfly": true}

var (
	inListRe  = regexp.MustCompile(`(?i)\b(IN)\s*\(\s*\?(?:\s*,\s*\?)*\s*\)`)
	valuesRe  = regexp.MustCompile(`(?i)\b(VALUES)\s*(\([^()]*\))(?:\s*,\s*\([^()]*\))+`)
	boolCmpRe = regexp.MustCompile(`(?i)([=<>]\s*)(true|false)\b`)
)

// NormalizeStatement replaces literals in a database statement (apm.md §7).
func NormalizeStatement(system, stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return ""
	}
	if kvSystems[strings.ToLower(system)] {
		fields := strings.Fields(stmt)
		out := strings.ToUpper(fields[0])
		if len(fields) > 1 {
			out += " ?"
		}
		return Truncate(out, MaxStatement)
	}
	s := scanSQL(stmt)
	s = inListRe.ReplaceAllString(s, "$1 (?)")
	s = valuesRe.ReplaceAllString(s, "$1 $2")
	s = boolCmpRe.ReplaceAllString(s, "$1?")
	s = strings.TrimSpace(s)
	if len(s) > MaxStatement {
		s = Truncate(s, MaxStatement-len("…")) + "…"
	}
	return s
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// scanSQL removes comments, replaces string/number literals and placeholders with ?
// and collapses whitespace. Quoted identifiers ("x", `x`) are kept.
func scanSQL(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	last := byte(' ') // last byte written
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
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space()
			i++
		case c == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
			space()
		case c == '#' && (i+1 >= n || s[i+1] == ' '):
			// MySQL comment
			for i < n && s[i] != '\n' {
				i++
			}
			space()
		case c == '/' && i+1 < n && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += end + 4
			}
			space()
		case c == '\'' || ((c == 'E' || c == 'e' || c == 'N' || c == 'n' || c == 'X' || c == 'x' || c == 'B' || c == 'b') && i+1 < n && s[i+1] == '\'' && !isIdentChar(last)):
			if c != '\'' {
				i++
			}
			i = skipQuoted(s, i, '\'')
			write("?")
		case c == '"' || c == '`':
			end := skipQuoted(s, i, c)
			write(s[i:end])
			i = end
		case c == '$':
			switch {
			case i+1 < n && s[i+1] >= '0' && s[i+1] <= '9':
				i++
				for i < n && s[i] >= '0' && s[i] <= '9' {
					i++
				}
				write("?")
			case isIdentChar(last) && last != ' ':
				write("$")
				i++
			default:
				// dollar quoting: $$...$$ or $tag$...$tag$
				j := i + 1
				for j < n && (isIdentStart(s[j]) || s[j] >= '0' && s[j] <= '9') {
					j++
				}
				if j < n && s[j] == '$' {
					tag := s[i : j+1]
					end := strings.Index(s[j+1:], tag)
					if end < 0 {
						i = n
					} else {
						i = j + 1 + end + len(tag)
					}
					write("?")
				} else {
					write("$")
					i++
				}
			}
		case c >= '0' && c <= '9':
			if isIdentChar(last) {
				j := i
				for j < n && isIdentChar(s[j]) {
					j++
				}
				write(s[i:j])
				i = j
				continue
			}
			j := i
			if c == '0' && j+1 < n && (s[j+1] == 'x' || s[j+1] == 'X') {
				j += 2
				for j < n && isHexChar(s[j]) {
					j++
				}
			} else {
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
			}
			write("?")
			i = j
		case (c == ':' || c == '@') && i+1 < n && isIdentStart(s[i+1]) && last != ':' && last != '@' && !(i > 0 && (s[i-1] == ':' || s[i-1] == '@')):
			j := i + 1
			for j < n && isIdentChar(s[j]) {
				j++
			}
			write("?")
			i = j
		case isIdentStart(c):
			j := i
			for j < n && isIdentChar(s[j]) {
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
	return b.String()
}

// skipQuoted returns the index after the closing quote of the literal starting at s[i]
// (doubled quotes and backslash escapes are part of the literal).
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
