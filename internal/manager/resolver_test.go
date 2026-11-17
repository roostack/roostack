package manager

import (
	"context"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

// TestResolverChoice: the lab's DNS server is the Owner's choice; an
// address must be one, the servers that use it must be the lab's, and
// each server is sent it only if it chose it (SPEC.md §6.5.2).
func TestResolverChoice(t *testing.T) {
	m, _, _, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	if r := m.Resolver(ctx); r.Service != nil || r.Addr != "" {
		t.Errorf("no DNS service yet: %+v", r)
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
