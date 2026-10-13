package authkit

import (
	"strings"
	"testing"
	"time"
)

// Fuzz tests try random and malformed input (go test -fuzz=Fuzz...;
// plain go test runs the seeds): nothing panics, and nothing but the
// right password, code or key passes.

var fuzzHash, _ = HashPassword("the right password")

func FuzzVerifyPassword(f *testing.F) {
	for _, s := range []string{"", "the right password ", "the right passwor", "$argon2id$", strings.Repeat("x", 1000)} {
		f.Add(s, fuzzHash)
	}
	f.Add("x", "$argon2id$v=19$m=1,t=1,p=1$AA$AA")
	f.Fuzz(func(t *testing.T, pw, hash string) {
		ok, _ := VerifyPassword(hash, pw)
		if ok && hash == fuzzHash && pw != "the right password" {
			t.Errorf("%q accepted", pw)
		}
	})
}

func FuzzVerifyTOTP(f *testing.F) {
	secret, _ := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	right, _ := TOTPCode(secret, now)
	for _, s := range []string{"", "000000", " 12 34 56 ", "1234567", "abcdef", right + "0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		_, ok := VerifyTOTP(secret, code, now, 0)
		norm := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
		if ok {
			// Only one of the codes for the steps either side of now.
			valid := false
			for d := -1; d <= 1; d++ {
				c, _ := TOTPCode(secret, now.Add(time.Duration(d)*30*time.Second))
				valid = valid || c == norm
			}
			if !valid {
				t.Errorf("%q accepted", code)
			}
		}
	})
}

func FuzzOpen(f *testing.F) {
	s, _ := NewSealer([]byte(strings.Repeat("k", 32)))
	// Sealed once with that key (fixed: each fuzz worker runs this setup
	// again, and Seal's nonce is random).
	const sealed = "sealed1:GpaP1xocQnSAM53c0DD09mqb2VTyRqjubL/rU7xwiCyoZDwTZfCymqiAULQ"
	f.Add(sealed)
	f.Add("sealed1:")
	f.Add("sealed1:AAAA")
	f.Fuzz(func(t *testing.T, in string) {
		out, err := s.Open(in)
		if err == nil && in != sealed {
			t.Errorf("%q opened (to %q): only what Seal made may", in, out)
		}
	})
}
