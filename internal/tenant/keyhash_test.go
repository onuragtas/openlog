package tenant

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
	"time"
)

func TestKeyHasher(t *testing.T) {
	plain := sha256.Sum256([]byte("olk_value"))
	var nilHasher *KeyHasher
	for _, h := range []*KeyHasher{nilHasher, NewKeyHasher("", "ignored-without-a-current-secret")} {
		if h.Keyed() || !bytes.Equal(h.Hash("olk_value"), plain[:]) {
			t.Fatalf("unkeyed hash must be SHA-256")
		}
		if c := h.Candidates("olk_value"); len(c) != 1 || !bytes.Equal(c[0], plain[:]) {
			t.Fatalf("unkeyed candidates %x", c)
		}
		if len(h.Legacy("olk_value")) != 0 {
			t.Fatal("unkeyed legacy hashes")
		}
	}

	cur := "0123456789abcdef0123456789abcdef-current"
	prev := "0123456789abcdef0123456789abcdef-previous"
	mac := func(secret string) []byte {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte("olk_value"))
		return m.Sum(nil)
	}
	h := NewKeyHasher(cur, "")
	if !h.Keyed() || !bytes.Equal(h.Hash("olk_value"), mac(cur)) {
		t.Fatal("keyed hash must be HMAC-SHA256(secret, key)")
	}
	if c := h.Candidates("olk_value"); len(c) != 2 || !bytes.Equal(c[0], mac(cur)) || !bytes.Equal(c[1], plain[:]) {
		t.Fatalf("candidates without previous %x", c)
	}
	h = NewKeyHasher(cur, prev)
	c := h.Candidates("olk_value")
	if len(c) != 3 || !bytes.Equal(c[1], mac(prev)) || MatchIndex(c, plain[:]) != 2 || MatchIndex(c, []byte("x")) != -1 {
		t.Fatalf("rotation candidates %x", c)
	}
	if len(NewKeyHasher(cur, cur).Candidates("k")) != 2 {
		t.Fatal("previous equal to current must be ignored")
	}
}

// The cache passes every candidate to the store on a miss and never re-hashes on a hit.
func TestCachedUsesHasherCandidates(t *testing.T) {
	f := newFake()
	h := NewKeyHasher("0123456789abcdef0123456789abcdef", "")
	f.mu.Lock()
	f.keys[string(h.Candidates("legacy-key")[1])] = KeyInfo{KeyID: "id1", TenantID: "t1"} // stored as plain SHA-256
	f.mu.Unlock()
	c, _ := newCache(f, CacheOptions{TTL: time.Minute, Hasher: h})
	for i := 0; i < 3; i++ {
		if got, err := c.Resolve(ctx, "legacy-key"); err != nil || got != "t1" {
			t.Fatalf("Resolve = %q %v", got, err)
		}
	}
	if f.count() != 1 {
		t.Fatalf("lookups = %d, want 1", f.count())
	}
}
