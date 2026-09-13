package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// Visible prefixes of generated secrets, so leaked keys are recognizable
// (secret scanners) and users can tell key types apart.
const (
	PrefixLicenseKey = "olk_"
	PrefixAPIKey     = "ola_"
	PrefixInvitation = "oli_"
)

// secretBytes is the entropy of generated keys and tokens (192 bits).
const secretBytes = 24

// NewSecret returns prefix + 48 lowercase hex characters of random data.
func NewSecret(prefix string) (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// HashSecret returns SHA-256(secret). Secrets are high-entropy random values,
// so a fast unsalted hash is sufficient; only the hash is stored.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// DisplayPrefix is the non-secret part of a key shown in listings: the type
// prefix plus the first 8 characters (e.g. "olk_1a2b3c4d").
func DisplayPrefix(secret string) string {
	for _, p := range []string{PrefixLicenseKey, PrefixAPIKey, PrefixInvitation} {
		// Generated keys have 48 characters after the prefix; shorter operator-chosen
		// values that merely start with a type prefix fall through to the half rule.
		if len(secret) >= len(p)+24 && secret[:len(p)] == p {
			return secret[:len(p)+8]
		}
	}
	// Operator-chosen keys (bootstrap): never reveal more than half.
	return secret[:min(8, len(secret)/2)]
}

// Length limits of operator-chosen ("custom") ingest license key values.
const (
	MinCustomKeyLen = 16  // POST /api/v1/license-keys with "key"
	MaxCustomKeyLen = 256 // also applies to bootstrap keys
)

// ValidateCustomKey checks an operator-chosen key value (already trimmed):
// minLen–MaxCustomKeyLen characters of printable ASCII without spaces, quotes
// or backslashes, so it fits HTTP headers, gRPC metadata and shell arguments
// (install.sh). Bootstrap passes MinProvidedKeyLen as minLen.
func ValidateCustomKey(key string, minLen int) error {
	if len(key) < minLen || len(key) > MaxCustomKeyLen {
		return invalid("key must be %d–%d characters", minLen, MaxCustomKeyLen)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x21 || c > 0x7e || c == '"' || c == '\'' || c == '\\' {
			return invalid("key may contain only printable ASCII characters without spaces, quotes or backslashes")
		}
	}
	return nil
}

// newOpaqueToken returns 32 random bytes, base64url encoded (session and CSRF tokens).
func newOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
