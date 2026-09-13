package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// PasswordParams are argon2id parameters.
type PasswordParams struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultPasswordParams follow the OWASP argon2id baseline (19 MiB, t=2, p=1).
// They keep memory per login small because API pods are sized for queries;
// hashPermits bounds concurrent hashing per process.
var DefaultPasswordParams = PasswordParams{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Password length limits (bytes for the maximum, characters for the minimum).
const (
	MinPasswordLen = 8
	MaxPasswordLen = 256
)

// hashPermits bounds concurrent argon2id computations (~19 MiB each).
var hashPermits = make(chan struct{}, 4)

// ValidatePassword checks the password policy.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return newError(CodeInvalidArgument, "password must be at least %d characters", MinPasswordLen)
	}
	if len(pw) > MaxPasswordLen {
		return newError(CodeInvalidArgument, "password must be at most %d bytes", MaxPasswordLen)
	}
	return nil
}

// HashPassword returns an argon2id PHC string:
// $argon2id$v=19$m=<KiB>,t=<time>,p=<threads>$<salt b64>$<hash b64>.
func HashPassword(pw string) (string, error) {
	return hashWith(pw, DefaultPasswordParams)
}

func hashWith(pw string, p PasswordParams) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashPermits <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	<-hashPermits
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// VerifyPassword checks pw against an encoded hash in constant time. The
// parameters are read from the hash, so old hashes keep working after the
// defaults change.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p PasswordParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, errBadHash
	}
	// Refuse absurd parameters from a tampered row (memory DoS).
	if p.Memory == 0 || p.Memory > 1<<20 || p.Time == 0 || p.Time > 16 || p.Threads == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 128 {
		return false, errBadHash
	}
	if len(pw) > MaxPasswordLen {
		return false, nil
	}
	hashPermits <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	<-hashPermits
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnPasswordCheck spends the same time as a real verification so that
// unknown emails are not distinguishable by response time.
func burnPasswordCheck(pw string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("openlog-dummy-password") })
	_, _ = VerifyPassword(dummyHash, pw)
}
