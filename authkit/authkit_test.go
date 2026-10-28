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
	if h := HashToken(a); h == a || HashToken(a) != h {
		t.Error("HashToken must be a deterministic transform")
	}
}

// A login naming no account costs as much as a wrong password, so timing
// doesn't reveal which usernames exist: its hash has the same argon2
// settings as every other. The timing check is loose, takes the quickest
// of a few tries of each, and takes them in turn, so a busy machine (CI
// runs other packages' tests beside this one) slows both alike rather
// than only one kind's tries.
func TestVerifyNoAccountTakesAsLong(t *testing.T) {
	hash, _ := HashPassword("correct horse battery")
	settings := func(h string) string { return strings.Split(h, "$")[3] }
	if settings(noAccountHash()) != settings(hash) {
		t.Fatalf("no account's hash uses %s, a password's %s", settings(noAccountHash()), settings(hash))
	}
	timed := func(f func()) time.Duration {
		start := time.Now()
		f()
		return time.Since(start)
	}
	real, none := time.Duration(1<<63-1), time.Duration(1<<63-1)
	for range 5 {
		real = min(real, timed(func() { VerifyPassword(hash, "wrong") }))
		none = min(none, timed(func() { VerifyNoAccount("wrong") }))
	}
	if none < real/2 {
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

func TestSealFor(t *testing.T) {
	s, _ := NewSealer([]byte(strings.Repeat("k", 32)))
	other, _ := NewSealer([]byte(strings.Repeat("o", 32)))
	a, err := s.SealFor("usr_a", "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := s.SealFor("usr_a", "JBSWY3DPEHPK3PXP"); a == b || !strings.HasPrefix(a, "sealedfor1:") ||
		strings.Contains(a, "JBSWY3") || strings.Contains(a, "usr_a") {
		t.Errorf("sealed: %q %q", a, b)
	}
	if _, err := s.SealFor("", "x"); err == nil {
		t.Error("sealed for no one")
	}
	// Sealed the old way: the plaintext alone, and Cloud's owner line
	// before it (bound by hand, before SealFor).
	plain, _ := s.Seal("JBSWY3DPEHPK3PXP")
	line, _ := s.Seal("usr_a\nJBSWY3DPEHPK3PXP")
	for _, c := range []struct {
		name, owner, sealed string
		s                   *Sealer
		want                string // "" for an error
	}{
		{"its owner", "usr_a", a, s, "JBSWY3DPEHPK3PXP"},
		{"another owner", "usr_b", a, s, ""},
		{"an owner that starts the same", "usr_ab", a, s, ""},
		{"an owner that's shorter", "usr_", a, s, ""},
		{"no owner", "", a, s, ""},
		{"another key", "usr_a", a, other, ""},
		{"changed", "usr_a", a[:len(a)-2] + "AA", s, ""},
		{"Seal's form", "usr_a", plain, s, ""},
		{"Seal's, with an owner line", "usr_a", line, s, ""},
		{"Seal's, with SealFor's prefix", "usr_a", "sealedfor1:" + strings.TrimPrefix(line, "sealed1:"), s, ""},
		{"SealFor's, with Seal's prefix", "usr_a", "sealed1:" + strings.TrimPrefix(a, "sealedfor1:"), s, ""},
		{"empty", "usr_a", "", s, ""},
		{"the prefix alone", "usr_a", "sealedfor1:", s, ""},
	} {
		got, err := c.s.OpenFor(c.owner, c.sealed)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	// Open opens only Seal's form: what was sealed the old way still
	// opens, and SealFor's doesn't.
	if out, err := s.Open(line); err != nil || out != "usr_a\nJBSWY3DPEHPK3PXP" {
		t.Errorf("the old way: %q %v", out, err)
	}
	if out, err := s.Open(a); err == nil {
		t.Errorf("SealFor's opened without its owner: %q", out)
	}
	if out, err := s.Open("sealed1:" + strings.TrimPrefix(a, "sealedfor1:")); err == nil {
		t.Errorf("SealFor's opened with Seal's prefix: %q", out)
	}
	for in, want := range map[string]bool{plain: true, line: true, a: false, "": false, "sealed1": false, "bound1:" + line: false} {
		if IsSealed(in) != want {
			t.Errorf("IsSealed(%q) = %v", in, !want)
		}
	}
}
