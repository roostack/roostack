package manager

import (
	"context"
	"errors"
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
// address. On another server it can, as the sign-in page reaches it there
// too (SPEC.md §5.2).
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
	if why := m.ssoRequiredProblem(p, "n-elsewhere"); why != "" {
		t.Errorf("on another server: %q", why)
	}
	if why := m.ssoRequiredProblem(p, m.Lab.NodeID); why != "" {
		t.Errorf("here: %q", why)
	}
}

type fakeKeeper map[string][]byte

func (k fakeKeeper) Read(_ context.Context, name string) ([]byte, error) {
	if v, ok := k[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such secret")
}

func (k fakeKeeper) Replace(_ context.Context, name string, value []byte) error {
	k[name] = value
	return nil
}

// The controller makes an app's client secret for another server once,
// keeps it, and hands over the same one each time (SPEC.md §5.2).
func TestClientSecretKept(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if _, err := m.clientSecret(ctx, "mealie"); err == nil {
		t.Error("a client secret with nowhere to keep it")
	}
	k := fakeKeeper{}
	m.ClientSecrets = k
	first, err := m.clientSecret(ctx, "mealie")
	if err != nil || len(first) < 32 || string(k[generate.SSOSecretName("mealie")]) != string(first) {
		t.Fatalf("made %q, kept %q, %v", first, k[generate.SSOSecretName("mealie")], err)
	}
	if again, _ := m.clientSecret(ctx, "mealie"); string(again) != string(first) {
		t.Errorf("made again: %q", again)
	}
}
