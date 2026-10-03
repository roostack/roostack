package dnsrecords

import (
	"context"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

// link.<domain> is made only once it's wanted, points at the home's public
// address without waiting for CrowdSec, and stays there; on a lab on the
// internet the wildcard covers it; unmanaged, it's listed to add by hand.
func TestLinkRecord(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20", "81.2.69.160"})
	m.Entry = fakeEntry{ip: "81.2.69.160"}
	m.Protected = func(context.Context) bool { return false }
	wanted := false
	m.LinkWanted = func() bool { return wanted }

	if st := m.Sync(context.Background()); st.Link != nil || g.get("link.lab") != "" {
		t.Fatalf("not wanted: %+v, record %q", st.Link, g.get("link.lab"))
	}
	wanted = true
	st := m.Sync(context.Background())
	if st.Link == nil || st.Link.State != PublicOK || g.get("link.lab") != "81.2.69.160" || st.PublicIP != "81.2.69.160" {
		t.Fatalf("wanted: %+v, record %q", st, g.get("link.lab"))
	}
	if m.Sync(context.Background()); g.get("link.lab") != "81.2.69.160" {
		t.Error("it stays, unlike a private address")
	}

	m.Lab.DNS.ManageRecords = false
	st = m.Sync(context.Background())
	if st.Link.State != PublicOK || !strings.Contains(st.Link.Message, "points at 81.2.69.160") {
		t.Errorf("unmanaged, already there: %+v", st.Link)
	}
	m.PublicLookup = func(context.Context, string) ([]string, error) { return nil, nil }
	if st = m.Sync(context.Background()); st.Link.State != PublicManual || !strings.Contains(st.Link.Message, "link.lab.example.com  A  81.2.69.160") {
		t.Errorf("unmanaged: %+v", st.Link)
	}

	m.Lab.DNS.ManageRecords = true
	m.Lab.Network = config.NetworkInternet
	m.PublicLookup = func(context.Context, string) ([]string, error) { return []string{"81.2.69.160"}, nil }
	if st = m.Sync(context.Background()); st.Link.State != PublicOK || !strings.Contains(st.Link.Message, "through *.lab.example.com") {
		t.Errorf("on the internet: %+v", st.Link)
	}
}
