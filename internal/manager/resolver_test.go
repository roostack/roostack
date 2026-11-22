package manager

import (
	"context"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/state"
)

// TestResolverChoice: the lab's DNS server is the Owner's choice; an
// address must be one, the servers that use it must be the lab's, and
// each server is sent it only if it chose it (SPEC.md §6.5.2).
func TestResolverChoice(t *testing.T) {
	m, _, _, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.DNS.LANIP = "192.168.1.5"
	if r := m.Resolver(ctx); r.Service != nil || !r.Own || r.OwnServer != m.Lab.NodeID || r.Addr != "192.168.1.5" ||
		r.ServerAddr != "192.168.1.5:5055" {
		t.Errorf("no DNS service yet, so Roostack's own: %+v", r)
	}
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverAddress, Address: "dns.example.com"}); err == nil {
		t.Error("a name accepted")
	}
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverAddress, Address: " 192.168.1.2 ",
		Servers: []string{"n-0000000002", "n-9999999999"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got := m.Lab.Resolver
	if got.Address != "192.168.1.2" || len(got.Servers) != 1 || got.Servers[0] != "n-0000000002" {
		t.Errorf("saved %+v", got)
	}
	r := m.Resolver(ctx)
	if r.Addr != "192.168.1.2" || resolverFor(r, "n-0000000002") != "192.168.1.2:53" || resolverFor(r, "n-0000000003") != "" {
		t.Errorf("resolver %+v", r)
	}
	// A device reaches a home address only through the VPN; a public one
	// anyway.
	if got := m.DeviceDNS(ctx, 1, []string{"192.168.1.20/32"}); got != "" {
		t.Errorf("unreachable: %q", got)
	}
	if got := m.DeviceDNS(ctx, 1, []string{"192.168.1.0/24"}); got != "192.168.1.2" {
		t.Errorf("home network: %q", got)
	}
	m.SetResolver(ctx, config.Resolver{Use: config.ResolverAddress, Address: "9.9.9.9"})
	if got := m.DeviceDNS(ctx, 1, nil); got != "9.9.9.9" {
		t.Errorf("public: %q", got)
	}
	if err := m.SetResolver(ctx, config.Resolver{Use: "auto"}); err != nil || m.Lab.Resolver.Use != "" || m.Lab.Resolver.Address != "" {
		t.Errorf("automatic: %v %+v", err, m.Lab.Resolver)
	}
}

// TestOwnResolver: Roostack's own DNS server runs on the controller at
// home, else on a server at home the Owner chose (the first by name by
// default), never where a DNS service takes port 53, and the server
// running it never passes its own lookups to it (SPEC.md §6.5.3).
func TestOwnResolver(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.DNS.LANIP = "192.168.1.5"
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverRoostack, Servers: []string{m.Lab.NodeID, "n-0000000002"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	r := m.Resolver(ctx)
	if !r.Own || r.OwnServer != m.Lab.NodeID || r.Addr != "192.168.1.5" {
		t.Fatalf("at home: %+v", r)
	}
	if resolverFor(r, m.Lab.NodeID) != "" || resolverFor(r, "n-0000000002") != "192.168.1.5:5055" {
		t.Errorf("servers that use it: controller %q, home %q", resolverFor(r, m.Lab.NodeID), resolverFor(r, "n-0000000002"))
	}

	// The controller in the cloud: a server at home runs it.
	m.Lab.Network = config.NetworkInternet
	if r := m.Resolver(ctx); r.OwnServer != "" || r.Why == "" {
		t.Errorf("no server at home: %+v", r)
	}
	home.home, vps.home = []string{"192.168.1.10"}, []string{"192.168.1.20"}
	r = m.Resolver(ctx)
	if len(r.Homes) != 2 || r.OwnServer != r.Homes[0].ID || r.Addr != r.Homes[0].Addr {
		t.Fatalf("the first at home by name: %+v", r)
	}
	other := r.Homes[1]
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverRoostack, Server: other.ID}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if r := m.Resolver(ctx); r.OwnServer != other.ID || r.ServerAddr != other.Addr+":5055" {
		t.Errorf("the Owner's choice: %+v", r)
	}
	// Not before first-run setup has chosen the firewall.
	running, idle := vps.fakeRemote, home.fakeRemote
	if other.ID == home.id.ID {
		running, idle = idle, running
	}
	if c := running.last.Lab.DNS; c != nil {
		t.Errorf("sent before setup chose the firewall: %+v", c)
	}
	m.mu.Lock()
	m.saveLab(func(l *config.Lab) { l.Firewall.SSH = config.SSHFromLAN })
	m.mu.Unlock()
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverRoostack, Server: other.ID}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	// Only that server is sent the names; with the controller in the
	// cloud, it passes them all on (the public records point at it).
	running, idle = vps.fakeRemote, home.fakeRemote
	if other.ID == home.id.ID {
		running, idle = idle, running
	}
	if c := running.last.Lab.DNS; c == nil || c.Domain != m.Lab.Domain || len(c.Names) != 0 || c.CheckAddr != "" {
		t.Errorf("the server running it was sent %+v", c)
	}
	if c := idle.last.Lab.DNS; c != nil {
		t.Errorf("the other server was sent %+v", c)
	}
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverRoostack, Server: "n-9999999999", Fallback: "192.168.1.1"}); err == nil {
		t.Error("a private fallback accepted")
	}
	if err := m.SetResolver(ctx, config.Resolver{Use: config.ResolverRoostack, Server: "n-9999999999", Fallback: " 1.1.1.1 "}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got := m.Lab.Resolver; got.Server != "" || got.Fallback != "1.1.1.1" {
		t.Errorf("saved %+v: an unknown server is forgotten", got)
	}
}

// TestLabDNSNames: Roostack's own DNS server answers every name Caddy
// serves with the home address, and those at the old address too.
func TestLabDNSNames(t *testing.T) {
	m, _, _ := setUpMove(t)
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		t.Fatal(err)
	}
	lab := m.Lab
	lab.Monitoring.StatusPage = true
	c := labDNSConfig(lab, services, "192.168.1.5", "")
	d := lab.Domain
	for _, n := range []string{"roost." + d, "my." + d, "status." + d, "git." + d} {
		if c.Names[n] != "192.168.1.5" {
			t.Errorf("%s: %q in %v", n, c.Names[n], c.Names)
		}
	}
	if _, ok := c.Names["www."+d]; ok || c.CheckAddr != "192.168.1.5" {
		t.Errorf("names %v, check %q", c.Names, c.CheckAddr)
	}
	lab.OldAddress = &config.OldAddress{Domain: "old.example.net", Until: time.Now().Add(time.Hour)}
	if c := labDNSConfig(lab, services, "192.168.1.5", ""); c.Names["git.old.example.net"] != "192.168.1.5" || c.Names["roost.old.example.net"] == "" {
		t.Errorf("the old address's names: %v", c.Names)
	}
	if c := labDNSConfig(lab, services, "", "1.1.1.1"); len(c.Names) != 0 || c.Fallback != "1.1.1.1" {
		t.Errorf("no home address: %+v", c)
	}
}
