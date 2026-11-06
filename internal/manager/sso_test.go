package manager

import (
	"context"
	"os"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// requireSSO makes the catalog's Mealie one with no sign-in of its own.
func requireSSO(t *testing.T, m *Manager) {
	t.Helper()
	path := m.Lab.Paths.CatalogPackageDir("mealie") + "/" + strings.TrimPrefix(pkgtest.File("mealie"), pkgtest.Dir()+"/mealie/")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\nsso:\n") {
		t.Fatal("Mealie's fixture has no sso part")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "\nsso:\n", "\nsso:\n  required: true\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A package requiring single sign-on has it on from the install, ticked or
// not, and no Owner can turn it off (SPEC.md §5.2).
func TestRequiredSSO(t *testing.T) {
	m, _ := newManager(t)
	requireSSO(t, m)
	ctx := context.Background()
	if err := m.Install(ctx, "mealie", InstallRequest{Subdomain: "recipes", Settings: map[string]string{"admin_email": "mum@example.com"},
		Secrets: map[string]string{"admin_password": "correct horse battery"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	services, err := state.LoadServices(m.Lab)
	if err != nil || len(services) != 1 || !services[0].Instance.SSO || !generate.SSOOn(m.Lab, services[0].Package, services[0].Instance) {
		t.Fatalf("not on from the install: %+v, %v", services, err)
	}
	if msg, ok := usererr.Message(m.SetSSO(ctx, "mealie", false)); !ok || !strings.Contains(msg, "stays on") {
		t.Errorf("turned off: %q", msg)
	}
	// A new version requiring it needs no switch.
	services[0].Instance.SSO = false
	if !generate.SSOOn(m.Lab, services[0].Package, services[0].Instance) {
		t.Error("off for an instance that hadn't turned it on")
	}
}

// It can't be installed where nobody could sign in: without the lab's
// address, or on another server.
func TestRequiredSSOWhereItWorks(t *testing.T) {
	m, _ := newManager(t)
	requireSSO(t, m)
	ctx := context.Background()
	lab := m.Lab
	lab.Domain = ""
	m.Lab = lab
	err := m.Install(ctx, "mealie", InstallRequest{Subdomain: "recipes", Settings: map[string]string{}})
	if msg, ok := usererr.Message(err); !ok || !strings.Contains(msg, "your lab's address first") {
		t.Errorf("without an address: %v", err)
	}
	if services, _ := state.LoadServices(m.Lab); len(services) != 0 {
		t.Errorf("installed anyway: %+v", services)
	}
	lab.Domain = "lab.example.com"
	m.Lab = lab
	p, err := m.Package("mealie")
	if err != nil {
		t.Fatal(err)
	}
	if why := m.ssoRequiredProblem(p, "n-elsewhere"); !strings.Contains(why, "on this server") {
		t.Errorf("on another server: %q", why)
	}
	if why := m.ssoRequiredProblem(p, m.Lab.NodeID); why != "" {
		t.Errorf("here: %q", why)
	}
}
