package agent

import (
	"strings"
	"testing"
)

// TestRelayFirewall: from Roostack Cloud's relay only TCP 443 gets in,
// settled before the home-network rules its addresses would match.
func TestRelayFirewall(t *testing.T) {
	rs := DefaultFirewall().Ruleset()
	accept := strings.Index(rs, `iifname "roostack-relay" tcp dport 443 accept`)
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
