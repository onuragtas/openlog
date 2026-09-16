package logpattern

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cespare/xxhash/v2"
)

func TestMask(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// Numbers, ids and addresses.
		{"user 4711 logged in from 10.0.0.3", "user <*> logged in from <*>"},
		{"user 12 logged in from 192.168.1.7", "user <*> logged in from <*>"},
		{"request 0f8a2b3c-1d4e-4f6a-9b8c-2d3e4f5a6b7c failed", "request <*> failed"},
		{"trace deadbeefcafebabe started", "trace <*> started"},
		{"took 12.5ms", "took <*>"},
		// Quoted runs are one token even with spaces inside.
		{`msg "connection reset"`, "msg <*>"},
		{`level=error msg="upstream timed out" retries=3`, "level=error msg=<*> retries=<*>"},
		// Emails.
		{"mail to ada@example.com queued", "mail to <*> queued"},
		// Paths with digits are values; paths without them are part of the message.
		{"rotating /var/log/app-12/out.log", "rotating <*>"},
		{"reading /etc/hosts", "reading /etc/hosts"},
		// key=value keeps the key.
		{"timeout=30s retries=3 handler=checkout", "timeout=<*> retries=<*> handler=checkout"},
		// Punctuation around a value is kept so templates stay readable.
		{"failed after 3s.", "failed after <*>."},
		{"worker (7) stopped", "worker (<*>) stopped"},
		// Words are never masked.
		{"disk full", "disk full"},
		{"", ""},
		{"   ", ""},
	} {
		if got := Mask(tc.in); got != tc.want {
			t.Errorf("Mask(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPatternIDIsTheTemplateHashAndDoesNotDependOnHistory(t *testing.T) {
	s := NewStore(0, 0)
	id, tmpl := s.Pattern("t1", "user 4711 logged in from 10.0.0.3")
	if tmpl != "user <*> logged in from <*>" {
		t.Fatalf("template %q", tmpl)
	}
	if id != xxhash.Sum64String(tmpl) {
		t.Errorf("id %d is not the hash of the template (%d)", id, xxhash.Sum64String(tmpl))
	}
	// Other values of the same shape give the same id and template.
	if got, gotT := s.Pattern("t1", "user 99 logged in from 10.1.2.3"); got != id || gotT != tmpl {
		t.Errorf("id %d template %q, want %d %q", got, gotT, id, tmpl)
	}
	// A store that has seen nothing else, and a store that has seen a lot, agree: the id is a pure function of the
	// message. This is what lets several processors stamp the same id on the same line without coordinating.
	fresh := NewStore(0, 0)
	if got, _ := fresh.Pattern("other-tenant", "user 1 logged in from 8.8.8.8"); got != id {
		t.Errorf("fresh store id %d, want %d", got, id)
	}
	busy := NewStore(0, 0)
	for i := range 100 {
		busy.Pattern("t1", "unrelated message number "+strconv.Itoa(i)+" here")
	}
	if got, _ := busy.Pattern("t1", "user 7 logged in from 1.2.3.4"); got != id {
		t.Errorf("busy store id %d, want %d", got, id)
	}
	if n := s.Templates("t1"); n != 1 {
		t.Errorf("templates = %d, want 1", n)
	}
}

func TestSimilarMessagesShareAPatternAndDissimilarDoNot(t *testing.T) {
	s := NewStore(0, 0)
	id := func(msg string) uint64 {
		v, _ := s.Pattern("t1", msg)
		return v
	}
	// Same shape, different values: one pattern.
	a := id("GET /api/orders 200 in 12ms")
	if b := id("GET /api/orders 500 in 91ms"); b != a {
		t.Errorf("same shape gave two patterns: %d != %d", a, b)
	}
	// Different messages stay apart.
	full := id("disk full on /dev/sda")
	refused := id("connection refused by upstream")
	if full == refused || full == a {
		t.Error("dissimilar messages share a pattern")
	}
	// Documented limit: a difference in an unmasked *word* is a different pattern (no adaptive merging, see the
	// package doc). Masking is what collapses values.
	if id("worker started for queue orders") == id("worker started for queue emails") {
		t.Error("unmasked words are expected to separate patterns")
	}
	if n := s.Templates("t1"); n != 5 {
		t.Errorf("templates = %d, want 5", n)
	}
}

func TestTenantIsNotPartOfTheID(t *testing.T) {
	s := NewStore(0, 0)
	id1, _ := s.Pattern("t1", "user 1 logged in")
	id2, _ := s.Pattern("t2", "user 2 logged in")
	if id1 != id2 {
		t.Errorf("the same template must get the same id in every tenant: %d != %d", id1, id2)
	}
	if s.Templates("t1") != 1 || s.Templates("t2") != 1 || s.Tenants() != 2 {
		t.Errorf("tenants kept separately: %d %d %d", s.Templates("t1"), s.Templates("t2"), s.Tenants())
	}
}

func TestClusterCapIsPerTenant(t *testing.T) {
	const max = 16
	s := NewStore(max, 0)
	// Distinct word-only messages: every one is its own template.
	for i := range 200 {
		s.Pattern("t1", "alpha"+strconv.Itoa(i)+" beta gamma delta epsilon zeta eta")
	}
	if n := s.Templates("t1"); n > max {
		t.Errorf("templates = %d, want at most %d", n, max)
	}
	s.Pattern("t2", "one two three four five six seven")
	if n := s.Templates("t2"); n != 1 {
		t.Errorf("second tenant templates = %d, want 1", n)
	}
	// Evicting a template does not change the id it gets when it comes back.
	want, _ := s.Pattern("t3", "steady message here")
	for i := range 100 {
		s.Pattern("t3", "noise"+strconv.Itoa(i)+" a b c d e")
	}
	if got, _ := s.Pattern("t3", "steady message here"); got != want {
		t.Errorf("id after eviction %d, want %d", got, want)
	}
}

func TestTenantCapEvictsLeastRecentlyUsed(t *testing.T) {
	s := NewStore(0, 4)
	for i := range 20 {
		s.Pattern("tenant-"+strconv.Itoa(i), "hello world")
	}
	if n := s.Tenants(); n > 4 {
		t.Errorf("tenants = %d, want at most 4", n)
	}
}

func TestLongMessageIsBounded(t *testing.T) {
	s := NewStore(0, 0)
	_, tmpl := s.Pattern("t1", strings.Repeat("word ", 500))
	if n := len(strings.Fields(tmpl)); n > maxTokens+1 {
		t.Errorf("template has %d tokens, want at most %d", n, maxTokens+1)
	}
}

// A record whose template is already known must not allocate: this runs on every log record.
func TestKnownPatternDoesNotAllocate(t *testing.T) {
	s := NewStore(0, 0)
	const msg = "user 4711 logged in from 10.0.0.3"
	s.Pattern("t1", msg)
	if n := testing.AllocsPerRun(200, func() { s.Pattern("t1", msg) }); n > 0 {
		t.Errorf("%v allocations per record, want 0", n)
	}
}

// The common case is a known template with new values on every line (ids, numbers): also allocation-free.
func TestNewValuesOfKnownPatternDoNotAllocate(t *testing.T) {
	s := NewStore(0, 0)
	msgs := make([]string, 64)
	for j := range msgs {
		msgs[j] = "user " + strconv.Itoa(j) + " logged in from 10.0.0." + strconv.Itoa(j)
	}
	s.Pattern("t1", msgs[0])
	i := 0
	n := testing.AllocsPerRun(200, func() {
		s.Pattern("t1", msgs[i%len(msgs)])
		i++
	})
	if n > 0 {
		t.Errorf("%v allocations per record, want 0", n)
	}
	if got := s.Templates("t1"); got != 1 {
		t.Errorf("templates = %d, want 1", got)
	}
}

func TestConcurrentUse(t *testing.T) {
	s := NewStore(0, 0)
	done := make(chan struct{})
	for g := range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 500 {
				s.Pattern("t"+strconv.Itoa(g%2), "user "+strconv.Itoa(i)+" logged in from 10.0.0.1")
			}
		}()
	}
	for range 4 {
		<-done
	}
	for _, tenant := range []string{"t0", "t1"} {
		if n := s.Templates(tenant); n != 1 {
			t.Errorf("%s: templates = %d, want 1", tenant, n)
		}
	}
}

func TestEmptyMessageHasNoPattern(t *testing.T) {
	s := NewStore(0, 0)
	for _, msg := range []string{"", "   ", "\n\t"} {
		if id, tmpl := s.Pattern("t1", msg); id != 0 || tmpl != "" {
			t.Errorf("Pattern(%q) = %d %q, want 0 \"\"", msg, id, tmpl)
		}
	}
	if s.Templates("t1") != 0 {
		t.Error("empty messages must not create a template")
	}
}

// A nil store is usable and derives nothing (Rows without SetPatterns).
func TestNilStore(t *testing.T) {
	var s *Store
	if id, tmpl := s.Pattern("t1", "user 1 logged in"); id != 0 || tmpl != "" {
		t.Errorf("nil store: %d %q", id, tmpl)
	}
}

func BenchmarkPattern(b *testing.B) {
	s := NewStore(0, 0)
	msgs := make([]string, 128)
	for i := range msgs {
		msgs[i] = "GET /api/orders/" + strconv.Itoa(i) + " 200 in " + strconv.Itoa(i) + "ms"
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		s.Pattern("t1", msgs[i%len(msgs)])
	}
}
