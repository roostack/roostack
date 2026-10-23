package attention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

// fakeManager is the lab's manager: the servers it has, and nothing else
// unless a test says.
type fakeManager struct {
	servers    []manager.Server
	nodes      map[string]node.Node
	updates    map[string]*manager.Update
	last       map[string]*manager.UpdateRecord
	withdraw   map[string]*manager.Withdrawal
	pauses     manager.PublicPauses
	advisories map[string][]*manager.ServiceAdvisory
	roostack   []*manager.ServiceAdvisory
}

func (f *fakeManager) Servers() ([]manager.Server, error)                     { return f.servers, nil }
func (f *fakeManager) NodeByID(id string) node.Node                           { return f.nodes[id] }
func (f *fakeManager) NodeFor(service string) node.Node                       { return nil }
func (f *fakeManager) EndingStorages(context.Context) []manager.EndingStorage { return nil }
func (f *fakeManager) LostMainBackup(context.Context, string) (store.LostBackup, bool) {
	return store.LostBackup{}, false
}
func (f *fakeManager) JunkReports(context.Context) []manager.JunkReport { return nil }
func (f *fakeManager) PublicHosts() []string                            { return nil }
func (f *fakeManager) PublicPauses() (manager.PublicPauses, error)      { return f.pauses, nil }
func (f *fakeManager) Advisories(context.Context) map[string][]*manager.ServiceAdvisory {
	return f.advisories
}
func (f *fakeManager) RoostackAdvisories(context.Context) []*manager.ServiceAdvisory {
	return f.roostack
}
func (f *fakeManager) Edge(context.Context) (manager.EdgeServer, bool) {
	return manager.EdgeServer{}, false
}
func (f *fakeManager) RelayOn() bool { return false }
func (f *fakeManager) Withdrawals(context.Context) map[string]*manager.Withdrawal {
	return f.withdraw
}
func (f *fakeManager) FailedMoves(context.Context) map[string]manager.FailedMove { return nil }
func (f *fakeManager) AvailableUpdates(context.Context) map[string]*manager.Update {
	return f.updates
}
func (f *fakeManager) Updating(string) *manager.ServiceUpdate { return nil }
func (f *fakeManager) LastUpdate(_ context.Context, name string) *manager.UpdateRecord {
	return f.last[name]
}
func (f *fakeManager) GuestServer(context.Context) string { return "" }
func (f *fakeManager) StorageGuests(context.Context) ([]manager.GuestView, error) {
	return nil, nil
}

// fakeNode is this server: its backups and updates as a test sets them.
type fakeNode struct {
	id       nodeid.Identity
	backups  *fakeBackups
	updates  *fakeUpdates
	settings node.Settings
}

func (n *fakeNode) Identity() nodeid.Identity { return n.id }
func (n *fakeNode) InstallStatus(context.Context, string) manager.HookStatus {
	return manager.HookStatus{}
}
func (n *fakeNode) Backups() node.Backups {
	if n.backups == nil {
		return nil
	}
	return n.backups
}
func (n *fakeNode) Updates() node.Updates {
	if n.updates == nil {
		return nil
	}
	return n.updates
}
func (n *fakeNode) Settings() node.Settings { return n.settings }
func (n *fakeNode) Disks() node.Disks       { return nil }

// fakeBackups and fakeUpdates answer only what the rules ask.
type fakeBackups struct {
	node.Backups
	st backup.Status
}

func (f *fakeBackups) Status(context.Context) (backup.Status, error) { return f.st, nil }

type fakeUpdates struct {
	node.Updates
	restart osupdate.RestartStatus
	checks  int
}

func (f *fakeUpdates) Last(context.Context) (*osupdate.Result, error)  { return nil, nil }
func (f *fakeUpdates) Series(context.Context) (osupdate.Series, error) { return osupdate.Series{}, nil }
func (f *fakeUpdates) RestartStatus(context.Context) (osupdate.RestartStatus, error) {
	return f.restart, nil
}
func (f *fakeUpdates) HostChanges(context.Context) (osupdate.HostCheck, error) {
	f.checks++
	return osupdate.HostCheck{HostChanges: agent.HostChanges{Held: []string{"linux-image-amd64"}}}, nil
}

// fakeSetup has every step done, at home.
type fakeSetup struct{ p setup.Progress }

func (f *fakeSetup) Progress(context.Context) (setup.Progress, error) { return f.p, nil }
func (f *fakeSetup) Network(func() string) setup.NetworkChoice {
	return setup.NetworkChoice{Saved: config.NetworkHome}
}

type fakeDNS struct{ st dnsrecords.Status }

func (f *fakeDNS) Status() dnsrecords.Status { return f.st }

// newLab is a lab of one server, nuc, with backups set up and backed up
// lately.
func newLab() (*Lab, *fakeManager, *fakeNode) {
	this := manager.Server{ID: "n-1", Name: "nuc", This: true, Controller: true, Reachable: true}
	fm := &fakeManager{servers: []manager.Server{this}}
	ok := &store.BackupRun{OK: true, Finished: time.Now()}
	fn := &fakeNode{id: nodeid.Identity{ID: "n-1", Name: "nuc"},
		backups: &fakeBackups{st: backup.Status{Configured: true, Progress: -1, Last: ok, LastOK: ok}}}
	lab := &Lab{Manager: fm, Guests: fm, Node: fn, Setup: &fakeSetup{}, Domain: "lab.example.com",
		Services: func(context.Context) ([]Service, error) { return nil, nil }, HostChecks: &HostChecks{}}
	return lab, fm, fn
}

func owner() *store.User {
	return &store.User{ID: 1, Username: "sam", Role: auth.RoleOwner, TOTPSecret: "x"}
}
func admin() *store.User {
	return &store.User{ID: 2, Username: "alex", Role: auth.RoleAdmin, TOTPSecret: "x"}
}
func member() *store.User { return &store.User{ID: 3, Username: "kim", Role: auth.RoleMember} }

// find is the item with key, or nil.
func find(items []Item, key string) *Item {
	for i := range items {
		if items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}

// A lab with nothing wrong needs no attention; Members are only asked to
// turn two-factor on.
func TestNothingNeedsAttention(t *testing.T) {
	lab, _, _ := newLab()
	if items := lab.For(context.Background(), owner()); len(items) != 0 {
		t.Errorf("owner: %+v", items)
	}
	items := lab.For(context.Background(), member())
	if len(items) != 1 || items[0].Key != "two-factor" || items[0].Link != "/account/2fa" || items[0].Group != GroupProblems {
		t.Errorf("member: %+v", items)
	}
}

// A server's backups: not set up, failing (unless retried soon), the
// first one still to come, and kept only on the server.
func TestBackupItems(t *testing.T) {
	ctx := context.Background()
	lab, _, fn := newLab()
	b := fn.backups

	b.st = backup.Status{Progress: -1}
	if it := find(lab.For(ctx, owner()), "backup-unset:n-1"); it == nil || it.Level != Problem || it.Link != "/setup/backup" ||
		it.Text != "Backups aren't set up. Until they are, a failed disk means losing everything." {
		t.Errorf("not set up, for the Owner: %+v", it)
	}
	if it := find(lab.For(ctx, admin()), "backup-unset:n-1"); it == nil || it.Link != "/backups" {
		t.Errorf("not set up, for an Admin: %+v", it)
	}
	if find(lab.For(ctx, member()), "backup-unset:n-1") != nil {
		t.Error("Members can't act on backups")
	}

	at := time.Now().Add(2 * time.Hour)
	b.st = backup.Status{Configured: true, Progress: -1, Last: &store.BackupRun{Error: "no space left on device\nmore"},
		Retry: &backup.Retry{At: at}}
	if it := find(lab.For(ctx, owner()), "backup-failed:n-1"); it != nil {
		t.Errorf("tried again soon: not yet: %+v", it)
	}
	b.st.Retry = nil
	if it := find(lab.For(ctx, owner()), "backup-failed:n-1"); it == nil || it.Text != "The last backup didn't finish: no space left on device" {
		t.Errorf("failed: %+v", it)
	}
	b.st.Retry = &backup.Retry{Drive: true}
	if it := find(lab.For(ctx, owner()), "backup-failed:n-1"); it == nil ||
		!strings.HasSuffix(it.Text, " It will back up there as soon as the drive is plugged in.") {
		t.Errorf("waiting for its drive: %+v", it)
	}

	b.st = backup.Status{Configured: true, Progress: -1, Queued: true}
	if it := find(lab.For(ctx, owner()), "backup-first-queued:n-1"); it == nil || it.Level != Info {
		t.Errorf("first, queued: %+v", it)
	}
	old := &store.BackupRun{OK: true, Finished: time.Now().Add(-3 * 24 * time.Hour)}
	b.st = backup.Status{Configured: true, Progress: -1, Last: old, LastOK: old, LocalOnly: true}
	items := lab.For(ctx, owner())
	if it := find(items, "backup-stale:n-1"); it == nil || it.Text != "No backup has finished in the last two days." {
		t.Errorf("stale: %+v", it)
	}
	if it := find(items, "backup-local-only:n-1"); it == nil || !strings.HasPrefix(it.Text, "Backups are only stored on this server.") {
		t.Errorf("local only: %+v", it)
	}
}

// The lab's servers: applying changes, a change that failed, one that
// can't be reached (and, after days, may be gone), a certificate that
// isn't renewing; another server's backups link to its own page.
func TestServerItems(t *testing.T) {
	ctx := context.Background()
	lab, fm, _ := newLab()
	unreached := manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach pi: %w", node.ErrUnreachable)}
	fm.servers = append(fm.servers,
		manager.Server{ID: "n-2", Name: "pi", Status: unreached, UnreachableSince: time.Now().Add(-4 * 24 * time.Hour)},
		manager.Server{ID: "n-3", Name: "vps", Status: manager.ApplyStatus{Running: true}, CertExpires: time.Now().Add(-time.Hour)},
		manager.Server{ID: "n-4", Name: "nas", Status: manager.ApplyStatus{Finished: true, LastErr: errors.New("restarting forgejo: exit status 1\nmore")}})
	fm.servers[0].Status = manager.ApplyStatus{Running: true}
	fm.nodes = map[string]node.Node{}
	items := lab.For(ctx, owner())

	if it := find(items, "applying:n-1"); it == nil || it.Text != "Applying changes on nuc… Downloading a new service can take a few minutes." {
		t.Errorf("applying, named among several: %+v", it)
	}
	if it := find(items, "unreachable:n-2"); it == nil || !strings.HasPrefix(it.Text, "Roostack hasn't reached pi since ") ||
		!strings.HasSuffix(it.Text, "remove it from the lab.") || it.Link != "/servers?remove=n-2#server-n-2" {
		t.Errorf("gone for days: %+v", it)
	}
	if it := find(items, "cert-expired:n-3"); it == nil || it.Level != Problem {
		t.Errorf("certificate: %+v", it)
	}
	if it := find(items, "apply:n-4"); it == nil || it.Text != "The last change on nas didn't finish: restarting forgejo: exit status 1" {
		t.Errorf("a failed change: %+v", it)
	}
	// An Admin isn't told about certificates or whether a server is gone.
	items = lab.For(ctx, admin())
	if find(items, "cert-expired:n-3") != nil {
		t.Error("certificates are the Owner's")
	}
	if it := find(items, "unreachable:n-2"); it == nil || it.Text != "Roostack can't reach pi. It may be switched off, or its network down." {
		t.Errorf("an Admin sees it unreached: %+v", it)
	}
}

// Services: those waiting to be restored, one that stopped working, a
// failed update and those waiting for one.
func TestServiceItems(t *testing.T) {
	ctx := context.Background()
	lab, fm, _ := newLab()
	services := []Service{{Name: "immich", Title: "Immich", Waiting: true, WasOn: "pi"}}
	lab.Services = func(context.Context) ([]Service, error) { return services, nil }
	if it := find(lab.For(ctx, owner()), "waiting-restore"); it == nil || it.Text != "Immich is waiting to be restored (it was on pi)." {
		t.Errorf("one: %+v", it)
	}
	services = append(services, Service{Name: "forgejo", Title: "Forgejo", Waiting: true, WasOn: "pi"})
	if it := find(lab.For(ctx, owner()), "waiting-restore"); it == nil || it.Text != "2 services are waiting to be restored (they were on pi)." {
		t.Errorf("two: %+v", it)
	}
	if find(lab.For(ctx, admin()), "waiting-restore") != nil {
		t.Error("restoring is the Owner's")
	}

	services = []Service{{Name: "vaultwarden", Title: "Vaultwarden"}, {Name: "immich", Title: "Immich"}, {Name: "jellyfin", Title: "Jellyfin"}}
	fm.last = map[string]*manager.UpdateRecord{"vaultwarden": {From: "1.34.3-roostack.1", To: "1.35.0-roostack.1",
		Result: manager.UpdateRolledBack, Finished: time.Now()}}
	fm.updates = map[string]*manager.Update{"immich": {To: "2.1.0-roostack.1", Kind: "minor"},
		"jellyfin": {To: "10.11.0-roostack.1", Kind: "minor"}}
	items := lab.For(ctx, admin())
	if it := find(items, "update:vaultwarden:1.35.0-roostack.1"); it == nil ||
		it.Text != "Updating Vaultwarden to 1.35.0 didn't work; Roostack went back to 1.34.3." {
		t.Errorf("a failed update: %+v", it)
	}
	if it := find(items, "updates-available"); it == nil || it.Text != "Updates are available for Immich (2.1.0) and Jellyfin (10.11.0)." ||
		it.Link != "/services" {
		t.Errorf("updates waiting: %+v", it)
	}
	// A withdrawal says it already, with the update.
	fm.withdraw = map[string]*manager.Withdrawal{"jellyfin": {Withdrawal: registry.Withdrawal{Reason: "It loses data"}, Version: "10.10.0-roostack.1", Shown: "warning"}}
	items = lab.For(ctx, admin())
	if it := find(items, "updates-available"); it == nil || it.Text != "An update is available for Immich (2.1.0)." {
		t.Errorf("one update: %+v", it)
	}
	if it := find(items, "withdrawn:jellyfin:10.10.0-roostack.1:warning"); it == nil ||
		it.Text != "The version of Jellyfin you have has been withdrawn by the service registry: It loses data." {
		t.Errorf("withdrawn: %+v", it)
	}
}

// A restart to finish updates waits for a person in notify-only mode, and
// is planned otherwise; changes Roostack didn't make are checked once in
// a while, not on every look.
func TestRestartAndHostChanges(t *testing.T) {
	ctx := context.Background()
	lab, _, fn := newLab()
	fn.updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}}
	lab.Maintenance = config.Maintenance{Restart: config.RestartNotify}
	items := lab.For(ctx, owner())
	if it := find(items, "restart-needed:n-1"); it == nil || it.Group != GroupWaiting || it.Text != "This server needs a restart to finish installing updates." {
		t.Errorf("waiting: %+v", it)
	}
	if it := find(items, "host-changes:n-1"); it == nil || !strings.Contains(it.Text, ": packages held back from updates.") {
		t.Errorf("host changes: %+v", it)
	}
	lab.For(ctx, owner())
	if fn.updates.checks != 1 {
		t.Errorf("checked %d times", fn.updates.checks)
	}
	lab.HostChecks.Forget("n-1")
	lab.Maintenance = config.Maintenance{Restart: config.RestartAuto}
	items = lab.For(ctx, owner())
	if fn.updates.checks != 2 {
		t.Errorf("forgotten, then checked %d times", fn.updates.checks)
	}
	if it := find(items, "restart-planned:n-1"); it == nil || it.Level != Info || it.Group != GroupProblems {
		t.Errorf("planned: %+v", it)
	}
}

// DNS: a headline for each problem, and where to delete records left
// under an old address.
func TestDNSItems(t *testing.T) {
	ctx := context.Background()
	lab, _, _ := newLab()
	lab.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateBlocked, Left: []dnsrecords.Record{{Name: "a"}, {Name: "b"}}}}
	items := lab.For(ctx, owner())
	if it := find(items, "dns:blocked"); it == nil || it.Text != DNSHeadline[dnsrecords.StateBlocked] {
		t.Errorf("blocked: %+v", it)
	}
	if it := find(items, "dns-left"); it == nil || !strings.HasSuffix(it.Text, "delete them at your DNS provider.") {
		t.Errorf("left: %+v", it)
	}
	if find(lab.For(ctx, admin()), "dns:blocked") != nil {
		t.Error("DNS is the Owner's")
	}
}

// The lab's old address: information, then a warning in its last week;
// only the Owner gets the link.
func TestOldAddress(t *testing.T) {
	ctx := context.Background()
	lab, _, _ := newLab()
	lab.OldAddress = &config.OldAddress{Domain: "old.example.com", Until: time.Now().Add(30 * 24 * time.Hour)}
	if it := find(lab.For(ctx, owner()), "old-address:old.example.com"); it == nil || it.Level != Info || it.Link != "/settings/domain#old-address" {
		t.Errorf("a month left: %+v", it)
	}
	lab.OldAddress.Until = time.Now().Add(2 * 24 * time.Hour)
	if it := find(lab.For(ctx, member()), "old-address:old.example.com"); it == nil || it.Level != Warning || it.Link != "" || it.LinkText != "" {
		t.Errorf("days left, for a Member: %+v", it)
	}
	lab.OldAddress.Until = time.Now().Add(-time.Hour)
	if find(lab.For(ctx, owner()), "old-address:old.example.com") != nil {
		t.Error("ended")
	}
}

// Every item has a key of its own and a group.
func TestKeysAndGroups(t *testing.T) {
	ctx := context.Background()
	lab, fm, fn := newLab()
	fn.backups.st = backup.Status{Configured: true, Progress: -1, LocalOnly: true, Last: &store.BackupRun{Error: "boom"}}
	fn.updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}}
	lab.Maintenance = config.Maintenance{Restart: config.RestartNotify}
	lab.Setup = &fakeSetup{p: setup.Progress{DomainLater: true}}
	lab.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateError, NotDeleted: []dnsrecords.PublicRecord{{}}}}
	fm.servers = append(fm.servers, manager.Server{ID: "n-2", Name: "pi",
		Status: manager.ApplyStatus{Finished: true, LastErr: errors.New("boom")}, CertExpires: time.Now().Add(time.Hour)})
	lab.Services = func(context.Context) ([]Service, error) {
		return []Service{{Name: "immich", Title: "Immich", Waiting: true}, {Name: "forgejo", Title: "Forgejo", Waiting: true}}, nil
	}
	items := lab.For(ctx, owner())
	if len(items) < 9 {
		t.Fatalf("only %d items: %+v", len(items), items)
	}
	keys := map[string]bool{}
	for _, it := range items {
		if it.Key == "" || it.Group != GroupProblems && it.Group != GroupWaiting || it.Level == "" || it.Text == "" {
			t.Errorf("%+v", it)
		}
		if keys[it.Key] {
			t.Errorf("two items have the key %q", it.Key)
		}
		keys[it.Key] = true
	}
}
