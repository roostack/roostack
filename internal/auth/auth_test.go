package auth

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

func TestRoles(t *testing.T) {
	cases := []struct {
		role string
		p    Permission
		want bool
	}{
		{RoleOwner, ManageUsers, true},
		{RoleOwner, ManageHost, true},
		{RoleAdmin, ManageServices, true},
		{RoleAdmin, ManageHost, false},
		{RoleAdmin, ManageUsers, false},
		{RoleMember, ViewDashboard, true},
		{RoleMember, ManageServices, false},
		{"bogus", ViewDashboard, false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.p); got != c.want {
			t.Errorf("Can(%s, %d) = %v, want %v", c.role, c.p, got, c.want)
		}
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
// doesn't reveal which usernames exist. (Loose bound: it only has to show
// the argon2 work is done.)
func TestVerifyNoAccountTakesAsLong(t *testing.T) {
	hash, _ := HashPassword("correct horse battery")
	VerifyNoAccount("warm up") // makes its hash once
	start := time.Now()
	VerifyPassword(hash, "wrong")
	real := time.Since(start)
	start = time.Now()
	VerifyNoAccount("wrong")
	if none := time.Since(start); none < real/2 {
		t.Errorf("no account took %v, a wrong password %v", none, real)
	}
}
