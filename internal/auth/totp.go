package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) with the settings every authenticator app supports:
// SHA-1, 6 digits, 30-second steps.
const (
	totpStep   = 30
	totpDigits = 6
	// totpSkew accepts codes one step either side, for clock drift.
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random secret, base32-encoded as apps expect.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPURL is the otpauth:// link authenticator apps import (usually via QR).
func TOTPURL(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// totpCode computes the code for one time step.
func totpCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", fmt.Errorf("auth: bad TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000), nil
}

// TOTPCode returns the code for time t. Used by tests and tooling; login
// uses VerifyTOTP.
func TOTPCode(secret string, t time.Time) (string, error) {
	return totpCode(secret, t.Unix()/totpStep)
}

// VerifyTOTP checks a code at time now. It returns the matched time step,
// which callers store so the same code can't be used twice (replay).
// Steps at or before lastStep are rejected.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	current := now.Unix() / totpStep
	for s := current - totpSkew; s <= current+totpSkew; s++ {
		if s <= lastStep {
			continue
		}
		want, err := totpCode(secret, s)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return s, true
		}
	}
	return 0, false
}
