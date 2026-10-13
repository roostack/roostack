package dnsrecords

import (
	"context"
	"errors"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

type fakeEntry struct {
	ip  string
	err error
}

func (fakeEntry) Kind() string                              { return "port forwarding" }
func (e fakeEntry) Address(context.Context) (string, error) { return e.ip, e.err }

// A public address gets its own record at the entry's address, only once
// CrowdSec is up; made private again, it points home like the wildcard
// (SPEC.md §6.11).
func TestPublicRecords(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20", "81.2.69.160"})
	hosts := []string{"photos.lab.example.com"}
	protected := false
	m.PublicHosts = func() []string { return hosts }
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	m.Protected = func(context.Context) bool { return protected }

	st := m.Sync(context.Background())
	if len(st.Public) != 1 || st.Public[0].State != PublicWaiting || g.get("photos.lab") != "" {
		t.Fatalf("before CrowdSec is up: %+v, record %q", st.Public, g.get("photos.lab"))
	}
	protected = true
	st = m.Sync(context.Background())
	if st.Public[0].State != PublicOK || g.get("photos.lab") != "81.2.69.160" || st.PublicIP != "81.2.69.160" {
		t.Fatalf("protected: %+v, record %q", st, g.get("photos.lab"))
	}
	if g.get("*.lab") != "192.168.1.20" {
		t.Error("the wildcard should still point home")
	}

	// Private again: back home, and no longer listed.
	hosts = nil
	st = m.Sync(context.Background())
	if len(st.Public) != 0 || g.get("photos.lab") != "192.168.1.20" {
		t.Errorf("private again: %+v, record %q", st.Public, g.get("photos.lab"))
	}
}

// Someone else's record for a public address is never overwritten; no
// address to point at is an error; unmanaged records are listed to add
// by hand.
func TestPublicRecordProblems(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20"})
	g.put("vault.lab", "1.2.3.4")
	m.PublicHosts = func() []string { return []string{"vault.lab.example.com"} }
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	if st := m.Sync(context.Background()); st.Public[0].State != PublicConflict || g.get("vault.lab") != "1.2.3.4" {
		t.Errorf("conflict: %+v", st.Public)
	}

	m.Entry = fakeEntry{err: errors.New("no answer")}
	if st := m.Sync(context.Background()); st.Public[0].State != PublicError {
		t.Errorf("no address: %+v", st.Public)
	}

	m.Lab.DNS.ManageRecords = false
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	st := m.Sync(context.Background())
	if st.Public[0].State != PublicManual || !strings.Contains(st.Public[0].Message, "vault.lab.example.com  A  81.2.69.160") {
		t.Errorf("manual: %+v", st.Public)
	}
}

// On a lab on the internet (SPEC.md §6.12) the wildcard points at the
// lab's public address and covers public addresses: no records of their
// own, and no waiting for CrowdSec (the wildcard already reaches them).
func TestInternetLab(t *testing.T) {
	m, g := newManager(t, managed, []string{"172.105.165.39"}, []string{"172.105.165.39"})
	m.Lab.Network = config.NetworkInternet
	m.LANIP = func() (string, error) { return "10.0.0.5", nil } // behind the provider's NAT
	m.Entry = fakeEntry{ip: "172.105.165.39"}
	m.PublicHosts = func() []string { return []string{"git.lab.example.com"} }
	m.Protected = func(context.Context) bool { return false }

	st := m.Sync(context.Background())
	if st.State != StateOK || st.IP != "172.105.165.39" || g.get("*.lab") != "172.105.165.39" || !st.Internet ||
		!strings.Contains(st.Message, "point at this server") {
		t.Fatalf("wildcard: %+v, record %q", st, g.get("*.lab"))
	}
	if len(st.Public) != 1 || st.Public[0].State != PublicOK || g.get("git.lab") != "" {
		t.Errorf("public address: %+v, own record %q", st.Public, g.get("git.lab"))
	}

	// Unmanaged: the one record to add is the wildcard.
	m.Lab.DNS.ManageRecords = false
	m.PublicLookup = func(context.Context, string) ([]string, error) { return nil, nil }
	st = m.Sync(context.Background())
	if st.Public[0].State != PublicManual || !strings.Contains(st.Public[0].Message, "*.lab.example.com  A  172.105.165.39") {
		t.Errorf("manual: %+v", st.Public)
	}
}

// While the lab's own domain is relayed through Roostack Cloud (SPEC.md
// §10.5): each public address is a CNAME to the relay name, Roostack's A
// record deleted first, and the proof's TXT record is in place; someone
// else's A record is in the way; relaying stopped, it's A records again
// and the proof goes. Kept by hand, the records to add are listed.
func TestRelayedRecords(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"198.51.100.10"})
	hosts := []string{"photos.lab.example.com", "vault.lab.example.com"}
	m.PublicHosts = func() []string { return hosts }
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	g.put("vault.lab", "203.0.113.9") // someone else's, from the start
	m.Sync(context.Background())      // A records first, through port forwarding
	if g.get("photos.lab") != "81.2.69.160" {
		t.Fatalf("before the relay: %q", g.get("photos.lab"))
	}
	relay := Relayed{Name: "r-abc.relay.roost.test", ProofName: "_roostack.lab.example.com", ProofValue: "roostack-proof=t0k"}
	relayed := true
	m.Relay = func() (Relayed, bool) { return relay, relayed }
	st := m.Sync(context.Background())
	if g.get("photos.lab") != "" || g.getType("photos.lab", "CNAME") != "r-abc.relay.roost.test." {
		t.Errorf("photos: A %q, CNAME %q", g.get("photos.lab"), g.getType("photos.lab", "CNAME"))
	}
	if st.Public[0].State != PublicOK || st.Public[0].Type != "CNAME" || st.PublicIP != relay.Name {
		t.Errorf("photos' state: %+v", st.Public[0])
	}
	if st.Public[1].State != PublicConflict || g.get("vault.lab") != "203.0.113.9" {
		t.Errorf("vault, someone else's A record: %+v", st.Public[1])
	}
	if g.getType("_roostack.lab", "TXT") != `"roostack-proof=t0k"` || st.Proof == nil || st.Proof.State != PublicOK {
		t.Errorf("the proof: %q %+v", g.getType("_roostack.lab", "TXT"), st.Proof)
	}
	if g.get("*.lab") != "192.168.1.20" {
		t.Error("the wildcard should still point home")
	}
	// Relaying stopped: A records again, the proof gone.
	relayed = false
	st = m.Sync(context.Background())
	if g.getType("photos.lab", "CNAME") != "" || g.get("photos.lab") != "81.2.69.160" || g.getType("_roostack.lab", "TXT") != "" || st.Proof != nil {
		t.Errorf("after: CNAME %q, A %q, TXT %q, proof %+v", g.getType("photos.lab", "CNAME"), g.get("photos.lab"),
			g.getType("_roostack.lab", "TXT"), st.Proof)
	}
	// By hand: what to add.
	relayed = true
	m.Lab.DNS.ManageRecords = false
	m.PublicLookup = func(context.Context, string) ([]string, error) { return nil, nil }
	st = m.Sync(context.Background())
	if st.Public[0].State != PublicManual || !strings.Contains(st.Public[0].Message, "photos.lab.example.com  CNAME  r-abc.relay.roost.test") ||
		st.Proof == nil || !strings.Contains(st.Proof.Message, "_roostack.lab.example.com  TXT  roostack-proof=t0k") {
		t.Errorf("by hand: %+v %+v", st.Public, st.Proof)
	}
}
