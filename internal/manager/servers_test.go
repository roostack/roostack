package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/node/nodetest"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// fakeRemote is another server that records its assignments.
type fakeRemote struct {
	nodetest.Base
	guestLink []agent.WireGuard
	media     node.Media
	deleted   []string
	mu        sync.Mutex
	last      node.Assignment
	n         int
	left      bool
	name      string
	settings  *fakeServerSettings
	closed    bool
	// home are its home-network addresses; handshakes when its direct
	// links last connected (links_test.go).
	home       []string
	handshakes map[string]int64
	// backups are its backups' results, if set (quarantine_test.go).
	backups node.Backups
	// arch is its processor ("": it doesn't say; arch_test.go).
	arch string
}

func (f *fakeRemote) Arch() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.arch
}

func (f *fakeRemote) Close() { f.closed = true }
func (f *fakeRemote) LinkStatus(context.Context) ([]string, map[string]int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.home, f.handshakes
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
	return []node.BuiltIn{{Name: "roostack", Kind: "network", Status: "up"}}, nil
}
func (f *fakeRemote) Backups() node.Backups { return f.backups }
func (f *fakeRemote) Media() node.Media     { return f.media }

// Capabilities: it hosts guests, says its processor and its links, and
// has its backups and media if set; nothing else (nodetest.Base).
func (f *fakeRemote) Linked() node.Linked       { return f }
func (f *fakeRemote) GuestHost() node.GuestHost { return f }
func (f *fakeRemote) Processor() node.Processor { return f }

func (f *fakeRemote) Settings() node.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		return nil
	}
	return f.settings
}

// fakeServerSettings records a backup destination.
type fakeServerSettings struct {
	mu       sync.Mutex
	dest     *node.BackupDestination
	copies   []node.BackupCopy
	promoted string
	destErr  error  // SetBackupDestination fails with it
	zone     string // SetTimezone records it
}

func (f *fakeServerSettings) Timezones(context.Context) (string, []string, error) {
	return "", nil, nil
}
func (f *fakeServerSettings) SetTimezone(_ context.Context, z string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.zone != z
	f.zone = z
	return changed, nil
}
func (f *fakeServerSettings) SSHAccess(context.Context, bool) (node.SSHAccess, error) {
	return node.SSHAccess{}, nil
}
func (f *fakeServerSettings) SetSSH(context.Context, node.SSHChange) error { return nil }
func (f *fakeServerSettings) SetRestartPolicy(context.Context, string, int) error {
	return nil
}
func (f *fakeServerSettings) RestartPolicy(context.Context) (string, int, error) {
	return "auto", 4, nil
}
func (f *fakeServerSettings) PromoteBackupCopy(_ context.Context, name string, old node.BackupCopy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promoted = name + " <-> " + old.Name
	return nil
}
func (f *fakeServerSettings) SetBackupCopies(_ context.Context, copies []node.BackupCopy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copies = copies
	return nil
}
func (f *fakeServerSettings) SetBackupDestination(_ context.Context, d node.BackupDestination) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destErr != nil {
		return f.destErr
	}
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

type fakeRegistry struct {
	removed, renamed []string
	peers            map[string]labnet.Peer
}

func (f *fakeRegistry) Peers(context.Context) (map[string]labnet.Peer, error) { return f.peers, nil }

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

	var fe *usererr.Error
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
	if string(a.Secrets["roostack-forgejo-admin_password"]) != "correct horse" || len(a.SetUp) != 1 {
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
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-forgejo-app.container")); err == nil {
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

	var fe *usererr.Error
	if err := m.RemoveServer(ctx, "n-0000000002"); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "is still running Forgejo") {
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
	if err := m.RemoveServer(ctx, "n-0000000002"); err != nil || !vps.left || len(reg.removed) != 1 || !vps.closed {
		t.Errorf("remove: %v left=%v removed=%v", err, vps.left, reg.removed)
	}
}

// A server that just joined gets the controller's time zone once it
// answers (it restarts as part of the lab first).
func TestNewServerGetsTheLabsTimezone(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	m.Node.(*node.Local).Host = &node.HostSettings{Zone: func() string { return "Australia/Sydney" },
		Zones: func() ([]string, error) { return []string{"Australia/Sydney", "Etc/UTC"}, nil }}
	vps := &fakeRemote{}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	done := make(chan struct{})
	go func() { m.MatchTimezone(context.Background(), "n-0000000002"); close(done) }()
	time.Sleep(50 * time.Millisecond) // not answering yet
	vps.mu.Lock()
	vps.settings = &fakeServerSettings{zone: "Etc/UTC"}
	vps.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("it should be set once the server answers")
	}
	if vps.settings.zone != "Australia/Sydney" {
		t.Errorf("zone = %q", vps.settings.zone)
	}
}

// A service's files, made again for its page, are what was written.
func TestServiceFiles(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Install(context.Background(), "vaultwarden", InstallRequest{Subdomain: "vault", Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	f, err := m.ServiceFiles("vaultwarden")
	if err != nil || len(f.Quadlets) == 0 || f.CaddySite == nil {
		t.Fatalf("files: %+v %v", f, err)
	}
	for _, q := range f.Quadlets {
		written, err := os.ReadFile(filepath.Join(f.QuadletDir, q.Name))
		if err != nil || string(written) != q.Content {
			t.Errorf("%s isn't what was written: %v", q.Name, err)
		}
	}
	if written, err := os.ReadFile(filepath.Join(f.CaddySitesDir, f.CaddySite.Name)); err != nil || string(written) != f.CaddySite.Content {
		t.Errorf("the Caddy site isn't what was written: %v", err)
	}
	if _, err := m.ServiceFiles("nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown service: %v", err)
	}
}

// pendingRemote is a server that keeps its latest assignment, as
// node.Remote does, for fetching.
type pendingRemote struct {
	*fakeRemote
}

func (p pendingRemote) Pending() (node.WireAssignment, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n == 0 {
		return node.WireAssignment{}, false
	}
	return node.WireAssignment{Version: int64(p.n)}, true
}

// A server fetching its assignment gets the latest one; assignments are
// built only when there's none, and fetching again at once is refused
// (M26 of the 2026-10-16 review).
func TestPendingFor(t *testing.T) {
	m, _ := newManager(t)
	vps := pendingRemote{&fakeRemote{settings: &fakeServerSettings{}}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.waits.pullGap = time.Hour

	// None yet (the controller just started): it's built.
	w, err := m.PendingFor("n-0000000002")
	m.Wait()
	if err != nil || w.Version != 1 {
		t.Fatalf("first fetch: %+v %v", w, err)
	}
	if _, err := m.PendingFor("n-0000000002"); !errors.Is(err, ErrPullTooSoon) {
		t.Errorf("fetching again at once: %v", err)
	}
	if _, err := m.PendingFor("n-0000000009"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a server not in the lab: %v", err)
	}
	// Later fetches get the latest without applying anew.
	m.waits.pullGap = time.Nanosecond
	for range 5 {
		if w, err := m.PendingFor("n-0000000002"); err != nil || w.Version != 1 {
			t.Fatalf("later fetch: %+v %v", w, err)
		}
	}
	m.Wait()
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if vps.n != 1 {
		t.Errorf("fetches applied %d times, want once", vps.n)
	}
}

// TestServiceLastBackup: a service's last backup is its server's only if
// that backup has it (one installed since has none yet); a backup from
// before runs listed their services counts.
func TestServiceLastBackup(t *testing.T) {
	m, _ := newManager(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, c := range []struct {
		services []string
		want     time.Time
	}{{[]string{}, time.Time{}}, {[]string{"immich"}, time.Time{}}, {[]string{"immich", "forgejo"}, t0}, {nil, t0}} {
		vps := &fakeRemote{backups: fakeBackups{st: backup.Status{Configured: true, LastOK: &store.BackupRun{Finished: t0, OK: true, Services: c.services}}}}
		m.Others = func() []RemoteServer { return []RemoteServer{vps} }
		if got := m.ServiceInfos(context.Background())["forgejo"].LastBackup; !got.Equal(c.want) {
			t.Errorf("a backup of %q: forgejo's last backup %v, want %v", c.services, got, c.want)
		}
	}
}
