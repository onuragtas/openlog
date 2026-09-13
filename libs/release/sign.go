package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SignaturePrefix starts every signature line: "openlog-sig-v1 <key_id> <base64 signature>".
const SignaturePrefix = "openlog-sig-v1"

var (
	// ErrNoTrustedSignature means no signature line was made by a trusted key.
	ErrNoTrustedSignature = errors.New("release: no signature from a trusted key")
	// ErrBadSignature means a line claimed a trusted key but did not verify.
	ErrBadSignature = errors.New("release: signature does not verify")
)

// KeyID returns the key identifier used in signature lines: the first 8 bytes of SHA-256 of the
// raw public key, as lowercase hex.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// ParsePublicKey decodes a standard base64 raw 32-byte Ed25519 public key.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("release: public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release: public key: want %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePublicKeys decodes several keys, e.g. the keys compiled into a binary.
func ParsePublicKeys(b64 ...string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(b64))
	for _, s := range b64 {
		k, err := ParsePublicKey(s)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// PrivateKeyFromSeed decodes a standard base64 32-byte Ed25519 seed (the CI signing secret).
func PrivateKeyFromSeed(b64 string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("release: signing seed: %w", err)
	}
	if len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("release: signing seed: want %d bytes, got %d", ed25519.SeedSize, len(raw))
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

// GenerateKey creates a new key pair and returns the base64 public key and seed.
func GenerateKey() (publicKey, seed string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv.Seed()), nil
}

// SignatureLine signs data and returns one signature line (without trailing newline).
func SignatureLine(data []byte, priv ed25519.PrivateKey) string {
	pub := priv.Public().(ed25519.PublicKey)
	return fmt.Sprintf("%s %s %s", SignaturePrefix, KeyID(pub), base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
}

// Verify checks a signature file against data. It succeeds if any line verifies with a trusted
// key and returns that key's id. Lines for unknown keys are ignored so keys can be rotated.
func Verify(data, signatureFile []byte, trusted []ed25519.PublicKey) (string, error) {
	byID := make(map[string]ed25519.PublicKey, len(trusted))
	for _, k := range trusted {
		if len(k) == ed25519.PublicKeySize {
			byID[KeyID(k)] = k
		}
	}
	sawTrusted := false
	sc := bufio.NewScanner(bytes.NewReader(signatureFile))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 || fields[0] != SignaturePrefix {
			continue
		}
		key, ok := byID[fields[1]]
		if !ok {
			continue
		}
		sawTrusted = true
		sig, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		if ed25519.Verify(key, data, sig) {
			return fields[1], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("release: reading signature: %w", err)
	}
	if sawTrusted {
		return "", ErrBadSignature
	}
	return "", ErrNoTrustedSignature
}

// VerifyManifest verifies the signature over the exact manifest bytes, then parses them.
func VerifyManifest(data, signatureFile []byte, trusted []ed25519.PublicKey) (*Manifest, string, error) {
	keyID, err := Verify(data, signatureFile, trusted)
	if err != nil {
		return nil, "", err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, "", err
	}
	return m, keyID, nil
}

// VerifyIndex verifies the signature over the exact index bytes, then parses them.
func VerifyIndex(data, signatureFile []byte, trusted []ed25519.PublicKey) (*Index, string, error) {
	keyID, err := Verify(data, signatureFile, trusted)
	if err != nil {
		return nil, "", err
	}
	idx, err := ParseIndex(data)
	if err != nil {
		return nil, "", err
	}
	return idx, keyID, nil
}
