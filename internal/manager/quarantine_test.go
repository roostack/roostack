package manager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
)

// fakeBackups is a server's backups as the controller reads them.
type fakeBackups struct{ st backup.Status }

func (f fakeBackups) Status(context.Context) (backup.Status, error) { return f.st, nil }
func (f fakeBackups) RunNow(context.Context) error                  { return nil }

// A server's junk in its repository on another server's disk is found
// with the recovery key, removed through the store's admin access, and
// reported until the Owner dismisses it; the prune then works.
func TestJunkJob(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	defer func(d time.Duration) { backup.JunkRecheck = d }(backup.JunkRecheck)
	backup.JunkRecheck = 0
	m, _ := newManager(t)
	m.Lab.NodeID, m.Lab.OverlayAddr = "n-0000000001", "100.96.0.1"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	local(m).Activity = m.Activity
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore, m.Settings = st, st
	// The repository is a folder here, reached as the store's would be.
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	repoDir := filepath.Join(dir, "repo")
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: repoDir, PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	ctx := context.Background()
	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if l, _ := m.Activity.Latest(activity.BackupSetupFor("n-0000000002")); !l.Running() {
			break
		}
	}
	if _, _, ok := m.BackupTargetOf(ctx, "n-0000000002"); !ok {
		t.Fatal("no backup set up")
	}
	// Nothing to find in a clean repository.
	if err := m.JunkJob(nil).Run(ctx, nil); err != nil || len(m.JunkReports(ctx)) != 0 {
		t.Fatalf("clean: %v %+v", err, m.JunkReports(ctx))
	}

	// The vps, taken over, adds a lock and an index file restic can't read.
	plant := func(kind string) string {
		b := make([]byte, 300)
		rand.Read(b)
		sum := sha256.Sum256(b)
		name := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(repoDir, kind, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return name
	}
	lock, index := plant("locks"), plant("index")
	if err := m.PruneJob().Run(ctx, nil); err == nil {
		t.Fatal("the prune works with junk in the repository")
	}
	if err := m.JunkJob(nil).Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	reports := m.JunkReports(ctx)
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	r := reports[0]
	var names []string
	for _, j := range r.Files {
		names = append(names, j.Path())
	}
	slices.Sort(names)
	if r.ServerID != "n-0000000002" || r.Copy || r.Where != "nuc's disk" || !r.Removed() || r.Err != "" ||
		!slices.Equal(names, []string{"index/" + index, "locks/" + lock}) {
		t.Errorf("report = %+v", r)
	}
	if !slices.Equal(r.Writers, []string{m.nameOf("n-0000000002"), "nuc"}) {
		t.Errorf("writers = %v", r.Writers)
	}
	for _, f := range []string{"locks/" + lock, "index/" + index} {
		if _, err := os.Stat(filepath.Join(repoDir, f)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", f)
		}
	}
	if l, ok := m.Activity.Latest(activity.Backup); !ok || !strings.Contains(strings.Join(l.Lines, "\n"), "locks/"+lock) {
		t.Errorf("the log doesn't name the files")
	}
	if err := m.PruneJob().Run(ctx, nil); err != nil {
		t.Fatalf("prune after removing the junk: %v", err)
	}
	if err := m.DismissJunk(ctx); err != nil || len(m.JunkReports(ctx)) != 0 {
		t.Errorf("dismissed: %v %+v", err, m.JunkReports(ctx))
	}

	// The vps's backup failing as junk makes the controller look soon;
	// failing for another reason doesn't.
	var submitted []maintenance.Job
	m.Submit = func(j maintenance.Job) { submitted = append(submitted, j) }
	submitted = nil
	fail := func(msg string) {
		vps.backups = fakeBackups{backup.Status{Last: &store.BackupRun{Error: msg}}}
	}
	fail("restic backup: exit status 1: Fatal: unable to save snapshot: no space left on device")
	m.CheckForJunk(ctx)
	if len(submitted) != 0 {
		t.Error("checked after an unrelated failure")
	}
	fail("restic backup: exit status 1: unable to create lock in backend: ciphertext verification failed")
	m.CheckForJunk(ctx)
	m.CheckForJunk(ctx)
	if len(submitted) != 1 {
		t.Fatalf("submitted %d checks, want one", len(submitted))
	}
	plant("snapshots")
	if err := submitted[0].Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if reports := m.JunkReports(ctx); len(reports) != 1 || reports[0].Files[0].Kind != "snapshots" {
		t.Errorf("after the check: %+v", reports)
	}
}
