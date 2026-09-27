package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/nodeid"
)

// fakeRemote is another machine that records its assignments.
type fakeRemote struct {
	mu   sync.Mutex
	last node.Assignment
	n    int
	left bool
	name string
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
func (f *fakeRemote) Settings() node.Settings { return nil }
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
func (f *fakeRegistry) Rename(_ context.Context, id, name string) error {
	f.renamed = append(f.renamed, id+"="+name)
	return nil
}

func TestAssignmentsPerMachine(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{}
	m.Others = func() []RemoteMachine { return []RemoteMachine{vps} }
	ctx := context.Background()

	var fe *FormError
	if err := m.Install(ctx, "forgejo", InstallRequest{Host: "n-0000000009"}); !errors.As(err, &fe) {
		t.Errorf("unknown machine: %v", err)
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
		t.Errorf("forgejo's typed secret and set-up go to its machine: %v %v", a.Secrets, a.SetUp)
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
	if st["forgejo"].Machine != "vps" || st["forgejo"].Status != "running" {
		t.Errorf("states = %+v", st)
	}
	machines, _ := m.Machines()
	if len(machines) != 2 || machines[1].Name != "vps" || len(machines[1].Services) != 1 {
		t.Errorf("machines = %+v", machines)
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
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if len(vps.last.Services) != 0 {
		t.Error("removed service still assigned")
	}
}

func TestRemoveAndRenameMachine(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	vps, reg := &fakeRemote{}, &fakeRegistry{}
	m.Others = func() []RemoteMachine { return []RemoteMachine{vps} }
	m.Registry = reg
	ctx := context.Background()
	req := forgejoRequest()
	req.Host = "n-0000000002"
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	var fe *FormError
	if err := m.RemoveMachine(ctx, "n-0000000002"); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "still runs Forgejo") {
		t.Errorf("removing a machine with services: %v", err)
	}
	if err := m.RemoveMachine(ctx, "n-0000000001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("this machine can't be removed: %v", err)
	}
	if err := m.RenameMachine(ctx, "n-0000000002", "  cloud  "); err != nil || vps.name != "cloud" ||
		len(reg.renamed) != 1 || reg.renamed[0] != "n-0000000002=cloud" {
		t.Errorf("rename: %v %q %v", err, vps.name, reg.renamed)
	}
	if err := m.RenameMachine(ctx, "n-0000000002", ""); !errors.As(err, &fe) {
		t.Errorf("empty name: %v", err)
	}
	var renamedSelf string
	m.RenameSelf = func(name string) error { renamedSelf = name; return nil }
	if err := m.RenameMachine(ctx, "n-0000000001", "nuc"); err != nil || renamedSelf != "nuc" {
		t.Errorf("rename self: %v %q", err, renamedSelf)
	}

	m.Remove(ctx, "forgejo")
	m.Wait()
	if err := m.RemoveMachine(ctx, "n-0000000002"); err != nil || !vps.left || len(reg.removed) != 1 {
		t.Errorf("remove: %v left=%v removed=%v", err, vps.left, reg.removed)
	}
}
