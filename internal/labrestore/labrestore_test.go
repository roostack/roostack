package labrestore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/backup/restictest"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
)

func resticBin(t *testing.T) string {
	t.Helper()
	bin := restictest.Bin(t)
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
	// The old server kept a hint for its backups that's out of date: the
	// restore's own, just learned, replaces it.
	stale := strings.Repeat("e", 64)
	backup.Hints(paths).Set(filepath.Join(disk, "roostack", "lab-1", id.ID), []byte("the lab's recovery key"), stale)
	repoAt := func(keyFile, dir string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: dir, PasswordFile: keyFile, CacheDir: filepath.Join(keys, "cache")}
	}
	old := repoAt(key, filepath.Join(disk, "roostack", "lab-1", id.ID))
	other := repoAt(wrong, filepath.Join(disk, "roostack", "lab-2", "n-9999999999"))
	// A node of the lab (it has the recovery key and its own folder) made
	// a backup that says it's the controller (review M26).
	forged := repoAt(key, filepath.Join(disk, "roostack", "lab-1", "n-1111111111"))
	// Vaultwarden runs on this server; Photos on another (its data is in
	// that server's backups).
	write(t, filepath.Join(paths.InstancesDir(), "vaultwarden.yaml"), "package: vaultwarden\n")
	write(t, filepath.Join(paths.InstancesDir(), "photos.yaml"), "package: photos\nhost: n-1111111111\n")
	photos := filepath.Join(paths.ServicesDir, "photos")
	write(t, filepath.Join(photos, "old.jpg"), "from before it moved")
	for _, r := range []*backup.Repo{old, other, forged} {
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

	// The new server opens them with the hints of the restore, and every
	// command is noted, with the hint it gave restic.
	hints := &backup.KeyHints{File: backup.RestoreHintsFile(paths)}
	type command struct{ url, verb, hint string }
	var commands []command
	var cmu sync.Mutex
	restoring := func(url string) *backup.Repo {
		r := repoAt(key, url)
		r.Hints = hints
		r.Launch = func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
			c := command{url: url, verb: args[0]}
			for _, e := range env {
				if h, ok := strings.CutPrefix(e, "RESTIC_KEY_HINT="); ok {
					c.hint = h
				}
			}
			cmu.Lock()
			commands = append(commands, c)
			cmu.Unlock()
			return backup.DirectLaunch(ctx, env, name, args...)
		}
		return r
	}
	f := &Finder{OpenRepo: func(url string, env []string) *backup.Repo { return restoring(url) }}
	if _, _, err := f.Find(ctx, Source{Kind: "folder", Folder: filepath.Join(root, "nowhere")}); err == nil {
		t.Error("a missing folder")
	}
	found, unopened, err := f.Find(ctx, Source{Kind: "folder", Folder: disk})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Domain != "lab.example.com" || found[0].ServerName != "nuc" || !found[0].Controller ||
		found[0].StateDir != paths.StateDir || found[0].Folder != id.ID {
		t.Fatalf("found = %+v", found)
	}
	var refused []Unopened
	for _, u := range unopened {
		if u.Forged {
			refused = append(refused, u)
		} else if u.Problem == "" || u.Repository != other.URL {
			t.Errorf("another lab's backups can't be opened: %+v", u)
		}
	}
	if len(unopened) != 2 || len(refused) != 1 || refused[0].Repository != forged.URL ||
		!strings.Contains(refused[0].Problem, "it's in server n-1111111111's folder") {
		t.Errorf("a controller's backup in a node's folder: %+v", unopened)
	}

	// From Roostack Cloud: every lab with the recovery key, in Cloud's
	// order, each repository opened with its lab's keys; a lab whose
	// backups are gone is said so.
	var envs [][]string
	var mu sync.Mutex
	cf := &Finder{OpenRepo: func(url string, env []string) *backup.Repo {
		mu.Lock()
		envs = append(envs, env)
		mu.Unlock()
		return repoAt(key, url)
	},
		CloudAccess: func(context.Context) (cloudapi.RestoreAccess, error) {
			return cloudapi.RestoreAccess{Labs: []cloudapi.RestoreLab{
				{LabID: "lab_new", LabName: "Smith family", LinkedAt: time.Now()},
				{LabID: "lab_old", LabName: "Smith family", UnlinkedAt: time.Now().Add(-time.Hour), Repositories: []string{old.URL, forged.URL},
					Keys: cloudapi.StorageKeys{AccessKeyID: "restore-key", Region: "x"}},
			}}, nil
		}}
	cfound, cunopened, err := cf.Find(ctx, Source{Kind: "cloud"})
	if err != nil || len(cfound) != 1 || cfound[0].ServerName != "nuc" || cfound[0].Lab == nil || cfound[0].Lab.ID != "lab_old" ||
		!slices.Contains(cfound[0].Env, "AWS_ACCESS_KEY_ID=restore-key") || len(envs) != 2 || !slices.Contains(envs[0], "AWS_ACCESS_KEY_ID=restore-key") {
		t.Errorf("from Cloud: %+v %v", cfound, err)
	}
	if len(cunopened) != 2 || !cunopened[0].Forged || cunopened[1].Lab == nil || cunopened[1].Lab.ID != "lab_new" ||
		!strings.Contains(cunopened[1].Problem, "no longer keeps") {
		t.Errorf("from Cloud, not offered: %+v", cunopened)
	}
	if _, _, err := (&Finder{}).Find(ctx, Source{Kind: "cloud"}); err == nil {
		t.Error("Cloud turned off: no error")
	}
	// Restore itself refuses a controller's backup outside its folder.
	bad := found[0]
	bad.Folder = "n-1111111111"
	if err := Restore(ctx, paths, repoAt(key, bad.Repository), bad, key, activity.New().Start("restore", "x")); err == nil ||
		!strings.Contains(err.Error(), "isn't offered") {
		t.Errorf("restoring a controller's backup from another server's folder: %v", err)
	}

	log := activity.New().Start("restore", "Restoring the lab")
	if err := Restore(ctx, paths, restoring(found[0].Repository), found[0], key, log); err != nil {
		t.Fatal(err)
	}
	// Each repository the key opens was asked which key once (`key list`);
	// every other command on it said which key to try. Another lab's
	// failed once, and wasn't opened again.
	recovery := []byte("the lab's recovery key\n")
	keyOf := map[string]string{}
	for _, r := range []*backup.Repo{old, forged} {
		if keyOf[r.URL], err = r.CurrentKey(ctx); err != nil {
			t.Fatal(err)
		}
	}
	asked := map[string]int{}
	for _, c := range commands {
		switch {
		case c.url == other.URL:
			asked[c.url]++
		case c.verb == "key":
			asked[c.url]++
			if c.hint != "" {
				t.Errorf("asked with a hint: %+v", c)
			}
		case keyOf[c.url] == "" || c.hint != keyOf[c.url]:
			t.Errorf("%s on %s with hint %q, want %q", c.verb, c.url, c.hint, keyOf[c.url])
		}
	}
	if asked[old.URL] != 1 || asked[forged.URL] != 1 || asked[other.URL] != 1 || len(commands) < 6 {
		t.Errorf("asked %v in %d commands", asked, len(commands))
	}
	// The restored lab keeps them, over its own from before.
	for _, r := range []*backup.Repo{old, forged} {
		if got := backup.Hints(paths).Get(r.URL, recovery); got != keyOf[r.URL] {
			t.Errorf("the restored lab's hint for %s = %q, want %q", r.URL, got, keyOf[r.URL])
		}
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

// A restore puts back only Roostack's places: the state folder, the
// secrets store, and each service's own folders. A backup listing
// anything else (made to look like the controller's, say) is refused
// before anything is put back (review M26).
func TestRestoreOnlyRoostacksPlaces(t *testing.T) {
	bin := resticBin(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), ServicesDir: filepath.Join(root, "services"),
		MediaDir: filepath.Join(root, "media"), HomeDir: filepath.Join(root, "home")}
	key := filepath.Join(t.TempDir(), "recovery-key")
	write(t, key, "the lab's recovery key\n")
	write(t, backup.StateDumpFile(paths), "the lab's database")
	write(t, filepath.Join(paths.InstancesDir(), "vaultwarden.yaml"), "package: vaultwarden\n")
	id := nodeid.Identity{ID: "n-0123456789", Name: "nuc"}
	if err := id.Save(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(root, "etc", "cron.d")
	vault, media, other := filepath.Join(paths.ServicesDir, "vaultwarden"), filepath.Join(paths.MediaDir, "vaultwarden", "files"),
		filepath.Join(paths.ServicesDir, "photos")
	// backupOf backs up the old server's files, then removes them (all but
	// its state) as from a new server.
	backupOf := func(server []string, service ...string) (*backup.Repo, Found) {
		write(t, filepath.Join(elsewhere, "job"), "* * * * * root sh -c 'anything'")
		for _, d := range []string{vault, media, other} {
			write(t, filepath.Join(d, "data"), d)
		}
		defer func() {
			for _, d := range []string{elsewhere, paths.ServicesDir, paths.MediaDir} {
				os.RemoveAll(d)
			}
		}()
		dir := filepath.Join(t.TempDir(), "roostack", "lab-1", id.ID)
		r := &backup.Repo{Bin: bin, URL: dir, PasswordFile: key, CacheDir: filepath.Join(root, "cache")}
		if err := r.Init(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Backup(ctx, backup.BackupOptions{Paths: server, Tags: []string{backup.TagRoostack, backup.TagServer}}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Backup(ctx, backup.BackupOptions{Paths: service, Tags: []string{backup.TagRoostack, backup.ServiceTag("vaultwarden")}}); err != nil {
			t.Fatal(err)
		}
		fd, err := (&Finder{OpenRepo: func(string, []string) *backup.Repo { return r }}).open(ctx, place{url: dir})
		if err != nil || fd.Folder != id.ID || !fd.Controller {
			t.Fatalf("finding it: %+v %v", fd, err)
		}
		return r, *fd
	}
	for name, c := range map[string]struct{ server, service []string }{
		"a server's backup with another folder":  {[]string{paths.StateDir, elsewhere}, []string{vault}},
		"a service's backup with another's data": {[]string{paths.StateDir}, []string{vault, other}},
	} {
		r, fd := backupOf(c.server, c.service...)
		err := Restore(ctx, paths, r, fd, key, activity.New().Start("restore", name))
		if err == nil || !strings.Contains(err.Error(), "isn't one of the places Roostack restores") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(elsewhere); err == nil {
			t.Errorf("%s: restored a folder outside Roostack's places", name)
		}
		if _, err := os.Stat(other); err == nil {
			t.Errorf("%s: restored another service's folder", name)
		}
	}
	// What Roostack backs up is restored: a service's folders under both
	// the services' and the media folder.
	r, fd := backupOf([]string{paths.StateDir}, vault, media)
	if err := Restore(ctx, paths, r, fd, key, activity.New().Start("restore", "restore")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(media, "data")); got != media {
		t.Errorf("media folder = %q", got)
	}
}

func TestFolderServer(t *testing.T) {
	for repo, want := range map[string]string{
		"/mnt/backup/roostack/lab-1/n-0123456789":                  "n-0123456789",
		"s3:https://s3.wasabisys.com/b/labs/lab_x/n-0123456789":    "n-0123456789",
		"s3:https://s3.example.com/b/roostack/lab-1/n-0123456789/": "n-0123456789",
		"rest:http://10.0.0.2:8001/n-0123456789/lab-1":             "n-0123456789", // a lab's own backup store
		"rest:http://10.0.0.2:8001/n-1111111111/n-0123456789":      "n-1111111111", // the store's user is the first
		"rest:http://127.0.0.1:8002/g3/n-0123456789":               "n-0123456789", // a friend's lab
		"rest:https://roostack:s3cret@backup.example.com/lab":      "",
		"/mnt/backup":  "",
		"n-0123456789": "n-0123456789",
		"/mnt/backup/roostack/lab-1/n-0123456789/extra": "n-0123456789", // inside its folder
	} {
		if got := FolderServer(repo); got != want {
			t.Errorf("FolderServer(%q) = %q, want %q", repo, got, want)
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
