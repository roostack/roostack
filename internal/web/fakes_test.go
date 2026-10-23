package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/node/nodetest"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/units"
)

// The fakes the web tests share. fakeManager is the manager, and plays
// the lab's servers too (node.Node, ServiceNode): one fake per area,
// embedded, so each area's state sits with its methods.

// fakeManager is internal/manager, by area (ServiceManager), and this
// server.
type fakeManager struct {
	fakeCatalog
	fakeServers
	fakeBackupPlan
	fakeStorage
	fakePublic
	fakePauses
	fakeKeys
	fakeLabRestores
	fakeWithdrawals
	fakeHolds
	fakeServiceUpdates
	fakeJunk
	fakeDataImports
	fakeNode

	// serviceNode, if set, is the server services run on (else this one).
	serviceNode node.Node
}

func (f *fakeManager) NodeFor(string) node.Node {
	if f.serviceNode != nil {
		return f.serviceNode
	}
	return f
}

// ServiceBackups lists the node's own backups (restores), each a moment
// of its own, plus any others the test adds.
func (f *fakeManager) ServiceBackups(ctx context.Context, name string) ([]manager.BackupMoment, []string, error) {
	var out []manager.BackupMoment
	if f.restores != nil {
		snaps, _ := f.restores.Snapshots(ctx, name)
		for _, s := range snaps {
			out = append(out, manager.BackupMoment{ServerID: "n-0123456789", ServerName: "nuc", Time: s.Time,
				Places: []manager.ServiceBackup{{Ref: "backup:n-0123456789", ServerID: "n-0123456789", ServerName: "nuc",
					Where: "USB drive BACKUP", Main: true, Snapshot: s}}})
		}
	}
	return append(out, f.otherBackups...), f.backupNotes, nil
}

func (f *fakeManager) RestoreService(ctx context.Context, name, id, to string) error {
	ref, snap, _ := strings.Cut(id, "@")
	if ref == "backup:n-0123456789" && cmp.Or(to, "n-0123456789") == "n-0123456789" {
		return f.restores.Restore(ctx, name, snap)
	}
	f.labRestores = append(f.labRestores, name+" "+id+" → "+to)
	return nil
}

func (f *fakeManager) NodeByID(id string) node.Node {
	ms, _ := f.Servers()
	for _, m := range ms {
		if m.ID == id {
			return f // one fake plays every server
		}
	}
	return nil
}

func (f *fakeManager) SetTitle(_ context.Context, name, title string) error {
	if strings.TrimSpace(title) == "" {
		return &usererr.Error{Msg: "Enter a title."}
	}
	f.serverOps = append(f.serverOps, "title "+name+"="+title)
	return nil
}

func (f *fakeManager) AllBuiltIns(ctx context.Context) []manager.ServerBuiltIns {
	items, _ := f.BuiltIns(ctx)
	return []manager.ServerBuiltIns{{Server: "nuc", Items: items}}
}

// fakeCatalog is the catalog and the installed services (ServiceCatalog).
type fakeCatalog struct {
	// summaries are the install summaries asked for.
	summaries []manager.InstallRequest
	// serverMemory is the install summary's server (nuc, 3.6 GB free of 8
	// GB, if empty); leftData, the data from before it finds there.
	serverMemory manager.ServerMemory
	leftData     []string
	installs     []manager.InstallRequest
	// local are the Owner's own packages (by name), with the risks
	// they ask for; policy is what registry packages break.
	local         map[string]bool
	risks, policy []string
	// added and removedLocal record "Add your own".
	added        [][]byte
	removedLocal []string
	removed      []string
	failWith     error

	ports   []manager.PortStatus
	portErr error
	setPort []string

	values  map[string]any
	updated []manager.SettingsRequest

	noBuild     []manager.NoBuild
	retriedWith []string
	moves       []string
}

func (f *fakeCatalog) RetryInstallWith(_ context.Context, name string, settings, secrets map[string]string) error {
	if settings["admin_username"] == "" {
		return &usererr.Error{Msg: "Administrator username is required."}
	}
	f.retriedWith = append(f.retriedWith, name+" "+settings["admin_username"]+" "+secrets["admin_password"])
	return nil
}

func (f *fakeCatalog) MoveInfo(_ context.Context, name string) (manager.MoveInfo, error) {
	if name != "vaultwarden" {
		return manager.MoveInfo{}, manager.ErrNotFound
	}
	return manager.MoveInfo{From: manager.Server{ID: "n-0123456789", Name: "nuc", This: true}, Size: 5 * units.MB,
		Suggested: manager.MoveCopy}, nil
}

func (f *fakeCatalog) MoveService(_ context.Context, name, to, way string) error {
	if to == "" {
		return &usererr.Error{Msg: "Choose a server in this lab."}
	}
	f.moves = append(f.moves, name+" → "+to+" ("+way+")")
	return nil
}

func (f *fakeCatalog) Catalog() ([]manager.CatalogItem, error) {
	items := []manager.CatalogItem{
		{Name: "immich", Title: "Immich", Description: "Photos", Categories: []string{"photos"}, Icon: true},
		{Name: "vaultwarden", Title: "Vaultwarden", Installed: true},
	}
	// Forgejo is the Owner's own when local says so.
	if f.local["forgejo"] {
		items = append(items, manager.CatalogItem{Name: "forgejo", Title: "Forgejo", Description: "Git", Local: true})
	}
	return items, nil
}

func (f *fakeCatalog) Package(name string) (*pkgspec.Package, error) {
	if name != "immich" && name != "vaultwarden" && name != "forgejo" {
		return nil, manager.ErrNotFound
	}
	return pkgspec.Load(pkgtest.File(name))
}

func (f *fakeCatalog) ServicePackage(name string) (*pkgspec.Package, error) { return f.Package(name) }

func (f *fakeCatalog) ServiceFiles(name string) (*manager.ServiceFiles, error) {
	return &manager.ServiceFiles{QuadletDir: "/var/lib/roostack/home/.config/containers/systemd", CaddySitesDir: "/var/lib/roostack/caddy/sites",
		Quadlets:  []generate.File{{Name: "roostack-" + name + "-app.container", Content: "[Container]\nContainerName=roostack-" + name + "-app\n"}},
		CaddySite: &generate.File{Name: name + ".caddy", Content: "# Generated by Roostack\n"}}, nil
}

func (f *fakeCatalog) Install(_ context.Context, _ string, req manager.InstallRequest) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.installs = append(f.installs, req)
	return nil
}

// InstallSummary summarizes the request as the manager would, simply:
// ticked boxes are true, the main route gets the request's subdomain and
// a port (manager.Manager.InstallSummary has the real rules).
func (f *fakeCatalog) InstallSummary(name string, req manager.InstallRequest) (*manager.InstallSummary, error) {
	f.summaries = append(f.summaries, req)
	p, err := f.Package(name)
	if err != nil {
		return nil, err
	}
	if req.Name == p.Name+"-bad" {
		return nil, &usererr.Error{Msg: "Choose another name."}
	}
	in := &instance.Instance{Package: p.Name, Version: p.Version, Updates: instance.UpdatesNotify, Settings: map[string]any{},
		Routes: map[string]instance.RouteOverride{}}
	for _, st := range p.Settings {
		if v, ok := req.Settings[st.Key]; ok {
			in.Settings[st.Key] = v
		}
		if st.Type == pkgspec.SettingBool {
			in.Settings[st.Key] = req.Settings[st.Key] == "on"
		}
	}
	for route := range p.Routes {
		o := instance.RouteOverride{Port: 20001}
		if route == manager.MainRoute(p) {
			o.Subdomain = req.Subdomain
		}
		if req.Public {
			o.Exposure = pkgspec.ExposurePublic
		}
		in.Routes[route] = o
	}
	lab := config.Default()
	lab.Domain = "lab.example.com"
	sum, err := generate.Summarize(lab, p, in)
	if err != nil {
		return nil, err
	}
	server := f.serverMemory
	if server.Name == "" {
		server = manager.ServerMemory{Name: "nuc", This: true, Servers: 1, Total: 8_000_000_000, Available: 3_600_000_000,
			Expected: 50_000_000}
	}
	return &manager.InstallSummary{Summary: sum, Server: server, LeftData: f.leftData}, nil
}

func (f *fakeCatalog) PackageFile(name string) ([]byte, error) {
	if _, err := f.Package(name); err != nil {
		return nil, err
	}
	return os.ReadFile(pkgtest.File(name))
}

func (f *fakeCatalog) PackageIcon(name string) ([]byte, error) {
	if _, err := f.Package(name); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File(name)), "icon.png"))
}

func (f *fakeCatalog) NoBuildOn(*pkgspec.Package) []manager.NoBuild { return f.noBuild }

func (f *fakeCatalog) CheckPackage(p *pkgspec.Package) manager.PolicyCheck {
	if f.local[p.Name] {
		return manager.PolicyCheck{Local: true, Problems: f.risks}
	}
	return manager.PolicyCheck{Problems: f.policy}
}

func (f *fakeCatalog) AddLocalPackage(data, icon []byte) (*pkgspec.Package, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.added = append(f.added, data)
	return pkgspec.Parse(data)
}

func (f *fakeCatalog) CheckLocalPackage(data []byte) (manager.LocalPackageCheck, error) {
	p, err := pkgspec.Parse(data)
	if err != nil {
		return manager.LocalPackageCheck{Errors: []string{err.Error()}}, nil
	}
	return manager.LocalPackageCheck{Policy: p.Policy(), Risks: p.Risks()}, nil
}

func (f *fakeCatalog) RemoveLocalPackage(name string) error {
	if !f.local[name] {
		return manager.ErrNotFound
	}
	f.removedLocal = append(f.removedLocal, name)
	return nil
}

func (f *fakeCatalog) LocalPackagesDir() string { return "/var/lib/roostack/local-packages" }

func (f *fakeCatalog) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeCatalog) DataDirs(name string) []string {
	return []string{"/srv/roostack/services/" + name}
}

func (f *fakeCatalog) Ports(string) ([]manager.PortStatus, error) { return f.ports, nil }

func (f *fakeCatalog) CurrentSettings(name string) (map[string]any, string, error) {
	if f.values == nil {
		return map[string]any{"signups_allowed": false}, "git", nil
	}
	return f.values, "git", nil
}

func (f *fakeCatalog) UpdateSettings(_ context.Context, name string, req manager.SettingsRequest) error {
	if req.Subdomain == "taken" {
		return &usererr.Error{Msg: "That web address is already used by another service."}
	}
	f.updated = append(f.updated, req)
	return nil
}

func (f *fakeCatalog) SetPort(_ context.Context, name, port string, enabled bool, hostPort int) error {
	if f.portErr != nil {
		return f.portErr
	}
	f.setPort = append(f.setPort, fmt.Sprintf("%s %s %v %d", name, port, enabled, hostPort))
	return nil
}

// fakeServers is the lab's servers (LabServers), and how the last apply
// went.
type fakeServers struct {
	status    manager.ApplyStatus
	servers   []manager.Server
	serverOps []string
	// Direct links (links_test.go).
	linkAddrs  map[string]string
	linkPath   string
	linkFailed bool
	// kept are the servers' kept copies; nil: Forgejo's old copy on
	// n-9876543210.
	kept []manager.KeptCopy
	// locations are where the Owner said each server is, kept as the
	// manager keeps them (manager.LocationKey).
	locations settingsKV
}

// settingsKV is the controller's settings (manager.KV), in memory.
type settingsKV map[string]string

func (kv settingsKV) Setting(_ context.Context, key string) (string, error) { return kv[key], nil }

func (kv settingsKV) SetSetting(_ context.Context, key, value string) error {
	kv[key] = value
	return nil
}

// ServerLocation and ServerPlace are the manager's own, on the fake's
// settings.
func (f *fakeServers) ServerLocation(ctx context.Context, id string) string {
	return (&manager.Manager{Settings: f.locations}).ServerLocation(ctx, id)
}

func (f *fakeServers) ServerPlace(ctx context.Context, from, host string) (bool, bool, []string) {
	return (&manager.Manager{Settings: f.locations}).ServerPlace(ctx, from, host)
}

func (f *fakeServers) SetServerLocation(ctx context.Context, id, loc string) error {
	if f.locations == nil {
		f.locations = settingsKV{}
	}
	f.locations[manager.LocationKey+id] = loc
	f.ApplyBecause("A server's location changed")
	return nil
}

func (f *fakeServers) KeptCopies(_ context.Context, id string) ([]manager.KeptCopy, error) {
	all := f.kept
	if all == nil {
		all = []manager.KeptCopy{{Copy: kept.Copy{Service: "forgejo", Why: kept.MovedAway, Note: "to-nuc", Time: time.Now(),
			Whole: true, Size: 70 * units.MB, Paths: []string{"/srv/roostack/services/forgejo.moved-to-nuc-20260928-002806"}},
			Server: "n-9876543210", Host: "n-0123456789"}}
	}
	var out []manager.KeptCopy
	for _, c := range all {
		if c.Server == id {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeServers) RemoveKept(_ context.Context, id, path string) error {
	f.serverOps = append(f.serverOps, "delete "+path+" on "+id)
	return nil
}

func (f *fakeServers) PutBackKept(_ context.Context, id, path string) error {
	f.serverOps = append(f.serverOps, "put back "+path+" on "+id)
	return nil
}

func (f *fakeServers) LinkAddress(_ context.Context, id string) string { return f.linkAddrs[id] }

func (f *fakeServers) SetLinkAddress(_ context.Context, id, addr string) error {
	if addr != "" && !strings.Contains(addr, ":") {
		return &usererr.Error{Msg: "Enter the address as host:port."}
	}
	if f.linkAddrs == nil {
		f.linkAddrs = map[string]string{}
	}
	f.linkAddrs[id] = addr
	return nil
}

func (f *fakeServers) LinkPath(_ context.Context, from, to string) string { return f.linkPath }

func (f *fakeServers) LinkFailed(context.Context, string, string) bool { return f.linkFailed }

func (f *fakeServers) ApplyBecause(reason string) {
	f.serverOps = append(f.serverOps, "apply: "+reason)
}

func (f *fakeServers) RemoveServer(_ context.Context, id string) error {
	if id == "n-9876543210" {
		return &usererr.Error{Msg: "vps is still running Forgejo. Remove it first or move it to another server."}
	}
	f.serverOps = append(f.serverOps, "remove "+id)
	return nil
}

func (f *fakeServers) RenameServer(_ context.Context, id, name string) error {
	f.serverOps = append(f.serverOps, "rename "+id+" "+name)
	return nil
}

func (f *fakeServers) Servers() ([]manager.Server, error) {
	if f.servers != nil {
		return f.servers, nil
	}
	return []manager.Server{{ID: "n-0123456789", Name: "nuc", Controller: true, This: true,
		Services: []string{"vaultwarden"}, Status: f.status}}, nil
}

func (f *fakeServers) Status() manager.ApplyStatus { return f.status }

// fakeBackupPlan is each server's backups (BackupPlan), and what its
// credentials can do there.
type fakeBackupPlan struct {
	destination string
	destRepo    string // the main backup's repository ("": fakeMainRepo)
	copies      []manager.BackupCopy
	providers   []string
	// cloudBackup offers Roostack Cloud as a backup destination.
	cloudBackup bool

	// What servers' credentials can do at their backups, by reference
	// (else by kind), and what a removed server keeps (plan_test.go).
	backupAccess map[string]manager.Access
	shared       []manager.SharedCredential
	// New keys given for online storage accounts: "ref access secret"
	// (plan_test.go); keyErr fails them.
	s3Keys []string
	keyErr error

	// Main backups lost with a removed server, by server (plan_test.go).
	lostMain map[string]store.LostBackup

	// Rolling backups back: "check|run server ref time" (plan_test.go);
	// rollbackPlan is the latest check, rollbackErr fails them.
	rollbacks    []string
	rollbackPlan *manager.RollbackPlan
	rollbackErr  error
}

// fakeMainRepo is the main backup's repository, unless a test sets one.
const fakeMainRepo = "rest:http://100.96.0.1:8000/x"

func (f *fakeBackupPlan) rollback(step, serverID, ref string, before time.Time) error {
	if f.rollbackErr != nil {
		return f.rollbackErr
	}
	f.rollbacks = append(f.rollbacks, strings.Join([]string{step, serverID, ref, before.Format("2006-01-02T15:04")}, " "))
	return nil
}

func (f *fakeBackupPlan) CheckRollback(_ context.Context, serverID, ref string, before time.Time) error {
	return f.rollback("check", serverID, ref, before)
}

func (f *fakeBackupPlan) RollBack(_ context.Context, serverID, ref string, before time.Time) error {
	return f.rollback("run", serverID, ref, before)
}

func (f *fakeBackupPlan) RollbackChecked(string, string) *manager.RollbackPlan { return f.rollbackPlan }

func (f *fakeBackupPlan) LostMainBackup(_ context.Context, serverID string) (store.LostBackup, bool) {
	l, ok := f.lostMain[serverID]
	return l, ok
}

// BackupAccess is f.backupAccess's, else what the kind suggests: another
// server, a friend's lab and Roostack Cloud only take new backups.
func (f *fakeBackupPlan) BackupAccess(ctx context.Context, serverID, ref string) manager.Access {
	if a, ok := f.backupAccess[ref]; ok {
		return a
	}
	switch f.BackupKind(ctx, serverID, ref) {
	case manager.KindServer, manager.KindFriend, manager.KindCloud:
		return manager.Access{}
	}
	return manager.Access{CanDelete: true}
}

func (f *fakeBackupPlan) SharedCredentials(context.Context, string) []manager.SharedCredential {
	return f.shared
}

func (f *fakeBackupPlan) ChangeS3Key(_ context.Context, ref, accessKey, secretKey string) error {
	if f.keyErr != nil {
		return f.keyErr
	}
	f.s3Keys = append(f.s3Keys, ref+" "+accessKey+" "+secretKey)
	return nil
}

func (f *fakeBackupPlan) BackupChoices(context.Context) ([]manager.BackupChoice, error) {
	out := []manager.BackupChoice{{Ref: "server:n-0123456789", Title: "nuc's disk"}, {Ref: "provider:1", Title: "Backblaze"}}
	if f.cloudBackup {
		out = append(out, manager.BackupChoice{Ref: destination.Cloud, Title: "Roostack Cloud", Note: "This lab uses 120.0 GB of 500.0 GB."})
	}
	return out, nil
}

func (f *fakeBackupPlan) BackupKind(_ context.Context, _, ref string) string {
	switch {
	case ref == "drive:system":
		return manager.KindSystem
	case strings.HasPrefix(ref, "drive:"):
		return manager.KindUSB
	case strings.HasPrefix(ref, "provider:"):
		return manager.KindOnline
	case strings.HasPrefix(ref, "remote:"):
		return manager.KindFriend
	case ref == destination.Cloud:
		return manager.KindCloud
	}
	return manager.KindServer
}

func (f *fakeBackupPlan) PromoteBackupCopy(_ context.Context, _, ref string) error {
	for i, c := range f.copies {
		if c.Ref == ref {
			main, _, _ := f.BackupTargetOf(context.Background(), "")
			f.copies[i] = manager.BackupCopy{Ref: f.destination, Title: "nuc's disk", Repository: main.Repository}
			f.destination, f.destRepo = ref, c.Repository
			return nil
		}
	}
	return &usererr.Error{Msg: "That isn't one of its backups."}
}

func (f *fakeBackupPlan) BackupCopiesOf(context.Context, string) []manager.BackupCopy {
	return f.copies
}

func (f *fakeBackupPlan) AddBackupCopy(_ context.Context, id, ref string) error {
	if ref == f.destination {
		return &usererr.Error{Msg: "That's where its backups already go."}
	}
	f.copies = append(f.copies, manager.BackupCopy{Ref: ref, Title: "Backblaze", Repository: "s3:https://s3.example.com/b/roostack/l/" + id})
	return nil
}

func (f *fakeBackupPlan) RemoveBackupCopy(_ context.Context, _, ref string) error {
	f.copies = slices.DeleteFunc(f.copies, func(c manager.BackupCopy) bool { return c.Ref == ref })
	return nil
}

func (f *fakeBackupPlan) BackupTargetOf(_ context.Context, id string) (store.BackupTarget, string, bool) {
	if f.destination == "" {
		return store.BackupTarget{}, "", false
	}
	return store.BackupTarget{ServerID: id, Provider: f.destination, Repository: cmp.Or(f.destRepo, fakeMainRepo)},
		"nuc's disk", true
}

func (f *fakeBackupPlan) SetBackupDestination(_ context.Context, id, ref string) error {
	if ref == "" {
		return &usererr.Error{Msg: "Choose where backups go."}
	}
	f.destination, f.destRepo = ref, ""
	return nil
}

func (f *fakeBackupPlan) AddS3Provider(_ context.Context, name string, p manager.S3Provider) error {
	if p.Bucket == "" {
		return &usererr.Error{Msg: "Enter the storage endpoint (starting with https://) and a bucket name."}
	}
	f.providers = append(f.providers, name)
	return nil
}

// fakeStorage is storage for other labs, and at them (StorageSharing).
type fakeStorage struct {
	guestSpace  manager.GuestSpace
	ending      []manager.EndingStorage
	guestServer string
	guests      []manager.GuestView
	guestOps    []string
}

func (f *fakeStorage) OfferStorage(context.Context) bool { return len(f.guests) > 0 }

func (f *fakeStorage) StorageGuests(context.Context) ([]manager.GuestView, error) {
	return f.guests, nil
}

func (f *fakeStorage) InviteGuest(_ context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error) {
	if quota < 1*units.GB {
		return labnet.StorageInvite{}, &usererr.Error{Msg: "Offer at least 1 GB."}
	}
	f.guestOps = append(f.guestOps, fmt.Sprintf("invite %s %d %s", name, quota/units.GB, endpoint))
	return labnet.StorageInvite{Token: "t", User: "g1"}, nil
}

func (f *fakeStorage) GuestServer(context.Context) string {
	if f.guestServer == "" {
		return "n-0123456789"
	}
	return f.guestServer
}

func (f *fakeStorage) SetGuestServer(_ context.Context, id string) error {
	if len(f.guests) > 0 {
		return &usererr.Error{Msg: "Other labs store backups on nuc."}
	}
	f.guestServer = id
	return nil
}

func (f *fakeStorage) DeleteRemovedGuest(_ context.Context, id int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("delete now %d", id))
	return nil
}

func (f *fakeStorage) EndingStorages(context.Context) []manager.EndingStorage { return f.ending }

func (f *fakeStorage) RemoveGuest(_ context.Context, id int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("remove %d", id))
	return nil
}

func (f *fakeStorage) SetGuestQuota(_ context.Context, id, quota int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("quota %d %d", id, quota/units.GB))
	return nil
}

func (f *fakeStorage) GuestSpaceLeft(context.Context) (manager.GuestSpace, bool) {
	return f.guestSpace, true
}

func (f *fakeStorage) LeftoverGuests(context.Context) ([]manager.LeftoverGuest, error) {
	return []manager.LeftoverGuest{{User: "g9", Size: 2 * units.GB}}, nil
}

func (f *fakeStorage) DeleteLeftoverGuest(_ context.Context, user string) error {
	if user != "g9" {
		return manager.ErrNotFound
	}
	f.guestOps = append(f.guestOps, "delete "+user)
	return nil
}

func (f *fakeStorage) AddRemoteStorage(_ context.Context, code string) error {
	if !strings.HasPrefix(code, "roostack-storage-") {
		return &usererr.Error{Msg: "that isn't a Roostack storage invite"}
	}
	f.guestOps = append(f.guestOps, "add remote")
	return nil
}

func (f *fakeStorage) RemoteStorages(context.Context) []manager.RemoteStorage {
	return []manager.RemoteStorage{{HostName: "Mum's lab", Quota: 50 * units.GB, Ref: "provider:7"}}
}

func (f *fakeStorage) RemoteUsage(context.Context, manager.RemoteStorage) (labnet.GuestUsage, error) {
	return labnet.GuestUsage{Used: 3 * units.GB, Quota: 50 * units.GB}, nil
}

// fakePauses is pausing public access (pause_test.go): Vaultwarden is
// public and built with rust, unless public is set.
type fakePauses struct {
	public   []manager.PublicService
	all      *config.Pause
	builtW   map[string]config.Pause
	own      map[string]*config.Pause
	paused   []manager.PauseRequest
	lifted   []manager.PauseRequest
	pausedBy []string
}

func (f *fakePauses) PublicPauses() (manager.PublicPauses, error) {
	if f.public == nil {
		f.public = []manager.PublicService{{Name: "vaultwarden", Title: "Vaultwarden", Hosts: []string{"passwords.lab.example.com"}, BuiltWith: []string{"rust", "sqlite"}}}
	}
	out := manager.PublicPauses{All: f.all, BuiltWith: f.builtW}
	for _, s := range f.public {
		s.Own, s.ByAll, s.ByBuiltWith = f.own[s.Name], f.all != nil, nil
		for _, b := range s.BuiltWith {
			if _, ok := f.builtW[b]; ok {
				s.ByBuiltWith = append(s.ByBuiltWith, b)
			}
		}
		out.Services = append(out.Services, s)
	}
	return out, nil
}

func (f *fakePauses) PausePublic(by string, r manager.PauseRequest) error {
	if !r.All && len(r.BuiltWith) == 0 && len(r.Services) == 0 {
		return &usererr.Error{Msg: "Choose what to pause: everything public, what services are built with, or some services."}
	}
	f.paused, f.pausedBy = append(f.paused, r), append(f.pausedBy, by)
	p := config.Pause{Since: time.Now(), By: by}
	if r.All {
		f.all = &p
	}
	for _, b := range r.BuiltWith {
		if f.builtW == nil {
			f.builtW = map[string]config.Pause{}
		}
		f.builtW[b] = p
	}
	for _, n := range r.Services {
		if n != "vaultwarden" {
			return manager.ErrNotFound
		}
		if f.own == nil {
			f.own = map[string]*config.Pause{}
		}
		f.own[n] = &p
	}
	return nil
}

func (f *fakePauses) LiftPause(r manager.PauseRequest) error {
	f.lifted = append(f.lifted, r)
	if r.All {
		f.all = nil
	}
	for _, b := range r.BuiltWith {
		delete(f.builtW, b)
	}
	for _, n := range r.Services {
		delete(f.own, n)
	}
	return nil
}

// fakePublic is public access (public_test.go).
type fakePublic struct {
	access       []manager.RouteAccess
	publicReqs   []manager.PublicRequest
	sharingAsked bool
	share        bool
	// edges can be the lab's edge; edge is the chosen one.
	edges []manager.EdgeServer
	edge  string
	// relay: through Roostack Cloud's relay; relayErr, Cloud's refusal.
	relay     bool
	relayErr  error
	countries config.Countries
	// relayChecks counts "Check now".
	relayChecks int
}

func (f *fakePublic) Access(name string) ([]manager.RouteAccess, error) {
	if name != "vaultwarden" {
		return nil, manager.ErrNotFound
	}
	if f.access == nil {
		f.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com", WAF: true,
			PrivatePaths: []manager.PathAccess{{Path: "/admin", Reason: "Vaultwarden's admin panel."}}}}
	}
	return f.access, nil
}

func (f *fakePublic) SetPublic(_ context.Context, name string, req manager.PublicRequest) error {
	access, err := f.Access(name)
	if err != nil {
		return err
	}
	if req.Public && access[0].Unsafe != "" && !req.AcceptUnsafe {
		return &usererr.Error{Msg: "Tick the box to confirm you understand why this address isn't safe to make public."}
	}
	f.publicReqs = append(f.publicReqs, req)
	f.access[0].Public = req.Public
	for i, pp := range f.access[0].PrivatePaths {
		f.access[0].PrivatePaths[i].Open = req.Public && slices.Contains(req.OpenPaths, pp.Path)
	}
	return nil
}

func (f *fakePublic) SetWAF(_ context.Context, name string, on bool) error {
	if _, err := f.Access(name); err != nil {
		return err
	}
	for i := range f.access {
		f.access[i].WAF = on
	}
	return nil
}

func (f *fakePublic) PublicHosts() []string {
	if len(f.access) > 0 && f.access[0].Public {
		return []string{f.access[0].Host}
	}
	return nil
}

func (f *fakePublic) SignalSharing() (bool, bool) { return f.sharingAsked, f.share }

func (f *fakePublic) SetSignalSharing(share bool) error {
	f.sharingAsked, f.share = true, share
	return nil
}

func (f *fakePublic) EdgeServers(context.Context) []manager.EdgeServer { return f.edges }

func (f *fakePublic) Edge(context.Context) (manager.EdgeServer, bool) {
	for _, e := range f.edges {
		if e.ID == f.edge {
			return e, true
		}
	}
	return manager.EdgeServer{ID: f.edge}, f.edge != ""
}

func (f *fakePublic) RelayOn() bool { return f.relay }

func (f *fakePublic) Countries() config.Countries { return f.countries }

func (f *fakePublic) CheckRelay(context.Context) error { f.relayChecks++; return f.relayErr }

func (f *fakePublic) SetCountries(_ context.Context, c config.Countries) error {
	if f.relayErr != nil {
		return f.relayErr
	}
	f.countries = c
	return nil
}

func (f *fakePublic) SetRelay(_ context.Context, on bool) error {
	if on && f.relayErr != nil {
		return f.relayErr
	}
	f.relay = on
	if on {
		f.edge = ""
	}
	return nil
}

func (f *fakePublic) SetEdge(_ context.Context, id string) error {
	if id != "" && !slices.ContainsFunc(f.edges, func(e manager.EdgeServer) bool { return e.ID == id }) {
		return &usererr.Error{Msg: "That server can't be the way in."}
	}
	f.edge = id
	return nil
}

// fakeKeys is changing the recovery key (keychange_test.go).
type fakeKeys struct {
	keyChange *manager.KeyChange
	keyOps    []string
}

func (f *fakeKeys) KeyChangeStatus() (*manager.KeyChange, error) { return f.keyChange, nil }

func (f *fakeKeys) StartKeyChange(context.Context) error {
	f.keyOps = append(f.keyOps, "start")
	f.keyChange = &manager.KeyChange{Phase: manager.KeyConfirm}
	return nil
}

func (f *fakeKeys) NewRecoveryKey() (string, error) {
	if f.keyChange == nil || f.keyChange.Phase != manager.KeyConfirm {
		return "", manager.ErrNotFound
	}
	return "the-new-recovery-key", nil
}

func (f *fakeKeys) ConfirmKeyChange(context.Context) error {
	f.keyOps = append(f.keyOps, "confirm")
	f.keyChange.Phase = manager.KeyAdding
	f.keyChange.Repos = []manager.KeyRepo{{Repository: "/mnt/usb/roostack", Added: true}, {Repository: "s3:s3.example.com/b", Error: "unreachable"}}
	return nil
}

func (f *fakeKeys) CancelKeyChange() error {
	f.keyOps = append(f.keyOps, "cancel")
	f.keyChange = nil
	return nil
}

// fakeLabRestores is restoring from the lab's other backups
// (restore_test.go), and a gone server's plan (gone_test.go).
type fakeLabRestores struct {
	otherBackups []manager.BackupMoment
	backupNotes  []string
	labRestores  []string
	lostPlan     *manager.LostPlan
}

func (f *fakeLabRestores) Restoring(string) bool { return false }

func (f *fakeLabRestores) LostServerPlan(_ context.Context, id string) (manager.LostPlan, error) {
	if f.lostPlan == nil || f.lostPlan.Server.ID != id {
		return manager.LostPlan{}, manager.ErrNotFound
	}
	return *f.lostPlan, nil
}

func (f *fakeLabRestores) RestoreLostServer(_ context.Context, id string, names []string, to string) error {
	f.labRestores = append(f.labRestores, "gone "+id+": "+strings.Join(names, ",")+" → "+to)
	return nil
}

func (f *fakeLabRestores) RemoveWaiting(_ context.Context, name string) error {
	f.labRestores = append(f.labRestores, "remove waiting "+name)
	return nil
}

// fakeWithdrawals is the registry's withdrawals (withdrawals_test.go).
type fakeWithdrawals struct {
	withdrawn     map[string]*manager.Withdrawal
	startedAnyway []string
	stopsOff      bool
}

func (f *fakeWithdrawals) Withdrawals(context.Context) map[string]*manager.Withdrawal {
	return f.withdrawn
}

func (f *fakeWithdrawals) StartAnyway(_ context.Context, name string, private bool) error {
	w := f.withdrawn[name]
	if w == nil {
		return manager.ErrNotFound
	}
	f.startedAnyway = append(f.startedAnyway, name+map[bool]string{true: " private", false: ""}[private])
	w.Stopped, w.StartedAnyway, w.Shown = false, true, registry.LevelWarning
	return nil
}

func (f *fakeWithdrawals) StopsOn() bool { return !f.stopsOff }

func (f *fakeWithdrawals) SetStops(on bool) error {
	f.stopsOff = !on
	return nil
}

// fakeHolds is services held after a restore, and moves that went back.
type fakeHolds struct {
	// held are the Owner's choices for held services ("start immich",
	// "put-back immich"); failedMoves the moves that went back.
	held        []string
	failedMoves map[string]manager.FailedMove
}

func (f *fakeHolds) StartHeld(_ context.Context, name string) error {
	f.held = append(f.held, "start "+name)
	return nil
}

func (f *fakeHolds) PutBackHeld(_ context.Context, name string) error {
	f.held = append(f.held, "put-back "+name)
	return nil
}

func (f *fakeHolds) FailedMoves(context.Context) map[string]manager.FailedMove {
	return f.failedMoves
}

// fakeServiceUpdates is updates to services (serviceupdates_test.go).
type fakeServiceUpdates struct {
	svcUpdates map[string]*manager.Update
	updating   map[string]*manager.ServiceUpdate
	lastUpdate map[string]*manager.UpdateRecord
	policies   map[string]string
	svcUpdated []string
}

func (f *fakeServiceUpdates) AvailableUpdates(context.Context) map[string]*manager.Update {
	return f.svcUpdates
}

func (f *fakeServiceUpdates) UpdateService(_ context.Context, name, to, by string, acceptRisks bool) error {
	u := f.svcUpdates[name]
	if u == nil {
		return manager.ErrNotFound
	}
	if u.To != to {
		return &usererr.Error{Msg: "The update on offer has changed."}
	}
	if len(u.NewRisks) > 0 && !acceptRisks {
		return &usererr.Error{Msg: "Tick the box."}
	}
	f.svcUpdated = append(f.svcUpdated, name+" "+to+" by "+by)
	return nil
}

func (f *fakeServiceUpdates) Updating(name string) *manager.ServiceUpdate { return f.updating[name] }

func (f *fakeServiceUpdates) LastUpdate(_ context.Context, name string) *manager.UpdateRecord {
	return f.lastUpdate[name]
}

func (f *fakeServiceUpdates) UpdatePolicy(name string) string {
	if p := f.policies[name]; p != "" {
		return p
	}
	return "notify"
}

func (f *fakeServiceUpdates) SetUpdatePolicy(_ context.Context, name, policy string) error {
	if f.policies == nil {
		f.policies = map[string]string{}
	}
	f.policies[name] = policy
	return nil
}

// fakeJunk is files found in backups and couldn't be read (junk_test.go).
type fakeJunk struct {
	junk []manager.JunkReport
}

func (f *fakeJunk) JunkReports(context.Context) []manager.JunkReport { return f.junk }

func (f *fakeJunk) DismissJunk(context.Context) error {
	f.junk = nil
	return nil
}

// fakeDataImports brings data in (DataImports): imports is the server's
// (node.Imports), and switched the services switched to it.
type fakeDataImports struct {
	imports  node.Imports
	switched []string
}

func (f *fakeDataImports) Imports() node.Imports { return f.imports }

func (f *fakeDataImports) ImportsOf(string) (node.Imports, error) {
	if f.imports == nil {
		return nil, &usererr.Error{Msg: "Data can't be brought into services on that server."}
	}
	return f.imports, nil
}

func (f *fakeDataImports) SwitchImport(_ context.Context, service string) error {
	f.switched = append(f.switched, service)
	return nil
}

func (f *fakeDataImports) Switching(string) bool { return false }

// fakeNode is the lab's one server (node.Node, ServiceNode): its
// services' setup and actions, its jobs, history and disks.
type fakeNode struct {
	nodetest.Base
	setup     map[string]manager.HookStatus
	retried   []string
	actions   []string
	actionOut string
	actionErr error

	acts     *activity.Registry
	backups  BackupService
	restores RestoreService
	updates  HostUpdates
	settings node.Settings
	events   []store.Event
	disk     *node.DiskSpace
	disks    []node.DiskSpace
}

func (f *fakeNode) HistoryPage(_ context.Context, before time.Time, kind string, limit int) ([]store.Event, error) {
	var out []store.Event
	for _, e := range f.events {
		if (kind == "" || e.Kind == kind) && (before.IsZero() || e.Finished.Before(before)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeNode) HistoryEvent(_ context.Context, id int64) (store.Event, error) {
	for _, e := range f.events {
		if e.ID == id {
			return e, nil
		}
	}
	return store.Event{}, node.ErrNotFound
}

func (f *fakeNode) CullHistory(_ context.Context, before time.Time) (int64, error) {
	var keep []store.Event
	for _, e := range f.events {
		if !e.Finished.Before(before) {
			keep = append(keep, e)
		}
	}
	n := int64(len(f.events) - len(keep))
	f.events = keep
	return n, nil
}

// DiskSpaces is f.disk (the system disk, with the backup store) and any
// more disks in f.disks.
// Capabilities: its disks and history; nothing else (nodetest.Base).
func (f *fakeNode) Disks() node.Disks     { return f }
func (f *fakeNode) History() node.History { return f }

func (f *fakeNode) DiskSpaces(context.Context) ([]node.DiskSpace, error) {
	if f.disk == nil {
		return nil, errors.New("no disk")
	}
	d := *f.disk
	d.Kind, d.Mount, d.Store = node.DiskSystem, "/", true
	return append([]node.DiskSpace{d}, f.disks...), nil
}

func (f *fakeNode) Settings() node.Settings { return f.settings }

// The fake is also the lab's one server.
func (f *fakeNode) Identity() nodeid.Identity {
	return nodeid.Identity{ID: "n-0123456789", Name: "nuc"}
}

func (f *fakeNode) Backups() node.Backups { return f.backups }

func (f *fakeNode) Restores() node.Restores { return f.restores }

func (f *fakeNode) Updates() node.Updates { return f.updates }

func (f *fakeNode) LatestActivity(kind string) (activity.Snapshot, bool) {
	return f.acts.Latest(kind)
}

func (f *fakeNode) InstallStatus(_ context.Context, name string) manager.HookStatus {
	return f.setup[name]
}

func (f *fakeNode) RevealSecret(_ context.Context, name, key string) (string, error) {
	if key != "admin_token" {
		return "", manager.ErrNotFound
	}
	return "s3cret-token", nil
}

func (f *fakeNode) RetryInstall(_ context.Context, name string) error {
	f.retried = append(f.retried, name)
	return nil
}

func (f *fakeNode) RunAction(_ context.Context, name, action string, inputs map[string]string) (string, error) {
	f.actions = append(f.actions, fmt.Sprintf("%s %s %v", name, action, inputs))
	return f.actionOut, f.actionErr
}

func (f *fakeNode) BuiltIns(context.Context) ([]node.BuiltIn, error) {
	return []node.BuiltIn{{Name: "Caddy", Kind: "proxy", Status: "active (running)", Detail: "Web addresses"}}, nil
}

type fakeBackups struct {
	status backup.Status
	runs   int
}

func (f *fakeBackups) Status(context.Context) (backup.Status, error) { return f.status, nil }

func (f *fakeBackups) RunNow(context.Context) error { f.runs++; return nil }

// fakeMail keeps settings and records messages.
type fakeMail struct {
	m        config.Mail
	password string
	sent     []mail.Message
	fail     error
}

func (f *fakeMail) Settings() (config.Mail, bool) { return f.m, f.password != "" }

func (f *fakeMail) Save(m config.Mail, password string, clear bool) error {
	if m.From == "" {
		return &usererr.Error{Msg: "Enter the address emails come from."}
	}
	f.m = m
	if clear {
		f.password = ""
	} else if password != "" {
		f.password = password
	}
	return nil
}

func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, msg)
	return nil
}

type fakeLabRestore struct {
	mu        sync.Mutex
	src       labrestore.Source
	key       string
	started   *labrestore.Found
	usedDrive string
	// unfinished: a restore started earlier and didn't finish.
	unfinished bool
	// found and unopened, if set, are what Find finds.
	found    []labrestore.Found
	unopened []labrestore.Unopened
	// finished: the restore is done, and Roostack about to restart.
	finished bool
}

func (f *fakeLabRestore) Unfinished() bool { return f.unfinished }

func (f *fakeLabRestore) Find(_ context.Context, src labrestore.Source, key string) ([]labrestore.Found, []labrestore.Unopened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.src, f.key = src, key
	if f.found != nil {
		return f.found, f.unopened, nil
	}
	snap := backup.Snapshot{ID: "abc", Time: time.Now().Add(-time.Hour)}
	return []labrestore.Found{
			{Repository: "/mnt/backup/roostack/l/n-1", Domain: "lab.example.com", ServerName: "nuc", Controller: true, Snapshot: snap, Snapshots: 12},
			{Repository: "/mnt/backup/roostack/l/n-2", Domain: "lab.example.com", ServerName: "vps", Snapshot: snap, Snapshots: 9},
		},
		[]labrestore.Unopened{{Repository: "rest:https://u:hunter22@elsewhere/lab", Problem: "The recovery key doesn't open it."}}, nil
}

func (f *fakeLabRestore) Start(fd labrestore.Found) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = &fd
	return nil
}

func (f *fakeLabRestore) Drives(context.Context) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "system", Kind: "system", Usable: true},
		{ID: "uuid:64B1", Kind: "usb", Name: "OLD BACKUPS", Usable: true}}, nil
}

func (f *fakeLabRestore) UseDrive(_ context.Context, id string) (string, error) {
	f.usedDrive = id
	return "/mnt/roostack-old-backups", nil
}

func (f *fakeLabRestore) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started != nil && !f.finished
}

func (f *fakeLabRestore) Progress() (activity.Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := activity.Snapshot{Title: "Restoring lab.example.com", Status: "Restoring nuc's files", Lines: []string{"/var/lib/roostack"}}
	if f.finished {
		snap.Finished = time.Now()
	}
	return snap, true
}

// fakeLibraries is the manager's media library side: one server with a
// drive MEDIA (Films, Music) and a library Films read by Plex.
type fakeLibraries struct {
	added    []manager.LibraryRequest
	removed  []string
	readers  map[string][]string
	svcLibs  map[string][]string
	folderOf []agent.Library
	// reader is the service that reads libraries (ServiceLibraries).
	reader string
	// setLibs counts SetServiceLibraries calls; svcPaths are the paths
	// last given.
	setLibs  int
	svcPaths map[string]string
	// gone are the libraries bound at their own paths whose drive is
	// unplugged (ServiceLibraries).
	gone []string
}

func newFakeLibraries() *fakeLibraries {
	return &fakeLibraries{readers: map[string][]string{}, svcLibs: map[string][]string{}}
}

func (f *fakeLibraries) MediaLibraries(context.Context) ([]manager.ServerLibraries, error) {
	return []manager.ServerLibraries{{ServerID: "n-1", ServerName: "nuc", This: true, CanHave: true, Libraries: []manager.LibraryView{{
		MediaLibrary: store.MediaLibrary{ServerID: "n-1", Name: "films", Title: "Films", OwnerName: "sam"},
		Where:        "The drive MEDIA › Films", Mounted: true, Connected: true, Size: 2e12, ReadBy: []string{"Plex"}}}}}, nil
}

func (f *fakeLibraries) LibraryDrives(context.Context, string) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "uuid:9999", Kind: "usb", Name: "ROOSTACK", UUID: "9999", Backup: true},
		{ID: "uuid:1111-2222", Kind: "usb", Name: "MEDIA", UUID: "1111-2222", FSType: "ext4", Size: 2e12, Free: 1e12, Usable: true}}, nil
}

func (f *fakeLibraries) LibraryFolders(_ context.Context, _ string, l agent.Library) (agent.FolderList, error) {
	f.folderOf = append(f.folderOf, l)
	if l.Sub == "Backups" {
		return agent.FolderList{}, &usererr.Error{Msg: "that's a backup drive: keep media on another drive"}
	}
	list := agent.FolderList{Path: l.Sub, Folders: []agent.FolderEntry{{Name: "Films", Folders: 412}, {Name: "Music", Folders: 3, Files: 1}}, Files: 1}
	if l.Sub == "Films" {
		list.Folders = nil
		list.Check = agent.Readability{Sampled: 10, Unreadable: 8, Owner: &agent.Owner{UID: 1000, GID: 1000, Name: "sam"}, OwnerFiles: 8}
	}
	return list, nil
}

func (f *fakeLibraries) LibraryReaders(context.Context, string) ([]manager.LibraryReader, error) {
	return []manager.LibraryReader{{Name: "plex", Title: "Plex", Libraries: []string{"films"}}, {Name: "navidrome", Title: "Navidrome"}}, nil
}

func (f *fakeLibraries) AddLibrary(_ context.Context, req manager.LibraryRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return &usererr.Error{Msg: "Give the library a name with some letters or digits, e.g. Films."}
	}
	f.added = append(f.added, req)
	return nil
}

func (f *fakeLibraries) RemoveLibrary(_ context.Context, server, name string) error {
	f.removed = append(f.removed, server+"/"+name)
	return nil
}

func (f *fakeLibraries) SetLibraryReaders(_ context.Context, server, name string, services []string) error {
	f.readers[server+"/"+name] = services
	return nil
}

func (f *fakeLibraries) SetServiceLibraries(_ context.Context, service string, libs []string, paths map[string]string) error {
	f.svcLibs[service] = libs
	if paths != nil {
		f.svcPaths = paths
	}
	f.setLibs++
	return nil
}

func (f *fakeLibraries) ServerLibraryChoices(context.Context, string) []store.MediaLibrary {
	return []store.MediaLibrary{{ServerID: "n-1", Name: "films", Title: "Films"}}
}

func (f *fakeLibraries) ServiceLibraries(_ context.Context, service string) (manager.ServiceLibraryChoice, error) {
	if service != f.reader {
		return manager.ServiceLibraryChoice{}, nil
	}
	chosen, ok := f.svcLibs[service]
	if !ok {
		chosen = []string{"films"}
	}
	return manager.ServiceLibraryChoice{Reads: true, ServerName: "nuc", Chosen: chosen, Inside: "/media", Paths: f.svcPaths, Gone: f.gone,
		Choices: []store.MediaLibrary{{ServerID: "n-1", Name: "films", Title: "Films"}, {ServerID: "n-1", Name: "music", Title: "Music"}}}, nil
}

// fakeRegistry is the controller's registry.Updater.
type fakeRegistry struct {
	on      bool
	status  registry.Status
	checks  int // Check calls
	opened  int // CheckIfOlder calls
	setOnTo []bool
	listing map[string]registry.Listed
}

func (f *fakeRegistry) On() bool { return f.on }

func (f *fakeRegistry) SetOn(on bool) error {
	f.on = on
	f.setOnTo = append(f.setOnTo, on)
	return nil
}

func (f *fakeRegistry) Check(context.Context) error { f.checks++; return nil }

func (f *fakeRegistry) CheckIfOlder(context.Context, time.Duration) error {
	f.opened++
	return nil
}

func (f *fakeRegistry) Status() registry.Status { return f.status }

func (f *fakeRegistry) Listing() (map[string]registry.Listed, error) {
	return f.listing, nil
}

func (f *fakeRegistry) URL() string { return registry.DefaultURL }

func (f *fakeRegistry) Dir() string { return "/var/lib/roostack/catalog" }

type fakeRestores struct {
	snaps    []backup.Snapshot
	restored []string
	pending  bool
	putBack  []string
}

func (f *fakeRestores) Snapshots(context.Context, string) ([]backup.Snapshot, error) {
	return f.snaps, nil
}

func (f *fakeRestores) Restore(_ context.Context, name, id string) error {
	if f.pending {
		return backup.ErrRestoreQueued
	}
	f.restored = append(f.restored, name+" "+id)
	return nil
}

func (f *fakeRestores) Pending(context.Context) (bool, error) { return f.pending, nil }

func (f *fakeRestores) Outcome(context.Context, string) (backup.RestoreOutcome, error) {
	return backup.RestoreOutcome{}, nil
}

func (f *fakeRestores) PutBack(_ context.Context, _, p string) error {
	f.putBack = append(f.putBack, p)
	return nil
}

// fakeSSO is the manager's single sign-on side.
type fakeSSO struct {
	on  map[string]bool
	why string
}

func (f *fakeSSO) SSOAvailable(string) (bool, string) { return f.why == "", f.why }

func (f *fakeSSO) SetSSO(_ context.Context, name string, on bool) error {
	if on && f.why != "" {
		return &usererr.Error{Msg: f.why}
	}
	f.on[name] = on
	return nil
}

func (f *fakeSSO) SSOOff(name string) string {
	if titles := map[string]string{"mealie": "Mealie", "forgejo": "Forgejo"}; !f.on[name] {
		return titles[name]
	}
	return ""
}

func (f *fakeSSO) SSOClients() []sso.Client {
	var out []sso.Client
	if f.on["mealie"] {
		out = append(out, sso.Client{ID: "mealie", Title: "Mealie", RedirectURIs: []string{"https://recipes.lab.example.com/login"}})
	}
	if f.on["stirling-pdf"] {
		out = append(out, sso.Client{ID: "stirling-pdf", Title: "Stirling-PDF", Host: "pdf.lab.example.com"})
	}
	if f.on["forgejo"] {
		out = append(out, sso.Client{ID: "forgejo", Title: "Forgejo", RedirectURIs: []string{"https://git.lab.example.com/user/oauth2/lab.example.com/callback"}})
	}
	return out
}

type fakeSetup struct {
	pending bool
	// noKey: the lab has no recovery key yet (the first backup makes it).
	noKey       bool
	drives      []agent.Drive
	erased      string
	labName     string
	restartMode string
	destination string
	p           setup.Progress
	zone        string
	restarted   bool
	ssh         string
	sshLogin    string
	sshd        agent.SSHStatus
	policy      string
	clients     []netip.Addr
	domainErr   error
	// network is where the lab is (as saved), uiTunnelOnly its UI choice.
	network      string
	uiTunnelOnly bool

	lastProvider string
	lastValues   map[string]string
	lastOpts     setup.DomainOptions
	oldEnded     bool
	oldFinished  bool
}

func (f *fakeSetup) Progress(context.Context) (setup.Progress, error) { return f.p, nil }

func (f *fakeSetup) Done(_ context.Context, step string) error {
	switch step {
	case setup.StepTimezone:
		f.p.TimezoneDone = true
	case setup.StepSSH:
		f.p.SSHDone = true
	default:
		return fmt.Errorf("unknown step %q", step)
	}
	return nil
}

// fakeSetup is also the server's settings (node.Settings).
func (f *fakeSetup) Timezones(context.Context) (string, []string, error) {
	return f.zone, []string{"Australia/Melbourne", "Etc/UTC"}, nil
}

func (f *fakeSetup) SSHAccess(context.Context, bool) (node.SSHAccess, error) {
	return node.SSHAccess{Choice: f.ssh, Login: f.sshLogin, Clients: f.clients, SSHD: f.sshd}, nil
}

func (f *fakeSetup) SetSSH(_ context.Context, c node.SSHChange) error {
	if c.Choice == "off" && len(f.clients) > 0 && !c.Confirmed {
		return &usererr.Error{Msg: "That would disconnect the SSH connection."}
	}
	if c.Choice == "off" {
		c.Login = config.SSHLoginKey
	}
	if _, ok := f.sshd.SudoKeyLogin(); c.Login == config.SSHLoginKey && c.Choice != "off" && !f.sshd.KeysOnly && !ok {
		return &usererr.Error{Msg: "Roostack hasn't seen anyone log in with an SSH key."}
	}
	f.sshd.KeysOnly = c.Login == config.SSHLoginKey
	f.sshd.Passwords = !f.sshd.KeysOnly
	f.ssh, f.sshLogin, f.restarted = c.Choice, c.Login, true
	return nil
}

func (f *fakeSetup) RestartPolicy(context.Context) (string, int, error) {
	return cmp.Or(f.restartMode, config.RestartAuto), config.Maintenance{}.Hour(), nil
}

func (f *fakeSetup) PendingRestart() bool { return f.pending }

func (f *fakeSetup) BackupDrives(context.Context) ([]agent.Drive, error) {
	if f.drives != nil {
		return f.drives, nil
	}
	return []agent.Drive{{ID: "system", Kind: "system", Name: "This server's own disk", Usable: true, Free: 30 * units.GB}}, nil
}

func (f *fakeSetup) EraseDrive(_ context.Context, disk string) error {
	f.erased = disk
	for i, d := range f.drives {
		if d.Disk == disk {
			f.drives[i] = agent.Drive{ID: "uuid:new", Kind: d.Kind, Name: "ROOSTACK", Disk: disk, Usable: true, Erasable: true}
		}
	}
	return nil
}

func (f *fakeSetup) SetLabName(_ context.Context, name string, restart bool) error {
	if strings.TrimSpace(name) == "" {
		return &usererr.Error{Msg: "Give the lab a name."}
	}
	f.labName, f.restarted = name, restart
	return nil
}

func (f *fakeSetup) Network(detect func() string) setup.NetworkChoice {
	c := setup.NetworkChoice{Saved: f.network, UITunnelOnly: f.uiTunnelOnly}
	if detect != nil {
		c.Detected = detect()
	}
	return c
}

func (f *fakeSetup) SetNetwork(network string, uiTunnelOnly bool) error {
	if network != config.NetworkHome && network != config.NetworkInternet {
		return &usererr.Error{Msg: "Choose where this lab is."}
	}
	f.network, f.uiTunnelOnly, f.restarted = network, uiTunnelOnly, true
	return nil
}

func (f *fakeSetup) PromoteBackupCopy(context.Context, string, node.BackupCopy) error { return nil }

func (f *fakeSetup) SetBackupCopies(context.Context, []node.BackupCopy) error { return nil }

func (f *fakeSetup) SetBackupDestination(_ context.Context, d node.BackupDestination) error {
	f.destination = d.Repository
	return nil
}

func (f *fakeSetup) SetRestartPolicy(_ context.Context, mode string, hour int) error {
	if hour < 0 || hour > 23 {
		return &usererr.Error{Msg: "Choose an hour between 0 and 23."}
	}
	f.policy, f.restarted = fmt.Sprintf("%s %d", mode, hour), true
	return nil
}

func (f *fakeSetup) SetTimezone(_ context.Context, name string) (bool, error) {
	if name != "Australia/Melbourne" && name != "Etc/UTC" {
		return false, &usererr.Error{Msg: "Choose a time zone from the list."}
	}
	changed := name != f.zone
	f.zone, f.restarted = name, f.restarted || changed
	return changed, nil
}

func (f *fakeSetup) SaveDomain(_ context.Context, d, e, provider string, values map[string]string, opts setup.DomainOptions) error {
	f.lastProvider, f.lastValues, f.lastOpts = provider, values, opts
	if f.domainErr != nil {
		return f.domainErr
	}
	f.p.DomainDone = true
	return nil
}

func (f *fakeSetup) DomainLater(context.Context) error { f.p.DomainLater = true; return nil }

func (f *fakeSetup) EndOldAddress(context.Context) error { f.oldEnded = true; return nil }

func (f *fakeSetup) FinishOldAddress(context.Context) error { f.oldFinished = true; return nil }

func (f *fakeSetup) BackupAdded(_ context.Context, first bool) error {
	f.noKey = false
	f.p.BackupDone = true
	if first {
		f.p.RecoverySaved = false
	}
	return nil
}

func (f *fakeSetup) BackupLater(context.Context) error { f.p.BackupLater = true; return nil }

func (f *fakeSetup) RecoveryKey() (string, error) {
	if f.noKey {
		return "", os.ErrNotExist
	}
	return "the-recovery-key", nil
}

func (f *fakeSetup) ConfirmRecoveryKey(context.Context) error { f.p.RecoverySaved = true; return nil }

type fakeDNS struct {
	st        dnsrecords.Status
	records   []dnsrecords.Record
	managing  []bool
	takenOver bool
	forgot    bool
}

func (f *fakeDNS) Status() dnsrecords.Status { return f.st }

func (f *fakeDNS) Records(context.Context) ([]dnsrecords.Record, error) { return f.records, nil }

func (f *fakeDNS) SetManaging(on, risk bool) error {
	if on && f.st.Risky && !risk {
		return dnsrecords.ErrNeedsRisk
	}
	f.managing = append(f.managing, on)
	return nil
}

func (f *fakeDNS) TakeOver(context.Context) error { f.takenOver = true; return nil }

func (f *fakeDNS) Refresh() {}

// Forget forgets what Roostack couldn't delete under old addresses.
func (f *fakeDNS) Forget(context.Context) error {
	f.forgot = true
	if f.st.Old != nil && f.st.Old.Ended {
		f.st.Old = nil
	}
	f.st.Left = nil
	return nil
}

type fakeUpdates struct {
	changes   osupdate.HostCheck
	known     bool
	last      *osupdate.Result
	restart   osupdate.RestartStatus
	submitted int
	restarted bool
	busy      bool
	series    osupdate.Series
	accepted  string
}

func (f *fakeUpdates) Series(context.Context) (osupdate.Series, error) {
	s := f.series
	if f.accepted != "" {
		s.Accepted = f.accepted
	}
	return s, nil
}

func (f *fakeUpdates) AcceptSeries(_ context.Context, series string) error {
	if series != f.series.Newest {
		return osupdate.ErrNoSuchSeries
	}
	f.accepted = series
	return nil
}

func (f *fakeUpdates) Last(context.Context) (*osupdate.Result, error) { return f.last, nil }

func (f *fakeUpdates) Pending(context.Context) (bool, error) { return f.submitted > 0, nil }

func (f *fakeUpdates) Submit(context.Context) error { f.submitted++; return nil }

func (f *fakeUpdates) RestartStatus(context.Context) (osupdate.RestartStatus, error) {
	return f.restart, nil
}

func (f *fakeUpdates) HostChanges(context.Context) (osupdate.HostCheck, error) {
	c := f.changes
	c.Known = f.known
	return c, nil
}

func (f *fakeUpdates) AcknowledgeHostChanges(context.Context) error { f.known = true; return nil }

func (f *fakeUpdates) RestartNow(context.Context) error {
	if f.busy {
		return osupdate.ErrBusy{Job: "backup"}
	}
	f.restarted = true
	return nil
}
