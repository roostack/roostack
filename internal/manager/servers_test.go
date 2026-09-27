package manager

import (
	"context"
	"errors"
	"github.com/mylab/mylab/internal/agent"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/nodeid"
)

// fakeRemote is another server that records its assignments.
type fakeRemote struct {
	guestLink []agent.WireGuard
	deleted   []string
	mu        sync.Mutex
	last      node.Assignment
	n         int
	left      bool
	name      string
	settings  *fakeServerSettings
}

func (f *fakeRemote) Identity() nodeid.Identity {
	return nodeid.Identity{ID: "n-0000000002", Name: "vps"}
}
func (f *fakeRemote) Address() string { return "100.96.0.2" }
func (f *fakeRemote) Apply(a node.Assignment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last, f.n = a, f.n+1
}
func (f *fakeRemote) Status() node.ApplyStatus { return node.ApplyStatus{Finished: true} }
func (f *fakeRemote) ServiceStates(context.Context) (map[string]string, error) {
	return map[string]string{"forgejo": "running"}, nil
}
func (f *fakeRemote) ServiceDetails(context.Context) (map[string]node.ServiceDetail, error) {
	return map[string]node.ServiceDetail{"forgejo": {Status: "running", DataSize: -1}}, nil
}
func (f *fakeRemote) BuiltIns(context.Context) ([]node.BuiltIn, error) {
	return []node.BuiltIn{{Name: "mylab", Kind: "network", Status: "up"}}, nil
}
func (f *fakeRemote) InstallStatus(context.Context, string) node.HookStatus { return node.HookStatus{} }
func (f *fakeRemote) RetryInstall(context.Context, string) error            { return nil }
func (f *fakeRemote) RunAction(context.Context, string, string, map[string]string) (string, error) {
	return "", errors.New("unused")
}
func (f *fakeRemote) RevealSecret(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeRemote) LatestActivity(string) (activity.Snapshot, bool) {
	return activity.Snapshot{}, false
}
func (f *fakeRemote) Backups() node.Backups   { return nil }
func (f *fakeRemote) Restores() node.Restores { return nil }
func (f *fakeRemote) Updates() node.Updates   { return nil }
func (f *fakeRemote) Settings() node.Settings {
	if f.settings == nil {
		return nil
	}
	return f.settings
}

// fakeServerSettings records a backup destination.
type fakeServerSettings struct {
	mu     sync.Mutex
	dest   *node.BackupDestination
	copies []node.BackupCopy
}

func (f *fakeServerSettings) Timezones() (string, []string, error)              { return "", nil, nil }
func (f *fakeServerSettings) SetTimezone(context.Context, string) (bool, error) { return false, nil }
func (f *fakeServerSettings) SSHAccess() (node.SSHAccess, error)                { return node.SSHAccess{}, nil }
func (f *fakeServerSettings) SetSSH(context.Context, string, bool) error        { return nil }
func (f *fakeServerSettings) SetRestartPolicy(context.Context, string, int) error {
	return nil
}
func (f *fakeServerSettings) RestartPolicy() (string, int) { return "auto", 4 }
func (f *fakeServerSettings) SetBackupCopies(_ context.Context, copies []node.BackupCopy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copies = copies
	return nil
}
func (f *fakeServerSettings) SetBackupDestination(_ context.Context, d node.BackupDestination) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dest = &d
	return nil
}
func (f *fakeRemote) ApplyGuestLink(_ context.Context, wg agent.WireGuard) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guestLink = append(f.guestLink, wg)
	return "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", nil
}
func (f *fakeRemote) GuestLinkStatus(context.Context) (agent.WireGuardStatus, error) {
	return agent.WireGuardStatus{}, nil
}
func (f *fakeRemote) RemoveGuestLink(context.Context) error             { return nil }
func (f *fakeRemote) GuestUsage(context.Context, string) (int64, error) { return 1 << 30, nil }
func (f *fakeRemote) GuestDirs(context.Context) ([]string, error)       { return nil, nil }
func (f *fakeRemote) RemoveGuestDir(_ context.Context, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, user)
	return nil
}
func (f *fakeRemote) GuestSpace(context.Context) (int64, int64, error) {
	return 500 << 30, 1000 << 30, nil
}
func (f *fakeRemote) Leave(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = true
	return nil
}
func (f *fakeRemote) Rename(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name = name
	return nil
}

type fakeRegistry struct{ removed, renamed []string }

func (f *fakeRegistry) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeRegistry) CertExpiries(context.Context) map[string]time.Time {
	return map[string]time.Time{"n-0000000002": time.Date(2027, 9, 28, 0, 0, 0, 0, time.UTC)}
}
func (f *fakeRegistry) Rename(_ context.Context, id, name string) error {
	f.renamed = append(f.renamed, id+"="+name)
	return nil
}

func TestAssignmentsPerServer(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	ctx := context.Background()

	var fe *FormError
	if err := m.Install(ctx, "forgejo", InstallRequest{Host: "n-0000000009"}); !errors.As(err, &fe) {
		t.Errorf("unknown server: %v", err)
	}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	req := forgejoRequest()
	req.Host = "n-0000000002"
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	vps.mu.Lock()
	a := vps.last
	vps.mu.Unlock()
	if len(a.Services) != 1 || a.Services[0].Package.Name != "forgejo" || a.Lab.Domain != "lab.example.com" {
		t.Fatalf("the vps runs forgejo only: %+v", a)
	}
	if string(a.Secrets["mylab-forgejo-admin_password"]) != "correct horse" || len(a.SetUp) != 1 {
		t.Errorf("forgejo's typed secret and set-up go to its server: %v %v", a.Secrets, a.SetUp)
	}
	if len(a.Proxied) != 0 {
		t.Error("only the controller proxies")
	}
	if m.NodeFor("forgejo") != vps || m.NodeFor("vaultwarden") != m.Node {
		t.Error("NodeFor")
	}
	m.Apply()
	m.Wait()
	st := m.ServiceStates(ctx)
	if st["forgejo"].Server != "vps" || st["forgejo"].Status != "running" {
		t.Errorf("states = %+v", st)
	}
	servers, _ := m.Servers()
	if len(servers) != 2 || servers[1].Name != "vps" || len(servers[1].Services) != 1 {
		t.Errorf("servers = %+v", servers)
	}
	site, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "forgejo.caddy"))
	if !strings.Contains(string(site), "reverse_proxy 100.96.0.2:") {
		t.Errorf("the controller's Caddy should forward forgejo to the vps:\n%s", site)
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "mylab-forgejo-app.container")); err == nil {
		t.Error("forgejo's containers must not run on the controller")
	}
	// Removing forgejo takes it off the vps.
	m.Remove(ctx, "forgejo")
	m.Wait() // this server's apply writes into the test's folders
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if len(vps.last.Services) != 0 {
		t.Error("removed service still assigned")
	}
}

func TestRemoveAndRenameServer(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	vps, reg := &fakeRemote{}, &fakeRegistry{}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = reg
	ctx := context.Background()
	req := forgejoRequest()
	req.Host = "n-0000000002"
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	var fe *FormError
	if err := m.RemoveServer(ctx, "n-0000000002"); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "still runs Forgejo") {
		t.Errorf("removing a server with services: %v", err)
	}
	if err := m.RemoveServer(ctx, "n-0000000001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("this server can't be removed: %v", err)
	}
	if err := m.RenameServer(ctx, "n-0000000002", "  cloud  "); err != nil || vps.name != "cloud" ||
		len(reg.renamed) != 1 || reg.renamed[0] != "n-0000000002=cloud" {
		t.Errorf("rename: %v %q %v", err, vps.name, reg.renamed)
	}
	if err := m.RenameServer(ctx, "n-0000000002", ""); !errors.As(err, &fe) {
		t.Errorf("empty name: %v", err)
	}
	var renamedSelf string
	m.RenameSelf = func(name string) error { renamedSelf = name; return nil }
	if err := m.RenameServer(ctx, "n-0000000001", "nuc"); err != nil || renamedSelf != "nuc" {
		t.Errorf("rename self: %v %q", err, renamedSelf)
	}

	m.Remove(ctx, "forgejo")
	m.Wait()
	if err := m.RemoveServer(ctx, "n-0000000002"); err != nil || !vps.left || len(reg.removed) != 1 {
		t.Errorf("remove: %v left=%v removed=%v", err, vps.left, reg.removed)
	}
}
