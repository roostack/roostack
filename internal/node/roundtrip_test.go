package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/testwait"
)

// The lab API, end to end: every Remote method against a Server wrapping
// a node that records what it's asked (the quality review of 2026-10-21,
// S9). A path, a JSON name or an argument lost on the way fails here.

// rtCall is one call the server made on its node.
type rtCall struct {
	Method string
	Args   []any
}

func call(method string, args ...any) rtCall { return rtCall{method, args} }

// rtRec records calls; every recording fake shares one.
type rtRec struct {
	mu    sync.Mutex
	calls []rtCall
}

func (f *rtRec) rec(method string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rtCall{method, args})
}

// take returns the calls so far, and forgets them.
func (f *rtRec) take() []rtCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// What the recording node answers: each value set, so a field the wire
// drops shows.
var (
	rtAt       = time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC)
	rtSince    = time.Unix(rtAt.Unix(), 0) // as a server rebuilds a Unix time
	rtApply    = ApplyStatus{Finished: true, LastRun: rtAt}
	rtStates   = map[string]string{"immich": "running"}
	rtDetails  = map[string]ServiceDetail{"immich": {Status: "running", DataDirs: []string{"/srv/immich"}, DataSize: 42}}
	rtBuiltIns = []BuiltIn{{Name: "Caddy", Kind: "proxy", Status: "active (running)", Detail: "3 sites", LastSeen: rtAt}}
	rtHook     = HookStatus{Running: true, Failed: "earlier"}
	rtActivity = activity.Snapshot{Title: "Backing up", Status: "Backing up immich", Started: rtAt, Lines: []string{"one"}}
	rtAddrs    = []string{"192.168.1.20"}
	rtLinks    = map[string]int64{"pubkey": 1790000000}

	rtBackupStatus = backup.Status{Configured: true, Running: true, Progress: 40, Step: "Copying", Repository: "/srv/backup", Next: rtAt}
	rtSnapshots    = []backup.Snapshot{{ID: "abc123", Short: "abc", Time: rtAt, Paths: []string{"/srv/immich"}, Hostname: "nuc"}}
	rtOutcome      = backup.RestoreOutcome{Finished: true, Err: "dump failed", DumpsFailed: true, Taken: rtAt, Kept: "/srv/x"}

	rtResult  = &osupdate.Result{At: rtAt, OK: true, Summary: "3 updates", Output: "done"}
	rtRestart = osupdate.RestartStatus{Needed: true, Since: rtAt, Packages: []string{"linux-image"}, Waiting: "a backup", LastBoot: rtAt}
	rtHost    = osupdate.HostCheck{Known: true}
	rtSeries  = osupdate.Series{Current: "0.4", Newest: "0.5", NewestVersion: "0.5.0", Checked: rtAt}

	rtZones    = []string{"Etc/UTC", "Australia/Sydney"}
	rtSSH      = SSHAccess{Choice: "lan", Login: "keys", SSHDError: "none"}
	rtSSHFresh = SSHAccess{Choice: "everywhere", Login: "keys"}

	rtKeptCopies = []kept.Copy{{Service: "immich", Why: kept.MovedAway, Note: "to-nuc", Time: rtKeptAt, Whole: true, Size: 99,
		Paths: []string{"/srv/media/immich.moved-to-nuc-20261022-093000", "/srv/services/immich.moved-to-nuc-20261022-093000"}},
		{Service: "immich", Why: kept.BeforeRestore, Time: rtKeptAt, Size: 12, Paths: []string{"/srv/services/immich/library.before-restore-20261022-093000"}}}
	rtKeptAt   = time.Date(2026, 10, 22, 9, 30, 0, 0, time.Local) // as a kept folder's name has it
	rtSecrets  = map[string][]byte{"roostack-immich-db": []byte("pw")}
	rtTar      = "a tar stream"
	rtUpdate   = UpdateStatus{Stage: "holding", Error: "slow", Backup: rtAt}
	rtHealth   = Health{Healthy: true, Problem: "none", Restarts: 2}
	rtDisks    = []DiskSpace{{Kind: "system", Mount: "/", Total: 100, Used: 40, Free: 60, Services: 10, Counted: rtAt}}
	rtEvents   = []store.Event{{ID: 7, Started: rtAt, Finished: rtAt, Kind: "backup", Title: "Backed up", Status: "ok"}}
	rtMemory   = MemoryInfo{Total: 8_000_000_000, Available: 3_000_000_000}
	rtWGStatus = agent.WireGuardStatus{Configured: true, PublicKey: "pk", Address: "100.103.66.1/24", Handshakes: map[string]int64{"a": 1}}

	rtDrives   = []agent.Drive{{ID: "uuid:1", Kind: "usb", Name: "T7", Size: 1000, Free: 500, Usable: true}}
	rtFolders  = agent.FolderList{Path: "/media", Files: 3}
	rtLibs     = []agent.LibraryStatus{{Name: "Films", Source: "T7", Mounted: true, ReadBy: []string{"jellyfin"}}}
	rtImport   = ImportView{Mounted: true}
	rtListing  = ImportListing{Path: "photos", Folders: []ImportEntry{{Name: "2024"}}}
	rtLibrary  = agent.Library{Name: "Films", Drive: "1", Sub: "films"}
	rtShare    = agent.Share{Type: "smb", Server: "nas", Path: "media", Username: "mum"}
	rtWG       = agent.WireGuard{Interface: agent.GuestsInterface, Address: "100.103.66.1/24", ListenPort: 51821}
	rtCopy     = BackupCopy{Name: "backup-2", Title: "Backup 2", Repository: "s3:x"}
	rtDest     = BackupDestination{Repository: "rest:y", KeyHint: "k1"}
	rtSetup    = &importer.Setup{From: importer.FromCompose, Project: "immich"}
	rtPrefixes = []importer.Prefix{{Old: "/mnt/photos", Now: "/photos"}}
	rtChoices  = importer.Choices{Storage: map[string]string{"upload": "photos"}, Without: []string{"thumbs"}}
	rtSource   = ImportSource{Kind: "drive", Library: rtLibrary, Title: "T7 › immich"}
)

// rtNode is a node that offers every operation and records each call.
type rtNode struct{ *rtRec }

func (f rtNode) Identity() nodeid.Identity { return nodeid.Identity{ID: "n-0000000002", Name: "vps"} }
func (f rtNode) Apply(a Assignment)        { f.rec("Apply", a.Lab.Domain, a.Reason) }
func (f rtNode) Status() ApplyStatus       { f.rec("Status"); return rtApply }
func (f rtNode) ServiceStates(context.Context) (map[string]string, error) {
	f.rec("ServiceStates")
	return rtStates, nil
}
func (f rtNode) ServiceDetails(context.Context) (map[string]ServiceDetail, error) {
	f.rec("ServiceDetails")
	return rtDetails, nil
}
func (f rtNode) BuiltIns(context.Context) ([]BuiltIn, error) {
	f.rec("BuiltIns")
	return rtBuiltIns, nil
}
func (f rtNode) InstallStatus(_ context.Context, s string) HookStatus {
	f.rec("InstallStatus", s)
	return rtHook
}
func (f rtNode) RetryInstall(_ context.Context, s string) error { f.rec("RetryInstall", s); return nil }
func (f rtNode) RunAction(_ context.Context, s, a string, in map[string]string) (string, error) {
	f.rec("RunAction", s, a, in)
	return "done", nil
}
func (f rtNode) RevealSecret(_ context.Context, s, k string) (string, error) {
	f.rec("RevealSecret", s, k)
	return "s3cret", nil
}
func (f rtNode) LatestActivity(kind string) (activity.Snapshot, bool) {
	f.rec("LatestActivity", kind)
	return rtActivity, true
}
func (f rtNode) Backups() Backups     { return rtBackups(f) }
func (f rtNode) Restores() Restores   { return rtRestores(f) }
func (f rtNode) Updates() Updates     { return rtUpdates(f) }
func (f rtNode) Settings() Settings   { return rtSettings(f) }
func (f rtNode) Media() Media         { return rtMedia(f) }
func (f rtNode) Imports() Imports     { return rtImports(f) }
func (f rtNode) Mover() Mover         { return f }
func (f rtNode) Updater() Updater     { return f }
func (f rtNode) Disks() Disks         { return f }
func (f rtNode) Memory() Memory       { return f }
func (f rtNode) LeftData() LeftData   { return f }
func (f rtNode) History() History     { return f }
func (f rtNode) Linked() Linked       { return f }
func (f rtNode) GuestHost() GuestHost { return f }
func (f rtNode) Processor() Processor { return f }
func (f rtNode) Photos() Photos       { return nil }

// Processor, Linked.
func (f rtNode) Arch() string { f.rec("Arch"); return "arm64" }
func (f rtNode) LinkStatus(context.Context) ([]string, map[string]int64) {
	f.rec("LinkStatus")
	return rtAddrs, rtLinks
}

// Mover.
func (f rtNode) DataDirs(string) []string { return nil }
func (f rtNode) ExportData(_ context.Context, s string, dir int, since time.Time, w io.Writer) error {
	f.rec("ExportData", s, dir, since)
	_, err := io.WriteString(w, rtTar)
	return err
}
func (f rtNode) ListData(_ context.Context, s string, dir int) ([]string, error) {
	f.rec("ListData", s, dir)
	return []string{"a", "b/c"}, nil
}
func (f rtNode) ImportData(_ context.Context, s string, dir int, r io.Reader, strip int) error {
	data, err := io.ReadAll(r)
	f.rec("ImportData", s, dir, string(data), strip)
	return err
}
func (f rtNode) PruneData(_ context.Context, s string, dir int, keep []string) error {
	f.rec("PruneData", s, dir, keep)
	return nil
}
func (f rtNode) CommitData(_ context.Context, s string, why kept.Why) ([]string, error) {
	f.rec("CommitData", s, why)
	return []string{"/srv/immich.old"}, nil
}
func (f rtNode) SetAsideData(_ context.Context, s, note string) ([]string, error) {
	f.rec("SetAsideData", s, note)
	return []string{"/srv/immich.moved"}, nil
}
func (f rtNode) StopService(_ context.Context, s string) error  { f.rec("StopService", s); return nil }
func (f rtNode) StartService(_ context.Context, s string) error { f.rec("StartService", s); return nil }
func (f rtNode) ExportSecrets(_ context.Context, s string) (map[string][]byte, error) {
	f.rec("ExportSecrets", s)
	return rtSecrets, nil
}
func (f rtNode) ImportSecrets(_ context.Context, secrets map[string][]byte) error {
	f.rec("ImportSecrets", secrets)
	return nil
}
func (f rtNode) DataSize(_ context.Context, s string) (int64, error) {
	f.rec("DataSize", s)
	return 4096, nil
}
func (f rtNode) LoadDumps(_ context.Context, s string) error { f.rec("LoadDumps", s); return nil }
func (f rtNode) KeptCopies(context.Context) ([]kept.Copy, error) {
	f.rec("KeptCopies")
	return rtKeptCopies, nil
}
func (f rtNode) RemoveKept(_ context.Context, p string) error {
	f.rec("RemoveKept", p)
	return nil
}
func (f rtNode) PutBackKept(_ context.Context, p string) error {
	f.rec("PutBackKept", p)
	return nil
}

// Updater.
func (f rtNode) StartUpdate(_ context.Context, req UpdateRequest) (string, error) {
	f.rec("StartUpdate", req)
	return "u-1", nil
}
func (f rtNode) UpdateStatus(_ context.Context, id string) (UpdateStatus, error) {
	f.rec("UpdateStatus", id)
	return rtUpdate, nil
}
func (f rtNode) FinishUpdate(_ context.Context, id string) error {
	f.rec("FinishUpdate", id)
	return nil
}
func (f rtNode) CheckHealth(_ context.Context, s string) (Health, error) {
	f.rec("CheckHealth", s)
	return rtHealth, nil
}

// Disks, Memory, LeftData, History.
func (f rtNode) DiskSpaces(context.Context) ([]DiskSpace, error) {
	f.rec("DiskSpaces")
	return rtDisks, nil
}
func (f rtNode) MemoryInfo(context.Context) (MemoryInfo, error) {
	f.rec("MemoryInfo")
	return rtMemory, nil
}
func (f rtNode) LeftDataDirs(_ context.Context, s string) ([]string, error) {
	f.rec("LeftDataDirs", s)
	return []string{"/srv/old"}, nil
}
func (f rtNode) HistoryPage(_ context.Context, before time.Time, kind string, limit int) ([]store.Event, error) {
	f.rec("HistoryPage", before, kind, limit)
	return rtEvents, nil
}
func (f rtNode) HistoryEvent(_ context.Context, id int64) (store.Event, error) {
	f.rec("HistoryEvent", id)
	return rtEvents[0], nil
}
func (f rtNode) CullHistory(_ context.Context, before time.Time) (int64, error) {
	f.rec("CullHistory", before)
	return 12, nil
}

// GuestHost.
func (f rtNode) ApplyGuestLink(_ context.Context, wg agent.WireGuard) (string, error) {
	f.rec("ApplyGuestLink", wg)
	return "guest-pub", nil
}
func (f rtNode) GuestLinkStatus(context.Context) (agent.WireGuardStatus, error) {
	f.rec("GuestLinkStatus")
	return rtWGStatus, nil
}
func (f rtNode) RemoveGuestLink(context.Context) error { f.rec("RemoveGuestLink"); return nil }
func (f rtNode) GuestUsage(_ context.Context, user string) (int64, error) {
	f.rec("GuestUsage", user)
	return 777, nil
}
func (f rtNode) GuestDirs(context.Context) ([]string, error) {
	f.rec("GuestDirs")
	return []string{"g1"}, nil
}
func (f rtNode) RemoveGuestDir(_ context.Context, user string) error {
	f.rec("RemoveGuestDir", user)
	return nil
}
func (f rtNode) GuestSpace(context.Context) (int64, int64, error) {
	f.rec("GuestSpace")
	return 300, 1000, nil
}

type rtBackups rtNode

func (f rtBackups) Status(context.Context) (backup.Status, error) {
	f.rec("Backups.Status")
	return rtBackupStatus, nil
}
func (f rtBackups) RunNow(context.Context) error { f.rec("Backups.RunNow"); return nil }

type rtRestores rtNode

func (f rtRestores) Snapshots(_ context.Context, s string) ([]backup.Snapshot, error) {
	f.rec("Restores.Snapshots", s)
	return rtSnapshots, nil
}
func (f rtRestores) Restore(_ context.Context, s, id string) error {
	f.rec("Restores.Restore", s, id)
	return nil
}
func (f rtRestores) Pending(context.Context) (bool, error) {
	f.rec("Restores.Pending")
	return true, nil
}
func (f rtRestores) Outcome(_ context.Context, s string) (backup.RestoreOutcome, error) {
	f.rec("Restores.Outcome", s)
	return rtOutcome, nil
}
func (f rtRestores) PutBack(_ context.Context, s, p string) error {
	f.rec("Restores.PutBack", s, p)
	return nil
}

type rtUpdates rtNode

func (f rtUpdates) Last(context.Context) (*osupdate.Result, error) {
	f.rec("Updates.Last")
	return rtResult, nil
}
func (f rtUpdates) Pending(context.Context) (bool, error) { f.rec("Updates.Pending"); return true, nil }
func (f rtUpdates) Submit(context.Context) error          { f.rec("Updates.Submit"); return nil }
func (f rtUpdates) RestartStatus(context.Context) (osupdate.RestartStatus, error) {
	f.rec("Updates.RestartStatus")
	return rtRestart, nil
}
func (f rtUpdates) RestartNow(context.Context) error { f.rec("Updates.RestartNow"); return nil }
func (f rtUpdates) HostChanges(context.Context) (osupdate.HostCheck, error) {
	f.rec("Updates.HostChanges")
	return rtHost, nil
}
func (f rtUpdates) AcknowledgeHostChanges(context.Context) error {
	f.rec("Updates.AcknowledgeHostChanges")
	return nil
}
func (f rtUpdates) Series(context.Context) (osupdate.Series, error) {
	f.rec("Updates.Series")
	return rtSeries, nil
}
func (f rtUpdates) AcceptSeries(_ context.Context, s string) error {
	f.rec("Updates.AcceptSeries", s)
	return nil
}

type rtSettings rtNode

func (f rtSettings) Timezones(context.Context) (string, []string, error) {
	f.rec("Settings.Timezones")
	return "Etc/UTC", rtZones, nil
}
func (f rtSettings) SetTimezone(_ context.Context, name string) (bool, error) {
	f.rec("Settings.SetTimezone", name)
	return true, nil
}
func (f rtSettings) SSHAccess(_ context.Context, fresh bool) (SSHAccess, error) {
	f.rec("Settings.SSHAccess", fresh)
	if fresh {
		return rtSSHFresh, nil
	}
	return rtSSH, nil
}
func (f rtSettings) SetSSH(_ context.Context, c SSHChange) error {
	f.rec("Settings.SetSSH", c)
	return nil
}
func (f rtSettings) SetRestartPolicy(_ context.Context, mode string, hour int) error {
	f.rec("Settings.SetRestartPolicy", mode, hour)
	return nil
}
func (f rtSettings) RestartPolicy(context.Context) (string, int, error) {
	f.rec("Settings.RestartPolicy")
	return "window", 4, nil
}
func (f rtSettings) SetBackupDestination(_ context.Context, d BackupDestination) error {
	f.rec("Settings.SetBackupDestination", d)
	return nil
}
func (f rtSettings) SetBackupCopies(_ context.Context, copies []BackupCopy) error {
	f.rec("Settings.SetBackupCopies", copies)
	return nil
}
func (f rtSettings) PromoteBackupCopy(_ context.Context, name string, old BackupCopy) error {
	f.rec("Settings.PromoteBackupCopy", name, old)
	return nil
}

type rtMedia rtNode

func (f rtMedia) Drives(context.Context) ([]agent.Drive, error) {
	f.rec("Media.Drives")
	return rtDrives, nil
}
func (f rtMedia) LibraryFolders(_ context.Context, l agent.Library) (agent.FolderList, error) {
	f.rec("Media.LibraryFolders", l)
	return rtFolders, nil
}
func (f rtMedia) Libraries(context.Context) ([]agent.LibraryStatus, error) {
	f.rec("Media.Libraries")
	return rtLibs, nil
}
func (f rtMedia) CheckShare(_ context.Context, s agent.Share) (agent.FolderList, error) {
	f.rec("Media.CheckShare", s)
	return rtFolders, nil
}
func (f rtMedia) SaveShareLogin(_ context.Context, name string, s agent.Share) error {
	f.rec("Media.SaveShareLogin", name, s)
	return nil
}

type rtImports rtNode

func (f rtImports) Import(_ context.Context, s string) (ImportView, error) {
	f.rec("Imports.Import", s)
	return rtImport, nil
}
func (f rtImports) SetImportSource(_ context.Context, s string, src ImportSource) error {
	f.rec("Imports.SetImportSource", s, src)
	return nil
}
func (f rtImports) SetImportSetup(_ context.Context, s string, setup *importer.Setup) error {
	f.rec("Imports.SetImportSetup", s, setup)
	return nil
}
func (f rtImports) SetImportPrefixes(_ context.Context, s string, p []importer.Prefix) error {
	f.rec("Imports.SetImportPrefixes", s, p)
	return nil
}
func (f rtImports) SetImportChoices(_ context.Context, s string, ch importer.Choices) error {
	f.rec("Imports.SetImportChoices", s, ch)
	return nil
}
func (f rtImports) BrowseImport(_ context.Context, s, rel string) (ImportListing, error) {
	f.rec("Imports.BrowseImport", s, rel)
	return rtListing, nil
}
func (f rtImports) CheckImport(_ context.Context, s string) error {
	f.rec("Imports.CheckImport", s)
	return nil
}
func (f rtImports) CopyImport(_ context.Context, s string) error {
	f.rec("Imports.CopyImport", s)
	return nil
}
func (f rtImports) StopImportCopy(_ context.Context, s string) error {
	f.rec("Imports.StopImportCopy", s)
	return nil
}
func (f rtImports) SwitchImport(_ context.Context, s, step string) error {
	f.rec("Imports.SwitchImport", s, step)
	return nil
}
func (f rtImports) CancelImport(_ context.Context, s string) error {
	f.rec("Imports.CancelImport", s)
	return nil
}

var (
	_ Node      = rtNode{}
	_ Mover     = rtNode{}
	_ Updater   = rtNode{}
	_ Disks     = rtNode{}
	_ Memory    = rtNode{}
	_ LeftData  = rtNode{}
	_ History   = rtNode{}
	_ GuestHost = rtNode{}
	_ Linked    = rtNode{}
	_ Processor = rtNode{}
)

// rtServer starts a Server for the recording node and returns a Remote
// that calls it as the controller.
func rtServer(t *testing.T) (*Remote, *rtRec, *Server) {
	t.Helper()
	ctrl, nodeID, lab := servers(t)
	rec := &rtRec{}
	api := &Server{Node: rtNode{rec}, ControllerID: "n-0000000001",
		Leave:  func() { rec.rec("Leave") },
		Rename: func(name string) error { rec.rec("Rename", name); return nil }}
	srv := httptest.NewUnstartedServer(api.Handler())
	srv.TLS, _ = nodeID.ServerTLS()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	r, err := NewRemote(ctrl, nodeid.Identity{ID: "n-0000000002", Name: "vps"}, netip.MustParseAddr("100.96.0.2"), lab,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.URL = srv.URL
	return r, rec, api
}

func TestRoundTrip(t *testing.T) {
	r, rec, _ := rtServer(t)
	ctx := context.Background()

	// The capabilities follow the server's status, read once here.
	backups, restores, updates, settings, media, imports := r.Backups(), r.Restores(), r.Updates(), r.Settings(), r.Media(), r.Imports()
	if backups == nil || restores == nil || updates == nil || settings == nil || media == nil || imports == nil {
		t.Fatal("a server with every capability should offer them all")
	}
	// fresh makes the next status read ask the server.
	fresh := func() {
		r.mu.Lock()
		r.statusAt = time.Time{}
		r.mu.Unlock()
	}
	// oldServer makes the server seem to be from before the kept-copies
	// routes, until the function it returns is called.
	oldServer := func() func() {
		r.mu.Lock()
		r.status.Kept = false
		r.mu.Unlock()
		return func() {
			r.mu.Lock()
			r.status.Kept = true
			r.mu.Unlock()
		}
	}
	// The status read: its apply status, processor and links.
	status := []rtCall{call("Status"), call("Arch"), call("LinkStatus")}
	// The settings read: time zone, SSH and restart policy at once.
	settingsRead := []rtCall{call("Settings.Timezones"), call("Settings.SSHAccess", false), call("Settings.RestartPolicy")}
	updatesRead := []rtCall{call("Updates.Last"), call("Updates.Pending"), call("Updates.RestartStatus")}
	inputs := map[string]string{"username": "mum"}
	change := SSHChange{Choice: "lan", Login: "keys", Confirmed: true}

	tests := []struct {
		// name is the Remote method ("Restores.Kept" for a capability's),
		// then anything that tells two rows for one method apart.
		name string
		do   func() (any, error)
		want []rtCall // what the server's node was asked
		out  any      // what came back (nil: nothing)
	}{
		{"Status", func() (any, error) { fresh(); return r.Status(), nil }, status, rtApply},
		{"Reachable", func() (any, error) { fresh(); return r.Reachable(), nil }, status, true},
		{"Arch", func() (any, error) { fresh(); return r.Arch(), nil }, status, "arm64"},
		{"LinkStatus", func() (any, error) {
			fresh()
			a, l := r.LinkStatus(ctx)
			return []any{a, l}, nil
		}, status, []any{rtAddrs, rtLinks}},
		{"Apply", func() (any, error) {
			r.Apply(Assignment{Lab: LabContext{Domain: "lab.example.com"}, Reason: "Testing"})
			testwait.For(t, 5*time.Second, func() bool {
				r.mu.Lock()
				defer r.mu.Unlock()
				return !r.pushing
			})
			return nil, nil
		}, []rtCall{call("Apply", "lab.example.com", "Testing")}, nil},
		{"Leave", func() (any, error) { return nil, r.Leave(ctx) }, []rtCall{call("Leave")}, nil},
		{"Rename", func() (any, error) { return nil, r.Rename(ctx, "attic") }, []rtCall{call("Rename", "attic")}, nil},
		{"ServiceStates", func() (any, error) { return r.ServiceStates(ctx) }, []rtCall{call("ServiceStates")}, rtStates},
		{"ServiceDetails", func() (any, error) { return r.ServiceDetails(ctx) }, []rtCall{call("ServiceDetails")}, rtDetails},
		{"BuiltIns", func() (any, error) { return r.BuiltIns(ctx) }, []rtCall{call("BuiltIns")}, rtBuiltIns},
		{"LatestActivity", func() (any, error) {
			snap, ok := r.LatestActivity("backup/x")
			return []any{snap, ok}, nil
		}, []rtCall{call("LatestActivity", "backup/x")}, []any{rtActivity, true}},
		{"InstallStatus", func() (any, error) { return r.InstallStatus(ctx, "im mich"), nil },
			[]rtCall{call("InstallStatus", "im mich")}, rtHook},
		{"RetryInstall", func() (any, error) { return nil, r.RetryInstall(ctx, "immich") },
			[]rtCall{call("RetryInstall", "immich")}, nil},
		{"RunAction", func() (any, error) { return r.RunAction(ctx, "immich", "reset password", inputs) },
			[]rtCall{call("RunAction", "immich", "reset password", inputs)}, "done"},
		{"RevealSecret", func() (any, error) { return r.RevealSecret(ctx, "immich", "admin/token") },
			[]rtCall{call("RevealSecret", "immich", "admin/token")}, "s3cret"},

		{"Backups.Status", func() (any, error) { return backups.Status(ctx) }, []rtCall{call("Backups.Status")}, rtBackupStatus},
		{"Backups.RunNow", func() (any, error) { return nil, backups.RunNow(ctx) }, []rtCall{call("Backups.RunNow")}, nil},

		{"Restores.Snapshots", func() (any, error) { return restores.Snapshots(ctx, "immich") },
			[]rtCall{call("Restores.Snapshots", "immich")}, rtSnapshots},
		{"Restores.Restore", func() (any, error) { return nil, restores.Restore(ctx, "immich", "abc123") },
			[]rtCall{call("Restores.Restore", "immich", "abc123")}, nil},
		{"Restores.Pending", func() (any, error) { return restores.Pending(ctx) }, []rtCall{call("Restores.Pending")}, true},
		{"Restores.Outcome", func() (any, error) { return restores.Outcome(ctx, "immich") },
			[]rtCall{call("Restores.Outcome", "immich")}, rtOutcome},
		{"Restores.PutBack", func() (any, error) { return nil, restores.PutBack(ctx, "immich", "/srv/k") },
			[]rtCall{call("Restores.PutBack", "immich", "/srv/k")}, nil},

		{"Updates.Last", func() (any, error) { return updates.Last(ctx) }, updatesRead, rtResult},
		{"Updates.Pending", func() (any, error) { return updates.Pending(ctx) }, updatesRead, true},
		{"Updates.RestartStatus", func() (any, error) { return updates.RestartStatus(ctx) }, updatesRead, rtRestart},
		{"Updates.Submit", func() (any, error) { return nil, updates.Submit(ctx) }, []rtCall{call("Updates.Submit")}, nil},
		{"Updates.RestartNow", func() (any, error) { return nil, updates.RestartNow(ctx) }, []rtCall{call("Updates.RestartNow")}, nil},
		{"Updates.HostChanges", func() (any, error) { return updates.HostChanges(ctx) }, []rtCall{call("Updates.HostChanges")}, rtHost},
		{"Updates.AcknowledgeHostChanges", func() (any, error) { return nil, updates.AcknowledgeHostChanges(ctx) },
			[]rtCall{call("Updates.AcknowledgeHostChanges")}, nil},
		{"Updates.Series", func() (any, error) { return updates.Series(ctx) }, []rtCall{call("Updates.Series")}, rtSeries},
		{"Updates.AcceptSeries", func() (any, error) { return nil, updates.AcceptSeries(ctx, "0.5") },
			[]rtCall{call("Updates.AcceptSeries", "0.5")}, nil},

		{"Settings.Timezones", func() (any, error) {
			zone, zones, err := settings.Timezones(ctx)
			return []any{zone, zones}, err
		}, settingsRead, []any{"Etc/UTC", rtZones}},
		{"Settings.SSHAccess", func() (any, error) { return settings.SSHAccess(ctx, false) }, settingsRead, rtSSH},
		{"Settings.SSHAccess fresh", func() (any, error) { return settings.SSHAccess(ctx, true) },
			[]rtCall{call("Settings.SSHAccess", true)}, rtSSHFresh},
		{"Settings.RestartPolicy", func() (any, error) {
			mode, hour, err := settings.RestartPolicy(ctx)
			return []any{mode, hour}, err
		}, settingsRead, []any{"window", 4}},
		{"Settings.SetTimezone", func() (any, error) { return settings.SetTimezone(ctx, "Australia/Sydney") },
			[]rtCall{call("Settings.SetTimezone", "Australia/Sydney")}, true},
		{"Settings.SetSSH", func() (any, error) { return nil, settings.SetSSH(ctx, change) },
			[]rtCall{call("Settings.SetSSH", change)}, nil},
		{"Settings.PromoteBackupCopy", func() (any, error) { return nil, settings.PromoteBackupCopy(ctx, "backup-3", rtCopy) },
			[]rtCall{call("Settings.PromoteBackupCopy", "backup-3", rtCopy)}, nil},
		{"Settings.SetBackupCopies", func() (any, error) { return nil, settings.SetBackupCopies(ctx, []BackupCopy{rtCopy}) },
			[]rtCall{call("Settings.SetBackupCopies", []BackupCopy{rtCopy})}, nil},
		{"Settings.SetBackupDestination", func() (any, error) { return nil, settings.SetBackupDestination(ctx, rtDest) },
			[]rtCall{call("Settings.SetBackupDestination", rtDest)}, nil},
		{"Settings.SetRestartPolicy", func() (any, error) { return nil, settings.SetRestartPolicy(ctx, "window", 5) },
			[]rtCall{call("Settings.SetRestartPolicy", "window", 5)}, nil},

		{"ExportData", func() (any, error) {
			var buf bytes.Buffer
			err := r.ExportData(ctx, "immich", 1, rtSince, &buf)
			return buf.String(), err
		}, []rtCall{call("ExportData", "immich", 1, rtSince)}, rtTar},
		{"ExportData all", func() (any, error) {
			var buf bytes.Buffer
			err := r.ExportData(ctx, "immich", 0, time.Time{}, &buf)
			return buf.String(), err
		}, []rtCall{call("ExportData", "immich", 0, time.Time{})}, rtTar},
		{"ImportData", func() (any, error) { return nil, r.ImportData(ctx, "immich", 2, strings.NewReader(rtTar), 3) },
			[]rtCall{call("ImportData", "immich", 2, rtTar, 3)}, nil},
		{"ListData", func() (any, error) { return r.ListData(ctx, "immich", 1) },
			[]rtCall{call("ListData", "immich", 1)}, []string{"a", "b/c"}},
		{"PruneData", func() (any, error) { return nil, r.PruneData(ctx, "immich", 1, []string{"a"}) },
			[]rtCall{call("PruneData", "immich", 1, []string{"a"})}, nil},
		{"CommitData", func() (any, error) { return r.CommitData(ctx, "immich", kept.BeforeRestore) },
			[]rtCall{call("CommitData", "immich", kept.BeforeRestore)}, []string{"/srv/immich.old"}},
		{"CommitData from an old controller", func() (any, error) {
			var out []string
			err := r.call(ctx, http.MethodPost, servicePath("immich", "/data/commit"), struct{}{}, &out)
			return out, err
		}, []rtCall{call("CommitData", "immich", kept.Why(""))}, []string{"/srv/immich.old"}},
		{"SetAsideData", func() (any, error) { return r.SetAsideData(ctx, "immich", FailedMove) },
			[]rtCall{call("SetAsideData", "immich", FailedMove)}, []string{"/srv/immich.moved"}},
		{"DataSize", func() (any, error) { return r.DataSize(ctx, "immich") }, []rtCall{call("DataSize", "immich")}, int64(4096)},
		{"StopService", func() (any, error) { return nil, r.StopService(ctx, "immich") }, []rtCall{call("StopService", "immich")}, nil},
		{"StartService", func() (any, error) { return nil, r.StartService(ctx, "immich") }, []rtCall{call("StartService", "immich")}, nil},
		{"ExportSecrets", func() (any, error) { return r.ExportSecrets(ctx, "immich") },
			[]rtCall{call("ExportSecrets", "immich")}, rtSecrets},
		{"ImportSecrets", func() (any, error) { return nil, r.ImportSecrets(ctx, rtSecrets) },
			[]rtCall{call("ImportSecrets", rtSecrets)}, nil},
		{"LoadDumps", func() (any, error) { return nil, r.LoadDumps(ctx, "immich") }, []rtCall{call("LoadDumps", "immich")}, nil},
		{"KeptCopies", func() (any, error) { return r.KeptCopies(ctx) }, []rtCall{call("KeptCopies")}, rtKeptCopies},
		{"PutBackKept", func() (any, error) { return nil, r.PutBackKept(ctx, "/srv/a.moved") },
			[]rtCall{call("PutBackKept", "/srv/a.moved")}, nil},
		{"RemoveKept", func() (any, error) { return nil, r.RemoveKept(ctx, "/srv/a.moved") },
			[]rtCall{call("RemoveKept", "/srv/a.moved")}, nil},
		// A server from before 2026-10-23 (keptwire.go): its old routes,
		// whole folders only, without sizes.
		{"KeptCopies of an old server", func() (any, error) { defer oldServer()(); return r.KeptCopies(ctx) },
			[]rtCall{call("KeptCopies")}, []kept.Copy{{Service: "immich", Why: kept.MovedAway, Note: "to-nuc", Time: rtKeptAt,
				Whole: true, Size: -1, Paths: rtKeptCopies[0].Paths}}},
		{"PutBackKept on an old server", func() (any, error) { defer oldServer()(); return nil, r.PutBackKept(ctx, "/srv/a.moved") },
			[]rtCall{call("PutBackKept", "/srv/a.moved")}, nil},
		{"RemoveKept on an old server", func() (any, error) {
			defer oldServer()()
			return nil, r.RemoveKept(ctx, rtKeptCopies[0].Paths[1])
		}, []rtCall{call("KeptCopies"), call("RemoveKept", rtKeptCopies[0].Paths[0]), call("RemoveKept", rtKeptCopies[0].Paths[1])}, nil},
		// A controller from before then: the old routes.
		{"KeptCopies for an old controller's restore page", func() (any, error) {
			var out []oldRestoreCopy
			err := r.call(ctx, http.MethodGet, restorePath("immich", "/kept"), nil, &out)
			return out, err
		}, []rtCall{call("KeptCopies")}, []oldRestoreCopy{{Path: rtKeptCopies[1].Paths[0], Of: "/srv/services/immich/library", Time: rtKeptAt}}},
		{"RemoveKept for an old controller's restore page", func() (any, error) {
			return nil, r.call(ctx, http.MethodPost, restorePath("immich", "/kept/remove"), pathRequest{Path: "/srv/k"}, nil)
		}, []rtCall{call("RemoveKept", "/srv/k")}, nil},

		{"StartUpdate", func() (any, error) {
			return r.StartUpdate(ctx, UpdateRequest{Title: "Updating Immich", Images: []string{"img:2"}, Backup: true})
		}, []rtCall{call("StartUpdate", UpdateRequest{Title: "Updating Immich", Images: []string{"img:2"}, Backup: true})}, "u-1"},
		{"UpdateStatus", func() (any, error) { return r.UpdateStatus(ctx, "u-1") }, []rtCall{call("UpdateStatus", "u-1")}, rtUpdate},
		{"FinishUpdate", func() (any, error) { return nil, r.FinishUpdate(ctx, "u-1") }, []rtCall{call("FinishUpdate", "u-1")}, nil},
		{"CheckHealth", func() (any, error) { return r.CheckHealth(ctx, "immich") }, []rtCall{call("CheckHealth", "immich")}, rtHealth},

		{"DiskSpaces", func() (any, error) { return r.DiskSpaces(ctx) }, []rtCall{call("DiskSpaces")}, rtDisks},
		{"MemoryInfo", func() (any, error) { return r.MemoryInfo(ctx) }, []rtCall{call("MemoryInfo")}, rtMemory},
		{"LeftDataDirs", func() (any, error) { return r.LeftDataDirs(ctx, "immich") }, []rtCall{call("LeftDataDirs", "immich")}, []string{"/srv/old"}},
		{"HistoryPage", func() (any, error) { return r.HistoryPage(ctx, rtSince, "backup", 50) },
			[]rtCall{call("HistoryPage", rtSince, "backup", 50)}, rtEvents},
		{"HistoryPage all", func() (any, error) { return r.HistoryPage(ctx, time.Time{}, "", 500) },
			[]rtCall{call("HistoryPage", time.Time{}, "", 200)}, rtEvents},
		{"HistoryEvent", func() (any, error) { return r.HistoryEvent(ctx, 7) }, []rtCall{call("HistoryEvent", int64(7))}, rtEvents[0]},
		{"CullHistory", func() (any, error) { return r.CullHistory(ctx, rtSince) }, []rtCall{call("CullHistory", rtSince)}, int64(12)},

		{"ApplyGuestLink", func() (any, error) { return r.ApplyGuestLink(ctx, rtWG) }, []rtCall{call("ApplyGuestLink", rtWG)}, "guest-pub"},
		{"GuestLinkStatus", func() (any, error) { return r.GuestLinkStatus(ctx) }, []rtCall{call("GuestLinkStatus")}, rtWGStatus},
		{"RemoveGuestLink", func() (any, error) { return nil, r.RemoveGuestLink(ctx) }, []rtCall{call("RemoveGuestLink")}, nil},
		{"GuestUsage", func() (any, error) { return r.GuestUsage(ctx, "g1") }, []rtCall{call("GuestUsage", "g1")}, int64(777)},
		{"GuestDirs", func() (any, error) { return r.GuestDirs(ctx) }, []rtCall{call("GuestDirs")}, []string{"g1"}},
		{"RemoveGuestDir", func() (any, error) { return nil, r.RemoveGuestDir(ctx, "g1") }, []rtCall{call("RemoveGuestDir", "g1")}, nil},
		{"GuestSpace", func() (any, error) {
			free, total, err := r.GuestSpace(ctx)
			return []any{free, total}, err
		}, []rtCall{call("GuestSpace")}, []any{int64(300), int64(1000)}},

		{"Media.Drives", func() (any, error) { return media.Drives(ctx) }, []rtCall{call("Media.Drives")}, rtDrives},
		{"Media.LibraryFolders", func() (any, error) { return media.LibraryFolders(ctx, rtLibrary) },
			[]rtCall{call("Media.LibraryFolders", rtLibrary)}, rtFolders},
		{"Media.Libraries", func() (any, error) { return media.Libraries(ctx) }, []rtCall{call("Media.Libraries")}, rtLibs},
		{"Media.CheckShare", func() (any, error) { return media.CheckShare(ctx, rtShare) },
			[]rtCall{call("Media.CheckShare", rtShare)}, rtFolders},
		{"Media.SaveShareLogin", func() (any, error) { return nil, media.SaveShareLogin(ctx, "nas media", rtShare) },
			[]rtCall{call("Media.SaveShareLogin", "nas media", rtShare)}, nil},

		{"Imports.Import", func() (any, error) { return imports.Import(ctx, "immich") }, []rtCall{call("Imports.Import", "immich")}, rtImport},
		{"Imports.SetImportSource", func() (any, error) { return nil, imports.SetImportSource(ctx, "immich", rtSource) },
			[]rtCall{call("Imports.SetImportSource", "immich", rtSource)}, nil},
		{"Imports.SetImportSetup", func() (any, error) { return nil, imports.SetImportSetup(ctx, "immich", rtSetup) },
			[]rtCall{call("Imports.SetImportSetup", "immich", rtSetup)}, nil},
		{"Imports.SetImportSetup none", func() (any, error) { return nil, imports.SetImportSetup(ctx, "immich", nil) },
			[]rtCall{call("Imports.SetImportSetup", "immich", (*importer.Setup)(nil))}, nil},
		{"Imports.SetImportPrefixes", func() (any, error) { return nil, imports.SetImportPrefixes(ctx, "immich", rtPrefixes) },
			[]rtCall{call("Imports.SetImportPrefixes", "immich", rtPrefixes)}, nil},
		{"Imports.SetImportChoices", func() (any, error) { return nil, imports.SetImportChoices(ctx, "immich", rtChoices) },
			[]rtCall{call("Imports.SetImportChoices", "immich", rtChoices)}, nil},
		{"Imports.BrowseImport", func() (any, error) { return imports.BrowseImport(ctx, "immich", "photos/2024 & more") },
			[]rtCall{call("Imports.BrowseImport", "immich", "photos/2024 & more")}, rtListing},
		{"Imports.CheckImport", func() (any, error) { return nil, imports.CheckImport(ctx, "immich") },
			[]rtCall{call("Imports.CheckImport", "immich")}, nil},
		{"Imports.CopyImport", func() (any, error) { return nil, imports.CopyImport(ctx, "immich") },
			[]rtCall{call("Imports.CopyImport", "immich")}, nil},
		{"Imports.StopImportCopy", func() (any, error) { return nil, imports.StopImportCopy(ctx, "immich") },
			[]rtCall{call("Imports.StopImportCopy", "immich")}, nil},
		{"Imports.SwitchImport", func() (any, error) { return nil, imports.SwitchImport(ctx, "immich", SwitchStop) },
			[]rtCall{call("Imports.SwitchImport", "immich", SwitchStop)}, nil},
		{"Imports.CancelImport", func() (any, error) { return nil, imports.CancelImport(ctx, "immich") },
			[]rtCall{call("Imports.CancelImport", "immich")}, nil},
	}

	covered := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, _, _ := strings.Cut(tt.name, " ")
			covered[name] = true
			// Each row asks afresh: no shared reads, and a status read only
			// where the row asks for one.
			r.mu.Lock()
			r.reads = nil
			r.statusAt = time.Now()
			r.mu.Unlock()
			rec.take()
			out, err := tt.do()
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := rec.take(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("the server's node was asked\n %#v\nwant\n %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.out) {
				t.Errorf("answer\n %#v\nwant\n %#v", out, tt.out)
			}
		})
	}

	// Every method that goes over the wire has a row.
	local := map[string]bool{"Identity": true, "Address": true, "Close": true, "Pending": true, "DataDirs": true,
		"Backups": true, "Restores": true, "Updates": true, "Settings": true, "Media": true, "Imports": true,
		"Mover": true, "Updater": true, "Disks": true, "Memory": true, "LeftData": true, "History": true,
		"Linked": true, "GuestHost": true, "Processor": true, "Photos": true}
	for prefix, v := range map[string]any{"": r, "Backups.": remoteBackups{}, "Restores.": remoteRestores{},
		"Updates.": remoteUpdates{}, "Settings.": remoteSettings{}, "Media.": remoteMedia{}, "Imports.": remoteImports{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumMethod() {
			name := typ.Method(i).Name
			if prefix == "" && local[name] {
				continue
			}
			if !covered[prefix+name] {
				t.Errorf("no round trip for %s%s", prefix, name)
			}
		}
	}
}

// bareNode is a node with no capabilities; fakes embed it for those they
// lack (nodetest.Base, which other packages use, imports node).
type bareNode struct{ Node }

func (bareNode) Backups() Backups     { return nil }
func (bareNode) Restores() Restores   { return nil }
func (bareNode) Updates() Updates     { return nil }
func (bareNode) Settings() Settings   { return nil }
func (bareNode) Media() Media         { return nil }
func (bareNode) Imports() Imports     { return nil }
func (bareNode) Mover() Mover         { return nil }
func (bareNode) Updater() Updater     { return nil }
func (bareNode) Disks() Disks         { return nil }
func (bareNode) Memory() Memory       { return nil }
func (bareNode) LeftData() LeftData   { return nil }
func (bareNode) History() History     { return nil }
func (bareNode) Linked() Linked       { return nil }
func (bareNode) GuestHost() GuestHost { return nil }
func (bareNode) Processor() Processor { return nil }
func (bareNode) Photos() Photos       { return nil }

// A server without a capability answers "not found" to each of its
// routes, so the controller knows it isn't there, rather than a server
// error (updates to services gave a 500 before the quality review of
// 2026-10-21, S9).
func TestRoundTripWithout(t *testing.T) {
	ctrl, nodeID, lab := servers(t)
	api := &Server{Node: bareNode{rtNode{&rtRec{}}}, ControllerID: "n-0000000001"}
	srv := httptest.NewUnstartedServer(api.Handler())
	srv.TLS, _ = nodeID.ServerTLS()
	srv.StartTLS()
	defer srv.Close()
	r, err := NewRemote(ctrl, nodeid.Identity{ID: "n-0000000002", Name: "vps"}, netip.MustParseAddr("100.96.0.2"), lab,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.URL = srv.URL
	ctx := context.Background()
	if r.Backups() != nil || r.Restores() != nil || r.Updates() != nil || r.Settings() != nil || r.Imports() != nil {
		t.Error("a server without capabilities shouldn't offer them")
	}
	// The capabilities as the controller would call them, had the
	// server's status said otherwise.
	backups, restores, updates, settings := remoteBackups{r}, remoteRestores{r}, remoteUpdates{r}, remoteSettings{r}
	media, imports := remoteMedia{r}, remoteImports{r}
	errs := map[string]func() error{
		"Leave":  func() error { return r.Leave(ctx) },
		"Rename": func() error { return r.Rename(ctx, "attic") },

		"Backups.Status":                 func() error { _, err := backups.Status(ctx); return err },
		"Backups.RunNow":                 func() error { return backups.RunNow(ctx) },
		"Restores.Snapshots":             func() error { _, err := restores.Snapshots(ctx, "immich"); return err },
		"Restores.Restore":               func() error { return restores.Restore(ctx, "immich", "abc") },
		"Restores.Pending":               func() error { _, err := restores.Pending(ctx); return err },
		"Restores.Outcome":               func() error { _, err := restores.Outcome(ctx, "immich"); return err },
		"Restores.PutBack":               func() error { return restores.PutBack(ctx, "immich", "/k") },
		"Updates.Last":                   func() error { _, err := updates.Last(ctx); return err },
		"Updates.Submit":                 func() error { return updates.Submit(ctx) },
		"Updates.RestartNow":             func() error { return updates.RestartNow(ctx) },
		"Updates.HostChanges":            func() error { _, err := updates.HostChanges(ctx); return err },
		"Updates.AcknowledgeHostChanges": func() error { return updates.AcknowledgeHostChanges(ctx) },
		"Updates.Series":                 func() error { _, err := updates.Series(ctx); return err },
		"Updates.AcceptSeries":           func() error { return updates.AcceptSeries(ctx, "0.5") },
		"Settings.Timezones":             func() error { _, _, err := settings.Timezones(ctx); return err },
		"Settings.SetTimezone":           func() error { _, err := settings.SetTimezone(ctx, "Etc/UTC"); return err },
		"Settings.SSHAccess":             func() error { _, err := settings.SSHAccess(ctx, true); return err },
		"Settings.SetSSH":                func() error { return settings.SetSSH(ctx, SSHChange{}) },
		"Settings.PromoteBackupCopy":     func() error { return settings.PromoteBackupCopy(ctx, "b", BackupCopy{}) },
		"Settings.SetBackupCopies":       func() error { return settings.SetBackupCopies(ctx, nil) },
		"Settings.SetBackupDestination":  func() error { return settings.SetBackupDestination(ctx, BackupDestination{}) },
		"Settings.SetRestartPolicy":      func() error { return settings.SetRestartPolicy(ctx, "window", 4) },

		"ExportData":    func() error { return r.ExportData(ctx, "immich", 0, time.Time{}, io.Discard) },
		"ImportData":    func() error { return r.ImportData(ctx, "immich", 0, strings.NewReader(""), 0) },
		"ListData":      func() error { _, err := r.ListData(ctx, "immich", 0); return err },
		"PruneData":     func() error { return r.PruneData(ctx, "immich", 0, nil) },
		"CommitData":    func() error { _, err := r.CommitData(ctx, "immich", kept.BeforeMove); return err },
		"SetAsideData":  func() error { _, err := r.SetAsideData(ctx, "immich", "x"); return err },
		"DataSize":      func() error { _, err := r.DataSize(ctx, "immich"); return err },
		"LoadDumps":     func() error { return r.LoadDumps(ctx, "immich") },
		"StopService":   func() error { return r.StopService(ctx, "immich") },
		"StartService":  func() error { return r.StartService(ctx, "immich") },
		"ExportSecrets": func() error { _, err := r.ExportSecrets(ctx, "immich"); return err },
		"ImportSecrets": func() error { return r.ImportSecrets(ctx, nil) },
		"KeptCopies":    func() error { _, err := r.KeptCopies(ctx); return err },
		"PutBackKept":   func() error { return r.PutBackKept(ctx, "/k") },
		"RemoveKept":    func() error { return r.RemoveKept(ctx, "/k") },
		"StartUpdate":   func() error { _, err := r.StartUpdate(ctx, UpdateRequest{}); return err },
		"UpdateStatus":  func() error { _, err := r.UpdateStatus(ctx, "u-1"); return err },
		"FinishUpdate":  func() error { return r.FinishUpdate(ctx, "u-1") },
		"CheckHealth":   func() error { _, err := r.CheckHealth(ctx, "immich"); return err },
		"DiskSpaces":    func() error { _, err := r.DiskSpaces(ctx); return err },
		"MemoryInfo":    func() error { _, err := r.MemoryInfo(ctx); return err },
		"LeftDataDirs":  func() error { _, err := r.LeftDataDirs(ctx, "immich"); return err },
		"HistoryPage":   func() error { _, err := r.HistoryPage(ctx, time.Time{}, "", 10); return err },
		"HistoryEvent":  func() error { _, err := r.HistoryEvent(ctx, 1); return err },
		"CullHistory":   func() error { _, err := r.CullHistory(ctx, rtSince); return err },

		"Media.Drives":           func() error { _, err := media.Drives(ctx); return err },
		"Media.Libraries":        func() error { _, err := media.Libraries(ctx); return err },
		"Imports.Import":         func() error { _, err := imports.Import(ctx, "immich"); return err },
		"Imports.CancelImport":   func() error { return imports.CancelImport(ctx, "immich") },
		"Imports.SetImportSetup": func() error { return imports.SetImportSetup(ctx, "immich", nil) },
		"Imports.SwitchImport":   func() error { return imports.SwitchImport(ctx, "immich", SwitchStop) },
	}
	for name, do := range errs {
		if err := do(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
}
