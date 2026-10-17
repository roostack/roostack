package agent

import (
	"net/netip"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/trust"
)

// TestRelayFirewall: from Roostack Cloud's relay only TCP 443 gets in,
// settled before the home-network rules its addresses would match.
func TestRelayFirewall(t *testing.T) {
	rs := DefaultFirewall().Ruleset()
	accept := strings.Index(rs, `iifname "roostack-relay" tcp dport 8474 accept`)
	drop := strings.Index(rs, `iifname "roostack-relay" drop`)
	lan := strings.Index(rs, "@lan4")
	if accept < 0 || drop < accept || lan < drop {
		t.Errorf("relay rules: accept %d, drop %d, first home-network rule %d\n%s", accept, drop, lan, rs)
	}
}

func TestRelayInterface(t *testing.T) {
	ok := WireGuard{Interface: RelayInterface, Address: "100.81.1.5/16",
		Peers: []Peer{{PublicKey: "aG9tZWhvbWVob21laG9tZWhvbWVob21laG9tZWhvbWU=", AllowedIPs: []string{"100.81.0.1/32"},
			Endpoint: "198.51.100.10:51830", Keepalive: 25}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("a relay interface: %v", err)
	}
	bad := ok
	bad.Address = "100.96.0.5/24"
	if bad.Validate() == nil {
		t.Error("the relay interface took an address outside the relay network")
	}
	bad = ok
	bad.Peers = []Peer{{PublicKey: ok.Peers[0].PublicKey, AllowedIPs: []string{"0.0.0.0/0"}}}
	if bad.Validate() == nil {
		t.Error("a relay peer may send from anywhere")
	}
}

// TestHomeNetworkSet: the nftables sets are trust.HomeRanges, with the
// CGNAT range cut up (review H3).
func TestHomeNetworkSet(t *testing.T) {
	rs := DefaultFirewall().Ruleset()
	if !strings.Contains(rs, "elements = { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/12, 100.80.0.0/16,") ||
		strings.Contains(rs, "100.64.0.0/10") {
		t.Errorf("home network set:\n%s", rs)
	}
}

// guestFirewall is a server offering storage to two other labs.
func guestFirewall() Firewall {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow,
		Rule{Port: 8080, Proto: "tcp", For: trust.Home, Reason: "a home-only package port"},
		Rule{Port: GuestsPort, Proto: "udp", For: trust.Anyone, Reason: "storage for other labs (wireguard)"},
		Rule{Port: 8472, Proto: "tcp", For: trust.Guests, Reason: "storage for other labs"},
		Rule{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2", Reason: "storage for g1"},
		Rule{Port: 8101, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2", Reason: "storage for g1"},
		Rule{Port: 8102, Proto: "tcp", For: trust.Guests, Source: "100.100.5.3", Reason: "storage for g2"},
		Rule{Port: 8103, Proto: "tcp", For: trust.Guests, Source: "100.100.5.3", Reason: "storage for g2"},
	)
	return fw
}

// TestGuestFirewall: another lab reaches the guest API and its own store,
// and nothing else: not SSH, home-only ports, or another guest's store;
// nothing is forwarded to or from it (review H3; SPEC.md §10.10).
func TestGuestFirewall(t *testing.T) {
	fw := guestFirewall()
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	rs := fw.Ruleset()
	in, fwd, _ := strings.Cut(rs, "chain forward")
	drop := strings.Index(in, `iifname "roostack-guests" drop`)
	stDrop := strings.Index(in, `iifname "roostack-st*" drop`)
	lan := strings.Index(in, "@lan4")
	if drop < 0 || stDrop < 0 || lan < drop || lan < stDrop {
		t.Errorf("guest drops %d, %d before the first home-network rule %d:\n%s", drop, stDrop, lan, rs)
	}
	for _, want := range []string{
		`iifname "roostack-guests" tcp dport 8472 accept`,
		`iifname "roostack-guests" ip saddr 100.100.5.2 tcp dport 8100 accept`,
		`iifname "roostack-guests" ip saddr 100.100.5.3 tcp dport 8103 accept`,
	} {
		if i := strings.Index(in, want); i < 0 || i > drop {
			t.Errorf("missing %q before the guests' drop:\n%s", want, rs)
		}
	}
	// Every rule for the guests' interface comes before its drop.
	if strings.Count(in, `iifname "roostack-guests"`) != 6 {
		t.Errorf("guest rules:\n%s", in)
	}
	for _, want := range []string{`iifname "roostack-guests" drop`, `oifname "roostack-guests" drop`,
		`iifname "roostack-st*" drop`, `oifname "roostack-st*" drop`, `iifname "roostack-relay" drop`} {
		if !strings.Contains(fwd, want) {
			t.Errorf("forward chain lacks %q:\n%s", want, fwd)
		}
	}
	// A guest's source is outside every "lan" rule's set.
	if trust.IsHome(netip.MustParseAddr("100.100.5.2")) {
		t.Error("a guest is on the home network")
	}
}

func TestRuleSource(t *testing.T) {
	for _, r := range []Rule{
		{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "192.168.1.5"},
		{Port: 8100, Proto: "tcp", For: trust.Anyone, Source: "100.100.5.2"},
		{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2/24"},
		{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2 accept"},
	} {
		if (Firewall{SSHFrom: FromLAN, Allow: []Rule{r}}).Validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

// TestEdgeFirewall: edges' and the relay's connections to 443 reach Caddy's
// edge listener, where no visitor is private, and only they reach that
// listener (review H4).
func TestEdgeFirewall(t *testing.T) {
	if EdgePort != caddy.EdgePort {
		t.Fatalf("EdgePort %d, Caddy's %d", EdgePort, caddy.EdgePort)
	}
	fw := DefaultFirewall()
	fw.Edges = []string{"100.96.0.2", "100.96.0.7"}
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	rs := fw.Ruleset()
	for _, want := range []string{
		"type nat hook prerouting priority dstnat; policy accept;",
		`iifname "roostack-relay" tcp dport 443 redirect to :8474`,
		`iifname "roostack" ip saddr { 100.96.0.2, 100.96.0.7 } tcp dport 443 redirect to :8474`,
		`iifname "roostack-relay" tcp dport 8474 accept`,
		`iifname "roostack" ip saddr { 100.96.0.2, 100.96.0.7 } tcp dport 8474 accept`,
	} {
		if !strings.Contains(rs, want) {
			t.Errorf("missing %q:\n%s", want, rs)
		}
	}
	if strings.Contains(rs, "dport 8474 accept\n") && strings.Count(rs, "dport 8474 accept") != 2 {
		t.Errorf("the edge port is open to others:\n%s", rs)
	}
	for _, bad := range [][]string{{"192.168.1.5"}, {"100.81.0.1"}, {"100.96.0.2 } accept {"}} {
		if (Firewall{SSHFrom: FromLAN, Edges: bad}).Validate() == nil {
			t.Errorf("accepted edges %v", bad)
		}
	}
	// Without edges of its own, only the relay's 443 is redirected.
	if rs := DefaultFirewall().Ruleset(); strings.Contains(rs, `iifname "roostack" ip saddr {`) {
		t.Errorf("edge rules without edges:\n%s", rs)
	}
}

// TestJoiningFirewall: on the hub, a server still joining (a join code's
// one-time key) reaches the lab API and nothing else, and nothing from it
// is forwarded; the lab's servers are unaffected (review M23).
func TestJoiningFirewall(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: 8471, Proto: "tcp", For: trust.Servers, Reason: "roostack servers"})
	fw.Forward = []Rule{{Port: 8000, Proto: "tcp", For: trust.Servers, Reason: "backups"}}
	fw.JoinPort, fw.Members = 8471, []string{"100.96.0.2", "100.96.0.3"}
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	rs := fw.Ruleset()
	in, fwd, _ := strings.Cut(rs, "chain forward")
	accept := strings.Index(in, `iifname "roostack" ip saddr 100.96.0.0/17 ip saddr != { 100.96.0.2, 100.96.0.3 } tcp dport 8471 accept`)
	drop := strings.Index(in, `iifname "roostack" ip saddr 100.96.0.0/17 ip saddr != { 100.96.0.2, 100.96.0.3 } drop`)
	lan := strings.Index(in, "@lan4")
	if accept < 0 || drop < accept || lan < drop {
		t.Errorf("joining rules: accept %d, drop %d, first home-network rule %d\n%s", accept, drop, lan, rs)
	}
	fdrop := strings.Index(fwd, `iifname "roostack" ip saddr 100.96.0.0/17 ip saddr != { 100.96.0.2, 100.96.0.3 } drop`)
	if fdrop < 0 || fdrop > strings.Index(fwd, "tcp dport 8000 accept") {
		t.Errorf("forward chain:\n%s", fwd)
	}
	// A hub with no other servers yet: everyone there is joining.
	fw.Members = nil
	if rs := fw.Ruleset(); !strings.Contains(rs, "\t\tiifname \"roostack\" ip saddr 100.96.0.0/17 tcp dport 8471 accept\n"+
		"\t\tiifname \"roostack\" ip saddr 100.96.0.0/17 drop\n") {
		t.Errorf("no members:\n%s", rs)
	}
	for _, bad := range []Firewall{{SSHFrom: FromLAN, Members: []string{"100.96.0.2"}},
		{SSHFrom: FromLAN, JoinPort: 8471, Members: []string{"192.168.1.5"}},
		{SSHFrom: FromLAN, JoinPort: 8471, Members: []string{"100.96.128.5"}}, // a person's device
		{SSHFrom: FromLAN, Controller: "100.96.128.1"}, {SSHFrom: FromLAN, Controller: "100.96.0.1 accept"},
		{SSHFrom: FromLAN, Edges: []string{"100.96.200.1"}}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
