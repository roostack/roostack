package manager

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
)

// audits records the manager's audit entries.
type audits struct {
	mu      sync.Mutex
	entries []string
}

func (a *audits) add(_ context.Context, action, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, action+": "+detail)
}

func (a *audits) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.entries)
}

// settingUp says whether a service is waiting for its setup.
func settingUp(t *testing.T, m *Manager, name string) bool {
	t.Helper()
	_, in, err := m.installedService(name)
	if err != nil {
		t.Fatal(err)
	}
	return in.SettingUp
}

// privateSite says whether the controller's Caddy keeps a service's
// address to private visitors.
func privateSite(t *testing.T, m *Manager, name, route string) bool {
	t.Helper()
	site, err := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), name+".caddy"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(site), "abort @"+name+"-"+route+"-external")
}

// A public address waits for the service's install hooks (SPEC.md §8.1):
// private while they run or have failed, public once they succeed.
func TestPublicWaitsForSetup(t *testing.T) {
	m, _, _, ex := newHookManager(t)
	ctx := context.Background()
	var log audits
	m.Audit = log.add
	local(m).SetUpDone = m.SetUpDone
	var ready atomic.Bool
	ex.fail = func(int) bool { return !ready.Load() }

	req := forgejoRequest()
	req.Public = true
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo"); st.Failed == "" {
		t.Fatalf("status = %+v, want failed", st)
	}
	if !settingUp(t, m, "forgejo") || !privateSite(t, m, "forgejo", "web") {
		t.Error("a service whose setup failed stays private")
	}
	if slices.Contains(m.PublicHosts(), "git.lab.example.com") {
		t.Errorf("public hosts = %v", m.PublicHosts())
	}
	access, _ := m.Access("forgejo")
	if len(access) != 1 || !access[0].Public || !access[0].AfterSetup {
		t.Errorf("access = %+v: chosen public, waiting for setup", access)
	}
	m.CheckSetUps(ctx)
	if !settingUp(t, m, "forgejo") {
		t.Error("checking doesn't open a failed setup")
	}

	ready.Store(true)
	if err := local(m).RetryInstall(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	local(m).WaitHooks()
	m.Wait()
	if settingUp(t, m, "forgejo") || privateSite(t, m, "forgejo", "web") {
		t.Error("public once its setup succeeded")
	}
	if !slices.Contains(m.PublicHosts(), "git.lab.example.com") {
		t.Errorf("public hosts = %v", m.PublicHosts())
	}
	if got := log.list(); !slices.Contains(got, "setup: forgejo finished setting up, so git.lab.example.com is public now") {
		t.Errorf("audit = %q", got)
	}
	setupLog, _ := m.Activity.Latest(activity.Setup)
	if !slices.Contains(setupLog.Lines, "Roostack will make git.lab.example.com public now that setup has finished.") {
		t.Errorf("setup log:\n%s", strings.Join(setupLog.Lines, "\n"))
	}
}

// A private service, or a package without install hooks, has nothing to
// wait for.
func TestNothingWaitsForSetup(t *testing.T) {
	m, _, _, _ := newHookManager(t)
	ctx := context.Background()
	var log audits
	m.Audit = log.add
	local(m).SetUpDone = m.SetUpDone
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}, Public: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	m.Wait()
	if settingUp(t, m, "vaultwarden") || privateSite(t, m, "vaultwarden", "web") {
		t.Error("vaultwarden has no install hooks: public at once")
	}
	if settingUp(t, m, "forgejo") {
		t.Error("forgejo's setup succeeded")
	}
	if got := log.list(); len(got) != 0 {
		t.Errorf("nothing opened, so nothing to audit: %q", got)
	}
}

// A service on another server opens once that server says its setup
// succeeded; one whose setup it never got is asked for again, once.
func TestRemoteSetUp(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	var log audits
	m.Audit = log.add
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{setup: node.HookStatus{Pending: true}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	ctx := context.Background()

	req := forgejoRequest()
	req.Host, req.Public = "n-0000000002", true
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if !settingUp(t, m, "forgejo") || !privateSite(t, m, "forgejo", "web") {
		t.Fatal("the controller's Caddy keeps it private while its server sets it up")
	}
	vps.mu.Lock()
	if svc := vps.last.Services; len(svc) != 1 || !svc[0].Instance.SettingUp {
		t.Error("its server is told, for its public ports")
	}
	vps.mu.Unlock()
	m.CheckSetUps(ctx)
	if !settingUp(t, m, "forgejo") {
		t.Error("still setting up")
	}

	// It never got its setup (the controller restarted before handing it
	// over): asked for again, once.
	vps.mu.Lock()
	vps.setup, vps.last.SetUp = node.HookStatus{}, nil
	vps.mu.Unlock()
	m.CheckSetUps(ctx)
	m.Wait()
	vps.mu.Lock()
	again, n := vps.last.SetUp, vps.n
	vps.mu.Unlock()
	if !slices.Equal(again, []string{"forgejo"}) || !settingUp(t, m, "forgejo") {
		t.Errorf("set up again: %v", again)
	}
	m.CheckSetUps(ctx)
	vps.mu.Lock()
	if vps.n != n {
		t.Error("a lost setup is asked for again only once")
	}
	vps.setup = node.HookStatus{Done: true}
	vps.mu.Unlock()

	m.CheckSetUps(ctx)
	m.Wait()
	if settingUp(t, m, "forgejo") || privateSite(t, m, "forgejo", "web") {
		t.Error("public once its server says the setup succeeded")
	}
	if got := log.list(); !slices.Contains(got, "setup: forgejo finished setting up, so git.lab.example.com is public now") {
		t.Errorf("audit = %q", got)
	}
}

// The install summary says a public address will wait for setup, unless
// the install hooks won't run.
func TestSummaryAfterSetup(t *testing.T) {
	m, _ := newManager(t)
	addForgejo(t, m)
	req := forgejoRequest()
	req.Public = true
	sum, err := m.InstallSummary("forgejo", req)
	if err != nil {
		t.Fatal(err)
	}
	if a := sum.Addresses; len(a) != 1 || !a[0].Public || !a[0].AfterSetup {
		t.Errorf("addresses = %+v", a)
	}
	req.BringData = true
	if sum, _ = m.InstallSummary("forgejo", req); sum.Addresses[0].AfterSetup {
		t.Error("bringing its data runs no install hooks")
	}
	sum, _ = m.InstallSummary("vaultwarden", InstallRequest{Settings: map[string]string{}, Public: true})
	if a := sum.Addresses[0]; !a.Public || a.AfterSetup {
		t.Errorf("vaultwarden has no install hooks: %+v", a)
	}
}
