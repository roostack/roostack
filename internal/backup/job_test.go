package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
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
	vw.Service = "vaultwarden"
	vw.Secrets = []generate.Secret{{Name: "roostack-vaultwarden-admin"}}
	cache := &generate.Output{Service: "cache"} // nothing to back up
	p := LabPlan(paths, []*generate.Output{vw, cache})
	if want := []string{"/var/lib/roostack", "/var/lib/roostack/home/.local/share/containers/storage/secrets"}; !slices.Equal(p.Paths, want) {
		t.Errorf("the server's own paths = %v", p.Paths)
	}
	for _, want := range []string{"/var/lib/roostack/home/*", "!/var/lib/roostack/home/.local", "/var/lib/roostack/roostack.db",
		"/var/lib/roostack/backup-key", "/var/lib/roostack/backup-key.*", "/var/lib/roostack/backup-secrets"} {
		if !slices.Contains(p.Excludes, want) {
			t.Errorf("excludes missing %s: %v", want, p.Excludes)
		}
	}
	if len(p.Services) != 1 || p.Services[0].Name != "vaultwarden" || len(p.Services[0].Paths) != 2 ||
		!slices.Equal(p.Services[0].Excludes, vw.Backup.Exclude) || !slices.Equal(p.Services[0].Secrets, []string{"roostack-vaultwarden-admin"}) {
		t.Errorf("services = %+v", p.Services)
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

// Each service gets its own snapshot, tagged with its name, holding its
// data and its secrets; the secrets file doesn't stay on the disk.
func TestServiceSnapshots(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	photos := filepath.Join(root, "srv", "photos")
	writeFile(t, filepath.Join(photos, "cat.jpg"), "meow")
	svc := &generate.Output{Service: "photos", Secrets: []generate.Secret{{Name: "roostack-photos-db"}, {Name: "roostack-photos-gone"}},
		Backup: generate.BackupPlan{Include: []string{photos, filepath.Join(root, "srv", "photos-media")}}}
	d := JobDeps{
		Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "test",
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db copy"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, []*generate.Output{svc}), nil },
		ReadSecret: func(_ context.Context, name string) ([]byte, error) {
			if name == "roostack-photos-gone" {
				return nil, errors.New("no such secret")
			}
			return []byte("s3cret"), nil
		},
	}
	sum, err := Run(ctx, d, nopReporter{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := repo.Snapshots(ctx, TagRoostack, TagServer)
	if err != nil || len(server) != 1 || server[0].ID != sum.SnapshotID {
		t.Fatalf("the server's snapshot: %v %v", server, err)
	}
	snap, err := repo.ServiceSnapshot(ctx, "photos", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Run() == "" || snap.Run() != server[0].Run() {
		t.Errorf("one run's snapshots share its tag: %q, %q", snap.Run(), server[0].Run())
	}
	if snap.Service() != "photos" || slices.Contains(snap.Paths, paths.StateDir) || !slices.Contains(snap.Paths, photos) {
		t.Errorf("photos' snapshot: %+v", snap)
	}
	data, err := repo.DumpFile(ctx, snap.ID, SecretsFile(paths, "photos"))
	if err != nil || string(data) != `{"roostack-photos-db":"czNjcmV0"}` {
		t.Errorf("its secrets: %s %v", data, err)
	}
	if _, err := os.Stat(SecretsDir(paths)); !os.IsNotExist(err) {
		t.Errorf("the secrets stayed on the disk: %v", err)
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

// A copy whose storage never answers (a server that's off) gives up after
// reachTimeout instead of holding the backup up while restic retries.
func TestBackupCopyUnreachable(t *testing.T) {
	main := testRepo(t)
	if err := main.Init(context.Background()); err != nil { // slow the first time (key derivation)
		t.Fatal(err)
	}
	reachTimeout = 5 * time.Second
	defer func() { reachTimeout = 2 * time.Minute }()
	// Accepts connections and never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	silent := testRepo(t)
	silent.URL = "rest:http://" + ln.Addr().String() + "/"
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	var copyErr error
	d := JobDeps{
		Repo: main, Preparer: &Preparer{}, Paths: paths, Host: "test",
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db copy"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil },
		Copies:    []Copy{{Name: "server:dev2", Title: "dev2's disk", Repo: silent}},
		OnCopy:    func(_ Copy, _ time.Time, _ *Summary, err error) { copyErr = err },
	}
	start := time.Now()
	if _, err := Run(context.Background(), d, nopReporter{}); err != nil {
		t.Fatalf("the main backup: %v", err)
	}
	if copyErr == nil || !strings.Contains(copyErr.Error(), "couldn't reach dev2's disk in 5s") {
		t.Errorf("copy: %v", copyErr)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

// The main backup's drive isn't plugged in: the copies still run. A
// retry backs up only to the destinations asked for.
func TestBackupMainDriveMissing(t *testing.T) {
	copyRepo := testRepo(t)
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	var copied []string
	d := JobDeps{
		Repo: testRepo(t), Preparer: &Preparer{}, Paths: paths, Host: "test",
		Precheck:  func() error { return errors.New("BACKUP isn't connected") },
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db copy"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil },
		Copies:    []Copy{{Name: "provider:1", Title: "Backblaze", Repo: copyRepo}, {Name: "provider:2", Title: "Other", Repo: copyRepo}},
		OnCopy:    func(c Copy, _ time.Time, _ *Summary, err error) { copied = append(copied, c.Name) },
	}
	_, err := Run(context.Background(), d, nopReporter{})
	var drive *DriveError
	if !errors.As(err, &drive) || !slices.Equal(copied, []string{"provider:1", "provider:2"}) {
		t.Fatalf("err %v, copies %v", err, copied)
	}
	copied = nil
	d.Only = []string{"provider:2"}
	if _, err := Run(context.Background(), d, nopReporter{}); err != nil || !slices.Equal(copied, []string{"provider:2"}) {
		t.Errorf("a retry of one copy: %v %v", err, copied)
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
