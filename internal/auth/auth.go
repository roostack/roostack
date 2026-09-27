// Package auth handles passwords, session tokens and roles (SPEC.md §5).
package auth

import (
	"regexp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Roles (SPEC.md §5).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Permissions checked by the web UI.
type Permission int

const (
	// ViewDashboard: see services and their status.
	ViewDashboard Permission = iota
	// ManageServices: install, configure, update, remove services; backups; logs.
	ManageServices
	// ManageHost: host settings, OS updates, restore, recovery key.
	ManageHost
	// ManageUsers: create and remove accounts.
	ManageUsers
)

// Can reports whether a role has a permission.
func Can(role string, p Permission) bool {
	switch role {
	case RoleOwner:
		return true
	case RoleAdmin:
		return p == ViewDashboard || p == ManageServices
	case RoleMember:
		return p == ViewDashboard
	}
	return false
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidUsername reports whether a username is acceptable: 1-32 letters,
// digits, dots, dashes or underscores, starting with a letter or digit.
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// NewTemporaryPassword returns a random password for a new account or a
// reset (~120 bits), to be changed at the next login.
func NewTemporaryPassword() (string, error) {
	t, err := NewToken()
	if err != nil {
		return "", err
	}
	return t[:20], nil
}

// ValidRole reports whether role is known.
func ValidRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

// argon2id parameters (RFC 9106 "second recommended option": 64 MiB, t=3).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
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
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
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
