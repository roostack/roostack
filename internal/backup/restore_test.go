package backup

import (
	"context"
	"database/sql"
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

	// Restoring a service whose data isn't in the backup puts the current
	// data back instead of starting it empty.
	other := filepath.Join(root, "srv", "other", "data")
	writeFile(t, filepath.Join(other, "keep.txt"), "current")
	_, err = r.RestoreService(ctx, &generate.Output{Service: "other", Backup: generate.BackupPlan{Include: []string{other}}}, "")
	if err == nil || !strings.Contains(err.Error(), "put back unchanged") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(other, "keep.txt")); string(got) != "current" {
		t.Error("current data must be put back when the backup doesn't contain it")
	}
}

type nopReporter struct{}

func (nopReporter) SetInterruptible(bool) {}
func (nopReporter) SetProgress(int)       {}
