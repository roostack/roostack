package backup

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewRecoveryKey(t *testing.T) {
	form := regexp.MustCompile(`^[0-9a-f]{4}(-[0-9a-f]{4}){15}$`)
	a, b := NewRecoveryKey(), NewRecoveryKey()
	if !form.MatchString(a) || a == b {
		t.Errorf("keys %q, %q", a, b)
	}
	if got := TypedRecoveryKey(a); got != a {
		t.Errorf("typed back as made: %q", got)
	}
}

func TestTypedRecoveryKey(t *testing.T) {
	key := "3f9a-07c2-11aa-bb22-cc33-dd44-ee55-ff66-0011-2233-4455-6677-8899-aabb-ccdd-eeff"
	for _, typed := range []string{
		key,
		"  " + key + "\n",
		strings.ToUpper(key),
		strings.ReplaceAll(key, "-", ""),
		strings.ReplaceAll(key, "-", " "),
		"3f9a07c2 11aabb22 cc33dd44 ee55ff66\n00112233 44556677 8899aabb ccddeeff",
	} {
		if got := TypedRecoveryKey(typed); got != key {
			t.Errorf("%q → %q", typed, got)
		}
	}
	// Keys made before 2026-10-19 are left as they are, but trimmed: one
	// in base64 (its case and dashes matter), and a changed key in plain
	// hex (48 digits).
	for typed, want := range map[string]string{
		" AbC-dEf_0123456789abcdefghijklmnopqrstuvwxyzAB \n":  "AbC-dEf_0123456789abcdefghijklmnopqrstuvwxyzAB",
		"0123456789abcdef0123456789abcdef0123456789abcdef":    "0123456789abcdef0123456789abcdef0123456789abcdef",
		"3f9a-07c2-11aa-bb22-cc33-dd44-ee55-ff66-0011-2233-x": "3f9a-07c2-11aa-bb22-cc33-dd44-ee55-ff66-0011-2233-x",
	} {
		if got := TypedRecoveryKey(typed); got != want {
			t.Errorf("%q → %q, want %q", typed, got, want)
		}
	}
}
