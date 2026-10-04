package labrestore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
)

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

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A lab backed up to a disk is found there with the recovery key and
// rebuilt on a fresh install, database included.
func TestFindAndRestoreALab(t *testing.T) {
	bin := resticBin(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), ServicesDir: filepath.Join(root, "services")}
	service := filepath.Join(paths.ServicesDir, "vaultwarden")
	keys := t.TempDir()
	key := filepath.Join(keys, "recovery-key")
	write(t, key, "the lab's recovery key\n")
	wrong := filepath.Join(keys, "wrong-key")
	write(t, wrong, "another lab's key\n")

	// The old server: a controller of lab.example.com, backed up to a disk
	// in its server's folder, next to another lab's backups.
	write(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: lab.example.com\n")
	id := nodeid.Identity{ID: "n-0123456789", Name: "nuc"}
	if err := id.Save(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	labnetDir := labnet.IdentityDir(paths.StateDir)
	os.MkdirAll(labnetDir, 0o700)
	m, _ := json.Marshal(labnet.Membership{Role: config.RoleController})
	write(t, labnet.MembershipFile(paths.StateDir), string(m))
	write(t, backup.StateDumpFile(paths), "the lab's database")
	write(t, filepath.Join(service, "data", "db.sqlite3"), "vaultwarden's passwords")
	disk := t.TempDir()
	repoAt := func(keyFile, dir string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: dir, PasswordFile: keyFile, CacheDir: filepath.Join(keys, "cache")}
	}
	old := repoAt(key, filepath.Join(disk, "roostack", "lab-1", id.ID))
	other := repoAt(wrong, filepath.Join(disk, "roostack", "lab-2", "n-9999999999"))
	// Vaultwarden runs on this server; Photos on another (its data is in
	// that server's backups).
	write(t, filepath.Join(paths.InstancesDir(), "vaultwarden.yaml"), "package: vaultwarden\n")
	write(t, filepath.Join(paths.InstancesDir(), "photos.yaml"), "package: photos\nhost: n-1111111111\n")
	photos := filepath.Join(paths.ServicesDir, "photos")
	write(t, filepath.Join(photos, "old.jpg"), "from before it moved")
	for _, r := range []*backup.Repo{old, other} {
		if err := r.Init(ctx); err != nil {
			t.Fatal(err)
		}
		for _, b := range []backup.BackupOptions{
			{Paths: []string{paths.StateDir}, Tags: []string{backup.TagRoostack, backup.TagServer}},
			{Paths: []string{service}, Tags: []string{backup.TagRoostack, backup.ServiceTag("vaultwarden")}},
			{Paths: []string{photos}, Tags: []string{backup.TagRoostack, backup.ServiceTag("photos")}},
		} {
			if _, err := r.Backup(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The new server: a fresh install with its own identity and database.
	os.RemoveAll(root)
	os.MkdirAll(paths.StateDir, 0o750)
	fresh := nodeid.Identity{ID: "n-5555555555", Name: "new"}
	if err := fresh.Save(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(paths.StateDir, "roostack.db"), "empty database")
	write(t, filepath.Join(paths.StateDir, "roostack.db-wal"), "wal")

	f := &Finder{OpenRepo: func(url string, env []string) *backup.Repo { return repoAt(key, url) }}
	if _, _, err := f.Find(ctx, Source{Kind: "folder", Folder: filepath.Join(root, "nowhere")}); err == nil {
		t.Error("a missing folder")
	}
	found, unopened, err := f.Find(ctx, Source{Kind: "folder", Folder: disk})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Domain != "lab.example.com" || found[0].ServerName != "nuc" || !found[0].Controller ||
		found[0].StateDir != paths.StateDir {
		t.Fatalf("found = %+v", found)
	}
	if len(unopened) != 1 || unopened[0].Problem == "" {
		t.Errorf("another lab's backups can't be opened: %+v", unopened)
	}

	log := activity.New().Start("restore", "Restoring the lab")
	if err := Restore(ctx, paths, repoAt(key, found[0].Repository), found[0], key, log); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(service, "data", "db.sqlite3")); got != "vaultwarden's passwords" {
		t.Errorf("service data = %q", got)
	}
	if _, err := os.Stat(photos); !os.IsNotExist(err) {
		t.Error("a service on another server was restored here")
	}
	if got, _ := nodeid.LoadOrCreate(paths.StateDir); got.ID != id.ID {
		t.Errorf("the restored server is %s, want the old server's identity", got.ID)
	}
	if got := backup.ReadGeneration(paths); got != 1 {
		t.Errorf("a restored lab is a newer generation: %d", got)
	}
	if got := read(t, backup.KeyFile(paths)); got != "the lab's recovery key\n" {
		t.Errorf("the recovery key should become the backup key: %q", got)
	}
	if _, pending := Pending(paths); pending {
		t.Error("the database comes first")
	}

	// Roostack restarts: the backed-up database replaces the empty one.
	if ok, err := StartFromBackup(paths); !ok || err != nil {
		t.Fatalf("start: %v %v", ok, err)
	}
	if got := read(t, filepath.Join(paths.StateDir, "roostack.db")); got != "the lab's database" {
		t.Errorf("database = %q", got)
	}
	if _, err := os.Stat(filepath.Join(paths.StateDir, "roostack.db-wal")); err == nil {
		t.Error("the new database's journal must go")
	}
	if ok, _ := StartFromBackup(paths); ok {
		t.Error("only once")
	}
	if _, pending := Pending(paths); !pending {
		t.Error("the services' databases are next")
	}
	Done(paths)
	if _, pending := Pending(paths); pending {
		t.Error("done")
	}
}

func TestRegion(t *testing.T) {
	for endpoint, want := range map[string]string{
		"https://s3.us-west-004.backblazeb2.com":  "us-west-004",
		"https://s3.eu-central-1.wasabisys.com":   "eu-central-1",
		"https://s3.ap-southeast-2.amazonaws.com": "ap-southeast-2",
		"https://abc123.r2.cloudflarestorage.com": "auto",
		"https://minio.home.example.com:9000":     "us-east-1",
	} {
		if got := region(endpoint); got != want {
			t.Errorf("region(%s) = %q, want %q", endpoint, got, want)
		}
	}
}

func TestShownHidesPasswords(t *testing.T) {
	for in, want := range map[string]string{
		"rest:https://roostack:s3cret@backup.example.com/lab": "rest:https://roostack@backup.example.com/lab",
		"s3:https://s3.example.com/bucket/roostack/l/n":       "s3:https://s3.example.com/bucket/roostack/l/n",
		"/mnt/backup/roostack/l/n":                            "/mnt/backup/roostack/l/n",
	} {
		if got := Shown(in); got != want {
			t.Errorf("Shown(%q) = %q", in, got)
		}
	}
}

func TestUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	disk := t.TempDir()
	locked := filepath.Join(disk, "roostack")
	os.MkdirAll(filepath.Join(locked, "lab-1", "n-1"), 0o755)
	os.Chmod(locked, 0)
	defer os.Chmod(locked, 0o755)
	f := &Finder{OpenRepo: func(string, []string) *backup.Repo { return nil }}
	_, _, err := f.Find(context.Background(), Source{Kind: "folder", Folder: disk})
	if err == nil || !strings.Contains(err.Error(), "sudo chown -R roostack:roostack "+disk) {
		t.Errorf("err = %v", err)
	}
}
