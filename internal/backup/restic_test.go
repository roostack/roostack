package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	if stored, err := r.Stored(ctx); err != nil || stored == 0 || stored > 1<<20 {
		t.Errorf("stored = %d, %v", stored, err)
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
	if _, err := r.AddKey(ctx, "server key", "n-0000000002"); err != nil {
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

// Replacing a key (a recovery key change, SPEC.md §6.9): add the new one,
// remove the old one with it; the old one no longer opens the repository.
func TestReplaceKey(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := r.CurrentKey(ctx)
	if err != nil || old == "" {
		t.Fatalf("current key: %q %v", old, err)
	}
	if _, err := r.AddKey(ctx, "new recovery key", "recovery"); err != nil {
		t.Fatal(err)
	}
	next := *r
	next.PasswordFile = filepath.Join(t.TempDir(), "new")
	os.WriteFile(next.PasswordFile, []byte("new recovery key\n"), 0o600)
	if err := r.RemoveKey(ctx, old); err == nil {
		t.Error("restic should refuse to remove the key in use")
	}
	if err := next.RemoveKey(ctx, old); err != nil {
		t.Fatal(err)
	}
	keys, err := next.Keys(ctx)
	if err != nil || len(keys) != 1 || !keys[0].Current || keys[0].UserName != "recovery" {
		t.Errorf("keys: %+v %v", keys, err)
	}
	if _, err := r.Snapshots(ctx); err == nil {
		t.Error("the old key still opens the repository")
	}
}

// The new key's file is next to the key file, not in /tmp: restic for a
// drive runs outside Roostack's sandbox, which has a private /tmp (found
// on VM1: "…/tmp/roostack-key-… does not exist").
func TestAddKeyFileVisibleOutsideSandbox(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var keyArg string
	r.Launch = func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		for i, a := range args {
			if a == "--new-password-file" && i+1 < len(args) {
				keyArg = args[i+1]
			}
		}
		return DirectLaunch(ctx, env, name, args...)
	}
	if _, err := r.AddKey(ctx, "another key", "recovery"); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(keyArg) != filepath.Dir(r.PasswordFile) {
		t.Errorf("new key file %s, want it next to %s", keyArg, r.PasswordFile)
	}
	if _, err := os.Stat(keyArg); !os.IsNotExist(err) {
		t.Error("the new key's file should be removed afterwards")
	}
}

// TestCredentials: credentials that can't be had stop the command before
// it runs (restic never starts without its keys); those that can are in
// its environment.
func TestCredentials(t *testing.T) {
	r := &Repo{Bin: "/bin/sh", URL: "/nowhere", Credentials: func(context.Context) ([]string, error) {
		return nil, errors.New("Cloud is out of reach")
	}}
	if _, err := r.run(context.Background(), "-c", "echo ran"); err == nil || !strings.Contains(err.Error(), "getting this backup's keys") {
		t.Errorf("without keys: %v", err)
	}
	r.Credentials = func(context.Context) ([]string, error) { return []string{"AWS_SESSION_TOKEN=tok"}, nil }
	out, err := r.run(context.Background(), "-c", "echo $AWS_SESSION_TOKEN")
	if err != nil || strings.TrimSpace(string(out)) != "tok" {
		t.Errorf("with keys: %q %v", out, err)
	}
}

// A restic that's killed leaves its lock behind, which blocks a prune
// until Unlock clears it; a running restic's lock stays
// (docs/ASSUMPTIONS.md N1).
func TestUnlockClearsStaleLocks(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	// A backup reading from stdin holds its lock until stdin closes.
	cmd := r.command(ctx, "backup", "--stdin", "--stdin-filename", "x")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	locks := filepath.Join(r.URL, "locks")
	for i := 0; ; i++ {
		if entries, _ := os.ReadDir(locks); len(entries) > 0 {
			break
		}
		if i == 100 {
			t.Fatal("the backup didn't lock the repository")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n, err := r.Unlock(ctx); err != nil || n != 0 {
		t.Errorf("a running backup's lock must stay: removed %d, %v", n, err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if err := r.Forget(ctx, DefaultRetention); !errors.Is(err, ErrLocked) {
		t.Fatalf("a killed backup's lock blocks a prune: %v", err)
	}
	if n, err := r.Unlock(ctx); err != nil || n != 1 {
		t.Errorf("Unlock = %d, %v; want 1 removed", n, err)
	}
	if err := r.Forget(ctx, DefaultRetention); err != nil {
		t.Errorf("prune after Unlock: %v", err)
	}
}

// A key hint makes restic try only the server's own key, so a key someone
// added can't stop or slow an open (ASSUMPTIONS O5, O6).
func TestKeyHint(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	// Made before hints were kept: the first command asks restic once.
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	want, err := r.CurrentKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r.Hints = &KeyHints{File: filepath.Join(t.TempDir(), "hints.json")}
	if _, err := r.Snapshots(ctx); err != nil {
		t.Fatal(err)
	}
	password, _ := os.ReadFile(r.PasswordFile)
	if got := r.Hints.Get(r.URL, password); got != want {
		t.Errorf("looked-up hint = %q, want %q", got, want)
	}

	// A new repository and a key added to it are noted as they're made.
	made := testRepo(t)
	made.Hints = r.Hints
	if err := made.Init(ctx); err != nil {
		t.Fatal(err)
	}
	mine, _ := os.ReadFile(made.PasswordFile)
	if id, _ := made.CurrentKey(ctx); id == "" || made.Hints.Get(made.URL, mine) != id {
		t.Errorf("Init's hint = %q, key %q", made.Hints.Get(made.URL, mine), id)
	}
	added, err := made.AddKey(ctx, "server key", "n-0000000002")
	if err != nil || added == "" || made.Hints.Get(made.URL, []byte("server key\n")) != added {
		t.Errorf("AddKey: %q %v, hint %q", added, err, made.Hints.Get(made.URL, []byte("server key")))
	}

	// A key restic refuses (scrypt needing 4 GiB), named to sort first:
	// without the hint restic may read it and give up; with it, never.
	junk := `{"created":"2026-10-17T00:00:00Z","username":"x","hostname":"x","kdf":"scrypt","N":4194304,"r":8,"p":1,` +
		`"salt":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==","data":"AAAA"}`
	writeFile(t, filepath.Join(r.URL, "keys", strings.Repeat("0", 64)), junk)
	if _, err := r.Snapshots(ctx); err != nil {
		t.Errorf("with the hint: %v", err)
	}
	var env []string
	r.Launch = func(ctx context.Context, e []string, name string, args ...string) *exec.Cmd {
		env = e
		return DirectLaunch(ctx, e, name, args...)
	}
	r.Snapshots(ctx)
	if !slices.Contains(env, "RESTIC_KEY_HINT="+want) {
		t.Errorf("env = %v", env)
	}

	// Hints that aren't key IDs are refused.
	if err := r.Hints.Set(r.URL, password, "abc; rm -rf /"); err == nil {
		t.Error("a hint that isn't an ID was taken")
	}
}
