package dnsrecords

import (
	"context"
	"testing"

	"roostack.cloud/roostack/internal/dns/dnstest"
)

// A DNS challenge for a name under the lab's address sets its own TXT
// record, never somebody else's; nothing else is a challenge, and without
// managed records there's none (SPEC.md §10.5, "Serving directly").
func TestSetChallenge(t *testing.T) {
	m, g := newManager(t, managed, nil, nil)
	ctx := context.Background()
	if err := m.SetChallenge(ctx, "git.lab.example.com", "token-1"); err != nil {
		t.Fatal(err)
	}
	if got := g.getType("_acme-challenge.git.lab", "TXT"); got != `"token-1"` {
		t.Errorf("the challenge's record: %q", got)
	}
	if err := m.SetChallenge(ctx, "git.lab.example.com", "token-2"); err != nil || g.getType("_acme-challenge.git.lab", "TXT") != `"token-2"` {
		t.Errorf("a second challenge: %v, %q", err, g.getType("_acme-challenge.git.lab", "TXT"))
	}
	g.Set(dnstest.Zone, "_acme-challenge.wiki.lab", "TXT", []string{"theirs"})
	if err := m.SetChallenge(ctx, "wiki.lab.example.com", "token-3"); err == nil || g.getType("_acme-challenge.wiki.lab", "TXT") != "theirs" {
		t.Errorf("somebody else's record: %v", err)
	}
	for _, host := range []string{"git.other.example.com", "lab.example.com.evil"} {
		if err := m.SetChallenge(ctx, host, "token"); err == nil {
			t.Errorf("a challenge for %q", host)
		}
	}
	if err := m.SetChallenge(ctx, "git.lab.example.com", "a\"b"); err == nil {
		t.Error("a value with a quote")
	}
	m.Lab.DNS.ManageRecords = false
	if err := m.SetChallenge(ctx, "git.lab.example.com", "token-4"); err == nil {
		t.Error("a challenge with records not managed")
	}
}
