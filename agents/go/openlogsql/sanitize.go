package openlogsql

import (
	"strings"
	"unicode/utf8"
)

// Sanitize normalizes a SQL statement for db.query.text so it carries no literal values
// and statements differing only in values group together:
//
//   - string literals ('…', E'…', and "…" for MySQL) → ?
//   - numeric, hex and boolean-free literals → ? (identifiers such as t1 or col_2 are kept)
//   - IN (?, ?, ?) and VALUES (?, ?), (?, ?) lists collapse to IN (?) / VALUES (?)
//   - comments are removed and whitespace is collapsed
//
// Placeholders ($1, ?, :name, @p1) are kept. system is the db.system.name value; it only
// changes how double-quoted text is treated (identifier except for mysql).
func Sanitize(q, system string) string {
	dqString := system == "mysql"
	var b strings.Builder
	b.Grow(len(q))
	space := false
	emit := func(s string) {
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteString(s)
	}
	n := len(q)
	for i := 0; i < n; {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
			i++
		case c == '-' && i+1 < n && q[i+1] == '-':
			for i < n && q[i] != '\n' {
				i++
			}
			space = true
		case c == '#' && system == "mysql":
			for i < n && q[i] != '\n' {
				i++
			}
			space = true
		case c == '/' && i+1 < n && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				i = n
			} else {
				i += j + 4
			}
			space = true
		case c == '\'' || (c == '"' && dqString) ||
			((c == 'E' || c == 'e' || c == 'N' || c == 'n' || c == 'X' || c == 'x' || c == 'B' || c == 'b') && i+1 < n && q[i+1] == '\'' && !identByteBefore(q, i)):
			if c != '\'' && c != '"' {
				i++ // prefix
			}
			quote := q[i]
			i++
			for i < n {
				if q[i] == '\\' && i+1 < n && quote == '\'' && (c == 'E' || c == 'e' || system == "mysql") {
					i += 2
					continue
				}
				if q[i] == quote {
					if i+1 < n && q[i+1] == quote { // doubled quote escape
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			emit("?")
		case c == '$' && i+1 < n && q[i+1] == '$': // dollar-quoted string $$…$$
			j := strings.Index(q[i+2:], "$$")
			if j < 0 {
				i = n
			} else {
				i += j + 4
			}
			emit("?")
		case c == '"' || c == '`' || c == '[':
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := strings.IndexByte(q[i+1:], closer)
			if j < 0 {
				emit(q[i:])
				i = n
			} else {
				emit(q[i : i+j+2])
				i += j + 2
			}
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(q[i+1])):
			if identByteBefore(q, i) {
				emit(string(c))
				i++
				continue
			}
			// optional sign was already emitted as an operator; consume the number
			if c == '0' && i+1 < n && (q[i+1] == 'x' || q[i+1] == 'X') {
				i += 2
				for i < n && isHex(q[i]) {
					i++
				}
			} else {
				for i < n && (isDigit(q[i]) || q[i] == '.') {
					i++
				}
				if i < n && (q[i] == 'e' || q[i] == 'E') {
					k := i + 1
					if k < n && (q[k] == '+' || q[k] == '-') {
						k++
					}
					if k < n && isDigit(q[k]) {
						i = k
						for i < n && isDigit(q[i]) {
							i++
						}
					}
				}
			}
			emit("?")
		case (c == '$' || c == '@' || c == ':') && i+1 < n && (isDigit(q[i+1]) || isIdentStart(q[i+1])) && !(c == ':' && i > 0 && q[i-1] == ':'):
			j := i + 1
			for j < n && isIdent(q[j]) {
				j++
			}
			emit(q[i:j])
			i = j
		case isIdentStart(c) || c >= utf8.RuneSelf:
			j := i + 1
			for j < n && (isIdent(q[j]) || q[j] >= utf8.RuneSelf) {
				j++
			}
			emit(q[i:j])
			i = j
		default:
			if c == ',' || c == ')' {
				space = false
			}
			emit(string(c))
			if c == '(' {
				space = false
			}
			i++
		}
	}
	return collapseLists(b.String())
}

// collapseLists turns "(?, ?, ?)" into "(?)" and "VALUES (?), (?)" into "VALUES (?)".
func collapseLists(s string) string {
	if !strings.Contains(s, "?,") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '(' {
			j := i + 1
			ok := false
			for j < len(s) {
				if s[j] == '?' {
					j++
					ok = true
					if j < len(s) && s[j] == ',' {
						j++
						if j < len(s) && s[j] == ' ' {
							j++
						}
						continue
					}
				}
				break
			}
			if ok && j < len(s) && s[j] == ')' {
				b.WriteString("(?)")
				i = j + 1
				// repeated tuples: ", (?, ?)"
				for {
					k := i
					if k < len(s) && s[k] == ',' {
						k++
						if k < len(s) && s[k] == ' ' {
							k++
						}
						if k < len(s) && s[k] == '(' {
							m := k + 1
							for m < len(s) && (s[m] == '?' || s[m] == ',' || s[m] == ' ') {
								m++
							}
							if m < len(s) && s[m] == ')' && m > k+1 {
								i = m + 1
								continue
							}
						}
					}
					break
				}
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHex(c byte) bool        { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdent(c byte) bool      { return isIdentStart(c) || isDigit(c) || c == '$' }

func identByteBefore(q string, i int) bool {
	return i > 0 && (isIdent(q[i-1]) || q[i-1] >= utf8.RuneSelf)
}
