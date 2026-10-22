package authkit

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected hash format: %s", hash)
	}
	if ok, err := VerifyPassword(hash, "correct horse battery"); !ok || err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if ok, _ := VerifyPassword(hash, "wrong horse battery"); ok {
		t.Error("wrong password accepted")
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Error("hashes must be salted")
	}
	if _, err := VerifyPassword("$2a$10$bcrypt", "x"); err == nil {
		t.Error("unsupported formats must error")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if CheckPasswordPolicy("short") == nil {
		t.Error("short password accepted")
	}
	if CheckPasswordPolicy("twelve chars") != nil {
		t.Error("12-character password rejected")
	}
}

func TestTokens(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if a == b || len(a) < 40 {
		t.Errorf("weak tokens: %q %q", a, b)
	}
	if HashToken(a) == a || HashToken(a) != HashToken(a) {
		t.Error("HashToken must be a deterministic transform")
	}
}

// A login naming no account costs as much as a wrong password, so timing
// doesn't reveal which usernames exist: its hash has the same argon2
// settings as every other. The timing check is loose and takes the
// quickest of a few tries, so a busy machine pausing once doesn't fail it.
func TestVerifyNoAccountTakesAsLong(t *testing.T) {
	hash, _ := HashPassword("correct horse battery")
	settings := func(h string) string { return strings.Split(h, "$")[3] }
	if settings(noAccountHash()) != settings(hash) {
		t.Fatalf("no account's hash uses %s, a password's %s", settings(noAccountHash()), settings(hash))
	}
	quickest := func(f func()) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			f()
			best = min(best, time.Since(start))
		}
		return best
	}
	real := quickest(func() { VerifyPassword(hash, "wrong") })
	if none := quickest(func() { VerifyNoAccount("wrong") }); none < real/2 {
		t.Errorf("no account took %v, a wrong password %v", none, real)
	}
}

func TestPepper(t *testing.T) {
	p := Pepper("a secret kept elsewhere")
	hash, err := p.Hash("a long password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.Verify(hash, "a long password"); !ok {
		t.Error("the right password, peppered, failed")
	}
	if ok, _ := VerifyPassword(hash, "a long password"); ok {
		t.Error("a peppered hash passed without the pepper")
	}
	if ok, _ := Pepper("another").Verify(hash, "a long password"); ok {
		t.Error("another pepper passed")
	}
	if ok, _ := Pepper(nil).Verify(fuzzHash, "the right password"); !ok {
		t.Error("no pepper is plain argon2id")
	}
}

func TestSealer(t *testing.T) {
	s, err := NewSealer([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Seal("JBSWY3DPEHPK3PXP")
	b, _ := s.Seal("JBSWY3DPEHPK3PXP")
	if a == b || !strings.HasPrefix(a, "sealed1:") || strings.Contains(a, "JBSWY3") {
		t.Errorf("sealed: %q %q", a, b)
	}
	if out, err := s.Open(a); err != nil || out != "JBSWY3DPEHPK3PXP" {
		t.Errorf("open: %q %v", out, err)
	}
	other, _ := NewSealer([]byte(strings.Repeat("o", 32)))
	if _, err := other.Open(a); err == nil {
		t.Error("opened with another key")
	}
	if _, err := s.Open(a[:len(a)-2] + "AA"); err == nil {
		t.Error("a changed value opened")
	}
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Error("a short key")
	}
}
