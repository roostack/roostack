package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B test vectors (SHA-1, 8 digits there; we use the
// last 6 digits, which is the same truncation mod 10^6).
func TestTOTPVectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	vectors := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for unix, want := range vectors {
		got, err := totpCode(secret, unix/totpStep)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("t=%d: got %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	code, _ := totpCode(secret, now.Unix()/totpStep)

	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok {
		t.Fatal("current code rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Error("a code must not be accepted twice")
	}
	if _, ok := VerifyTOTP(secret, code[:3]+" "+code[3:], now, 0); !ok {
		t.Error("codes with a space in the middle should be accepted")
	}
	// One step of clock drift is fine; two is not.
	if _, ok := VerifyTOTP(secret, code, now.Add(30*time.Second), 0); !ok {
		t.Error("code from the previous step rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now.Add(90*time.Second), 0); ok {
		t.Error("code from 3 steps ago accepted")
	}
	if _, ok := VerifyTOTP(secret, "000000x", now, 0); ok {
		t.Error("malformed code accepted")
	}
}

func TestTOTPURL(t *testing.T) {
	u := TOTPURL("Roostack", "tyris", "ABC")
	if !strings.HasPrefix(u, "otpauth://totp/Roostack:tyris?") || !strings.Contains(u, "secret=ABC") {
		t.Errorf("unexpected URL %s", u)
	}
}
