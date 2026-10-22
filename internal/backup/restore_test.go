package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
)

// TestMain lets the test binary act as `roostack backup-helper ...`, so the
// SQLite prepare and restore steps run for real without podman.
func TestMain(m *testing.M) {
	if os.Getenv("ROOSTACK_TEST_HELPER") == "1" {
		args := os.Args[len(os.Args)-3:]
		var err error
		switch args[0] {
		case "sqlite-copy":
			err = SQLiteCopy(context.Background(), args[1], args[2])
		case "sqlite-restore":
			err = SQLiteRestore(context.Background(), args[1], args[2])
		}
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helperLaunch runs "podman unshare <bin> backup-helper op a b" as this test
// binary acting as the helper.
func helperLaunch(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	i := slicesIndex(args, "backup-helper")
	cmd := exec.CommandContext(ctx, os.Args[0], args[i+1:]...)
	cmd.Env = append(os.Environ(), "ROOSTACK_TEST_HELPER=1")
	return cmd
}

func slicesIndex(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

type fakeUnits struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeUnits) Stop(_ context.Context, u ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+strings.Join(u, " "))
	return nil
}
func (f *fakeUnits) Start(_ context.Context, u ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start "+strings.Join(u, " "))
	return nil
}

func sqliteRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return n
}

func TestRestoreService(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	data := filepath.Join(root, "srv", "app", "data")
	dump := filepath.Join(root, "srv", "app", "db-dump")
	live := filepath.Join(data, "db.sqlite3")

	// The "app": a WAL-mode SQLite database with 3 rows, plus a file.
	os.MkdirAll(data, 0o755)
	db, _ := sql.Open("sqlite", "file:"+live+"?_pragma=journal_mode(WAL)")
	db.Exec(`CREATE TABLE items (n INTEGER)`)
	db.Exec(`INSERT INTO items VALUES (1), (2), (3)`)
	db.Close()
	writeFile(t, filepath.Join(data, "attachment.txt"), "precious")

	svc := &generate.Output{Service: "app", Units: []string{"roostack-app-app.service"}, Backup: generate.BackupPlan{
		Include: []string{data, dump},
		Exclude: []string{live, live + "-wal", live + "-shm"},
		Prepare: []generate.PrepareStep{{Type: pkgspec.DBSQLite, Source: live, Target: filepath.Join(dump, "main.sqlite3")}},
	}}
	_, err := Run(ctx, JobDeps{
		Repo: repo, Preparer: &Preparer{Launch: helperLaunch, RoostackBin: "roostack"}, Paths: paths, Host: "t",
		DumpState: func(context.Context, string) error { return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, []*generate.Output{svc}), nil },
	}, nopReporter{})
	if err != nil {
		t.Fatal(err)
	}

	// Disaster: the database is corrupted and a file is lost.
	os.WriteFile(live, []byte("garbage"), 0o644)
	os.Remove(filepath.Join(data, "attachment.txt"))

	units := &fakeUnits{}
	r := &Restorer{Repo: repo, Units: units, Launch: helperLaunch, RoostackBin: "roostack",
		Now: func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }}
	res, err := r.RestoreService(ctx, svc, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := sqliteRows(t, live); n != 3 {
		t.Errorf("restored database has %d rows, want 3", n)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "attachment.txt")); string(got) != "precious" {
		t.Error("lost file not restored")
	}
	aside := res.MovedAside[data]
	if got, _ := os.ReadFile(filepath.Join(aside, "db.sqlite3")); string(got) != "garbage" {
		t.Error("the broken data should be kept aside, not deleted")
	}
	if want := []string{"stop roostack-app-app.service", "start roostack-app-app.service"}; strings.Join(units.calls, ",") != strings.Join(want, ",") {
		t.Errorf("units = %v, want %v", units.calls, want)
	}

	// Its database doesn't load: a DumpsError, the files are the backup's,
	// its data from before is kept, and it isn't started (SPEC.md §6.9).
	units.calls = nil
	failing := *r
	failing.Now = func() time.Time { return time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC) }
	failing.Launch = func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	res, err = failing.RestoreService(ctx, svc, "")
	var de *DumpsError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "restoring databases") || res == nil || res.MovedAside[data] == "" {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "attachment.txt")); string(got) != "precious" {
		t.Error("the files should be the backup's")
	}
	if strings.Join(units.calls, ",") != "stop roostack-app-app.service" {
		t.Errorf("units = %v: stopped, never started", units.calls)
	}
	if msg := NotLoadedMsg("App", time.Date(2026, 10, 20, 3, 0, 0, 0, time.Local)); msg != "App's files are back from the backup of Tue 20 Oct 03:00, "+
		"but its database couldn't be loaded, so Roostack is keeping it stopped. Choose what to do on App's page." {
		t.Errorf("message %q", msg)
	}

	// A service with no backup of its own: nothing is touched.
	other := filepath.Join(root, "srv", "other", "data")
	writeFile(t, filepath.Join(other, "keep.txt"), "current")
	units.calls = nil
	_, err = r.RestoreService(ctx, &generate.Output{Service: "other", Units: []string{"roostack-other-app.service"},
		Backup: generate.BackupPlan{Include: []string{other}}}, "")
	if err == nil || !strings.Contains(err.Error(), "no backup of other") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(other, "keep.txt")); string(got) != "current" || len(units.calls) != 0 {
		t.Errorf("touched a service without a backup: %v", units.calls)
	}
}

// A failed restore says whether the data is back, and only then starts
// the service again.
func TestUndoFailedRestore(t *testing.T) {
	root := t.TempDir()
	svc := &generate.Output{Service: "app", Units: []string{"roostack-app-app.service"}}
	const suffix = keptBefore + "20260926-120000"
	cause := errors.New("restic failed")

	// Put back: the partial restore is kept aside, the data is back and
	// the service starts.
	dir := filepath.Join(root, "data")
	writeFile(t, filepath.Join(dir+suffix, "keep.txt"), "current")
	writeFile(t, filepath.Join(dir, "partial.txt"), "half")
	units := &fakeUnits{}
	r := &Restorer{Units: units}
	res := &RestoreResult{MovedAside: map[string]string{dir: dir + suffix}}
	err := r.undo(context.Background(), svc, res, suffix, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "put back unchanged") {
		t.Errorf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(got) != "current" {
		t.Error("the data isn't back")
	}
	if _, err := os.Stat(filepath.Join(root, "data.failed-restore-20260926-120000", "partial.txt")); err != nil {
		t.Errorf("the partial restore isn't kept: %v", err)
	}
	if strings.Join(units.calls, ",") != "start roostack-app-app.service" {
		t.Errorf("units = %v", units.calls)
	}

	// Not put back (its copy is gone): the error says so and where, and
	// the service isn't started on the wrong data.
	lost := filepath.Join(root, "lost")
	units.calls = nil
	res = &RestoreResult{MovedAside: map[string]string{lost: lost + suffix}}
	err = r.undo(context.Background(), svc, res, suffix, cause)
	if err == nil || strings.Contains(err.Error(), "put back unchanged") || !strings.Contains(err.Error(), lost+suffix) {
		t.Errorf("err = %v", err)
	}
	if len(units.calls) != 0 {
		t.Errorf("started without its data: %v", units.calls)
	}
	if res.MovedAside[lost] != lost+suffix {
		t.Error("what wasn't put back is still listed")
	}
}

type nopReporter struct{}

func (nopReporter) SetInterruptible(bool) {}
func (nopReporter) SetProgress(int)       {}
