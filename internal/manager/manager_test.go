package manager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
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
func (f *fakeSystemd) DaemonReload(context.Context) error         { return f.rec("daemon-reload", nil) }
func (f *fakeSystemd) Start(_ context.Context, u ...string) error { return f.rec("start", u) }
func (f *fakeSystemd) StartNoBlock(_ context.Context, u ...string) error {
	return f.rec("start --no-block", u)
}
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
func (f *fakeSecrets) Replace(_ context.Context, name string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[name] = v
	return nil
}
func (f *fakeSecrets) Create(_ context.Context, name string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[name] = v
	return nil
}

// local is the manager's server, which does the work.
func local(m *Manager) *node.Local { return m.Node.(*node.Local) }

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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &Manager{Lab: lab, Log: log, Node: &node.Local{
		Lab:        lab,
		Reconciler: &reconcile.Reconciler{Paths: lab.Paths, Systemd: &fakeSystemd{}, Secrets: secrets},
		Log:        log,
	}}
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
	if _, ok := secrets.values["roostack-vaultwarden-admin_token"]; !ok {
		t.Error("generated secret not created")
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); err != nil {
		t.Error("quadlet not written:", err)
	}
	// The lab keeps the package file it installed from (SPEC.md §8.3.1).
	kept := m.Lab.Paths.LabPackageFile("vaultwarden", in.Version)
	if fi, err := os.Stat(kept); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the lab's copy of the package: %v %v", fi, err)
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
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); !os.IsNotExist(err) {
		t.Error("quadlet should be removed")
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.ServicesDir, "vaultwarden", "data")); err != nil {
		t.Error("data must be kept")
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("no service uses the lab's copy of vaultwarden's package: it goes (%v)", err)
	}
	if _, err := os.Stat(m.Lab.Paths.LabPackageFile("immich", im.Version)); err != nil {
		t.Errorf("immich's copy stays: %v", err)
	}
	if err := m.Remove(ctx, "vaultwarden"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}

// A second copy of a package is a service of its own: its own name,
// title, address, units, secrets and data, and removed on its own
// (SPEC.md §8.2).
func TestSecondCopy(t *testing.T) {
	m, secrets := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	var fe *FormError
	for _, bad := range []string{"Work Vault", "work", "passwords-work"} {
		if err := m.Install(ctx, "vaultwarden", InstallRequest{Name: bad}); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "vaultwarden-") {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Name: "vaultwarden-work", Settings: map[string]string{}}); !errors.As(err, &fe) ||
		!strings.Contains(fe.Msg, "already used") {
		t.Errorf("the same address as the first: %v", err)
	}
	err := m.Install(ctx, "vaultwarden", InstallRequest{Name: "vaultwarden-work", Title: "Vaultwarden (work)", Subdomain: "vault-work",
		Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st := m.Status(); st.LastErr != nil {
		t.Fatalf("apply: %v", st.LastErr)
	}
	in, err := instance.Load(state.InstanceFile(m.Lab, "vaultwarden-work"))
	if err != nil || in.Name != "vaultwarden-work" || in.Package != "vaultwarden" || in.Title != "Vaultwarden (work)" {
		t.Fatalf("instance: %+v %v", in, err)
	}
	for _, f := range []string{"roostack-vaultwarden-app.container", "roostack-vaultwarden-work-app.container"} {
		if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, ok := secrets.values["roostack-vaultwarden-work-admin_token"]; !ok {
		t.Error("its own generated secret")
	}
	if p, err := m.ServicePackage("vaultwarden-work"); err != nil || p.Title != "Vaultwarden (work)" || p.Name != "vaultwarden" {
		t.Errorf("its package, titled as itself: %+v %v", p, err)
	}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Name: "vaultwarden-work", Subdomain: "vault-3"}); !errors.As(err, &fe) ||
		!strings.Contains(fe.Msg, "already installed") {
		t.Errorf("the same name twice: %v", err)
	}
	// The title changes later; back to the package's when cleared.
	if err := m.SetTitle(ctx, "vaultwarden-work", "Work passwords"); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.ServicePackage("vaultwarden-work"); p.Title != "Work passwords" {
		t.Errorf("title = %q", p.Title)
	}
	if err := m.SetTitle(ctx, "vaultwarden-work", "Vaultwarden"); err != nil {
		t.Fatal(err)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden-work")); in.Title != "" {
		t.Errorf("the package's own title isn't stored: %q", in.Title)
	}
	if err := m.SetTitle(ctx, "../etc", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad name: %v", err)
	}
	if err := m.Remove(ctx, "vaultwarden-work"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); err != nil {
		t.Error("removing the copy leaves the first alone")
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
	// Without the DNS token (the domain step was skipped), Caddy waits and
	// the rest applies.
	delete(secrets.values, dns.SecretName("api_token"))
	if err := m.Install(context.Background(), "vaultwarden", InstallRequest{}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st := m.Status(); st.LastErr != nil {
		t.Errorf("a lab without its domain set up should still apply: %v", st.LastErr)
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); err != nil {
		t.Error(err)
	}
	// A failing apply is reported.
	addForgejo(t, m)
	dir := m.Lab.Paths.QuadletDir()
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o755)
	if err := m.Install(context.Background(), "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st := m.Status(); st.LastErr == nil || !strings.Contains(st.LastErr.Error(), "permission denied") {
		t.Errorf("status: %+v, want the failure reported", st)
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
	unit, _ := os.ReadFile(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-forgejo-app.container"))
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

type fakeFirewall struct {
	mu      sync.Mutex
	applied []agent.Firewall
}

func (f *fakeFirewall) ApplyFirewall(_ context.Context, fw agent.Firewall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, fw)
	return nil
}

func TestFirewallFollowsServices(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	fw := &fakeFirewall{}
	local(m).Firewall = fw
	addForgejo(t, m)

	m.Apply()
	m.Wait()
	if len(fw.applied) != 0 {
		t.Fatal("the firewall must be left alone until the Owner chose SSH access")
	}

	m.Lab.Firewall.SSH = config.SSHFromLAN
	local(m).Lab = m.Lab
	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	m.SetPort(ctx, "forgejo", "ssh", true, 2222)
	m.Wait()
	last := fw.applied[len(fw.applied)-1]
	want := agent.Rule{Port: 2222, Proto: "tcp", From: agent.FromLAN, Reason: "forgejo ssh"}
	if last.SSHFrom != agent.FromLAN || !slices.Contains(last.Allow, want) {
		t.Errorf("firewall = %+v, want SSH from the LAN and forgejo's port", last)
	}

	// A new SSH choice while running: the firewall is rebuilt at once,
	// with the service's port still there.
	local(m).UseSSH(config.SSHOff)
	m.Wait()
	fw.mu.Lock()
	last = fw.applied[len(fw.applied)-1]
	fw.mu.Unlock()
	if last.SSHFrom != agent.FromOff || !slices.Contains(last.Allow, want) {
		t.Errorf("after UseSSH: %+v", last)
	}
}

func TestOnlyThisNodesServices(t *testing.T) {
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	ctx := context.Background()
	// Vaultwarden is installed here; Immich is placed on another server.
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	os.WriteFile(state.InstanceFile(m.Lab, "immich"), []byte("package: immich\nversion: 2.1.0-roostack.1\nhost: n-0000000002\n"), 0o640)
	m.Apply()
	m.Wait()
	if st := m.Status(); st.LastErr != nil {
		t.Fatal(st.LastErr)
	}
	in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if in.Host != "n-0000000001" {
		t.Errorf("installed service's host = %q", in.Host)
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); err != nil {
		t.Error("this server's service should be applied:", err)
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-immich-server.container")); err == nil {
		t.Error("a service placed on another server must not be applied here")
	}
	// The controller gives every server's routes ports, each server from
	// its own range: the other server's first port is the same as ours.
	other, _ := instance.Load(state.InstanceFile(m.Lab, "immich"))
	mine, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if other.Routes["web"].Port == 0 || other.Routes["web"].Port != mine.Routes["web"].Port {
		t.Errorf("ports: other server %d, this one %d", other.Routes["web"].Port, mine.Routes["web"].Port)
	}
	servers, err := m.Servers()
	if err != nil || len(servers) != 1 {
		t.Fatal(servers, err)
	}
	if mc := servers[0]; mc.Name != "nuc" || !mc.Controller || !mc.This || !slices.Equal(mc.Services, []string{"vaultwarden"}) ||
		!mc.Status.Finished {
		t.Errorf("server = %+v", mc)
	}
}

func TestAssignmentHandsOverSecretsAndSetUp(t *testing.T) {
	m, secrets, kv, _ := newHookManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	kept, setUp := m.secrets, m.setUp
	m.mu.Unlock()
	if kept != nil || setUp != nil {
		t.Errorf("the manager must forget what it handed to the node: %v %v", kept, setUp)
	}
	m.Wait()
	local(m).WaitHooks()
	if _, ok := secrets.values["roostack-forgejo-admin_password"]; ok {
		t.Error("the node should have used and deleted the typed password")
	}
	if err := m.Remove(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if v := kv.m["hooks.forgejo.install"]; v != "" {
		t.Errorf("setup progress after removing = %q", v)
	}
}

// On a lab on the internet a service starts public (SPEC.md §6.12), and
// its package's private paths stay closed until the Owner opens them.
func TestInstallPublic(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Install(context.Background(), "vaultwarden", InstallRequest{Settings: map[string]string{}, Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	routes, err := m.Access("vaultwarden")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if !r.Public {
			t.Errorf("%s should be public", r.Route)
		}
		for _, pp := range r.PrivatePaths {
			if pp.Open {
				t.Errorf("%s: %s opened without asking", r.Route, pp.Path)
			}
		}
	}
	if hosts := m.PublicHosts(); len(hosts) == 0 {
		t.Error("its address should be listed as public")
	}
}

// On a lab on the internet, CrowdSec starts with the public UI before
// anything has asked about sharing: it shares nothing until the Owner
// answers (SPEC.md §6.11, §6.12).
func TestNoSharingUntilAsked(t *testing.T) {
	m, _ := newManager(t)
	m.Lab.Network = config.NetworkInternet
	m.Node.(*node.Local).Lab.Network = config.NetworkInternet
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	quadlet := filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-crowdsec.container")
	m.Apply()
	m.Wait()
	if data, err := os.ReadFile(quadlet); err != nil || !strings.Contains(string(data), "DISABLE_ONLINE_API=true") {
		t.Fatalf("before the Owner answers, CrowdSec shares nothing: %v\n%s", err, data)
	}
	if err := m.SetSignalSharing(true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if data, _ := os.ReadFile(quadlet); strings.Contains(string(data), "DISABLE_ONLINE_API") {
		t.Error("once the Owner chooses to share, it does")
	}
}
