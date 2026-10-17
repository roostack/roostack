package firewall

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/trust"
)

func TestDesired(t *testing.T) {
	lab := config.Default()
	if _, ok := Desired(lab, nil, Roles{}); ok {
		t.Error("no firewall until the Owner has chosen SSH access")
	}
	lab.Firewall.SSH = config.SSHOff
	fw, ok := Desired(lab, []*generate.Output{{Service: "forgejo", HostPorts: []generate.HostPort{
		{Name: "ssh", Port: 2222, Protocol: "tcp", Exposure: "private"}}}}, Roles{})
	if !ok || fw.SSHFrom != agent.FromOff {
		t.Fatalf("fw = %+v", fw)
	}
	want := agent.Rule{Port: 2222, Proto: "tcp", For: trust.Home, Reason: "forgejo ssh"}
	if !slices.Contains(fw.Allow, want) || fw.Validate() != nil {
		t.Errorf("package port rule missing or invalid: %+v", fw.Allow)
	}
	if len(fw.Forward) != 0 {
		t.Error("forwarding only on the hub")
	}
	// The hub forwards backups to another server's store, nothing else.
	lab.OverlayAddr = "100.96.0.1"
	fw, _ = Desired(lab, nil, Roles{})
	if len(fw.Forward) != 1 || fw.Forward[0].Port != generate.StoragePort || fw.Validate() != nil {
		t.Errorf("hub forward = %+v", fw.Forward)
	}
	// The lab's own edge goes to Caddy's edge listener; a relay edge is
	// the agent's business (its interface).
	lab.EdgeAddrs = []string{"100.96.0.2/32", "100.81.0.9/32"}
	fw, _ = Desired(lab, nil, Roles{})
	if !slices.Equal(fw.Edges, []string{"100.96.0.2"}) || fw.Validate() != nil {
		t.Errorf("edges = %v", fw.Edges)
	}
}

// TestDesiredRoles: what a server does beyond its services opens a way in
// for the right class only (internal/trust).
func TestDesiredRoles(t *testing.T) {
	lab := config.Default()
	lab.Firewall.SSH = config.SSHFromLAN
	lab.OverlayAddr = "100.96.0.1"
	fw, _ := Desired(lab, nil, Roles{Hub: true, Members: []string{"100.96.0.2"}, Store: true, ProxyPorts: []int{8200},
		GuestAddr: "100.100.5.1", Guests: []generate.GuestStorage{{User: "g1", Addr: "100.100.5.2", Port: 8100}, {User: "old", Port: 8102}},
		LinksListen: true})
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	if fw.JoinPort != 8471 || !slices.Equal(fw.Members, []string{"100.96.0.2"}) {
		t.Errorf("join port %d, members %v", fw.JoinPort, fw.Members)
	}
	for _, want := range []agent.Rule{
		{Port: generate.StoragePort, Proto: "tcp", For: trust.Servers, Reason: "backup store"},
		{Port: generate.StorageAdminPort, Proto: "tcp", For: trust.Servers, Reason: "backup store"},
		{Port: 8200, Proto: "tcp", For: trust.Servers, Reason: "backups to another lab"},
		{Port: agent.GuestsPort, Proto: "udp", For: trust.Anyone, Reason: "storage for other labs (wireguard)"},
		{Port: 8472, Proto: "tcp", For: trust.Guests, Reason: "storage for other labs"},
		{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2", Reason: "storage for g1"},
		{Port: 8101, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2", Reason: "storage for g1"},
		{Port: agent.WireGuardPort, Proto: "udp", For: trust.Anyone, Reason: "roostack direct links between servers"},
	} {
		if !slices.Contains(fw.Allow, want) {
			t.Errorf("missing %+v in %+v", want, fw.Allow)
		}
	}
	// A guest without an address (an older controller) gets no store port.
	for _, r := range fw.Allow {
		if r.Port == 8102 {
			t.Errorf("a guest without an address: %+v", r)
		}
	}
	// A hub with no other servers yet: everyone there is joining.
	if fw, _ := Desired(lab, nil, Roles{Hub: true}); fw.JoinPort != 8471 || len(fw.Members) != 0 {
		t.Errorf("a hub alone: join port %d, members %v", fw.JoinPort, fw.Members)
	}
}

func TestSSHClients(t *testing.T) {
	dir := t.TempDir()
	ProcTCP, ProcTCP6 = filepath.Join(dir, "tcp"), filepath.Join(dir, "tcp6")
	t.Cleanup(func() { ProcTCP, ProcTCP6 = "/proc/net/tcp", "/proc/net/tcp6" })
	// Listening on :22, a session from 192.168.86.20, one from 10.0.2.2,
	// and an outgoing connection to someone else's port 22.
	os.WriteFile(ProcTCP, []byte(`  sl  local_address rem_address   st tx_queue rx_queue
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0
   1: 0F02000A:0016 1456A8C0:C350 01 00000000:00000000 02:000A3E2F 00000000     0
   2: 0F02000A:0016 0202000A:D431 01 00000000:00000000 02:000A3E2F 00000000     0
   3: 0F02000A:9C40 08080808:0016 01 00000000:00000000 02:000A3E2F 00000000  1000
`), 0o644)
	// An IPv4-mapped session from 203.0.113.9 over the IPv6 socket.
	os.WriteFile(ProcTCP6, []byte(`  sl  local_address                         remote_address                        st
   0: 0000000000000000FFFF00000F02000A:0016 0000000000000000FFFF0000097100CB:D000 01 00000000:00000000
`), 0o644)
	got, err := SSHClients()
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("10.0.2.2"), netip.MustParseAddr("192.168.86.20"), netip.MustParseAddr("203.0.113.9")}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if cut := CutsOff(config.SSHFromLAN, got); !slices.Equal(cut, want[2:]) {
		t.Errorf("home network only cuts off %v", cut)
	}
	if cut := CutsOff(config.SSHOff, got); len(cut) != 3 {
		t.Errorf("off cuts off %v", cut)
	}
	if cut := CutsOff(config.SSHFromAny, got); len(cut) != 0 {
		t.Errorf("anywhere cuts off %v", cut)
	}
}
