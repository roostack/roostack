package manager

import (
	"context"
	"testing"

	"roostack.cloud/roostack/internal/systemd"
)

// unitsIn reports every unit in one state.
type unitsIn string

func (u unitsIn) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	var out []systemd.UnitStatus
	for _, n := range units {
		out = append(out, systemd.UnitStatus{Unit: n, LoadState: "loaded", ActiveState: string(u)})
	}
	return out, nil
}

// Each route of each service is checked at its address through this
// server's Caddy, at the package's health path; a stopped service is
// paused (SPEC.md §6.15).
func TestMonitorChecks(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Subdomain: "passwords", Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).Units = unitsIn("active")
	checks := m.MonitorChecks(ctx)
	if len(checks) != 1 {
		t.Fatalf("checks %+v", checks)
	}
	c := checks[0]
	if c.Key != "service:vaultwarden:web" || c.URL != "https://passwords.lab.example.com/alive" || c.Dial != CaddyLocal ||
		c.Name != "Vaultwarden" || c.Address != "passwords.lab.example.com" || c.Paused != "" || !c.Alert || c.Public {
		t.Errorf("check %+v", c)
	}
	local(m).Units = unitsIn("inactive")
	if c := m.MonitorChecks(ctx)[0]; c.Paused != "it's stopped" {
		t.Errorf("stopped: %q", c.Paused)
	}
	m.Lab.Domain = ""
	if checks := m.MonitorChecks(ctx); len(checks) != 0 {
		t.Errorf("without an address: %+v", checks)
	}
}
