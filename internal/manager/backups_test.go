package manager

import (
	"context"
	"encoding/json"
	"github.com/mylab/mylab/internal/b2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/store"
)

func TestBackupDestinationOnAServersDisk(t *testing.T) {
	bin := os.Getenv("MYLAB_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set MYLAB_RESTIC_BIN")
		}
	}
	m, _ := newManager(t)
	m.Lab.NodeID, m.Lab.OverlayAddr = "n-0000000001", "100.96.0.1"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	local(m).Activity = m.Activity
	st, err := store.Open(filepath.Join(t.TempDir(), "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	// The repository really lives in a folder; the URLs are checked below.
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	var urls []string
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		urls = append(urls, url)
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	ctx := context.Background()

	choices, _ := m.BackupChoices(ctx)
	if len(choices) != 2 || choices[0].Ref != "server:n-0000000001" || choices[0].Title != "nuc's disk" {
		t.Fatalf("choices = %+v", choices)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:nope"); err == nil {
		t.Error("unknown choice")
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	var log activity.Snapshot
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if log, _ = m.Activity.Latest(activity.BackupSetup); !log.Running() {
			break
		}
	}
	if log.Err != "" || log.Running() {
		t.Fatalf("setup: %q\n%s", log.Err, strings.Join(log.Lines, "\n"))
	}

	// The vps got its repository on nuc's store, its own credentials and key.
	d := vps.settings.dest
	if d == nil || !strings.HasPrefix(d.Repository, "rest:http://100.96.0.1:8000/n-0000000002/lab-") ||
		!strings.Contains(strings.Join(d.Env, " "), "RESTIC_REST_USERNAME=n-0000000002") || d.Key == "" {
		t.Fatalf("destination = %+v", d)
	}
	if len(urls) == 0 || !strings.Contains(urls[0], ":8001/n-0000000002/") {
		t.Errorf("the controller creates it through the admin store: %v", urls)
	}
	// Its key opens the repository; the recovery key does too.
	server := filepath.Join(dir, "server")
	os.WriteFile(server, []byte(d.Key+"\n"), 0o600)
	r := &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: server, CacheDir: filepath.Join(dir, "cache")}
	if _, err := r.Snapshots(ctx); err != nil {
		t.Errorf("server key: %v", err)
	}
	// nuc's store lets the vps and the controller in.
	stg := m.storageFor(ctx, "n-0000000001")
	if stg == nil || stg.Users["n-0000000002"] == "" || stg.Admin == "" {
		t.Errorf("storage = %+v", stg)
	}
	if m.storageFor(ctx, "n-0000000002") != nil {
		t.Error("the vps stores nothing")
	}
	if !d.PrunedByController {
		t.Error("the vps only adds backups; the controller prunes")
	}
	// The weekly prune opens it through the admin store with the recovery key.
	urls = nil
	if err := m.PruneJob().Run(ctx, nil); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], ":8001/n-0000000002/") {
		t.Errorf("prune used %v", urls)
	}
}

func TestBackupDestinationB2ScopedKey(t *testing.T) {
	bin := os.Getenv("MYLAB_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set MYLAB_RESTIC_BIN")
		}
	}
	// A fake Backblaze: authorises the account key, makes and deletes keys.
	var created map[string]any
	var deleted []string
	mux := http.NewServeMux()
	var api *httptest.Server
	mux.HandleFunc("GET /b2api/v2/b2_authorize_account", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"accountId": "acct", "apiUrl": api.URL, "authorizationToken": "tok",
			"allowed": map[string]any{"capabilities": []string{"writeKeys", "deleteKeys", "listBuckets"}}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_list_buckets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"buckets": []any{map[string]string{"bucketId": "bkt1"}}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_create_key", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&created)
		json.NewEncoder(w).Encode(map[string]string{"applicationKeyId": "scoped-id", "applicationKey": "scoped-secret"})
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
	st, err := store.Open(filepath.Join(t.TempDir(), "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	m.NewB2 = func(id, key string) *b2.Client { return &b2.Client{KeyID: id, Key: key, API: api.URL} }
	ctx := context.Background()
	if err := m.AddS3Provider(ctx, "Backblaze", S3Provider{Endpoint: "https://s3.us-west-004.backblazeb2.com",
		Bucket: "backups", AccessKey: "master-id", SecretKey: "master-key"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "provider:1"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if l, _ := m.Activity.Latest(activity.BackupSetup); !l.Running() {
			if l.Err != "" {
				t.Fatalf("setup: %s", l.Err)
			}
			break
		}
	}
	d := vps.settings.dest
	env := strings.Join(d.Env, " ")
	if d == nil || !strings.Contains(env, "AWS_ACCESS_KEY_ID=scoped-id") || strings.Contains(env, "master") {
		t.Fatalf("the vps must get only its scoped key: %+v", d)
	}
	labID, _ := st.LabID(ctx)
	if created["namePrefix"] != "mylab/"+labID+"/n-0000000002/" || created["bucketId"] != "bkt1" {
		t.Errorf("key made with %v", created)
	}
	if !strings.HasSuffix(d.Repository, "backblazeb2.com/backups/mylab/"+labID+"/n-0000000002") {
		t.Errorf("repository = %q", d.Repository)
	}
	// Removing the server deletes its key.
	if err := m.RemoveServer(ctx, "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "scoped-id" {
		t.Errorf("deleted = %v", deleted)
	}
}
