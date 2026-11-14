package manager

import (
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/sso"
)

// A server's live view (SPEC.md §6.15.1): needs an address; turned on, the
// server runs Glances at its address on Roostack's network (loopback on the
// controller), Caddy serves it at glances-<server>.<domain>, and single
// sign-on lets only Owners and Admins in; turned off, all of it goes.
func TestGlancesSwitch(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	if err := m.SetGlances("n-0000000009", true); err == nil {
		t.Error("a server that isn't in the lab")
	}
	if err := m.SetGlances("n-0000000003", true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	vps.mu.Lock()
	va := vps.last
	vps.mu.Unlock()
	home.mu.Lock()
	ha := home.last
	home.mu.Unlock()
	if va.Glances != "100.96.0.9" || ha.Glances != "" {
		t.Errorf("where it runs: vps %q, home %q", va.Glances, ha.Glances)
	}
	sites := va.Lab.GlancesSites
	if len(sites) != 1 || sites[0].Host != "glances-vps.lab.example.com" || sites[0].Upstream != "100.96.0.9:61208" ||
		sites[0].Client != "roostack-glances-n-0000000003" {
		t.Errorf("sites: %+v", sites)
	}
	if !m.GlancesOn("n-0000000003") || m.GlancesURL("n-0000000003") != "https://glances-vps.lab.example.com" {
		t.Errorf("on: %v %q", m.GlancesOn("n-0000000003"), m.GlancesURL("n-0000000003"))
	}
	clients := m.SSOClients()
	i := slices.IndexFunc(clients, func(c sso.Client) bool { return c.ID == "roostack-glances-n-0000000003" })
	if i < 0 || !clients[i].AdminsOnly || clients[i].Host != "glances-vps.lab.example.com" {
		t.Errorf("clients: %+v", clients)
	}
	if err := m.SetGlances("n-0000000003", false); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	vps.mu.Lock()
	va = vps.last
	vps.mu.Unlock()
	if va.Glances != "" || len(va.Lab.GlancesSites) != 0 || m.GlancesURL("n-0000000003") != "" {
		t.Errorf("off: %q %+v", va.Glances, va.Lab.GlancesSites)
	}
}
