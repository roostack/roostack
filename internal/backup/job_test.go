package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/maintenance"
)

func TestLabPlan(t *testing.T) {
	paths := config.Default().Paths
	vw := &generate.Output{Backup: generate.BackupPlan{
		Include: []string{"/srv/mylab/services/vaultwarden/data", "/srv/mylab/services/vaultwarden/db-dump"},
		Exclude: []string{"/srv/mylab/services/vaultwarden/data/db.sqlite3"},
		Prepare: []generate.PrepareStep{{Type: "sqlite"}},
	}}
	p := LabPlan(paths, []*generate.Output{vw})
	for _, want := range []string{"/var/lib/mylab", "/var/lib/mylab/home/.local/share/containers/storage/secrets",
		"/srv/mylab/services/vaultwarden/data"} {
		if !slices.Contains(p.Paths, want) {
			t.Errorf("paths missing %s: %v", want, p.Paths)
		}
	}
	for _, want := range []string{"/var/lib/mylab/home/*", "!/var/lib/mylab/home/.local", "/var/lib/mylab/mylab.db", "/var/lib/mylab/backup-key",
		"/srv/mylab/services/vaultwarden/data/db.sqlite3"} {
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

	var done error = errNotCalled
	job := NewJob(JobDeps{
		Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "test",
		DumpState: func(_ context.Context, path string) error {
			writeFile(t, path, "db copy")
			return nil
		},
		Plan:   func() (Plan, error) { return LabPlan(paths, nil), nil },
		OnDone: func(_ *Summary, err error) { done = err },
	})

	c := &maintenance.Coordinator{
		Policy: maintenance.DefaultPolicy(), Now: time.Now, Reboot: func() error { return nil },
		Journal: nopJournal{}, Ticks: make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	c.Submit(job)
	for i := 0; i < 600 && done == errNotCalled; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if done != nil {
		t.Fatalf("backup job: %v", done)
	}

	snaps, err := repo.Snapshots(context.Background(), "mylab")
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

var errNotCalled = errorString("not called")

type errorString string

func (e errorString) Error() string { return string(e) }

type nopJournal struct{}

func (nopJournal) Load() ([]string, error) { return nil, nil }
func (nopJournal) Save([]string) error     { return nil }
