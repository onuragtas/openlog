// Package logpattern groups log messages into Drain-style templates ("user <*> logged in from <*>") so the Logs
// Explorer can show that 40 000 records are a handful of distinct messages, which of them are noisy, and which
// records belong to one of them (D-128).
//
// # The id is a pure function of the message
//
// The pattern id is the 64-bit hash of the masked template and nothing else. Classic Drain keeps a prefix tree of
// clusters and *generalizes* a cluster when a similar message arrives, so a template — and with it its id — changes
// as more data is seen. openlog cannot do that: the id is written on every log row, processors are horizontally
// scaled and share no state (D-006), and two processors that see the same messages in a different order would then
// generalize differently and stamp two different ids on the same logical pattern. Deriving the id from the message
// alone makes every processor, restart and release agree by construction, at the cost of not merging messages that
// differ in an unmasked *word* ("queue orders" and "queue emails" stay two patterns). Masking, not merging, is what
// collapses the high-cardinality parts.
//
// # Cost
//
// Extraction runs on every log record, so it stays allocation-light: messages are tokenized into a pooled buffer,
// variable tokens are masked by a hand-written classifier (no regular expressions), and the id is hashed from the
// tokens without building the template string. A record whose template is already known therefore costs one map
// lookup and no allocation; only a template seen for the first time allocates.
//
// Each tenant keeps at most maxClusters templates in an LRU, and the store keeps at most maxTenants tenants in an
// LRU of its own, so a processor's memory is bounded however many tenants send however noisy logs.
package logpattern

import (
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// Wildcard replaces a variable token in a template.
const Wildcard = "<*>"

// Defaults of a Store.
const (
	// DefaultMaxClusters is how many templates one tenant keeps (LRU).
	DefaultMaxClusters = 5000
	// DefaultMaxTenants is how many tenants one store keeps (LRU).
	DefaultMaxTenants = 1000
)

const (
	// maxTokens bounds the tokens of one message; everything after them becomes a single trailing wildcard.
	maxTokens = 128
	// maxTokenBytes is the longest token kept verbatim in a template (longer ones are values, not words).
	maxTokenBytes = 128
	// minHexLen is the shortest all-hex token without digits treated as an id ("deadbeefcafebabe").
	minHexLen = 8
)

// Store derives patterns for many tenants. The zero value is not usable; call NewStore. It is safe for concurrent
// use: the processor decodes and inserts several chunks at a time.
type Store struct {
	maxClusters int
	maxTenants  int

	mu      sync.Mutex
	tenants map[string]*tenantState
	// LRU of tenants, most recently used first.
	head, tail *tenantState
}

// NewStore creates a store. Non-positive limits fall back to the defaults.
func NewStore(maxClusters, maxTenants int) *Store {
	if maxClusters <= 0 {
		maxClusters = DefaultMaxClusters
	}
	if maxTenants <= 0 {
		maxTenants = DefaultMaxTenants
	}
	return &Store{maxClusters: maxClusters, maxTenants: maxTenants, tenants: map[string]*tenantState{}}
}

// Pattern returns the stable id and the template of message for tenant. An empty or whitespace-only message has no
// pattern (0, ""). The id is the hash of the template text, so the same message always yields the same id.
//
// The tenant only bounds how many templates are remembered; it is not part of the id, so the same template has the
// same id in every organization.
func (s *Store) Pattern(tenant, message string) (uint64, string) {
	if s == nil || strings.TrimSpace(message) == "" {
		return 0, ""
	}
	b := bufPool.Get().(*buffer)
	defer bufPool.Put(b)
	b.tokens = maskInto(b.tokens[:0], message)
	if len(b.tokens) == 0 {
		return 0, ""
	}
	id := hashTokens(b.tokens)

	ts := s.tenant(tenant)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if e, ok := ts.templates[id]; ok {
		ts.touch(e)
		return e.id, e.template
	}
	// First record of this template: build the string once and remember it.
	e := &entry{id: id, template: strings.Join(b.tokens, " ")}
	ts.templates[id] = e
	ts.push(e)
	if len(ts.templates) > ts.max && ts.tail != nil {
		last := ts.tail
		ts.unlink(last)
		delete(ts.templates, last.id)
	}
	return e.id, e.template
}

// Templates returns how many templates tenant currently keeps (tests and diagnostics).
func (s *Store) Templates(tenant string) int {
	s.mu.Lock()
	ts := s.tenants[tenant]
	s.mu.Unlock()
	if ts == nil {
		return 0
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.templates)
}

// Tenants returns how many tenants the store keeps.
func (s *Store) Tenants() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tenants)
}

// tenant returns the tenant's state, creating it and dropping the least recently used tenant when the store is full.
func (s *Store) tenant(name string) *tenantState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts, ok := s.tenants[name]; ok {
		s.touchTenant(ts)
		return ts
	}
	ts := &tenantState{name: name, max: s.maxClusters, templates: map[uint64]*entry{}}
	s.tenants[name] = ts
	s.pushTenant(ts)
	if len(s.tenants) > s.maxTenants && s.tail != nil {
		old := s.tail
		s.unlinkTenant(old)
		delete(s.tenants, old.name)
	}
	return ts
}

func (s *Store) pushTenant(ts *tenantState) {
	ts.next = s.head
	if s.head != nil {
		s.head.prev = ts
	}
	s.head = ts
	if s.tail == nil {
		s.tail = ts
	}
}

func (s *Store) unlinkTenant(ts *tenantState) {
	if ts.prev != nil {
		ts.prev.next = ts.next
	} else {
		s.head = ts.next
	}
	if ts.next != nil {
		ts.next.prev = ts.prev
	} else {
		s.tail = ts.prev
	}
	ts.prev, ts.next = nil, nil
}

func (s *Store) touchTenant(ts *tenantState) {
	if s.head == ts {
		return
	}
	s.unlinkTenant(ts)
	s.pushTenant(ts)
}

// tenantState is one tenant's template LRU.
type tenantState struct {
	name       string
	max        int
	prev, next *tenantState

	mu        sync.Mutex
	templates map[uint64]*entry
	// LRU of templates, most recently seen first.
	head, tail *entry
}

// entry is one remembered template.
type entry struct {
	id         uint64
	template   string
	prev, next *entry
}

func (ts *tenantState) push(e *entry) {
	e.next = ts.head
	if ts.head != nil {
		ts.head.prev = e
	}
	ts.head = e
	if ts.tail == nil {
		ts.tail = e
	}
}

func (ts *tenantState) unlink(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		ts.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		ts.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (ts *tenantState) touch(e *entry) {
	if ts.head == e {
		return
	}
	ts.unlink(e)
	ts.push(e)
}

// ---- masking ----

// buffer is a reusable token slice.
type buffer struct{ tokens []string }

var bufPool = sync.Pool{New: func() any { return &buffer{tokens: make([]string, 0, 32)} }}

// Mask returns the template of message: every variable token replaced by Wildcard. Two messages share a pattern
// exactly when their Mask is equal.
func Mask(message string) string {
	b := bufPool.Get().(*buffer)
	defer bufPool.Put(b)
	b.tokens = maskInto(b.tokens[:0], message)
	return strings.Join(b.tokens, " ")
}

// maskInto tokenizes message and appends the masked tokens to dst. Tokens are separated by whitespace, except that
// a quoted run is one token however many spaces it contains ("connection reset", msg='a b').
func maskInto(dst []string, message string) []string {
	i := 0
	for i < len(message) {
		for i < len(message) && isSpace(message[i]) {
			i++
		}
		if i >= len(message) {
			break
		}
		j := i
		for j < len(message) && !isSpace(message[j]) {
			if q := message[j]; q == '"' || q == '\'' || q == '`' {
				j++
				for j < len(message) && message[j] != q {
					j++
				}
				if j < len(message) {
					j++ // closing quote
				}
				continue
			}
			j++
		}
		if len(dst) == maxTokens {
			// A message with more tokens than this is a value dump, not a sentence: fold the rest.
			return append(dst, Wildcard)
		}
		dst = append(dst, maskToken(message[i:j]))
		i = j
	}
	return dst
}

// maskToken returns tok with its value part replaced by Wildcard, or tok itself when it is a word.
func maskToken(tok string) string {
	// key=value and key:value keep the key, which is what makes structured lines readable as templates.
	if i := keyEnd(tok); i > 0 {
		if variable(tok[i+1:]) {
			return tok[:i+1] + Wildcard
		}
	}
	prefix, core, suffix := trimPunct(tok)
	if core == "" {
		return tok
	}
	if variable(core) {
		if prefix == "" && suffix == "" {
			return Wildcard
		}
		return prefix + Wildcard + suffix
	}
	if len(tok) > maxTokenBytes {
		return Wildcard
	}
	return tok
}

// keyEnd returns the index of the '=' or ':' separating a key from a value, or -1. The key must be a plain word, so
// that "12:30:00" and "http://host" are classified as values instead of as a key with a value.
func keyEnd(tok string) int {
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if c == '=' || c == ':' {
			if i == 0 || i == len(tok)-1 {
				return -1
			}
			return i
		}
		if !isLetter(c) && c != '_' && c != '.' && c != '-' {
			return -1
		}
	}
	return -1
}

// trimPunct splits the punctuation around a token off, so it can be put back around the wildcard ("(7)" → "(<*>)").
func trimPunct(tok string) (prefix, core, suffix string) {
	s, e := 0, len(tok)
	for s < e && isOpen(tok[s]) {
		s++
	}
	for e > s && isClose(tok[e-1]) {
		e--
	}
	return tok[:s], tok[s:e], tok[e:]
}

// variable reports whether s is a value (a number, id, path, address or quoted string) rather than a word.
//
// The main rule is Drain's: a token that contains a digit is a value. One cheap pass therefore covers numbers,
// uuids, IPv4/IPv6 addresses, hex ids, timestamps, versions, paths with digits and host names with digits. Its cost
// is that words which legitimately contain a digit ("sha256", "utf8", "oauth2") are masked too; they are rare in the
// part of a message that distinguishes one template from another, and masking them only merges templates that would
// otherwise differ in that one token.
func variable(s string) bool {
	if len(s) == 0 {
		return false
	}
	if quoted(s) {
		return true
	}
	digit, letter, at, hex := false, false, false, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c == '@':
			at = true
			hex = false
		default:
			if isLetter(c) {
				letter = true
			}
			if !isHexLetter(c) {
				hex = false
			}
		}
	}
	switch {
	case digit:
		return true
	case at && letter: // emails and user@host
		return true
	case hex && letter && len(s) >= minHexLen: // hex ids that happen to contain no digit
		return true
	}
	return false
}

// quoted reports whether s is wrapped in matching quotes.
func quoted(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[0]
	return (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isHexLetter(c byte) bool { return c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func isOpen(c byte) bool { return c == '(' || c == '[' || c == '{' || c == '<' }

func isClose(c byte) bool {
	switch c {
	case ')', ']', '}', '>', ',', ';', ':', '.', '!', '?':
		return true
	}
	return false
}

// hashTokens returns the hash of the template the tokens would join into, without building that string.
func hashTokens(tokens []string) uint64 {
	var d xxhash.Digest
	d.Reset()
	for i, tok := range tokens {
		if i > 0 {
			_, _ = d.WriteString(" ")
		}
		_, _ = d.WriteString(tok)
	}
	return d.Sum64()
}
