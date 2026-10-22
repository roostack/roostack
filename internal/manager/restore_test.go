package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

func TestMoments(t *testing.T) {
	at := func(h, min int, run string) backup.Snapshot {
		s := backup.Snapshot{ID: fmt.Sprintf("%d:%02d", h, min), Time: time.Date(2026, 10, 4, h, min, 0, 0, time.UTC)}
		if run != "" {
			s.Tags = []string{backup.RunTag(time.Date(2026, 10, 4, h, 0, 0, 0, time.UTC))}
		}
		return s
	}
	show := func(ms []BackupMoment) string {
		var out []string
		for _, m := range ms {
			var ids []string
			for _, p := range m.Places {
				ids = append(ids, p.Ref+"="+p.Snapshot.ID)
			}
			out = append(out, m.ServerID+"["+strings.Join(ids, " ")+"]")
		}
		return strings.Join(out, " ")
	}
	got := moments([]ServiceBackup{
		// Tagged: last night's run on nuc, main and a copy made later.
		{Ref: "copy", ServerID: "nuc", Snapshot: at(2, 40, "run")},
		{Ref: "main", ServerID: "nuc", Main: true, Snapshot: at(2, 0, "run")},
		// Untagged (older): grouped by server and time.
		{Ref: "main", ServerID: "nuc", Main: true, Snapshot: at(0, 0, "")},
		{Ref: "copy", ServerID: "nuc", Snapshot: at(0, 20, "")},
		// The same hour on another server is another moment.
		{Ref: "main", ServerID: "nas", Main: true, Snapshot: at(0, 5, "")},
	})
	if want := "nuc[main=2:00 copy=2:40] nas[main=0:05] nuc[main=0:00 copy=0:20]"; show(got) != want {
		t.Errorf("got  %s\nwant %s", show(got), want)
	}
}

// backedUpForgejo makes home's backup of Forgejo (its data and its
// secrets) with real restic, and records it as home's main backup, next
// to a copy that can't be read.
func backedUpForgejo(t *testing.T, m *Manager, bin string) *store.Store {
	t.Helper()
	ctx := context.Background()
	paths := m.Lab.Paths
	svcDir := filepath.Join(paths.ServicesDir, "forgejo")
	os.MkdirAll(filepath.Join(svcDir, "data"), 0o755)
	os.WriteFile(filepath.Join(svcDir, "data", "app.ini"), []byte("from the backup"), 0o644)
	secrets, _ := json.Marshal(map[string][]byte{"roostack-forgejo-db-main-password": []byte("the db password"),
		"roostack-forgejo-2-db-main-password": []byte("another copy's")})
	os.MkdirAll(backup.SecretsDir(paths), 0o700)
	os.WriteFile(backup.SecretsFile(paths, "forgejo"), secrets, 0o600)
	os.WriteFile(backup.KeyFile(paths), []byte("the recovery key\n"), 0o600)
	dir := t.TempDir()
	open := func(url string, _ []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: url, PasswordFile: backup.KeyFile(paths), CacheDir: filepath.Join(dir, "cache")}
	}
	repo := open(filepath.Join(dir, "repo"), nil)
	if err := repo.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Backup(ctx, backup.BackupOptions{Paths: []string{svcDir, backup.SecretsFile(paths, "forgejo")},
		Tags: []string{backup.TagRoostack, backup.ServiceTag("forgejo")}}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(svcDir) // it lives on the servers, not here
	os.RemoveAll(backup.SecretsDir(paths))

	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	m.BackupStore, m.OpenRepo = st, open
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "local:" + repo.URL,
		Repository: repo.URL, Created: time.Now()})
	// A copy that can't be read is reported, not fatal.
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "local:" + filepath.Join(dir, "nothing"),
		Repository: filepath.Join(dir, "nothing"), Created: time.Now()})
	return st
}

func resticBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	return bin
}

// home is gone: Forgejo waits to be restored (home stays in the lab, for
// its backups), then is restored onto vps, and home leaves the lab.
func TestGoneServer(t *testing.T) {
	bin := resticBin(t)
	m, home, vps := setUpMove(t)
	ctx := context.Background()
	applyWait = 5 * time.Second
	defer func() { applyWait = 20 * time.Minute }()
	st := backedUpForgejo(t, m, bin)
	defer st.Close()
	reg := m.Registry.(*fakeRegistry)
	targets, _ := st.BackupTargets(ctx)
	homeRepo := targets[0].Repository

	if _, err := m.LostServerPlan(ctx, "n-0000000002"); err == nil {
		t.Error("home is connected: it isn't gone")
	}
	home.settings = nil // can't be reached
	plan, err := m.LostServerPlan(ctx, "n-0000000002")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || plan.Services[0].Backup == nil || plan.Services[0].Size <= 0 ||
		len(plan.Targets) != 2 || plan.Targets[1].ID != "n-0000000003" {
		t.Fatalf("plan = %+v", plan)
	}

	// None restored now: it waits, runs nowhere, and home can't be removed.
	if err := m.RestoreLostServer(ctx, "n-0000000002", nil, ""); err != nil {
		t.Fatal(err)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); !in.Waiting || in.Host != "n-0000000002" {
		t.Errorf("forgejo: %+v", in)
	}
	servers, _ := m.Servers()
	if !slices.Equal(servers[1].Waiting, []string{"forgejo"}) || len(servers[1].Services) != 0 || servers[1].Reachable {
		t.Errorf("home: %+v", servers[1])
	}
	if err := m.RemoveServer(ctx, "n-0000000002"); err == nil || len(reg.removed) != 0 {
		t.Error("home holds the backups forgejo waits on: it stays")
	}

	// Restored onto vps (from the gone server's page, as the batch does).
	vps.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour)}
	if err := m.RestoreLostServer(ctx, "n-0000000002", []string{"forgejo"}, "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	if l := waitRestore(t, m); l.Err != "" {
		t.Fatalf("restore: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Waiting || in.Host != "n-0000000003" {
		t.Errorf("forgejo: %+v", in)
	}
	if vps.data[0]["data/app.ini"].Data != "from the backup" || string(vps.secrets["roostack-forgejo-db-main-password"]) != "the db password" ||
		vps.secrets["roostack-forgejo-2-db-main-password"] != nil {
		t.Errorf("vps has %v", vps.data[0])
	}
	if !slices.Equal(reg.removed, []string{"n-0000000002"}) {
		t.Errorf("home should leave once nothing waits: %v", reg.removed)
	}
	// If home comes back, its backups tell it it was removed.
	if snaps, err := m.OpenRepo(homeRepo, nil).Snapshots(ctx, backup.TagRoostack, backup.TagRemoved); err != nil || len(snaps) != 1 {
		t.Errorf("the removed mark in home's backups: %d %v", len(snaps), err)
	}
}

// Removing a service that waits deletes its backups everywhere.
func TestRemoveWaiting(t *testing.T) {
	bin := resticBin(t)
	m, home, _ := setUpMove(t)
	ctx := context.Background()
	st := backedUpForgejo(t, m, bin)
	defer st.Close()
	home.settings = nil
	if err := m.RemoveWaiting(ctx, "forgejo"); err == nil {
		t.Error("only a service that waits")
	}
	if err := m.RestoreLostServer(ctx, "n-0000000002", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveWaiting(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	var l activity.Snapshot
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if l, _ = m.Activity.Latest(activity.Restore); !l.Running() {
			break
		}
	}
	// The copy can't be read: its backups can't be checked, so it's kept.
	if !strings.Contains(l.Err, "some backups couldn't be read, so nothing was deleted") {
		t.Fatalf("with a copy that can't be read: %q", l.Err)
	}
	if targets, _ := st.BackupTargets(ctx); len(targets) == 1 {
		snaps, _ := m.OpenRepo(targets[0].Repository, nil).Snapshots(ctx, backup.TagRoostack, backup.ServiceTag("forgejo"))
		if len(snaps) != 1 {
			t.Errorf("nothing should be deleted yet: %d backups", len(snaps))
		}
	}
	copies, _ := st.BackupCopies(ctx)
	st.RemoveBackupCopy(ctx, copies[0].ServerID, copies[0].Provider)
	if err := m.RemoveWaiting(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if l, _ = m.Activity.Latest(activity.Restore); !l.Running() {
			break
		}
	}
	if l.Err != "" {
		t.Fatalf("remove: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	targets, _ := st.BackupTargets(ctx)
	if snaps, err := m.OpenRepo(targets[0].Repository, nil).Snapshots(ctx, backup.TagRoostack, backup.ServiceTag("forgejo")); err != nil || len(snaps) != 0 {
		t.Errorf("its backups are still there: %d %v", len(snaps), err)
	}
	if _, err := os.Stat(state.InstanceFile(m.Lab, "forgejo")); !os.IsNotExist(err) {
		t.Error("it should be removed")
	}
}

func waitRestore(t *testing.T, m *Manager) activity.Snapshot {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if l, ok := m.Activity.Latest(activity.Restore); ok && !l.Running() && !m.Restoring("forgejo") {
			return l
		}
	}
	t.Fatal("the restore didn't finish")
	return activity.Snapshot{}
}

// A service's backup, made by one server, is found by the controller and
// restored onto another server, with its secrets; then again while the
// server it runs on can't be reached (lost).
func TestRestoreAcrossTheLab(t *testing.T) {
	bin := resticBin(t)
	m, home, vps := setUpMove(t)
	ctx := context.Background()
	applyWait = 5 * time.Second
	defer func() { applyWait = 20 * time.Minute }()

	st := backedUpForgejo(t, m, bin)
	defer st.Close()

	backups, notes, err := m.ServiceBackups(ctx, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || len(backups[0].Places) != 1 || !backups[0].Places[0].Main || backups[0].ServerName != "home" ||
		backups[0].Places[0].Ref != "backup:n-0000000002" {
		t.Fatalf("backups = %+v", backups)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Couldn't read home's backups") {
		t.Errorf("notes = %q", notes)
	}

	// Onto vps: Forgejo stops on home, comes back on vps with its secrets
	// and databases, and home's copy is set aside.
	vps.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour)}
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	if l := waitRestore(t, m); l.Err != "" {
		t.Fatalf("restore: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if vps.data[0]["data/app.ini"].Data != "from the backup" || string(vps.secrets["roostack-forgejo-db-main-password"]) != "the db password" ||
		vps.secrets["roostack-forgejo-2-db-main-password"] != nil {
		t.Errorf("vps has %v, secrets %q", vps.data[0], vps.secrets)
	}
	if !slices.Contains(home.log, "stop") || !slices.Contains(home.log, "set aside") || !slices.Contains(vps.log, "load dumps") {
		t.Errorf("home did %v, vps %v", home.log, vps.log)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Host != "n-0000000003" {
		t.Errorf("runs on %q", in.Host)
	}

	// vps is lost: restored onto home without stopping it there.
	vps.settings, vps.log = nil, nil
	home.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour)}
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if l := waitRestore(t, m); l.Err != "" {
		t.Fatalf("restore: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if len(vps.log) != 0 || home.data[0]["data/app.ini"].Data != "from the backup" {
		t.Errorf("vps did %v; home has %v", vps.log, home.data[0])
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Host != "n-0000000002" {
		t.Errorf("runs on %q", in.Host)
	}

	// Its databases don't load: the files are back, and it isn't started
	// again on them, but held stopped in its desired state until the Owner
	// chooses (SPEC.md §6.9, "A restore that didn't finish").
	var audited []string
	m.Audit = func(_ context.Context, action, detail string) { audited = append(audited, action+": "+detail) }
	home.log, home.failOn = nil, "load dumps"
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if l := waitRestore(t, m); !strings.Contains(l.Err, "Forgejo's files are back from the backup of ") ||
		!strings.HasSuffix(l.Err, ", but its database couldn't be loaded, so Roostack is keeping it stopped. Choose what to do on Forgejo's page.") ||
		slices.Contains(home.log, "start") {
		t.Errorf("restore: %q; home did %v", l.Err, home.log)
	}
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	if in.Held == nil || in.Held.Backup != backups[0].ID() || in.Held.Error != "load dumps failed" || !in.Held.Taken.Equal(backups[0].Time) {
		t.Fatalf("held %+v", in.Held)
	}
	services, _ := state.LoadServices(m.Lab)
	d, _ := state.Desired(m.Lab, state.OnNode(services, "n-0000000002"))
	for _, out := range d.Services {
		if out.Service == "forgejo" && (slices.Contains(out.Units, "roostack-forgejo-app.service") || out.CaddySite != nil) {
			t.Errorf("held, only its databases run: %v", out.Units)
		}
	}
	// Restored again once the cause is fixed: the hold ends, and that's
	// recorded.
	home.failOn = ""
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if l := waitRestore(t, m); l.Err != "" {
		t.Fatalf("restore again: %s", l.Err)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Held != nil {
		t.Errorf("still held: %+v", in.Held)
	}
	if len(audited) != 1 || audited[0] != "restore: Restored Forgejo, so it's no longer stopped" {
		t.Errorf("audited %q", audited)
	}

	// Onto a server that can't be reached: refused.
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000003"); err == nil || !strings.Contains(err.Error(), "can't reach vps") {
		t.Errorf("err = %v", err)
	}
}

// A restore needs nothing but the backup: a service installed at one
// version still generates from it when the catalog has moved on to a newer
// one (SPEC.md §8.3.1).
func TestRestoreUsesTheLabsPackages(t *testing.T) {
	bin := resticBin(t)
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	generated := func() string {
		t.Helper()
		services, err := state.LoadServices(m.Lab)
		if err != nil {
			t.Fatal(err)
		}
		d, err := state.Desired(m.Lab, services)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, s := range d.Services {
			for _, f := range s.Quadlets {
				out.WriteString(f.Name + "\n" + f.Content)
			}
		}
		return out.String()
	}
	before := generated()
	if !strings.Contains(before, "vaultwarden/server:1.34.3@") {
		t.Fatalf("installed from the catalog's 1.34.3:\n%s", before)
	}

	// The lab's backup, as its plan says (the folders that exist here).
	repo := &backup.Repo{Bin: bin, URL: filepath.Join(t.TempDir(), "repo"), PasswordFile: filepath.Join(t.TempDir(), "key"),
		CacheDir: filepath.Join(t.TempDir(), "cache")}
	os.WriteFile(repo.PasswordFile, []byte("the key\n"), 0o600)
	if err := repo.Init(ctx); err != nil {
		t.Fatal(err)
	}
	plan := backup.LabPlan(m.Lab.Paths, nil)
	var paths []string
	for _, p := range plan.Paths {
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	if _, err := repo.Backup(ctx, backup.BackupOptions{Paths: paths, Excludes: plan.Excludes,
		Tags: []string{backup.TagRoostack, backup.TagServer}}); err != nil {
		t.Fatal(err)
	}

	// Roostack is upgraded: its catalog has a newer Vaultwarden. Then the
	// server is lost, and the lab restored from its backup.
	catalog := m.Lab.Paths.CatalogPackageFile("vaultwarden", "1.34.3-roostack.1")
	data, _ := os.ReadFile(catalog)
	newer := strings.ReplaceAll(string(data), "1.34.3", "1.35.0")
	if newer == string(data) {
		t.Fatal("the catalog didn't change")
	}
	os.WriteFile(m.Lab.Paths.CatalogPackageFile("vaultwarden", "1.35.0-roostack.1"), []byte(newer), 0o644)
	os.Remove(catalog)
	os.RemoveAll(m.Lab.Paths.StateDir)
	snaps, err := repo.Snapshots(ctx, backup.TagServer)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v %v", snaps, err)
	}
	for _, p := range snaps[0].Paths {
		if err := repo.RestoreDir(ctx, snaps[0].ID, p); err != nil {
			t.Fatal(err)
		}
	}

	if after := generated(); after != before {
		t.Errorf("after the restore it generates from the catalog's newer version:\n%s", after)
	}
	if p, err := m.ServicePackage("vaultwarden"); err != nil || p.Version != "1.34.3-roostack.1" {
		t.Errorf("the service's package: %v %v", p, err)
	}
	// What can be installed is the catalog's.
	if p, err := m.Package("vaultwarden"); err != nil || p.Version != "1.35.0-roostack.1" {
		t.Errorf("the catalog's package: %v %v", p, err)
	}
}
