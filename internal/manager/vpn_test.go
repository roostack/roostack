package manager

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/store"
)

// fakePeople is the store's VPN devices and who may use what.
type fakePeople struct {
	devices []store.VPNDevice
	cannot  map[string]int64 // service → the person who may not use it
}

func (f *fakePeople) VPNDevices(context.Context) ([]store.VPNDevice, error) { return f.devices, nil }

func (f *fakePeople) CanUse(_ context.Context, service string, userID int64) (bool, error) {
	return f.cannot[service] != userID, nil
}

// fakeVPNLinks is the agent's WireGuard, keeping the VPN's interface.
type fakeVPNLinks struct {
	mu  sync.Mutex
	wg  *agent.WireGuard
	off int
}

func (f *fakeVPNLinks) ApplyWireGuard(_ context.Context, wg agent.WireGuard) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wg = &wg
	return "c2VydmVyLWtleS1zZXJ2ZXIta2V5LXNlcnZlci1rZXk=", nil
}

func (f *fakeVPNLinks) WireGuardStatus(context.Context, string) (agent.WireGuardStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return agent.WireGuardStatus{Configured: f.wg != nil}, nil
}

func (f *fakeVPNLinks) RemoveWireGuard(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wg = nil
	f.off++
	return nil
}

func (f *fakeVPNLinks) ApplyLinks(context.Context, agent.Links) error { return nil }

// The lab's VPN (SPEC.md §10.11): turned on, the controller runs its
// interface with every device as a peer; each device reaches the services
// its person may use, Roostack's pages and SSH only as its person's role
// allows, and its traffic goes on only as the Owner allows; turned off,
// the interface stops.
func TestVPN(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	m.Lab.Firewall.SSH = config.SSHFromLAN
	local(m).Lab = m.Lab
	fw, links := &fakeFirewall{}, &fakeVPNLinks{}
	local(m).Firewall, local(m).Links = fw, links
	people := &fakePeople{cannot: map[string]int64{"forgejo": 2}, devices: []store.VPNDevice{
		{UserID: 1, Role: auth.RoleOwner, PublicKey: "a2V5LWEta2V5LWEta2V5LWEta2V5LWEta2V5LWEta2U=", Address: "100.96.128.2",
			Pages: true, SSH: true, Reach: store.ReachEverything},
		{UserID: 2, Role: auth.RoleMember, PublicKey: "a2V5LWIta2V5LWIta2V5LWIta2V5LWIta2V5LWIta2U=", Address: "100.96.128.3",
			Pages: true, SSH: true, Reach: store.ReachHome},
	}}
	m.People = people
	addForgejo(t, m)
	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	if links.wg != nil {
		t.Fatal("the VPN's interface while it's off")
	}

	if err := m.SetVPN(config.VPN{On: true, HomeNetwork: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if links.wg == nil || links.wg.Iface() != agent.PeopleInterface || links.wg.ListenPort != agent.PeoplePort || len(links.wg.Peers) != 2 ||
		!links.wg.Forward || links.wg.Peers[1].AllowedIPs[0] != "100.96.128.3/32" {
		t.Fatalf("the interface: %+v", links.wg)
	}
	last := fw.applied[len(fw.applied)-1].People
	if last == nil || last.Port != agent.PeoplePort || !slices.Equal(last.Home, []string{"100.96.128.3"}) || len(last.Everything) != 0 {
		t.Errorf("the firewall: %+v", last)
	}
	ssh := slices.IndexFunc(last.Rules, func(r agent.DeviceRule) bool { return r.Port == 22 })
	if ssh < 0 || !slices.Equal(last.Rules[ssh].Devices, []string{"100.96.128.2"}) {
		t.Errorf("SSH for the Owner's device only: %+v", last.Rules)
	}
	main, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddyDir(), "Caddyfile"))
	site, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "forgejo.caddy"))
	// Forgejo has no single sign-on here, so who may use it isn't chosen:
	// every device reaches it.
	if !strings.Contains(string(main), "100.112.0.0/12 100.96.128.2\n") || !strings.Contains(string(site), "100.96.128.2 100.96.128.3") {
		t.Errorf("Caddy: pages for the Owner's device, Forgejo for every device:\n%s\n%s", main, site)
	}

	// With single sign-on on, Forgejo is for whoever may use it.
	if err := m.SetSSO(ctx, "forgejo", true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	site, _ = os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "forgejo.caddy"))
	if strings.Contains(string(site), "100.96.128.3") || !strings.Contains(string(site), "100.96.128.2") {
		t.Errorf("Forgejo with single sign-on, for the Owner only:\n%s", site)
	}

	if err := m.SetVPN(config.VPN{}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	// Stopped, its key kept for when it's turned on again.
	if links.wg == nil || !links.wg.Down || len(links.wg.Peers) != 0 || links.off != 0 || fw.applied[len(fw.applied)-1].People != nil {
		t.Errorf("turned off: %+v %+v", links.wg, fw.applied[len(fw.applied)-1].People)
	}
}
