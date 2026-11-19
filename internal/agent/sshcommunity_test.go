package agent

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// A list from the daemon: addresses and ranges on the internet, the
// overlaps merged; private and home addresses left out; a range too wide,
// text that isn't an address, or too long a list refused.
func TestCommunityList(t *testing.T) {
	got, err := communityList([]string{"203.0.113.9", "198.51.100.0/24", "198.51.100.7", "203.0.113.9",
		"192.168.1.5", "10.0.0.0/16", "127.0.0.1", "100.96.128.4", "::ffff:192.0.2.1", "2001:db8:1::/48", "2001:db8:1:2::5"})
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, p := range got {
		s = append(s, communityElement(p))
	}
	want := []string{"192.0.2.1", "198.51.100.0/24", "203.0.113.9", "2001:db8:1::/48"}
	if !slices.Equal(s, want) {
		t.Errorf("list = %q, want %q", s, want)
	}
	for _, bad := range [][]string{{"10.0.0.0/8"}, {"198.0.0.0/15"}, {"2001:db8::/31"}, {"not an address"}, {"0.0.0.0/0"}} {
		if _, err := communityList(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if _, err := communityList(make([]string, communityMax+1)); err == nil {
		t.Error("a list too long: no error")
	}
}

// The sets are replaced in one transaction, each address with what's left
// of communityFor; an address that logged in over SSH in the last day
// isn't in them; a firewall load puts them back; a list that stops coming
// lifts by itself.
func TestSetCommunity(t *testing.T) {
	b, _ := testBlocker()
	var scripts []string
	b.nftScript = func(s string) error { scripts = append(scripts, s); return nil }
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	b.loggedIn(t0.Add(-time.Hour), netip.MustParseAddr("203.0.113.20"))
	list, _ := communityList([]string{"203.0.113.9", "203.0.113.20", "2001:db8:1::/48"})
	b.setCommunity(list, t0)
	want := "flush set inet roostack ssh_community4\n" +
		"add element inet roostack ssh_community4 { 203.0.113.9 timeout 21600s }\n" +
		"flush set inet roostack ssh_community6\n" +
		"add element inet roostack ssh_community6 { 2001:db8:1::/48 timeout 21600s }\n"
	if len(scripts) != 1 || scripts[0] != want {
		t.Fatalf("nft script:\n%s\nwant:\n%s", strings.Join(scripts, "---\n"), want)
	}
	if n, at := b.communityCount(t0); n != 3 || !at.Equal(t0) {
		t.Errorf("count = %d at %v", n, at)
	}

	// The firewall loaded again two hours later: four hours left.
	b.reload(t0.Add(2 * time.Hour))
	if len(scripts) != 2 || !strings.Contains(scripts[1], "203.0.113.9 timeout 14400s") {
		t.Errorf("after a load: %q", scripts[1:])
	}
	// No new list for six hours: nothing to put back, and none counted.
	b.reload(t0.Add(communityFor))
	if len(scripts) != 2 {
		t.Errorf("put back after running out: %q", scripts[2:])
	}
	if n, _ := b.communityCount(t0.Add(communityFor)); n != 0 {
		t.Errorf("counted after running out: %d", n)
	}
}
