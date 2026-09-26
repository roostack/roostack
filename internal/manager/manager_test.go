package manager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
)

type fakeSystemd struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeSystemd) rec(verb string, u []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, verb+" "+strings.Join(u, " "))
	return nil
}
func (f *fakeSystemd) DaemonReload(context.Context) error           { return f.rec("daemon-reload", nil) }
func (f *fakeSystemd) Start(_ context.Context, u ...string) error   { return f.rec("start", u) }
func (f *fakeSystemd) Restart(_ context.Context, u ...string) error { return f.rec("restart", u) }
func (f *fakeSystemd) Stop(_ context.Context, u ...string) error    { return f.rec("stop", u) }
func (f *fakeSystemd) ReloadOrRestart(_ context.Context, u ...string) error {
	return f.rec("reload-or-restart", u)
}

type fakeSecrets struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (f *fakeSecrets) List(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for k := range f.values {
		out[k] = true
	}
	return out, nil
}
func (f *fakeSecrets) Read(_ context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[name]
	if !ok {
		return nil, errors.New("no such secret")
	}
	return v, nil
}
func (f *fakeSecrets) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, name)
	return nil
}
func (f *fakeSecrets) Create(_ context.Context, name string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[name] = v
	return nil
}

func newManager(t *testing.T) (*Manager, *fakeSecrets) {
	t.Helper()
	root := t.TempDir()
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Paths = config.Paths{
		StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "home"),
		ServicesDir: filepath.Join(root, "srv"), MediaDir: filepath.Join(root, "media"),
		CatalogDir: filepath.Join(root, "catalog"),
	}
	for _, name := range []string{"vaultwarden", "immich"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(lab.Paths.PackageFile(name)), 0o755)
		os.WriteFile(lab.Paths.PackageFile(name), data, 0o644)
	}
	os.MkdirAll(lab.Paths.InstancesDir(), 0o755)
	secrets := &fakeSecrets{values: map[string][]byte{dns.SecretName("api_token"): []byte("existing")}}
	m := &Manager{
		Lab:        lab,
		Reconciler: &reconcile.Reconciler{Paths: lab.Paths, Systemd: &fakeSystemd{}, Secrets: secrets},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return m, secrets
}

func TestInstallAndRemove(t *testing.T) {
	m, secrets := newManager(t)
	ctx := context.Background()

	items, err := m.Catalog()
	if err != nil || len(items) != 2 || items[0].Title != "Immich" || items[0].Installed {
		t.Fatalf("catalog: %+v %v", items, err)
	}

	err = m.Install(ctx, "vaultwarden", InstallRequest{Subdomain: "Passwords", Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st := m.Status(); st.LastErr != nil || st.Running || !st.Finished {
		t.Fatalf("apply status: %+v", st)
	}

	in, err := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	r := in.Routes["web"]
	if r.Subdomain != "passwords" || r.Port != config.DefaultRoutePortMin {
		t.Errorf("route = %+v, want subdomain passwords and the first port", r)
	}
	if in.Settings["signups_allowed"] != false {
		t.Errorf("unticked checkbox should be false, got %v", in.Settings["signups_allowed"])
	}
	if _, ok := secrets.values["mylab-vaultwarden-admin_token"]; !ok {
		t.Error("generated secret not created")
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "mylab-vaultwarden-app.container")); err != nil {
		t.Error("quadlet not written:", err)
	}

	items, _ = m.Catalog()
	if !items[1].Installed {
		t.Error("vaultwarden should show as installed")
	}

	// Same name twice, and a clashing web address, are user errors.
	var fe *FormError
	if err := m.Install(ctx, "vaultwarden", InstallRequest{}); !errors.As(err, &fe) {
		t.Errorf("second install: %v, want a form error", err)
	}
	err = m.Install(ctx, "immich", InstallRequest{Subdomain: "passwords", Settings: map[string]string{"machine_learning": "on"}})
	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, "already used") {
		t.Errorf("clashing subdomain: %v", err)
	}
	if _, err := os.Stat(state.InstanceFile(m.Lab, "immich")); !os.IsNotExist(err) {
		t.Error("a rejected install must not save anything")
	}

	// A second service gets the next port.
	if err := m.Install(ctx, "immich", InstallRequest{Settings: map[string]string{"machine_learning": "on"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	im, _ := instance.Load(state.InstanceFile(m.Lab, "immich"))
	if im.Routes["web"].Port != config.DefaultRoutePortMin+1 || im.Settings["machine_learning"] != true {
		t.Errorf("immich instance: %+v", im)
	}

	// Remove keeps the data.
	os.MkdirAll(filepath.Join(m.Lab.Paths.ServicesDir, "vaultwarden", "data"), 0o755)
	if err := m.Remove(ctx, "vaultwarden"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "mylab-vaultwarden-app.container")); !os.IsNotExist(err) {
		t.Error("quadlet should be removed")
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.ServicesDir, "vaultwarden", "data")); err != nil {
		t.Error("data must be kept")
	}
	if err := m.Remove(ctx, "vaultwarden"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}

func TestBadNames(t *testing.T) {
	m, _ := newManager(t)
	for _, name := range []string{"../etc", "", "Vault", "a/b"} {
		if _, err := m.Package(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("Package(%q) = %v", name, err)
		}
		if err := m.Remove(context.Background(), name); !errors.Is(err, ErrNotFound) {
			t.Errorf("Remove(%q) = %v", name, err)
		}
	}
}

func TestApplyErrorIsReported(t *testing.T) {
	m, secrets := newManager(t)
	delete(secrets.values, dns.SecretName("api_token")) // the DNS token was never entered
	if err := m.Install(context.Background(), "vaultwarden", InstallRequest{}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st := m.Status(); st.LastErr == nil || !strings.Contains(st.LastErr.Error(), dns.SecretName("api_token")) {
		t.Errorf("status: %+v, want the missing DNS secret reported", st)
	}
}

func TestPorts(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	addForgejo(t, m)
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	ports, err := m.Ports("forgejo")
	if err != nil || len(ports) != 1 || ports[0].Name != "ssh" || ports[0].Enabled || ports[0].HostPort != 2222 {
		t.Fatalf("ports = %+v, %v; want ssh off on 2222", ports, err)
	}

	var fe *FormError
	if err := m.SetPort(ctx, "forgejo", "ssh", true, m.Lab.UIPort); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "admin UI") {
		t.Errorf("UI port: %v", err)
	}
	if err := m.SetPort(ctx, "forgejo", "ssh", true, 22); !errors.As(err, &fe) {
		t.Errorf("port 22: %v", err)
	}
	if err := m.SetPort(ctx, "forgejo", "nope", true, 2222); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown port: %v", err)
	}

	if err := m.SetPort(ctx, "forgejo", "ssh", true, 2200); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	unit, _ := os.ReadFile(filepath.Join(m.Lab.Paths.QuadletDir(), "mylab-forgejo-app.container"))
	if !strings.Contains(string(unit), "PublishPort=2200:2222/tcp") {
		t.Errorf("ssh port not published:\n%s", unit)
	}
	// Back to the package's default port: the override is dropped.
	m.SetPort(ctx, "forgejo", "ssh", false, 2222)
	m.Wait()
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	if o := in.Ports["ssh"]; o.HostPort != 0 || o.Enabled == nil || *o.Enabled {
		t.Errorf("override = %+v, want disabled with the default port", o)
	}
}

func addForgejo(t *testing.T, m *Manager) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "forgejo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(m.Lab.Paths.PackageFile("forgejo")), 0o755)
	os.WriteFile(m.Lab.Paths.PackageFile("forgejo"), data, 0o644)
}

func forgejoRequest() InstallRequest {
	return InstallRequest{
		Settings: map[string]string{"admin_username": "mum", "admin_email": "mum@example.com"},
		Secrets:  map[string]string{"admin_password": "correct horse"},
	}
}
