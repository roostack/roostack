package manager

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/b2"
	"github.com/mylab/mylab/internal/labnet"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// An extra copy of the vps's backups on nuc's disk: its own repository,
// key and store user, never at the main destination.
func TestBackupCopyOnAnotherServersDisk(t *testing.T) {
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
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	ctx := context.Background()
	var fe *FormError
	if err := m.AddBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); !errors.As(err, &fe) {
		t.Errorf("a copy needs a main destination first: %v", err)
	}
	// The vps backs up to a provider.
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:9", Repository: "s3:https://s3.example.com/b/x"})
	if err := m.AddBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	var log activity.Snapshot
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if log, _ = m.Activity.Latest(activity.BackupSetup); !log.Running() {
			break
		}
	}
	if log.Err != "" {
		t.Fatalf("setup: %q\n%s", log.Err, strings.Join(log.Lines, "\n"))
	}
	vps.settings.mu.Lock()
	copies := vps.settings.copies
	vps.settings.mu.Unlock()
	if len(copies) != 1 || copies[0].Name != "server:n-0000000001" || copies[0].Title != "nuc's disk" || copies[0].Key == "" ||
		!strings.HasPrefix(copies[0].Repository, "rest:http://100.96.0.1:8000/n-0000000002/lab-") {
		t.Fatalf("copies = %+v", copies)
	}
	if vps.settings.dest != nil {
		t.Error("the main destination is unchanged")
	}
	if stg := m.storageFor(ctx, "n-0000000001"); stg == nil || stg.Users["n-0000000002"] == "" {
		t.Errorf("nuc's store should let the vps in: %+v", stg)
	}
	if got := m.BackupCopiesOf(ctx, "n-0000000002"); len(got) != 1 || got[0].Title != "nuc's disk" {
		t.Errorf("copies of the vps = %+v", got)
	}
	if err := m.AddBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); !errors.As(err, &fe) {
		t.Errorf("the same copy twice: %v", err)
	}
	if err := m.AddBackupCopy(ctx, "n-0000000002", "provider:9"); !errors.As(err, &fe) {
		t.Errorf("a copy at the main destination: %v", err)
	}

	// Stopping it tells the vps, and nuc's store forgets it.
	if err := m.RemoveBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	vps.settings.mu.Lock()
	defer vps.settings.mu.Unlock()
	if len(vps.settings.copies) != 0 || len(m.BackupCopiesOf(ctx, "n-0000000002")) != 0 {
		t.Errorf("after stopping: %+v", vps.settings.copies)
	}
	if m.storageFor(ctx, "n-0000000001") != nil {
		t.Error("nuc's store has no users left, so it stops")
	}
}

func TestRemoteStorageFollowsRename(t *testing.T) {
	m, _ := newManager(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	ctx := context.Background()
	cfg, _ := json.Marshal(RemoteStorage{HostName: "lab.example.com", Quota: 5 << 30})
	id, _ := st.AddBackupProvider(ctx, store.BackupProvider{Kind: "remote", Name: "lab.example.com's storage", Config: string(cfg)})
	r := m.RemoteStorages(ctx)[0]
	m.renameRemote(ctx, r, "Mum's lab")
	ps, _ := st.BackupProviders(ctx)
	if ps[0].ID != id || ps[0].Name != "Mum's lab's storage" || m.RemoteStorages(ctx)[0].HostName != "Mum's lab" ||
		m.RemoteStorages(ctx)[0].Quota != 5<<30 {
		t.Errorf("after the host's rename: %+v", ps[0])
	}
}

type fakeWG struct{ peers int }

func (f *fakeWG) ApplyWireGuard(_ context.Context, wg agent.WireGuard) (string, error) {
	f.peers = len(wg.Peers)
	return "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", nil
}

// A removed guest lab stops backing up at once, keeps its link (to hear
// about it) and its data for the grace period, then both go.
func TestRemovedGuestGrace(t *testing.T) {
	m, _ := newManager(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wg := &fakeWG{}
	m.GuestRecords = st
	m.GuestHub = &labnet.GuestHub{LabName: "nuc's lab", Store: st, Agent: wg, Log: m.Log, Now: time.Now}
	var deleted []string
	m.RemoveGuestDir = func(_ context.Context, user string) error { deleted = append(deleted, user); return nil }
	ctx := context.Background()
	inv, err := m.GuestHub.NewInvite(ctx, "Sam's lab", 10<<30, "203.0.113.7:51821")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinGuest(ctx, labnet.HashToken(inv.Token), "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", time.Now()); err != nil {
		t.Fatal(err)
	}
	guests, _ := st.Guests(ctx, time.Now())
	g := guests[0]
	if stores, _ := m.guestStores(ctx); len(stores) != 1 {
		t.Fatal("a guest's store runs")
	}
	if err := m.RemoveGuest(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if stores, _ := m.guestStores(ctx); len(stores) != 0 {
		t.Error("a removed guest's store stops")
	}
	m.GuestHub.SyncPeers(ctx)
	if wg.peers != 1 {
		t.Error("its link stays, so it can be told")
	}
	if err := m.PurgeRemovedGuests(ctx, time.Now().Add(GuestGrace-time.Hour)); err != nil || len(deleted) != 0 {
		t.Fatalf("within the grace period: %v %v", err, deleted)
	}
	if err := m.PurgeRemovedGuests(ctx, time.Now().Add(GuestGrace+time.Hour)); err != nil || !slices.Equal(deleted, []string{g.User()}) {
		t.Fatalf("after it: %v %v", err, deleted)
	}
	if guests, _ := st.Guests(ctx, time.Now()); len(guests) != 0 || wg.peers != 0 {
		t.Errorf("gone: %+v peers %d", guests, wg.peers)
	}
	if err := m.DeleteRemovedGuest(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("already deleted: %v", err)
	}
}
