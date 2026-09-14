// Package oql implements OQL, openlog's NRQL-like query language (docs/contracts/oql.md): a hand-written
// lexer and recursive-descent parser produce an AST, Compile validates it against a per-event-type attribute
// whitelist into a Plan, and the Plan is translated into parameterized ClickHouse SQL through
// internal/api/query, which always adds the tenant predicate. No text of the query is ever copied into SQL:
// identifiers are looked up in fixed tables and every literal is a bound parameter.
package oql

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Size limits of the query text (oql.md §5).
const (
	MaxQueryBytes  = 8192
	maxStringBytes = 4096
	maxTokens      = 4000
	maxDepth       = 20
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokQuotedIdent
	tokString
	tokNumber
	tokVariable
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokComma
	tokStar
	tokMinus
	tokEq
	tokNeq
	tokLt
	tokLte
	tokGt
	tokGte
)

var tokenNames = map[tokenKind]string{
	tokEOF: "end of query", tokIdent: "identifier", tokQuotedIdent: "quoted identifier", tokString: "string",
	tokNumber: "number", tokVariable: "variable", tokLParen: "'('", tokRParen: "')'", tokLBracket: "'['",
	tokRBracket: "']'", tokComma: "','", tokStar: "'*'", tokMinus: "'-'", tokEq: "'='", tokNeq: "'!='",
	tokLt: "'<'", tokLte: "'<='", tokGt: "'>'", tokGte: "'>='",
}

// token is one lexical token. text is the decoded value for strings, quoted identifiers and variables and the
// source text otherwise; pos/end are byte offsets in the query.
type token struct {
	kind     tokenKind
	text     string
	pos, end int
}

func (t token) describe() string {
	switch t.kind {
	case tokIdent, tokNumber:
		return fmt.Sprintf("%q", t.text)
	case tokString:
		return "string"
	}
	return tokenNames[t.kind]
}

// Error is a syntax or validation error at a byte range of the query.
type Error struct {
	Msg    string
	Offset int
	Length int
}

func (e *Error) Error() string { return e.Msg }

func errAt(pos, end int, format string, args ...any) *Error {
	if end < pos {
		end = pos
	}
	return &Error{Msg: fmt.Sprintf(format, args...), Offset: pos, Length: end - pos}
}

// Diagnostic is an error or warning with its position (1-based line and column in characters).
type Diagnostic struct {
	Message string `json:"message"`
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

// Position returns the 1-based line and column (in characters) of a byte offset.
func Position(src string, offset int) (line, column int) {
	if offset > len(src) {
		offset = len(src)
	}
	if offset < 0 {
		offset = 0
	}
	line, column = 1, 1
	for _, r := range src[:offset] {
		if r == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return line, column
}

// Diagnose converts an error of Parse/Compile into a Diagnostic (errors without position point at the start).
func Diagnose(src string, err error) Diagnostic {
	d := Diagnostic{Message: err.Error()}
	if e, ok := err.(*Error); ok {
		d.Offset, d.Length = e.Offset, e.Length
	}
	d.Line, d.Column = Position(src, d.Offset)
	return d
}

// Describe renders err with its position ("line L, column C: message").
func Describe(src string, err error) string {
	d := Diagnose(src, err)
	return fmt.Sprintf("line %d, column %d: %s", d.Line, d.Column, d.Message)
}

func isIdentStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '.' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }

func lex(src string) ([]token, error) {
	if len(src) > MaxQueryBytes {
		return nil, errAt(MaxQueryBytes, len(src), "query is longer than %d bytes", MaxQueryBytes)
	}
	if !utf8.ValidString(src) {
		return nil, errAt(0, 0, "query is not valid UTF-8")
	}
	var toks []token
	i := 0
	for {
		// whitespace and comments
		for i < len(src) {
			c := src[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				i++
				continue
			}
			if (c == '-' && strings.HasPrefix(src[i:], "--")) || (c == '/' && strings.HasPrefix(src[i:], "//")) {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			break
		}
		if len(toks) >= maxTokens {
			return nil, errAt(i, i, "query has more than %d tokens", maxTokens)
		}
		if i >= len(src) {
			toks = append(toks, token{kind: tokEOF, pos: len(src), end: len(src)})
			return toks, nil
		}
		start := i
		c := src[i]
		switch {
		case isIdentStart(c):
			for i < len(src) && isIdentPart(src[i]) {
				i++
			}
			toks = append(toks, token{kind: tokIdent, text: src[start:i], pos: start, end: i})
		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1])):
			for i < len(src) && isDigit(src[i]) {
				i++
			}
			if i < len(src) && src[i] == '.' {
				i++
				for i < len(src) && isDigit(src[i]) {
					i++
				}
			}
			if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
				j := i + 1
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				if j < len(src) && isDigit(src[j]) {
					i = j
					for i < len(src) && isDigit(src[i]) {
						i++
					}
				}
			}
			if i < len(src) && isIdentStart(src[i]) && !startsUnit(src[i:]) {
				return nil, errAt(start, i+1, "invalid number")
			}
			toks = append(toks, token{kind: tokNumber, text: src[start:i], pos: start, end: i})
		case c == '\'' || c == '"':
			s, n, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			i = n
			toks = append(toks, token{kind: tokString, text: s, pos: start, end: i})
		case c == '`':
			j := strings.IndexByte(src[i+1:], '`')
			if j < 0 {
				return nil, errAt(start, len(src), "unterminated quoted identifier")
			}
			name := src[i+1 : i+1+j]
			i += j + 2
			if name == "" || len(name) > 256 || strings.ContainsFunc(name, isControl) {
				return nil, errAt(start, i, "quoted identifier must be 1-256 bytes without control characters")
			}
			toks = append(toks, token{kind: tokQuotedIdent, text: name, pos: start, end: i})
		case c == '{' && strings.HasPrefix(src[i:], "{{"):
			j := strings.Index(src[i:], "}}")
			if j < 0 {
				return nil, errAt(start, len(src), "unterminated variable")
			}
			name := strings.TrimSpace(src[i+2 : i+j])
			i += j + 2
			if !validVariableName(name) {
				return nil, errAt(start, i, "invalid variable name %q", truncate(name, 64))
			}
			toks = append(toks, token{kind: tokVariable, text: name, pos: start, end: i})
		default:
			kind, n := tokEOF, 1
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch {
			case two == "!=" || two == "<>":
				kind, n = tokNeq, 2
			case two == "<=":
				kind, n = tokLte, 2
			case two == ">=":
				kind, n = tokGte, 2
			case two == "==":
				kind, n = tokEq, 2
			default:
				switch c {
				case '(':
					kind = tokLParen
				case ')':
					kind = tokRParen
				case '[':
					kind = tokLBracket
				case ']':
					kind = tokRBracket
				case ',':
					kind = tokComma
				case '*':
					kind = tokStar
				case '-':
					kind = tokMinus
				case '=':
					kind = tokEq
				case '<':
					kind = tokLt
				case '>':
					kind = tokGt
				}
			}
			if kind == tokEOF {
				r, size := utf8.DecodeRuneInString(src[i:])
				return nil, errAt(start, start+size, "unexpected character %q", r)
			}
			i += n
			toks = append(toks, token{kind: kind, text: src[start:i], pos: start, end: i})
		}
	}
}

// startsUnit allows "5minutes" style durations without a space.
func startsUnit(s string) bool {
	j := 0
	for j < len(s) && isIdentStart(s[j]) {
		j++
	}
	_, ok := durationUnits[strings.ToLower(s[:j])]
	return ok
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func lexString(src string, i int) (string, int, error) {
	quote := src[i]
	start := i
	i++
	var b strings.Builder
	for {
		if i >= len(src) {
			return "", 0, errAt(start, len(src), "unterminated string")
		}
		c := src[i]
		switch {
		case c == quote:
			if i+1 < len(src) && src[i+1] == quote {
				b.WriteByte(quote)
				i += 2
				continue
			}
			i++
			if b.Len() > maxStringBytes {
				return "", 0, errAt(start, i, "string is longer than %d bytes", maxStringBytes)
			}
			return b.String(), i, nil
		case c == '\\':
			if i+1 >= len(src) {
				return "", 0, errAt(start, len(src), "unterminated string")
			}
			switch src[i+1] {
			case '\\', '\'', '"':
				b.WriteByte(src[i+1])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", 0, errAt(i, i+2, "invalid escape sequence")
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
		if b.Len() > maxStringBytes+4 {
			return "", 0, errAt(start, i, "string is longer than %d bytes", maxStringBytes)
		}
	}
}

func validVariableName(s string) bool {
	if s == "" || len(s) > 64 || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentStart(s[i]) && !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
