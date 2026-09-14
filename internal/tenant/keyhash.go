package tenant

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
)

// MinKeyHashSecretLen is the minimum length of OPENLOG_KEY_HASH_SECRET (bytes).
const MinKeyHashSecretLen = 32

// KeyHasher hashes ingest license keys and API keys for storage and lookup (D-044).
//
// With a secret the stored hash is HMAC-SHA256(secret, key), so a leaked database dump alone does not allow
// offline guessing of imported (operator-chosen) key values. Without a secret it is SHA-256(key), the format
// of every row written before OPENLOG_KEY_HASH_SECRET existed. A nil *KeyHasher behaves like one without a
// secret.
type KeyHasher struct {
	current  []byte
	previous []byte
}

// NewKeyHasher returns a hasher for secret (may be empty) and the previous secret during a rotation (may be
// empty).
func NewKeyHasher(secret, previous string) *KeyHasher {
	h := &KeyHasher{}
	if secret != "" {
		h.current = []byte(secret)
		if previous != "" && previous != secret {
			h.previous = []byte(previous)
		}
	}
	return h
}

// Keyed reports whether a server-side secret is configured.
func (h *KeyHasher) Keyed() bool { return h != nil && len(h.current) > 0 }

// Hash returns the hash new rows are stored with.
func (h *KeyHasher) Hash(key string) []byte {
	if !h.Keyed() {
		return sha256Sum(key)
	}
	return hmacSum(h.current, key)
}

// Candidates returns every hash a stored key with this value may have, current format first: HMAC with the
// current secret, HMAC with the previous secret, plain SHA-256. A store that finds a row by a later candidate
// rewrites it to Candidates[0] (transparent migration).
func (h *KeyHasher) Candidates(key string) [][]byte {
	if !h.Keyed() {
		return [][]byte{sha256Sum(key)}
	}
	out := [][]byte{hmacSum(h.current, key)}
	if len(h.previous) > 0 {
		out = append(out, hmacSum(h.previous, key))
	}
	return append(out, sha256Sum(key))
}

// Legacy returns Candidates without the current hash (hashes that must also be treated as "this value exists").
func (h *KeyHasher) Legacy(key string) [][]byte { return h.Candidates(key)[1:] }

// MatchIndex returns the index of hash in candidates, or -1.
func MatchIndex(candidates [][]byte, hash []byte) int {
	for i, c := range candidates {
		if bytes.Equal(c, hash) {
			return i
		}
	}
	return -1
}

func sha256Sum(key string) []byte {
	s := sha256.Sum256([]byte(key))
	return s[:]
}

func hmacSum(secret []byte, key string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(key))
	return m.Sum(nil)
}
