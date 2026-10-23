package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/backup/restictest"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
	"roostack.cloud/roostack/internal/usererr"
)

// A new key for an account: on Backblaze, a key that can make keys gives
// each server one of its own; elsewhere every server gets the new key
// (SPEC.md §10.10).
func TestChangeS3Key(t *testing.T) {
	bin := restictest.Bin(t)
	// A fake Backblaze: the old key came from the website (it can't make
	// keys), the new one from the command-line tool.
	var created, deleted []string
	mux := http.NewServeMux()
	var api *httptest.Server
	mux.HandleFunc("GET /b2api/v2/b2_authorize_account", func(w http.ResponseWriter, r *http.Request) {
		caps := map[string][]string{"old-id old-key": {"listBuckets", "listFiles", "readFiles", "writeFiles", "deleteFiles"},
			"new-id new-key": {"listBuckets", "listFiles", "readFiles", "writeFiles", "deleteFiles", "writeKeys", "deleteKeys", "listKeys"}}
		id, key, _ := r.BasicAuth()
		c, ok := caps[id+" "+key]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": "wrong key"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"accountId": "acct", "apiUrl": api.URL, "authorizationToken": "tok",
			"allowed": map[string]any{"capabilities": c}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_list_buckets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"buckets": []any{map[string]any{"bucketId": "bkt1", "lifecycleRules": []any{}}}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_create_key", func(w http.ResponseWriter, r *http.Request) {
		id := "scoped-" + string(rune('0'+len(created)+1))
		created = append(created, id)
		json.NewEncoder(w).Encode(map[string]string{"applicationKeyId": id, "applicationKey": id + "-secret"})
	})
	mux.HandleFunc("POST /b2api/v2/b2_delete_key", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		deleted = append(deleted, in["applicationKeyId"])
		w.Write([]byte("{}"))
	})
	api = httptest.NewServer(mux)
	defer api.Close()

	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	st := storetest.Open(t)
	m.BackupStore = st
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	m.NewB2 = func(id, key string) *b2.Client { return &b2.Client{KeyID: id, Key: key, API: api.URL} }
	ctx := context.Background()
	wait := func(key string) activity.Snapshot {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if l, _ := m.Activity.Latest(key); !l.Running() {
				return l
			}
		}
		t.Fatalf("%s still running", key)
		return activity.Snapshot{}
	}

	// The vps backs up to Backblaze with a key from the website: it gets
	// the account's key.
	if err := m.AddS3Provider(ctx, "Backblaze", S3Provider{Endpoint: "https://s3.us-west-004.backblazeb2.com",
		Bucket: "backups", AccessKey: "old-id", SecretKey: "old-key"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "provider:1"); err != nil {
		t.Fatal(err)
	}
	if l := wait(activity.BackupSetupFor("n-0000000002")); l.Err != "" {
		t.Fatalf("setup: %s", l.Err)
	}
	if env := strings.Join(vps.settings.dest.Env, " "); !strings.Contains(env, "AWS_ACCESS_KEY_ID=old-id") {
		t.Fatalf("the vps should have the account's key: %s", env)
	}
	if a := m.BackupAccess(ctx, "n-0000000002", "provider:1"); !a.CanDelete || !a.B2 {
		t.Errorf("access = %+v", a)
	}

	// A wrong key is refused, and nothing changes.
	err := m.ChangeS3Key(ctx, "provider:1", "new-id", "typo")
	var fe *usererr.Error
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "Backblaze didn't accept this key") {
		t.Errorf("wrong key: %v", err)
	}
	if err := m.ChangeS3Key(ctx, "provider:1", " ", ""); err == nil {
		t.Error("an empty key")
	}
	if err := m.ChangeS3Key(ctx, "provider:9", "new-id", "new-key"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown account: %v", err)
	}

	// The vps can't be reached: it keeps the old key, and the key made for
	// it is deleted again.
	vps.settings.destErr = errors.New("connection refused")
	if err := m.ChangeS3Key(ctx, "provider:1", "new-id", "new-key"); err != nil {
		t.Fatal(err)
	}
	if l := wait(activity.BackupKeyFor("1")); !strings.Contains(l.Err, "couldn't reach vps, which still has the old key") {
		t.Errorf("err = %q", l.Err)
	}
	if !slices.Equal(deleted, []string{"scoped-1"}) {
		t.Errorf("deleted = %v", deleted)
	}
	if tg, _ := m.targetAt(ctx, "n-0000000002", "provider:1"); tg.ScopedKey != "" {
		t.Errorf("a key it didn't get is recorded: %+v", tg)
	}

	// Again, reachable: the vps gets a key of its own, which can't erase,
	// and the controller keeps the new account key.
	vps.settings.destErr = nil
	if err := m.ChangeS3Key(ctx, "provider:1", "new-id", "new-key"); err != nil {
		t.Fatal(err)
	}
	l := wait(activity.BackupKeyFor("1"))
	if l.Err != "" {
		t.Fatalf("err = %s", l.Err)
	}
	if log := strings.Join(l.Lines, "\n"); !strings.Contains(log, "Every server backing up to Backblaze has the new key") {
		t.Errorf("log:\n%s", log)
	}
	d := vps.settings.dest
	if env := strings.Join(d.Env, " "); !strings.Contains(env, "AWS_ACCESS_KEY_ID=scoped-2") || !d.PrunedByController ||
		!strings.HasSuffix(d.Repository, "/n-0000000002") {
		t.Errorf("the vps got %+v", d)
	}
	if tg, _ := m.targetAt(ctx, "n-0000000002", "provider:1"); tg.ScopedKey != "scoped-2" {
		t.Errorf("target = %+v", tg)
	}
	if a := m.BackupAccess(ctx, "n-0000000002", "provider:1"); a.CanDelete || !a.HidesOnly || !a.B2 {
		t.Errorf("access = %+v", a)
	}
	if p, _ := m.provider(ctx, "1"); !strings.Contains(p.Config, `"new-id"`) {
		t.Errorf("provider = %s", p.Config)
	}

	// Once more: the vps keeps the key it has.
	if err := m.ChangeS3Key(ctx, "provider:1", "new-id", "new-key"); err != nil {
		t.Fatal(err)
	}
	l = wait(activity.BackupKeyFor("1"))
	if log := strings.Join(l.Lines, "\n"); !strings.Contains(log, "vps keeps the key of its own it has") || len(created) != 2 {
		t.Errorf("created %v; log:\n%s", created, log)
	}

	// A copy at another provider: the vps gets the new key itself, and
	// keeps the copy's title.
	if err := m.AddS3Provider(ctx, "Wasabi", S3Provider{Endpoint: "https://s3.wasabisys.com", Bucket: "b",
		AccessKey: "wasabi-id", SecretKey: "wasabi-key"}); err != nil {
		t.Fatal(err)
	}
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:2", Repository: "s3:https://s3.wasabisys.com/b/x"})
	if err := m.ChangeS3Key(ctx, "provider:2", "wasabi-2", "secret-2"); err != nil {
		t.Fatal(err)
	}
	if l := wait(activity.BackupKeyFor("2")); l.Err != "" {
		t.Fatal(l.Err)
	}
	c := vps.settings.copies
	if len(c) != 1 || c[0].Name != "provider:2" || c[0].Title != "Wasabi" || c[0].Key != "" ||
		!slices.Contains(c[0].Env, "AWS_ACCESS_KEY_ID=wasabi-2") {
		t.Errorf("copies = %+v", c)
	}
}
