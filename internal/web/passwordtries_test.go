package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

// Past ten wrong passwords for a username in an hour, each try waits
// longer after the last wrong one, up to 30 seconds; never a lock. The
// Owners hear once a day reaches 50 (the security review of 2026-11-19,
// L5).
func TestPasswordTries(t *testing.T) {
	var p passwordTries
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	for i := range passwordFreeTries {
		if w := p.wait("Kim", now); w != 0 {
			t.Fatalf("after %d wrong: wait %v", i, w)
		}
		p.fail("kim", "203.0.113.1", now)
	}
	if w := p.wait("KIM", now); w != time.Second {
		t.Errorf("after ten wrong: wait %v, want 1s", w)
	}
	for range 10 {
		p.fail("kim", "203.0.113.2", now)
	}
	if w := p.wait("kim", now); w != passwordLongestWait {
		t.Errorf("after twenty wrong: wait %v, want the longest", w)
	}
	if w := p.wait("kim", now.Add(passwordLongestWait)); w != 0 {
		t.Errorf("once the wait is over: %v", w)
	}
	// An hour on, the count starts again; the right password clears it.
	if w := p.wait("kim", now.Add(passwordTriesWindow+time.Second)); w != 0 {
		t.Errorf("an hour on: %v", w)
	}
	p.clear("kim")
	if w := p.wait("kim", now); w != 0 {
		t.Errorf("after the right password: %v", w)
	}
	// The day's count: once, at 50, with the addresses it came from.
	var day, addrs int
	for i := range passwordWarnTries - 20 {
		if d, a := p.fail("kim", "203.0.113.3", now.Add(time.Duration(i)*time.Minute)); d > 0 {
			day, addrs = d, a
		}
	}
	if day != passwordWarnTries || addrs != 3 {
		t.Errorf("warned at %d from %d addresses", day, addrs)
	}
	if d, _ := p.fail("kim", "203.0.113.3", now.Add(time.Hour)); d != 0 {
		t.Error("warned twice in a day")
	}
}

// Through the login page: a try that must wait is refused before the
// password is checked, the same for a name that's no account, and the
// right password works once the wait is over.
func TestLoginSlowed(t *testing.T) {
	h := newHarness(t, "")
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(ctx, store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleAdmin})
	now := time.Now()
	for _, name := range []string{"mum", "nobody"} {
		for range passwordFreeTries + 5 {
			h.server.passwordTries.fail(name, "203.0.113.1", now)
		}
		code, body := h.post("/login", url.Values{"username": {name}, "password": {"a long password"}}, h.srv.URL)
		if code != http.StatusTooManyRequests || !strings.Contains(body, "Too many wrong passwords for this username, so Roostack is slowing tries down") {
			t.Errorf("%s, slowed: %d", name, code)
		}
	}
	h.server.passwordTries.byName["mum"].last = now.Add(-time.Minute)
	if code, _ := h.post("/login", url.Values{"username": {"mum"}, "password": {"a long password"}}, h.srv.URL); code != http.StatusOK {
		t.Errorf("the right password once the wait is over: %d", code)
	}
	if w := h.server.passwordTries.wait("mum", time.Now()); w != 0 {
		t.Errorf("the right password didn't clear the count: %v", w)
	}

	// The 50th wrong password in a day: a security event, which the
	// account's person and the Owners are emailed.
	for range passwordWarnTries - 1 - (passwordFreeTries + 5) {
		h.server.passwordTries.fail("mum", "203.0.113.1", now)
	}
	h.server.passwordTries.byName["mum"].n = 0
	h.post("/logout", nil, h.srv.URL)
	h.post("/login", url.Values{"username": {"mum"}, "password": {"not the password"}}, h.srv.URL)
	logs, _ := h.store.RecentAudit(ctx, 20)
	found := false
	for _, e := range logs {
		found = found || e.Action == attention.AuditPasswordGuesses && e.Username == "mum" && strings.HasPrefix(e.Detail, "50 times today, from ")
	}
	if !found {
		t.Errorf("no security event at 50: %+v", logs)
	}
}
