package backup

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// recoveryKeyGroups is how many groups of four hex digits a recovery key
// has: 16, for 32 random bytes (256 bits).
const recoveryKeyGroups = 16

// NewRecoveryKey makes a recovery key (SPEC.md §6.9): 32 random bytes as
// 64 hex digits in groups of four, "3f9a-07c2-…", since people write it
// down and type it back. Every key, the first and any that replaces it,
// is made here (until 2026-10-19 a changed key was 24 bytes in plain hex,
// the first 32 in base64).
func NewRecoveryKey() string {
	b := make([]byte, recoveryKeyGroups*2)
	rand.Read(b) // never fails (Go 1.24)
	return groupHex(hex.EncodeToString(b))
}

// TypedRecoveryKey is a recovery key as someone typed it, in the form it
// was made in: 64 hex digits, however they're grouped, spaced or
// capitalised, become NewRecoveryKey's form. Anything else (a key made
// before 2026-10-19) is only trimmed, as it was.
func TypedRecoveryKey(typed string) string {
	typed = strings.TrimSpace(typed)
	digits := strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
			return -1
		case r >= 'A' && r <= 'F':
			return r + ('a' - 'A')
		}
		return r
	}, typed)
	if len(digits) != recoveryKeyGroups*4 {
		return typed
	}
	if _, err := hex.DecodeString(digits); err != nil {
		return typed
	}
	return groupHex(digits)
}

// groupHex puts a dash after every four digits.
func groupHex(digits string) string {
	var b strings.Builder
	for i := 0; i < len(digits); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(digits[i:min(i+4, len(digits))])
	}
	return b.String()
}
