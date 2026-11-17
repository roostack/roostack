package manager

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
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
// paused (SPEC.md §6.15), and one turned off isn't checked.
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
	if err := m.SetOff(ctx, "vaultwarden", true, "sam"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if checks := m.MonitorChecks(ctx); len(checks) != 0 {
		t.Errorf("turned off: %+v", checks)
	}
	m.Lab.Domain = ""
	if checks := m.MonitorChecks(ctx); len(checks) != 0 {
		t.Errorf("without an address: %+v", checks)
	}
}

// Turning the status page on, as its card sees it (SPEC.md §6.15, "While
// it's being turned on"): applying until an apply has carried the switch
// to Caddy, then when it did; nothing before Roostack's first apply.
func TestStatusPageProgress(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	if p := m.StatusPageProgress(); p.Applying || !p.Applied.IsZero() {
		t.Fatalf("before any apply: %+v", p)
	}
	m.mu.Lock()
	m.startApplyLocked("Starting")
	m.mu.Unlock()
	m.Wait()
	if p := m.StatusPageProgress(); p.Applying || !p.Applied.IsZero() {
		t.Fatalf("after the first apply: %+v", p)
	}

	// Switched on in lab.yaml, its apply not yet run.
	m.mu.Lock()
	if err := m.saveLab(func(l *config.Lab) { l.Monitoring.StatusPage = true }); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	if p := m.StatusPageProgress(); !p.Applying || p.Failed != "" {
		t.Fatalf("switched on: %+v", p)
	}

	before := time.Now()
	m.mu.Lock()
	m.startApplyLocked("Turning the public status page on")
	m.mu.Unlock()
	m.Wait()
	if p := m.StatusPageProgress(); p.Applying || p.Applied.Before(before) {
		t.Errorf("applied: %+v", p)
	}
}

// TestLocalRoots: on a test address, the checks trust Caddy's own
// certificate authority, once Caddy has made it, and nothing else; on a
// real address, the system's (SPEC.md §6.6).
func TestLocalRoots(t *testing.T) {
	t.Parallel()
	lab := config.Default()
	lab.Paths.StateDir = t.TempDir()
	lab.Domain, lab.DNS = "lab.example.com", config.DNS{Provider: "cloudflare"}
	if localRoots(lab) != nil {
		t.Error("a real address doesn't use the system's roots")
	}
	lab.Domain, lab.DNS = "lab.localhost", config.DNS{Provider: config.ProviderTestAddress}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	leaf := srv.Certificate()
	verifies := func() bool {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: localRoots(lab), DNSName: "example.com"})
		return err == nil
	}
	if verifies() {
		t.Error("trusted before Caddy made its authority")
	}
	os.MkdirAll(filepath.Dir(lab.Paths.CaddyLocalRoot()), 0o700)
	os.WriteFile(lab.Paths.CaddyLocalRoot(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o644)
	if !verifies() {
		t.Error("Caddy's root isn't trusted")
	}
}
