package sso

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// SecretBox seals the secrets an SSO connection needs in plaintext later (OIDC client secret, SAML SP private
// key) with AES-256-GCM. The associated data binds a ciphertext to its connection and purpose, so a sealed value
// copied into another row does not open.
//
// Format: 0x01 | key id (4 bytes) | nonce (12) | ciphertext+tag. Without a key (no OPENLOG_SSO_SECRET_KEY and no
// OPENLOG_KEY_HASH_SECRET) values are stored as 0x00 | plaintext and the api logs a warning.
type SecretBox struct {
	keys [][]byte // current first
}

// ErrSecretKey is returned when a sealed value cannot be opened with the configured keys.
var ErrSecretKey = errors.New("sso: cannot decrypt a stored secret (OPENLOG_SSO_SECRET_KEY changed?)")

func deriveKey(material, label string) []byte {
	m := hmac.New(sha256.New, []byte(material))
	m.Write([]byte(label))
	return m.Sum(nil)
}

// NewSecretBox derives the keys: OPENLOG_SSO_SECRET_KEY (and _PREVIOUS) when set, else a key derived from
// OPENLOG_KEY_HASH_SECRET (and its previous value); no material at all gives a box that stores plaintext.
func NewSecretBox(key, previous, keyHashSecret, keyHashPrevious string) *SecretBox {
	b := &SecretBox{}
	add := func(material, label string) {
		if material != "" {
			b.keys = append(b.keys, deriveKey(material, label))
		}
	}
	if key != "" {
		add(key, "openlog-sso-secret-v1")
		add(previous, "openlog-sso-secret-v1")
	}
	// The derived keys stay readable after OPENLOG_SSO_SECRET_KEY is introduced.
	add(keyHashSecret, "openlog-sso-secret-from-key-hash-v1")
	add(keyHashPrevious, "openlog-sso-secret-from-key-hash-v1")
	return b
}

// Encrypted reports whether values are sealed (a key is configured).
func (b *SecretBox) Encrypted() bool { return b != nil && len(b.keys) > 0 }

func keyID(k []byte) []byte {
	h := sha256.Sum256(k)
	return h[:4]
}

// Seal encrypts plaintext for the associated data aad.
func (b *SecretBox) Seal(plaintext []byte, aad string) ([]byte, error) {
	if !b.Encrypted() {
		return append([]byte{0}, plaintext...), nil
	}
	g, err := gcm(b.keys[0])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+4+g.NonceSize()+len(plaintext)+g.Overhead())
	out = append(out, 1)
	out = append(out, keyID(b.keys[0])...)
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, []byte(aad)), nil
}

// Open decrypts a value sealed for aad.
func (b *SecretBox) Open(sealed []byte, aad string) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, ErrSecretKey
	}
	switch sealed[0] {
	case 0:
		return append([]byte(nil), sealed[1:]...), nil
	case 1:
	default:
		return nil, ErrSecretKey
	}
	if len(sealed) < 1+4+12 {
		return nil, ErrSecretKey
	}
	id, rest := sealed[1:5], sealed[5:]
	for _, k := range b.keysOrNil() {
		if !hmac.Equal(id, keyID(k)) {
			continue
		}
		g, err := gcm(k)
		if err != nil {
			return nil, err
		}
		pt, err := g.Open(nil, rest[:g.NonceSize()], rest[g.NonceSize():], []byte(aad))
		if err != nil {
			return nil, ErrSecretKey
		}
		return pt, nil
	}
	return nil, ErrSecretKey
}

func (b *SecretBox) keysOrNil() [][]byte {
	if b == nil {
		return nil
	}
	return b.keys
}

func gcm(key []byte) (cipher.AEAD, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(c)
}
