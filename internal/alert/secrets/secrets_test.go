package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newRawKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	kr, err := NewKeyring(newRawKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"url":"https://hooks.slack.com/services/T000/B000/XXXX"}`)
	stored, keyID, err := kr.Encrypt(secret, "org-a/ch-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "ol1:"+keyID+":") || keyID != kr.CurrentKeyID() || len(keyID) != 8 {
		t.Fatalf("stored %q key %q", stored, keyID)
	}
	if strings.Contains(stored, "hooks.slack.com") {
		t.Fatal("plaintext visible in ciphertext")
	}
	got, err := kr.Decrypt(stored, "org-a/ch-1")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	// Two encryptions of the same value differ (random nonce).
	again, _, _ := kr.Encrypt(secret, "org-a/ch-1")
	if again == stored {
		t.Error("nonce reuse: identical ciphertexts")
	}
}

func TestAADBindsCiphertextToRow(t *testing.T) {
	kr, _ := NewKeyring(newRawKey(t), "")
	stored, _, _ := kr.Encrypt([]byte("s3cret"), "org-a/ch-1")
	for _, aad := range []string{"org-b/ch-1", "org-a/ch-2", ""} {
		if _, err := kr.Decrypt(stored, aad); !errors.Is(err, ErrDecrypt) {
			t.Errorf("aad %q: err = %v, want ErrDecrypt", aad, err)
		}
	}
}

func TestTamperingDetected(t *testing.T) {
	kr, _ := NewKeyring(newRawKey(t), "")
	stored, _, _ := kr.Encrypt([]byte("s3cret"), "a")
	parts := strings.SplitN(stored, ":", 3)
	raw, _ := base64.StdEncoding.DecodeString(parts[2])
	raw[len(raw)-1] ^= 1
	bad := parts[0] + ":" + parts[1] + ":" + base64.StdEncoding.EncodeToString(raw)
	if _, err := kr.Decrypt(bad, "a"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered ciphertext: %v", err)
	}
	for _, s := range []string{"", "garbage", "ol1:abcd", "ol2:" + parts[1] + ":" + parts[2], "ol1:" + parts[1] + ":!!!"} {
		if _, err := kr.Decrypt(s, "a"); err == nil {
			t.Errorf("malformed %q accepted", s)
		}
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := newRawKey(t), newRawKey(t)
	oldRing, _ := NewKeyring(oldKey, "")
	stored, _, _ := oldRing.Encrypt([]byte("hmac"), "row")

	// Step 1: new key current, old key previous: old values still decrypt and are flagged.
	ring, err := NewKeyring(newKey, " , "+oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ring.Decrypt(stored, "row"); err != nil || string(got) != "hmac" {
		t.Fatalf("decrypt with previous key: %q %v", got, err)
	}
	if !ring.NeedsRotation(stored) {
		t.Error("old ciphertext not flagged for rotation")
	}
	rotated, _, _ := ring.Encrypt([]byte("hmac"), "row")
	if ring.NeedsRotation(rotated) || KeyIDOf(rotated) != ring.CurrentKeyID() {
		t.Error("re-encrypted value still flagged")
	}
	// Step 3: old key removed: only rotated values decrypt.
	final, _ := NewKeyring(newKey, "")
	if _, err := final.Decrypt(stored, "row"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("old ciphertext after key removal: %v", err)
	}
	if _, err := final.Decrypt(rotated, "row"); err != nil {
		t.Errorf("rotated ciphertext: %v", err)
	}
}

func TestKeyParsingAndUnconfigured(t *testing.T) {
	for _, bad := range []string{"short", base64.StdEncoding.EncodeToString(make([]byte, 16)), "%%%"} {
		if _, err := NewKeyring(bad, ""); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	raw := make([]byte, 32)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding} {
		if _, err := NewKeyring(enc.EncodeToString(raw), ""); err != nil {
			t.Errorf("valid encoding rejected: %v", err)
		}
	}
	if _, err := NewKeyring("", newRawKey(t)); err == nil {
		t.Error("previous keys without a current key accepted")
	}
	var nilRing *Keyring
	empty, _ := NewKeyring("", "")
	for _, kr := range []*Keyring{nilRing, empty} {
		if kr.Configured() {
			t.Error("empty keyring reports configured")
		}
		if _, _, err := kr.Encrypt([]byte("x"), "a"); !errors.Is(err, ErrNoKey) {
			t.Errorf("encrypt without key: %v", err)
		}
		if _, err := kr.Decrypt("ol1:00000000:AAAA", "a"); !errors.Is(err, ErrNoKey) {
			t.Errorf("decrypt without key: %v", err)
		}
	}
}
