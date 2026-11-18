package agent

import (
	"testing"
	"time"
)

// Failed passwords count, by address, for a day; other lines don't, and a
// guesser can't plant an address in the account name it tries.
func TestGuessLog(t *testing.T) {
	now := time.Date(2026, 11, 18, 21, 30, 0, 0, time.UTC)
	var g guessLog
	for _, l := range []struct {
		msg string
		ago time.Duration
	}{
		{"Failed password for root from 178.104.14.83 port 44320 ssh2", time.Minute},
		{"Failed password for invalid user lv from 194.195.115.85 port 56748 ssh2", 2 * time.Hour},
		{"Failed password for root from 178.104.14.83 port 51422 ssh2", 23 * time.Hour},
		{"Failed keyboard-interactive/pam for root from 2001:db8::7 port 22 ssh2", time.Hour},
		{"Failed password for invalid user x from 192.0.2.1 port 1 ssh2 from 203.0.113.50 port 2 ssh2", time.Minute},
		{"Failed password for root from 178.104.14.83 port 1 ssh2", 25 * time.Hour}, // too old
		{"Invalid user lv from 194.195.115.85 port 56748", time.Minute},
		{"Accepted publickey for root from 159.196.19.158 port 5 ssh2: ED25519 SHA256:x", time.Minute},
		{"Failed publickey for root from 198.51.100.1 port 5 ssh2: ED25519 SHA256:x", time.Minute},
	} {
		g.line(l.msg, now.Add(-l.ago))
	}
	guesses, addrs, ready := g.day(now)
	if guesses != 5 || addrs != 4 || ready {
		t.Errorf("day = %d guesses from %d addresses (ready %v), want 5 from 4, not ready", guesses, addrs, ready)
	}
	g.mu.Lock()
	planted := false
	for _, h := range g.hours {
		for a := range h.addrs {
			planted = planted || a.String() == "192.0.2.1"
		}
	}
	g.mu.Unlock()
	if planted {
		t.Error("an address in the account name was counted")
	}
	if guesses, _, _ := g.day(now.Add(2 * time.Hour)); guesses != 4 {
		t.Errorf("two hours later = %d, want 4 (the one from 23 hours ago gone)", guesses)
	}
	g.reset()
	if guesses, _, _ := g.day(now); guesses != 0 {
		t.Errorf("after reset = %d", guesses)
	}
}
