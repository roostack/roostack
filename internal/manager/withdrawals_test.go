package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/testwait"
)

// stopBackups is a server's backups, for the last backup before a stopped
// service's databases on the package's own images stop.
type stopBackups struct {
	mu     sync.Mutex
	lastOK *store.BackupRun
	runs   int
}

func (b *stopBackups) Status(context.Context) (backup.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backup.Status{Configured: true, LastOK: b.lastOK}, nil
}
func (b *stopBackups) RunNow(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs++
	return nil
}

// withdrawIndex is an index withdrawing the lab's copies of installed
// services' packages at the levels given.
func withdrawIndex(t *testing.T, m *Manager, levels map[string]string) *registry.Index {
	t.Helper()
	ix := &registry.Index{Type: registry.TypeIndex, Format: registry.Format, Serial: 1, Date: time.Now(),
		Packages: map[string]*registry.Package{}}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range services {
		data, err := os.ReadFile(m.Lab.Paths.LabPackageFile(s.Instance.Package, s.Instance.Version))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		v := &registry.Version{File: registry.File{SHA256: hex.EncodeToString(sum[:])}}
		if l := levels[s.Name()]; l != "" {
			v.Withdrawn = &registry.Withdrawal{Level: l, Reason: "Bad " + l, Link: "https://example.org/" + l}
		}
		ix.Packages[s.Instance.Package] = &registry.Package{Tier: registry.TierOfficial,
			Versions: map[string]*registry.Version{s.Instance.Version: v}}
	}
	return ix
}

func quadletExists(m *Manager, name string) bool {
	_, err := os.Stat(filepath.Join(m.Lab.Paths.QuadletDir(), name))
	return err == nil
}

// A stop stops the service's containers and address and keeps its data;
// a database on Roostack's image keeps running, one on the package's own
// image until a backup has finished. The Owner can start it again, made
// private; with stops off, it's only a warning.
func TestRegistryStop(t *testing.T) {
	m, _ := newManager(t)
	m.waits.lastBackupCheck = 10 * time.Millisecond
	ctx := context.Background()
	m.Settings = linkKV{}
	m.UpdateLab = func(func(*config.Lab)) error { return nil }
	backups := &stopBackups{}
	m.Node.(*node.Local).Jobs.Backups = backups

	for _, name := range []string{"immich", "vaultwarden"} {
		if err := m.Install(ctx, name, InstallRequest{Settings: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		m.Wait()
	}
	data := filepath.Join(m.Lab.Paths.ServicesDir, "immich", "data")
	os.MkdirAll(data, 0o755)
	// Immich public, to be made private when it's started anyway.
	if err := m.SetPublic(ctx, "immich", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	ix := withdrawIndex(t, m, map[string]string{"immich": registry.LevelStop, "vaultwarden": registry.LevelWarning})
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	m.RegistryChanged()
	m.Wait()

	ws := m.Withdrawals(ctx)
	if w := ws["immich"]; w == nil || !w.Stopped || w.Since.IsZero() || !w.LastBackup || !w.Public || w.Reason != "Bad stop" {
		t.Fatalf("immich: %+v", w)
	}
	if w := ws["vaultwarden"]; w == nil || w.Stopped || w.Shown != registry.LevelWarning {
		t.Errorf("vaultwarden: %+v", w)
	}
	if quadletExists(m, "roostack-immich-server.container") || !quadletExists(m, "roostack-immich-db-main.container") ||
		!quadletExists(m, "roostack-immich-db-cache.container") || !quadletExists(m, "roostack-vaultwarden-app.container") {
		t.Error("the app stops; its databases run until the last backup; a warning stops nothing")
	}
	if _, err := os.Stat(filepath.Join(m.Lab.Paths.CaddySitesDir(), "immich.caddy")); !os.IsNotExist(err) {
		t.Error("its address goes")
	}
	if _, err := os.Stat(data); err != nil {
		t.Error("its data stays")
	}
	if backups.runs != 1 {
		t.Errorf("a backup starts now: %d", backups.runs)
	}

	// Once a backup that started after the stop has finished, the database
	// on Immich's own image stops; Roostack's Redis keeps running.
	backups.mu.Lock()
	backups.lastOK = &store.BackupRun{Started: time.Now().Add(time.Second), Finished: time.Now().Add(time.Minute), OK: true}
	backups.mu.Unlock()
	testwait.For(t, 10*time.Second, func() bool {
		m.Wait()
		return !quadletExists(m, "roostack-immich-db-main.container")
	})
	if quadletExists(m, "roostack-immich-db-main.container") || !quadletExists(m, "roostack-immich-db-cache.container") {
		t.Error("after the backup, only Roostack's database runs")
	}

	// Started anyway, made private: this version runs from now on.
	if err := m.StartAnyway(ctx, "immich", true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	in, _ := instance.Load(state.InstanceFile(m.Lab, "immich"))
	if in.StartedAnyway != in.Version || in.Routes["web"].Exposure != pkgspec.ExposurePrivate {
		t.Errorf("instance: %+v", in)
	}
	if w := m.Withdrawals(ctx)["immich"]; w == nil || w.Stopped || !w.StartedAnyway || w.Shown != registry.LevelWarning {
		t.Errorf("started anyway: %+v", w)
	}
	if !quadletExists(m, "roostack-immich-server.container") || !quadletExists(m, "roostack-immich-db-main.container") {
		t.Error("started again")
	}
	if v, _ := m.Settings.Setting(ctx, stopKey+"immich"); v != "" {
		t.Errorf("the stop is forgotten: %q", v)
	}

	// With stops off, a stop is a warning and nothing is stopped.
	in.StartedAnyway = ""
	in.Save(state.InstanceFile(m.Lab, "immich"))
	if err := m.SetStops(false); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if w := m.Withdrawals(ctx)["immich"]; w == nil || w.Stopped || !w.NoStops || w.Shown != registry.LevelWarning {
		t.Errorf("stops off: %+v", w)
	}
	if !quadletExists(m, "roostack-immich-server.container") {
		t.Error("with stops off it runs")
	}

	// A package of your own with the registry's name isn't withdrawn.
	ix.Packages["vaultwarden"].Versions[ws["vaultwarden"].Version].SHA256 = "00"
	if w := m.Withdrawals(ctx)["vaultwarden"]; w != nil {
		t.Errorf("another file: %+v", w)
	}
}
