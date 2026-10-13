// Package authkit is passwords (argon2id), tokens and two-factor codes
// (TOTP), shared by Roostack and Roostack Cloud (whose repository imports
// it): one implementation, each side turning on what it needs (SPEC.md
// §5). Cloud, holding many accounts, adds a pepper and seals two-factor
// secrets (Pepper, Sealer), so a stolen database alone gives neither
// passwords nor codes; a lab doesn't need them (its database and keys are
// on one machine).
package authkit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 "second recommended option": 64 MiB, t=3).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// The most a stored hash may ask for (several times today's settings,
// room to raise them), so a damaged one can't exhaust the server.
const (
	maxIter   = 16
	maxMemory = 1024 * 1024 // KiB: 1 GiB
)

// MinPasswordLength is the shortest password accepted.
const MinPasswordLength = 12

// CheckPasswordPolicy returns a user-facing error if a password is too weak.
func CheckPasswordPolicy(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

// HashPassword returns an argon2id hash in the standard PHC string format.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// noAccountHash is checked when a login names no account, so the answer
// takes as long as a wrong password would and doesn't reveal which
// usernames exist.
var noAccountHash = sync.OnceValue(func() string {
	h, _ := HashPassword("no such account")
	return h
})

// VerifyNoAccount does the work of a password check for a login that names
// no account, and always fails.
func VerifyNoAccount(pw string) {
	VerifyPassword(noAccountHash(), pw)
}

// VerifyPassword checks a password against a hash in constant time.
func VerifyPassword(hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported hash format")
	}
	var version int
	var mem, iter uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &threads); err != nil {
		return false, fmt.Errorf("auth: bad hash parameters: %w", err)
	}
	// Settings out of range would make argon2 panic (zero rounds or
	// threads, found by fuzzing, 2026-10-07) or use absurd memory or time:
	// a damaged or tampered hash fails instead.
	if iter < 1 || iter > maxIter || threads < 1 || mem < 8*uint32(threads) || mem > maxMemory {
		return false, errors.New("auth: hash parameters out of range")
	}
	b64 := base64.RawStdEncoding.Strict()
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if len(salt) < 8 || len(want) < 16 || len(want) > 64 {
		return false, errors.New("auth: hash salt or length out of range")
	}
	got := argon2.IDKey([]byte(pw), salt, iter, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewToken returns a random URL-safe token (sessions, setup token).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is what is stored for a session, so a leaked database doesn't
// contain usable session tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokensEqual compares two tokens in constant time.
func TokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Pepper is a secret key, kept outside the database, mixed into passwords
// before they're hashed (Cloud's; Roostack uses none): with only a stolen
// database, a guessed password can't be checked. It's HMAC-SHA256, so any
// length of password becomes a fixed-length input to argon2id. Changing
// it makes every stored hash fail, so it's set once.
type Pepper []byte

// Hash is HashPassword with the pepper mixed in.
func (p Pepper) Hash(pw string) (string, error) { return HashPassword(p.mix(pw)) }

// Verify is VerifyPassword with the pepper mixed in.
func (p Pepper) Verify(hash, pw string) (bool, error) { return VerifyPassword(hash, p.mix(pw)) }

// VerifyNoAccount is VerifyNoAccount with the pepper's work too.
func (p Pepper) VerifyNoAccount(pw string) { VerifyNoAccount(p.mix(pw)) }

func (p Pepper) mix(pw string) string {
	if len(p) == 0 {
		return pw
	}
	m := hmac.New(sha256.New, p)
	m.Write([]byte(pw))
	return base64.RawStdEncoding.EncodeToString(m.Sum(nil))
}
