package manager

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/testwait"
)

// healthNode is the controller's own server, whose services' health the
// test decides (no containers run in tests).
type healthNode struct {
	*node.Local
	healthy func(version string) bool
	m       *Manager
	// restarts climb while it's unhealthy, as systemd restarts it.
	restarts int
}

// Updater is h itself, so updates use its CheckHealth (Local's would
// return the Local).
func (h *healthNode) Updater() node.Updater { return h }

func (h *healthNode) CheckHealth(_ context.Context, service string) (node.Health, error) {
	in, err := instance.Load(state.InstanceFile(h.m.Lab, service))
	if err != nil {
		return node.Health{}, err
	}
	if !h.healthy(in.Version) {
		// systemd restarts it twice, then gives up (failed).
		if h.restarts < 2 {
			h.restarts++
		}
		return node.Health{Problem: service + "-app has stopped", Restarts: h.restarts, Failed: h.restarts == 2}, nil
	}
	return node.Health{Healthy: true, Restarts: h.restarts}, nil
}

// chainBackups is a server's backups that run jobs after them on its
// coordinator, as backup.Service does.
type chainBackups struct {
	coord *maintenance.Coordinator
	mu    sync.Mutex
	last  *store.BackupRun
	runs  int
}

func (b *chainBackups) Status(context.Context) (backup.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backup.Status{Configured: true, Last: b.last, LastOK: b.last}, nil
}
func (b *chainBackups) RunNow(context.Context) error { return b.Submit() }
func (b *chainBackups) Submit(chain ...maintenance.Job) error {
	b.coord.Submit(append([]maintenance.Job{{Name: "backup", Run: func(context.Context, *maintenance.Handle) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.runs++
		now := time.Now()
		b.last = &store.BackupRun{Started: now, Finished: now, OK: true}
		return nil
	}}}, chain...)...)
	return nil
}

// offerVersion puts a newer version of a package in the catalog, as the
// fetcher would (only the offered version), with upgrade notes.
func offerVersion(t *testing.T, m *Manager, name, from, to string, notes ...pkgspec.UpgradeNote) {
	t.Helper()
	data, err := os.ReadFile(m.Lab.Paths.CatalogPackageFile(name, from))
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(m.Lab.Paths.CatalogPackageDir(name), "*.yaml"))
		for _, f := range matches {
			if !strings.HasSuffix(f, "registry.yaml") {
				data, err = os.ReadFile(f)
				os.Remove(f)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	} else {
		os.Remove(m.Lab.Paths.CatalogPackageFile(name, from))
	}
	// Notes come only from this call (an earlier one's go).
	s, _, _ := strings.Cut(string(data), "\nupgrade_notes:\n")
	s = strings.Replace(s, "version: "+from, "version: "+to, 1)
	if !strings.Contains(s, "version: "+to) {
		t.Fatalf("no version line in %s %s", name, from)
	}
	for _, n := range notes {
		s += "\nupgrade_notes:\n  - version: " + n.Version + "\n    note: " + n.Note + "\n"
	}
	if err := os.WriteFile(m.Lab.Paths.CatalogPackageFile(name, to), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitUpdated(t *testing.T, m *Manager, name string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for m.Updating(name) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the update didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Wait()
}

// updateSetup gives the manager a running maintenance coordinator,
// backups that run jobs after them, and services whose health is bad
// for the version *bad points at.
func updateSetup(t *testing.T, m *Manager) (*chainBackups, *string) {
	t.Helper()
	m.Settings = linkKV{}
	healthWait, healthStable, healthEvery, statusEvery = 2*time.Second, 100*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	failedFor = 100 * time.Millisecond
	t.Cleanup(func() {
		healthWait, healthStable, healthEvery, statusEvery = 5*time.Minute, time.Minute, 5*time.Second, 2*time.Second
		failedFor = 30 * time.Second
	})
	local := m.Node.(*node.Local)
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now,
		Journal: maintenance.FileJournal{Path: filepath.Join(t.TempDir(), "journal.json")},
		Ticks:   make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go coord.Run(cctx)
	backups := &chainBackups{coord: coord}
	local.Jobs.Coord, local.Jobs.Backups = coord, backups
	bad := new(string)
	m.Node = &healthNode{Local: local, m: m, healthy: func(v string) bool { return v != *bad }}
	return backups, bad
}

// withVersion is a package file with another version.
func withVersion(t *testing.T, data []byte, version string) []byte {
	t.Helper()
	re := regexp.MustCompile(`(?m)^version: \S+`)
	if !re.Match(data) {
		t.Fatal("no version line")
	}
	return re.ReplaceAll(data, []byte("version: "+version))
}

// A service from one of the Owner's own packages updates to the version
// of it the Owner added last, never to the registry's; one asking for
// more than before needs the Owner to accept it, and never updates by
// itself (SPEC.md §7.1).
func TestLocalPackageUpdate(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	updateSetup(t, m)
	data, icon := pkgtest.Read(t, "forgejo"), forgejoIcon(t)
	if _, err := m.AddLocalPackage(data, icon); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if len(m.AvailableUpdates(ctx)) != 0 {
		t.Fatal("nothing newer yet")
	}
	if _, err := m.AddLocalPackage(withVersion(t, data, "15.0.10-roostack.1"), icon); err != nil {
		t.Fatal(err)
	}
	u := m.AvailableUpdates(ctx)["forgejo"]
	if u == nil || !u.Local || u.To != "15.0.10-roostack.1" || u.Kind != pkgspec.UpdatePatch || len(u.NewRisks) != 0 ||
		u.WhatsNew != "https://codeberg.org/forgejo/forgejo/releases/tag/v15.0.10" {
		t.Fatalf("update: %+v", u)
	}
	if err := m.UpdateService(ctx, "forgejo", u.To, "alice", false); err != nil {
		t.Fatal(err)
	}
	waitUpdated(t, m, "forgejo")
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	if rec := m.LastUpdate(ctx, "forgejo"); in.Version != "15.0.10-roostack.1" || !in.Local || rec.Result != UpdateDone {
		t.Fatalf("updated: %s local=%v %+v", in.Version, in.Local, rec)
	}

	// A newer one with an unpinned image: the Owner must accept it.
	if _, err := m.AddLocalPackage(unpinned(withVersion(t, data, "15.0.11-roostack.1")), icon); err != nil {
		t.Fatal(err)
	}
	m.SetUpdatePolicy(ctx, "forgejo", "all")
	u = m.AvailableUpdates(ctx)["forgejo"]
	if u == nil || len(u.NewRisks) != 1 || !strings.Contains(u.NewRisks[0], "image: pin") || u.Auto {
		t.Fatalf("new risks: %+v", u)
	}
	wantFormError(t, m.UpdateService(ctx, "forgejo", u.To, "alice", false), "tick the box")
	if err := m.UpdateService(ctx, "forgejo", u.To, "alice", true); err != nil {
		t.Fatal(err)
	}
	waitUpdated(t, m, "forgejo")
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Version != "15.0.11-roostack.1" || !in.Local {
		t.Fatalf("accepted: %s", in.Version)
	}

	// Removed from the list: nothing newer to offer.
	if err := m.RemoveLocalPackage("forgejo"); err != nil {
		t.Fatal(err)
	}
	if len(m.AvailableUpdates(ctx)) != 0 {
		t.Error("no package, no update")
	}
}

// An update backs up, holds the server's maintenance, switches the
// version and checks it; one that doesn't work goes back, isn't offered
// automatically again, and the previous package file is still there.
func TestServiceUpdate(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	m.Settings = linkKV{}
	healthWait, healthStable, healthEvery, statusEvery = 2*time.Second, 100*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	failedFor = 100 * time.Millisecond
	t.Cleanup(func() {
		healthWait, healthStable, healthEvery, statusEvery = 5*time.Minute, time.Minute, 5*time.Second, 2*time.Second
		failedFor = 30 * time.Second
	})
	local := m.Node.(*node.Local)
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now,
		Journal: maintenance.FileJournal{Path: filepath.Join(t.TempDir(), "journal.json")},
		Ticks:   make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go coord.Run(cctx)
	backups := &chainBackups{coord: coord}
	local.Jobs.Coord, local.Jobs.Backups = coord, backups
	bad := ""
	m.Node = &healthNode{Local: local, m: m, healthy: func(v string) bool { return v != bad }}

	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	from := in.Version
	ix := withdrawIndex(t, m, nil)
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	if len(m.AvailableUpdates(ctx)) != 0 {
		t.Fatal("no newer version, no update")
	}

	// A fix release: offered, not automatic (notify, no backups set up).
	offerVersion(t, m, "vaultwarden", from, "1.34.4-roostack.1")
	u := m.AvailableUpdates(ctx)["vaultwarden"]
	if u == nil || u.To != "1.34.4-roostack.1" || u.Kind != pkgspec.UpdatePatch || u.Auto || !u.NoBackups || u.Policy != "notify" {
		t.Fatalf("update: %+v", u)
	}
	if err := m.UpdateService(ctx, "vaultwarden", "1.34.9-roostack.1", "alice", false); err == nil {
		t.Error("a version not on offer is refused")
	}
	if err := m.UpdateService(ctx, "vaultwarden", u.To, "alice", false); err != nil {
		t.Fatal(err)
	}
	waitUpdated(t, m, "vaultwarden")
	in, _ = instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	rec := m.LastUpdate(ctx, "vaultwarden")
	if in.Version != "1.34.4-roostack.1" || rec == nil || rec.Result != UpdateDone || rec.From != from || rec.By != "alice" {
		t.Fatalf("updated: %s %+v", in.Version, rec)
	}
	if !quadletExists(m, "roostack-vaultwarden-app.container") || len(m.AvailableUpdates(ctx)) != 0 {
		t.Error("it runs the new version, and nothing more is offered")
	}
	if backups.runs != 0 {
		t.Error("no backups set up: none run")
	}

	// Backups set up: a manual update backs up first. This version
	// doesn't work, so it goes back.
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "local:usb", Repository: "/mnt/usb/backups"})
	m.BackupStore = st
	ix = withdrawIndex(t, m, nil)
	offerVersion(t, m, "vaultwarden", "1.34.4-roostack.1", "1.35.0-roostack.1")
	bad = "1.35.0-roostack.1"
	if err := m.SetUpdatePolicy(ctx, "vaultwarden", "minor"); err != nil {
		t.Fatal(err)
	}
	u = m.AvailableUpdates(ctx)["vaultwarden"]
	if u == nil || u.Kind != pkgspec.UpdateMinor || !u.Auto || u.NoBackups {
		t.Fatalf("a minor update with policy minor and backups: %+v", u)
	}
	if err := m.UpdateService(ctx, "vaultwarden", u.To, "alice", false); err != nil {
		t.Fatal(err)
	}
	waitUpdated(t, m, "vaultwarden")
	in, _ = instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	rec = m.LastUpdate(ctx, "vaultwarden")
	if in.Version != "1.34.4-roostack.1" || rec.Result != UpdateRolledBack || !strings.Contains(rec.Error, "systemd gave up restarting it") ||
		!strings.Contains(rec.Error, "vaultwarden-app has stopped") ||
		rec.Backup.IsZero() || backups.runs != 1 {
		t.Fatalf("rolled back: %s %+v (backups %d)", in.Version, rec, backups.runs)
	}
	if _, err := os.Stat(m.Lab.Paths.LabPackageFile("vaultwarden", "1.34.4-roostack.1")); err != nil {
		t.Error("the version it went back to keeps its package file")
	}
	if u := m.AvailableUpdates(ctx)["vaultwarden"]; u == nil || !u.FailedBefore || u.Auto {
		t.Errorf("a version that failed isn't automatic: %+v", u)
	}

	// The nightly run: after the night's backup, a fix release the policy
	// takes is applied (no second backup); one with notes waits.
	bad = ""
	offerVersion(t, m, "vaultwarden", "1.35.0-roostack.1", "1.34.5-roostack.1")
	since := time.Now()
	backups.Submit()
	m.AutoUpdate(ctx, since)
	waitUpdated(t, m, "vaultwarden")
	in, _ = instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if rec := m.LastUpdate(ctx, "vaultwarden"); in.Version != "1.34.5-roostack.1" || !rec.Automatic || backups.runs != 2 {
		t.Fatalf("automatic: %s %+v (backups %d)", in.Version, rec, backups.runs)
	}
	offerVersion(t, m, "vaultwarden", "1.34.5-roostack.1", "1.34.6-roostack.1",
		pkgspec.UpgradeNote{Version: "1.34.6-roostack.1", Note: "Sign in again"})
	u = m.AvailableUpdates(ctx)["vaultwarden"]
	if u == nil || len(u.Notes) != 1 || u.Auto {
		t.Errorf("notes wait for a person: %+v", u)
	}

	// The registry says the version it runs is dangerous: Roostack stops
	// it, and a fix its policy takes is applied straight away (with a
	// backup), not at night. On notify it would only be offered.
	offerVersion(t, m, "vaultwarden", "1.34.6-roostack.1", "1.34.7-roostack.1")
	ix = withdrawIndex(t, m, map[string]string{"vaultwarden": registry.LevelStop})
	m.UpdateLab = func(func(*config.Lab)) error { return nil }
	m.SetUpdatePolicy(ctx, "vaultwarden", "notify")
	m.RegistryChanged()
	m.Wait()
	// RegistryChanged decides in its own goroutine, with UpdateStopped:
	// deciding again here, and waiting for that, shows what it decides
	// without waiting a while to see that nothing happens.
	m.UpdateStopped(ctx)
	if m.Updating("vaultwarden") != nil {
		t.Fatal("notify only offers it")
	}
	// Counted before the policy changes, so the update is counted once,
	// whichever goroutine starts it.
	runs := backups.runs
	m.SetUpdatePolicy(ctx, "vaultwarden", "patch")
	m.RegistryChanged()
	// Started in the background: wait for it to start (or to have
	// finished already), then for it to finish.
	testwait.For(t, 20*time.Second, func() bool {
		in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
		return m.Updating("vaultwarden") != nil || in.Version == "1.34.7-roostack.1"
	})
	waitUpdated(t, m, "vaultwarden")
	in, _ = instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if rec := m.LastUpdate(ctx, "vaultwarden"); in.Version != "1.34.7-roostack.1" || !rec.Automatic || backups.runs != runs+1 {
		t.Fatalf("a stopped service updates: %s %+v (backups %d)", in.Version, rec, backups.runs)
	}
	if w := m.Withdrawals(ctx)["vaultwarden"]; w != nil {
		t.Errorf("the new version isn't withdrawn: %+v", w)
	}
}

// A failed update stays news while its version is on offer; a newer
// offer replaces it, and with nothing on offer it goes after a week.
func TestUpdateRecordShown(t *testing.T) {
	failed := &UpdateRecord{To: "2.1.0-roostack.1", Result: UpdateRolledBack, Finished: time.Now().Add(-30 * 24 * time.Hour)}
	if !failed.Shown(&Update{To: "2.1.0-roostack.1"}) {
		t.Error("still on offer: shown")
	}
	if failed.Shown(&Update{To: "2.1.1-roostack.1"}) || failed.Shown(nil) {
		t.Error("replaced, or old with nothing on offer: not shown")
	}
	failed.Finished = time.Now()
	if !failed.Shown(nil) {
		t.Error("recent with nothing on offer: shown")
	}
}
