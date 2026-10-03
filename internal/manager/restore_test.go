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

func TestSuggestFirst(t *testing.T) {
	at := func(h int) backup.Snapshot {
		return backup.Snapshot{ID: fmt.Sprint(h), Time: time.Date(2026, 10, 4, h, 0, 0, 0, time.UTC)}
	}
	ids := func(bs []ServiceBackup) string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Snapshot.ID)
		}
		return strings.Join(out, " ")
	}
	// A copy at 3:00 just after the main backup at 2:00: the main one is
	// suggested; the rest stay newest first.
	got := suggestFirst([]ServiceBackup{{Snapshot: at(1)}, {Main: true, Snapshot: at(2)}, {Snapshot: at(3)}, {Main: true, Snapshot: at(0)}})
	if ids(got) != "2 3 1 0" {
		t.Errorf("got %s", ids(got))
	}
	// The main backup is much older (failing for days): the newest wins.
	got = suggestFirst([]ServiceBackup{{Main: true, Snapshot: at(1)}, {Snapshot: at(20)}})
	if ids(got) != "20 1" {
		t.Errorf("got %s", ids(got))
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
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	m, home, vps := setUpMove(t)
	ctx := context.Background()
	applyWait = 5 * time.Second
	defer func() { applyWait = 20 * time.Minute }()

	// home's backup of Forgejo: its data and its secrets.
	paths := m.Lab.Paths
	svcDir := filepath.Join(paths.ServicesDir, "forgejo")
	os.MkdirAll(filepath.Join(svcDir, "data"), 0o755)
	os.WriteFile(filepath.Join(svcDir, "data", "app.ini"), []byte("from the backup"), 0o644)
	secrets, _ := json.Marshal(map[string][]byte{"roostack-forgejo-db": []byte("the db password")})
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
	defer st.Close()
	m.BackupStore, m.OpenRepo = st, open
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "local:" + repo.URL,
		Repository: repo.URL, Created: time.Now()})
	// A copy that can't be read is reported, not fatal.
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "local:" + filepath.Join(dir, "nothing"),
		Repository: filepath.Join(dir, "nothing"), Created: time.Now()})

	backups, notes, err := m.ServiceBackups(ctx, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || !backups[0].Main || backups[0].ServerName != "home" || backups[0].Ref != "backup:n-0000000002" {
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
	if vps.data[0]["data/app.ini"].Data != "from the backup" || string(vps.secrets["roostack-forgejo-db"]) != "the db password" {
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

	// Onto a server that can't be reached: refused.
	if err := m.RestoreService(ctx, "forgejo", backups[0].ID(), "n-0000000003"); err == nil || !strings.Contains(err.Error(), "can't reach vps") {
		t.Errorf("err = %v", err)
	}
}
