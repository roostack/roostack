package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
)

func TestLabPlan(t *testing.T) {
	paths := config.Default().Paths
	vw := &generate.Output{Backup: generate.BackupPlan{
		Include: []string{"/srv/roostack/services/vaultwarden/data", "/srv/roostack/services/vaultwarden/db-dump"},
		Exclude: []string{"/srv/roostack/services/vaultwarden/data/db.sqlite3"},
		Prepare: []generate.PrepareStep{{Type: "sqlite"}},
	}}
	p := LabPlan(paths, []*generate.Output{vw})
	for _, want := range []string{"/var/lib/roostack", "/var/lib/roostack/home/.local/share/containers/storage/secrets",
		"/srv/roostack/services/vaultwarden/data"} {
		if !slices.Contains(p.Paths, want) {
			t.Errorf("paths missing %s: %v", want, p.Paths)
		}
	}
	for _, want := range []string{"/var/lib/roostack/home/*", "!/var/lib/roostack/home/.local", "/var/lib/roostack/roostack.db", "/var/lib/roostack/backup-key",
		"/srv/roostack/services/vaultwarden/data/db.sqlite3"} {
		if !slices.Contains(p.Excludes, want) {
			t.Errorf("excludes missing %s: %v", want, p.Excludes)
		}
	}
	if len(p.Prepare) != 1 {
		t.Errorf("prepare steps = %d", len(p.Prepare))
	}
}

// End to end with real restic: the job dumps state, runs prepare steps,
// and backs up; the excluded home directory stays out but the explicitly
// listed secrets directory inside it is included.
func TestBackupJob(t *testing.T) {
	repo := testRepo(t)
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	secrets := filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "secrets")
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	writeFile(t, filepath.Join(secrets, "secrets.json"), "{}")
	writeFile(t, filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "overlay", "big-image"), "layers")
	writeFile(t, KeyFile(paths), "key")

	result := make(chan error, 1)
	job := NewJob(JobDeps{
		Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "test",
		DumpState: func(_ context.Context, path string) error {
			writeFile(t, path, "db copy")
			return nil
		},
		Plan:   func() (Plan, error) { return LabPlan(paths, nil), nil },
		OnDone: func(_ *Summary, err error) { result <- err },
	})

	c := &maintenance.Coordinator{
		Policy: maintenance.DefaultPolicy(), Now: time.Now, Reboot: func() error { return nil },
		Journal: nopJournal{}, Ticks: make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	c.Submit(job)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("backup job: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("backup job did not finish")
	}

	snaps, err := repo.Snapshots(context.Background(), "roostack")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v %v", snaps, err)
	}
	target := t.TempDir()
	if err := repo.Restore(context.Background(), snaps[0].ID, target); err != nil {
		t.Fatal(err)
	}
	has := func(p string) bool { _, err := os.Stat(filepath.Join(target, p)); return err == nil }
	if !has(StateDumpFile(paths)) || !has(filepath.Join(secrets, "secrets.json")) || !has(filepath.Join(paths.StateDir, "lab.yaml")) {
		t.Error("restore is missing state, the database copy or secrets")
	}
	if has(filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "overlay")) || has(KeyFile(paths)) {
		t.Error("container images or the backup key were backed up")
	}
}

type nopJournal struct{}

func (nopJournal) Load() ([]string, error) { return nil, nil }
func (nopJournal) Save([]string) error     { return nil }

func TestBytes(t *testing.T) {
	for n, want := range map[uint64]string{0: "0 B", 999: "999 B", 1500: "1.5 kB", 2_600_000_000: "2.6 GB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// Each copy gets its own backup after the main one, even when the main
// destination fails: that's what copies are for.
func TestBackupCopies(t *testing.T) {
	copyRepo := testRepo(t)
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	// The main destination refuses the key (fails at once; restic retries
	// an unreachable one for minutes).
	main := testRepo(t)
	if err := main.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, main.PasswordFile, "another key")
	var copied []string
	var copyErr error
	d := JobDeps{
		Repo: main, Preparer: &Preparer{}, Paths: paths, Host: "test",
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db copy"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil },
		Copies:    []Copy{{Name: "provider:1", Title: "Backblaze", Repo: copyRepo}},
		OnCopy: func(c Copy, _ time.Time, sum *Summary, err error) {
			copied, copyErr = append(copied, c.Name), err
		},
	}
	if _, err := Run(context.Background(), d, nopReporter{}); err == nil {
		t.Fatal("the main destination can't be reached")
	}
	if !slices.Equal(copied, []string{"provider:1"}) || copyErr != nil {
		t.Fatalf("copies: %v %v", copied, copyErr)
	}
	if snaps, err := copyRepo.Snapshots(context.Background(), "roostack"); err != nil || len(snaps) != 1 {
		t.Errorf("the copy should have its own backup: %v %v", snaps, err)
	}
}

func TestBackupWaitsForItsDrive(t *testing.T) {
	d := JobDeps{Precheck: func() error { return CheckDrive(t.TempDir(), "no-such-uuid", "BACKUP") },
		Plan: func() (Plan, error) { t.Fatal("nothing runs without the drive"); return Plan{}, nil }}
	if _, err := Run(context.Background(), d, nopReporter{}); err == nil || !strings.Contains(err.Error(), "BACKUP isn't connected") {
		t.Errorf("err = %v", err)
	}
	// Connected but not mounted there: the folder is on another filesystem.
	dir := t.TempDir()
	DiskByUUID = dir
	defer func() { DiskByUUID = "/dev/disk/by-uuid" }()
	os.Symlink("/dev/null", filepath.Join(dir, "u1"))
	if err := CheckDrive(t.TempDir(), "u1", ""); err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Errorf("not mounted: %v", err)
	}
}
