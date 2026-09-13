// Package secrets encrypts alert channel secrets at rest with AES-256-GCM (docs/contracts/alerting.md §5.4).
//
// Stored form: "ol1:<key id>:<base64(nonce ‖ ciphertext ‖ tag)>". The key id is the first 8 hex characters of
// sha256(key). The additional authenticated data binds a ciphertext to its row (org id + "/" + channel id), so a
// value copied into another organization's channel does not decrypt.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const prefix = "ol1"

// ErrNoKey reports that OPENLOG_SECRETS_KEY is not configured.
var ErrNoKey = errors.New("OPENLOG_SECRETS_KEY is not configured")

// ErrUnknownKey reports a ciphertext encrypted with a key that is neither current nor previous.
var ErrUnknownKey = errors.New("secret encrypted with an unknown key")

// ErrDecrypt reports a ciphertext that does not authenticate (wrong key, wrong row, tampered).
var ErrDecrypt = errors.New("cannot decrypt secret")

type key struct {
	id   string
	aead cipher.AEAD
}

// Keyring holds the current encryption key and previous keys accepted for decryption.
// The zero value (and a nil *Keyring) has no key: Encrypt and Decrypt return ErrNoKey.
type Keyring struct {
	current *key
	all     map[string]*key
}

// ParseKey decodes a base64 (standard or URL, padded or not) 32-byte key.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("key must be 32 bytes, got %d (generate one with: openssl rand -base64 32)", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("key is not valid base64 (generate one with: openssl rand -base64 32)")
}

// KeyID returns the identifier stored next to ciphertexts of raw key k.
func KeyID(k []byte) string {
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:4])
}

func newKey(raw []byte) (*key, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &key{id: KeyID(raw), aead: aead}, nil
}

// NewKeyring builds a keyring from OPENLOG_SECRETS_KEY (current, may be empty) and
// OPENLOG_SECRETS_KEY_PREVIOUS (comma-separated, may be empty).
func NewKeyring(current, previous string) (*Keyring, error) {
	kr := &Keyring{all: map[string]*key{}}
	if strings.TrimSpace(current) != "" {
		raw, err := ParseKey(current)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SECRETS_KEY: %w", err)
		}
		k, err := newKey(raw)
		if err != nil {
			return nil, err
		}
		kr.current = k
		kr.all[k.id] = k
	}
	for i, p := range strings.Split(previous, ",") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		raw, err := ParseKey(p)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SECRETS_KEY_PREVIOUS[%d]: %w", i, err)
		}
		k, err := newKey(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := kr.all[k.id]; !dup {
			kr.all[k.id] = k
		}
	}
	if kr.current == nil && len(kr.all) > 0 {
		return nil, errors.New("OPENLOG_SECRETS_KEY_PREVIOUS is set but OPENLOG_SECRETS_KEY is empty")
	}
	return kr, nil
}

// Configured reports whether a current key is set.
func (kr *Keyring) Configured() bool { return kr != nil && kr.current != nil }

// CurrentKeyID returns the id of the encryption key ("" when not configured).
func (kr *Keyring) CurrentKeyID() string {
	if !kr.Configured() {
		return ""
	}
	return kr.current.id
}

// Encrypt seals plaintext for the row identified by aad. It returns the stored form and the key id.
func (kr *Keyring) Encrypt(plaintext []byte, aad string) (string, string, error) {
	if !kr.Configured() {
		return "", "", ErrNoKey
	}
	k := kr.current
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	sealed := k.aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return prefix + ":" + k.id + ":" + base64.StdEncoding.EncodeToString(sealed), k.id, nil
}

// Decrypt opens a stored value for the row identified by aad.
func (kr *Keyring) Decrypt(stored, aad string) ([]byte, error) {
	if kr == nil || len(kr.all) == 0 {
		return nil, ErrNoKey
	}
	parts := strings.SplitN(stored, ":", 3)
	if len(parts) != 3 || parts[0] != prefix {
		return nil, fmt.Errorf("%w: unknown format", ErrDecrypt)
	}
	k, ok := kr.all[parts[1]]
	if !ok {
		return nil, fmt.Errorf("%w (key id %s)", ErrUnknownKey, parts[1])
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < k.aead.NonceSize()+k.aead.Overhead() {
		return nil, fmt.Errorf("%w: malformed ciphertext", ErrDecrypt)
	}
	ns := k.aead.NonceSize()
	pt, err := k.aead.Open(nil, raw[:ns], raw[ns:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// KeyIDOf returns the key id of a stored value ("" if malformed).
func KeyIDOf(stored string) string {
	parts := strings.SplitN(stored, ":", 3)
	if len(parts) != 3 || parts[0] != prefix {
		return ""
	}
	return parts[1]
}

// NeedsRotation reports whether stored was encrypted with a key other than the current one.
func (kr *Keyring) NeedsRotation(stored string) bool {
	return stored != "" && kr.Configured() && KeyIDOf(stored) != kr.current.id
}
