package trust

import (
	"net/netip"
	"strings"
	"testing"
)

// TestHomeNetwork: other labs (storage for others) and Roostack Cloud's
// relay are never the home network (review H3).
func TestHomeNetwork(t *testing.T) {
	for _, a := range []string{"100.100.0.2", "100.103.255.254", "100.81.0.1", "::ffff:100.100.7.9", "203.0.113.9", "2001:db8::1"} {
		if IsHome(netip.MustParseAddr(a)) {
			t.Errorf("%s counts as the home network", a)
		}
	}
	for _, a := range []string{"192.168.1.5", "10.0.2.2", "100.96.0.2", "100.64.0.1", "100.127.255.254", "100.80.9.9",
		"100.99.1.1", "100.104.0.1", "fd00::5", "fe80::1"} {
		if !IsHome(netip.MustParseAddr(a)) {
			t.Errorf("%s isn't the home network", a)
		}
	}
}

func TestClassify(t *testing.T) {
	a := netip.MustParseAddr
	lab := Lab{Members: []netip.Addr{a("100.96.0.1"), a("100.96.0.2"), a("100.96.0.3")}, Edges: []netip.Addr{a("100.96.0.3")}}
	for _, c := range []struct {
		iface, addr string
		want        Class
	}{
		{"eth0", "192.168.1.5", Home},
		{"tailscale0", "100.64.1.1", Home},
		{"eth0", "203.0.113.9", Anyone},
		{"eth0", "100.100.5.2", Anyone}, // a guest's address, not on its interface
		{LabInterface, "100.96.0.2", Servers},
		{LabInterface, "100.96.0.3", Edge},
		{LabInterface, "100.96.0.9", Joining},
		{GuestsInterface, "100.100.5.2", Guests},
		{RelayInterface, "100.81.0.1", Relay},
		{"roostack-st2", "100.100.9.1", StoreLink},
	} {
		if got := Classify(c.iface, a(c.addr), lab); got != c.want {
			t.Errorf("Classify(%s, %s) = %q, want %q", c.iface, c.addr, got, c.want)
		}
	}
	// Away from the hub, everyone on the lab's network is a server.
	if got := Classify(LabInterface, a("100.96.0.9"), Lab{}); got != Servers {
		t.Errorf("a server elsewhere = %q", got)
	}
}

func TestWithout(t *testing.T) {
	p := netip.MustParsePrefix
	got := Without(p("10.0.0.0/8"), p("10.1.0.0/16"))
	var s []string
	for _, x := range got {
		s = append(s, x.String())
	}
	want := "10.0.0.0/16 10.2.0.0/15 10.4.0.0/14 10.8.0.0/13 10.16.0.0/12 10.32.0.0/11 10.64.0.0/10 10.128.0.0/9"
	if strings.Join(s, " ") != want {
		t.Errorf("Without = %v, want %s", s, want)
	}
	if got := Without(p("10.1.0.0/16"), p("10.0.0.0/8")); len(got) != 0 {
		t.Errorf("a range inside the hole: %v", got)
	}
	if got := Without(p("10.0.0.0/8"), p("192.168.0.0/16")); len(got) != 1 {
		t.Errorf("an unrelated hole: %v", got)
	}
}
