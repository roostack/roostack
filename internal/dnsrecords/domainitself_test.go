package dnsrecords

import (
	"context"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

// The domain itself (SPEC.md §5.3): no record while it's left alone; one
// at home when it's served home-only, taken over only with the Owner's
// say; one at the home's public address when it's public; Roostack's
// record goes when it's left alone again.
func TestDomainItselfRecord(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20", "81.2.69.160"})
	ctx := context.Background()
	choice := config.DomainItself{}
	m.DomainItself = func() config.DomainItself { return choice }
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	m.PublicHosts = func() []string {
		if choice.Public {
			return []string{"lab.example.com"}
		}
		return nil
	}
	g.put("lab", "203.0.113.7") // the Owner's website
	if st := m.Sync(ctx); st.State != StateOK || g.get("lab") != "203.0.113.7" {
		t.Fatalf("left alone: %+v, record %q", st, g.get("lab"))
	}

	choice = config.DomainItself{Show: config.ShowAccount}
	st := m.Sync(ctx)
	if st.State != StateConflict || st.Record != "lab.example.com" || st.Current != "203.0.113.7" || g.get("lab") != "203.0.113.7" {
		t.Fatalf("someone else's record: %+v", st)
	}
	if err := m.TakeOver(ctx); err != nil {
		t.Fatal(err)
	}
	if st := m.Sync(ctx); st.State != StateOK || g.get("lab") != "192.168.1.20" || g.get("*.lab") != "192.168.1.20" {
		t.Fatalf("taken over: %+v, record %q", st, g.get("lab"))
	}

	choice.Public = true
	st = m.Sync(ctx)
	if g.get("lab") != "81.2.69.160" || len(st.Public) != 1 || st.Public[0].State != PublicOK {
		t.Fatalf("public: %+v, record %q", st.Public, g.get("lab"))
	}
	if st := m.Sync(ctx); g.get("lab") != "81.2.69.160" {
		t.Errorf("a second sync moved it: %+v, record %q", st, g.get("lab"))
	}

	choice = config.DomainItself{Show: config.ShowNothing}
	if st := m.Sync(ctx); st.State != StateOK || g.get("lab") != "" {
		t.Errorf("left alone again: %+v, record %q", st, g.get("lab"))
	}
}

// On a lab on the internet, the domain itself points at the server, public
// or not, and Roostack's record goes when it's left alone.
func TestDomainItselfOnInternet(t *testing.T) {
	m, g := newManager(t, managed, nil, []string{"81.2.69.160"})
	m.Lab.Network = config.NetworkInternet
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	choice := config.DomainItself{Show: config.ShowPage}
	m.DomainItself = func() config.DomainItself { return choice }
	m.Sync(context.Background())
	if g.get("lab") != "81.2.69.160" || g.get("*.lab") != "81.2.69.160" {
		t.Fatalf("records %q %q", g.get("lab"), g.get("*.lab"))
	}
	choice = config.DomainItself{}
	if m.Sync(context.Background()); g.get("lab") != "" {
		t.Errorf("left alone: %q", g.get("lab"))
	}
}

// Through Roostack Cloud's relay, the domain itself at the top of its zone
// can't be a CNAME: the Owner is told what to do by hand.
func TestDomainItselfRelayed(t *testing.T) {
	cfg := managed
	cfg.Zone = "lab.example.com"
	m, _ := newManager(t, cfg, []string{"192.168.1.20"}, []string{"198.51.100.10"})
	m.DomainItself = func() config.DomainItself { return config.DomainItself{Show: config.ShowPage, Public: true} }
	m.PublicHosts = func() []string { return []string{"lab.example.com"} }
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	m.Relay = func() (Relayed, bool) { return Relayed{Name: "r-abc.relay.roost.test"}, true }
	m.PublicLookup = func(_ context.Context, host string) ([]string, error) {
		if host == "r-abc.relay.roost.test" {
			return []string{"198.51.100.10"}, nil
		}
		return nil, nil
	}
	st := m.Sync(context.Background())
	if len(st.Public) != 1 || st.Public[0].State != PublicManual || !strings.Contains(st.Public[0].Message, "flattens CNAMEs") {
		t.Errorf("relayed: %+v", st.Public)
	}
}
