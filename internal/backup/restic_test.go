package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the real restic binary when ROOSTACK_RESTIC_BIN is set (or
// restic is on PATH), against a repository in a temporary directory.
func testRepo(t *testing.T) *Repo {
	t.Helper()
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	dir := t.TempDir()
	pw := filepath.Join(dir, "key")
	if err := os.WriteFile(pw, []byte("test recovery key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: pw, CacheDir: filepath.Join(dir, "cache")}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()

	if _, err := r.Snapshots(ctx); !errors.Is(err, ErrNoRepository) {
		t.Fatalf("before init: %v, want ErrNoRepository", err)
	}
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Init(ctx); err != nil {
		t.Fatalf("Init must be idempotent: %v", err)
	}

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "data", "photo.jpg"), strings.Repeat("jpeg", 1000))
	writeFile(t, filepath.Join(src, "data", "db.sqlite3"), "live database (excluded)")
	writeFile(t, filepath.Join(src, "db-dump", "main.sqlite3"), "consistent copy")

	var progress int
	sum, err := r.Backup(ctx, BackupOptions{
		Paths:      []string{src},
		Excludes:   []string{filepath.Join(src, "data", "db.sqlite3")},
		Tags:       []string{"roostack", "service:vaultwarden"},
		Host:       "roostack-test",
		OnProgress: func(Progress) { progress++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.SnapshotID == "" || sum.FilesNew != 2 {
		t.Errorf("summary = %+v, want 2 new files", sum)
	}

	snaps, err := r.Snapshots(ctx, "service:vaultwarden")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v %v", snaps, err)
	}
	if other, _ := r.Snapshots(ctx, "service:immich"); len(other) != 0 {
		t.Error("tag filter not applied")
	}

	target := t.TempDir()
	if err := r.Restore(ctx, snaps[0].ID, target); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(target, src)
	if got, err := os.ReadFile(filepath.Join(restored, "db-dump", "main.sqlite3")); err != nil || string(got) != "consistent copy" {
		t.Errorf("restored dump: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(restored, "data", "db.sqlite3")); !os.IsNotExist(err) {
		t.Error("excluded live database was backed up")
	}

	if err := r.Check(ctx, "100%"); err != nil {
		t.Errorf("check: %v", err)
	}
	if err := r.Forget(ctx, DefaultRetention); err != nil {
		t.Errorf("forget: %v", err)
	}
}

func TestBackupCancelled(t *testing.T) {
	r := testRepo(t)
	if err := r.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Backup(ctx, BackupOptions{Paths: []string{t.TempDir()}}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestParseBackupOutput(t *testing.T) {
	out := `{"message_type":"status","percent_done":0.5,"total_bytes":200,"bytes_done":100,"seconds_remaining":7}
warning: not json
{"message_type":"summary","snapshot_id":"abc","files_new":3,"data_added":42,"total_duration":1.5}
`
	var got []Progress
	sum, err := parseBackupOutput(strings.NewReader(out), func(p Progress) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PercentDone != 50 || got[0].SecondsRemaining != 7 {
		t.Errorf("progress = %+v", got)
	}
	if sum.SnapshotID != "abc" || sum.FilesNew != 3 || sum.DataAdded != 42 || sum.Duration.Seconds() != 1.5 {
		t.Errorf("summary = %+v", sum)
	}
}

func TestAddKey(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.AddKey(ctx, "server key", "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	// The server opens the repository with its own key.
	other := *r
	other.PasswordFile = filepath.Join(t.TempDir(), "server")
	os.WriteFile(other.PasswordFile, []byte("server key\n"), 0o600)
	if _, err := other.Snapshots(ctx); err != nil {
		t.Errorf("server key: %v", err)
	}
}
