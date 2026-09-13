package apm

import "strings"

const maxPathSegments = 8

// NormalizePath templates id-like path segments (apm.md §2.2).
func NormalizePath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	var out strings.Builder
	n := 0
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" {
			continue
		}
		if n == maxPathSegments {
			out.WriteString("/…")
			break
		}
		out.WriteByte('/')
		out.WriteString(normalizeSegment(seg))
		n++
	}
	if out.Len() == 0 {
		return "/"
	}
	return out.String()
}

func normalizeSegment(s string) string {
	switch {
	case isUUID(s):
		return "{uuid}"
	case isNumber(s):
		return "{id}"
	case isHexID(s):
		return "{hex}"
	case isEmail(s):
		return "{email}"
	case isToken(s):
		return "{token}"
	}
	return s
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !isHexChar(c) {
			return false
		}
	}
	return true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHexChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isHexID: hex of >= 16 chars, or >= 8 chars with both a digit and a letter a-f.
func isHexID(s string) bool {
	if len(s) < 8 {
		return false
	}
	digit, letter := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isHexChar(c) {
			return false
		}
		if c <= '9' {
			digit = true
		} else {
			letter = true
		}
	}
	return len(s) >= 16 || (digit && letter)
}

func isToken(s string) bool {
	if len(s) < 20 {
		return false
	}
	digit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-':
		default:
			return false
		}
	}
	return digit
}

func isEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && strings.IndexByte(s[at+1:], '.') > 0 && !strings.ContainsAny(s, " /")
}
