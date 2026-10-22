package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

func TestBackupDestinationOnAServersDisk(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
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
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
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
	hints := &backup.KeyHints{File: filepath.Join(dir, "hints.json")}
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		urls = append(urls, url)
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache"),
			Hints: hints}
	}
	ctx := context.Background()

	choices, _ := m.BackupChoices(ctx)
	if len(choices) != 2 || choices[0].Ref != "server:n-0000000001" || choices[0].Title != "nuc's disk" {
		t.Fatalf("choices = %+v", choices)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:nope"); err == nil {
		t.Error("unknown choice")
	}
	// Setting up that fails leaves nothing behind.
	vps.settings.destErr = errors.New("the vps is unreachable")
	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	if l := waitDone(t, m, activity.BackupSetupFor("n-0000000002")); l.Err == "" {
		t.Fatal("setting up should have failed")
	}
	if _, _, ok := m.BackupTargetOf(ctx, "n-0000000002"); ok || m.storageFor(ctx, "n-0000000001") != nil && m.storageFor(ctx, "n-0000000001").Users["n-0000000002"] != "" {
		t.Error("a failed setup is forgotten, and its store user with it")
	}
	vps.settings.destErr, urls = nil, nil

	if err := m.SetBackupDestination(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	if log := waitDone(t, m, activity.BackupSetupFor("n-0000000002")); log.Err != "" {
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
	// It's told its key's ID for the key hint, and the controller noted
	// the recovery key's when it made the repository (ASSUMPTIONS O6).
	if id, _ := r.CurrentKey(ctx); id == "" || d.KeyHint != id {
		t.Errorf("key hint %q, the server's key is %q", d.KeyHint, id)
	}
	recoveryKey, _ := os.ReadFile(recovery)
	if id := hints.Get(filepath.Join(dir, "repo"), recoveryKey); id == "" || id == d.KeyHint {
		t.Errorf("the recovery key's hint = %q", id)
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
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
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
	// The bucket erases hidden files a day after they're hidden.
	mux.HandleFunc("POST /b2api/v2/b2_list_buckets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"buckets": []any{map[string]any{"bucketId": "bkt1",
			"lifecycleRules": []any{map[string]any{"fileNamePrefix": "", "daysFromHidingToDeleting": 1}}}}})
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
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
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
	if l := waitDone(t, m, activity.BackupSetupFor("n-0000000002")); l.Err != "" {
		t.Fatalf("setup: %s", l.Err)
	}
	d := vps.settings.dest
	env := strings.Join(d.Env, " ")
	if d == nil || !strings.Contains(env, "AWS_ACCESS_KEY_ID=scoped-id") || strings.Contains(env, "master") {
		t.Fatalf("the vps must get only its scoped key: %+v", d)
	}
	labID, _ := st.LabID(ctx)
	if created["namePrefix"] != "roostack/"+labID+"/n-0000000002/" || created["bucketId"] != "bkt1" {
		t.Errorf("key made with %v", created)
	}
	if !strings.HasSuffix(d.Repository, "backblazeb2.com/backups/roostack/"+labID+"/n-0000000002") {
		t.Errorf("repository = %q", d.Repository)
	}
	// The key adds backups but can't erase them (SPEC.md §10.10): what the
	// server deletes is only hidden, and the bucket keeps that for a day.
	caps, _ := created["capabilities"].([]any)
	if len(caps) == 0 || slices.Contains(caps, any("deleteFiles")) {
		t.Errorf("the server's key may delete: %v", caps)
	}
	if a := m.BackupAccess(ctx, "n-0000000002", "provider:1"); a.CanDelete || !a.HidesOnly || a.KeepsHidden != 1 || a.Safe() {
		t.Errorf("access = %+v", a)
	}
	setup, _ := m.Activity.Latest(activity.BackupSetupFor("n-0000000002"))
	if log := strings.Join(setup.Lines, "\n"); !strings.Contains(log, "can add backups but not erase them") ||
		!strings.Contains(log, "erases hidden files after a day") {
		t.Errorf("setup log:\n%s", log)
	}
	if c := m.SharedCredentials(ctx, "n-0000000002"); len(c) != 0 {
		t.Errorf("a scoped key isn't shared: %v", c)
	}

	// A copy at another provider, with the account's key: it can delete,
	// and it's shared.
	if err := m.AddS3Provider(ctx, "Wasabi", S3Provider{Endpoint: "https://s3.wasabisys.com", Bucket: "b",
		AccessKey: "wasabi-id", SecretKey: "wasabi-key"}); err != nil {
		t.Fatal(err)
	}
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:2", Repository: "s3:https://s3.wasabisys.com/b/x"})
	if a := m.BackupAccess(ctx, "n-0000000002", "provider:2"); !a.CanDelete || a.Safe() {
		t.Errorf("account key access = %+v", a)
	}
	want := []SharedCredential{{What: "the account key for Wasabi", Anywhere: true}}
	if c := m.SharedCredentials(ctx, "n-0000000002"); !slices.Equal(c, want) {
		t.Errorf("shared = %v", c)
	}
	// Another server backed up to the vps's disk.
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000003", Provider: "server:n-0000000002", StoreHost: "n-0000000002",
		Repository: "rest:http://100.96.0.2:8000/n-0000000003/x"})

	// Removing the server deletes its key, says what it still holds, and
	// forgets backups on its disk (nothing can reach them now); its own
	// backups at Backblaze stay, still read and pruned with the account's
	// key.
	if err := m.RemoveServer(ctx, "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "scoped-id" {
		t.Errorf("deleted = %v", deleted)
	}
	removal, _ := m.Activity.Latest(activity.BackupSetupFor("n-0000000002"))
	if log := strings.Join(removal.Lines, "\n"); !strings.Contains(log, "still holds the account key for Wasabi") ||
		!strings.Contains(log, "n-0000000003's main backup was on") {
		t.Errorf("removal log:\n%s", log)
	}
	targets, _ := st.BackupTargets(ctx)
	if len(targets) != 1 || targets[0].ServerID != "n-0000000002" || targets[0].Provider != "provider:1" {
		t.Errorf("targets = %+v", targets)
	}
	if copies, _ := st.BackupCopies(ctx); len(copies) != 0 {
		t.Errorf("copies = %+v", copies)
	}
	// The server whose main backup was there is told until it has a new
	// one.
	if l, ok := m.LostMainBackup(ctx, "n-0000000003"); !ok || l.HostName != "vps" {
		t.Errorf("lost main backup = %+v, %t", l, ok)
	}
	if _, ok := m.LostMainBackup(ctx, "n-0000000002"); ok {
		t.Error("the removed server's own main backup is elsewhere")
	}
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000003", Provider: "provider:2", Repository: "s3:https://s3.wasabisys.com/b/y"})
	if _, ok := m.LostMainBackup(ctx, "n-0000000003"); ok {
		t.Error("a new main backup ends it")
	}
}

// An extra copy of the vps's backups on nuc's disk: its own repository,
// key and store user, never at the main destination.
func TestBackupCopyOnAnotherServersDisk(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
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
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
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
	var fe *usererr.Error
	if err := m.AddBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); !errors.As(err, &fe) {
		t.Errorf("a copy needs a main destination first: %v", err)
	}
	// The vps backs up to a provider.
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:9", Repository: "s3:https://s3.example.com/b/x"})
	if err := m.AddBackupCopy(ctx, "n-0000000002", "server:n-0000000001"); err != nil {
		t.Fatal(err)
	}
	log := waitDone(t, m, activity.BackupSetupFor("n-0000000002"))
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
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
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

// A removed guest lab stops backing up at once, keeps its link (to hear
// about it) and its data for the grace period, then both go.
func TestRemovedGuestGrace(t *testing.T) {
	m, _ := newManager(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	local(m).Links = fakeLinks{}
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.GuestRecords = st
	m.GuestHub = &labnet.GuestHub{LabName: func() string { return "nuc's lab" }, Store: st, Agent: m.GuestLinkAgent(), Log: m.Log, Now: time.Now}
	ctx := context.Background()
	// The vps offers the storage.
	if err := m.SetGuestServer(ctx, "n-0000000002"); err != nil || m.HostsGuests(ctx) {
		t.Fatal(err)
	}
	inv, err := m.GuestHub.NewInvite(ctx, "Sam's lab", 10<<30, "203.0.113.7:51821")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinGuest(ctx, labnet.HashToken(inv.Token), "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", time.Now()); err != nil {
		t.Fatal(err)
	}
	guests, _ := st.Guests(ctx, time.Now())
	g := guests[0]
	// The vps's disk: 500 of 1000 GB free, 1 GB used by Sam, 10 GB
	// promised, 100 GB kept free: 391 GB more to offer. A quota change
	// doesn't count the guest's own quota.
	if sp, ok := m.GuestSpaceLeft(ctx); !ok || sp.Left != 391<<30 || sp.Promised != 9<<30 || sp.Free != 500<<30 {
		t.Errorf("space %+v, %v", sp, ok)
	}
	if err := m.SetGuestQuota(ctx, g.ID, 402<<30); err == nil || !strings.Contains(err.Error(), "doesn't fit") {
		t.Errorf("a quota that doesn't fit: %v", err)
	}
	if err := m.SetGuestQuota(ctx, g.ID, 401<<30); err != nil {
		t.Errorf("one that does: %v", err)
	}
	if stores, _ := m.guestStores(ctx); len(stores) != 1 {
		t.Fatal("a guest's store runs")
	}
	m.Apply()
	m.Wait()
	vps.mu.Lock()
	if len(vps.last.Guests) != 1 || vps.last.GuestAddr == "" || len(vps.guestLink) == 0 || vps.guestLink[0].Iface() != agent.GuestsInterface {
		t.Errorf("the vps runs the guest's store and link: %+v %d", vps.last.Guests, len(vps.guestLink))
	}
	vps.mu.Unlock()
	if err := m.SetGuestServer(ctx, "n-0000000001"); err == nil {
		t.Error("storage can't move while a lab stores there")
	}
	if err := m.RemoveGuest(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if stores, _ := m.guestStores(ctx); len(stores) != 0 {
		t.Error("a removed guest's store stops")
	}
	peers := func() int {
		vps.mu.Lock()
		defer vps.mu.Unlock()
		return len(vps.guestLink[len(vps.guestLink)-1].Peers)
	}
	m.GuestHub.SyncPeers(ctx)
	if peers() != 1 {
		t.Error("its link stays, so it can be told")
	}
	if err := m.PurgeRemovedGuests(ctx, time.Now().Add(GuestGrace-time.Hour)); err != nil || len(vps.deleted) != 0 {
		t.Fatalf("within the grace period: %v %v", err, vps.deleted)
	}
	if err := m.PurgeRemovedGuests(ctx, time.Now().Add(GuestGrace+time.Hour)); err != nil || !slices.Equal(vps.deleted, []string{g.User()}) {
		t.Fatalf("after it: %v %v", err, vps.deleted)
	}
	if guests, _ := st.Guests(ctx, time.Now()); len(guests) != 0 || peers() != 0 {
		t.Errorf("gone: %+v peers %d", guests, peers())
	}
	if err := m.SetGuestServer(ctx, "n-0000000001"); err != nil || !m.HostsGuests(ctx) {
		t.Errorf("with nobody storing, storage can move back: %v", err)
	}
	if err := m.DeleteRemovedGuest(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("already deleted: %v", err)
	}
}

type fakeLinks struct{}

func (fakeLinks) ApplyLinks(context.Context, agent.Links) error { return nil }

func (fakeLinks) ApplyWireGuard(context.Context, agent.WireGuard) (string, error) {
	return "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", nil
}
func (fakeLinks) WireGuardStatus(context.Context, string) (agent.WireGuardStatus, error) {
	return agent.WireGuardStatus{}, nil
}
func (fakeLinks) RemoveWireGuard(context.Context, string) error { return nil }

type fakeDriveAgent struct{ dir string }

func (f fakeDriveAgent) Drives(context.Context) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "system", Kind: "system", Usable: true}, {ID: "uuid:64B1", Kind: "usb", Name: "BACKUP", Usable: true}}, nil
}
func (f fakeDriveAgent) UseDrive(_ context.Context, id string) (agent.UsedDrive, error) {
	return agent.UsedDrive{Dir: f.dir, UUID: "64B1"}, nil
}
func (f fakeDriveAgent) PrepareBackupFolder(_ context.Context, dir string) (string, error) {
	return dir + "/roostack", nil
}

// This server backs up to its USB drive (backup 1), then a copy on vps's
// disk is made backup 1 instead: both repositories stay, swapped.
func TestBackupOnADriveAndPromote(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	m, _ := newManager(t)
	m.Lab.NodeID, m.Lab.OverlayAddr = "n-0000000001", "100.96.0.1"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	here := &fakeServerSettings{}
	local(m).Host = here
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	local(m).Activity = m.Activity
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	mount := t.TempDir()
	m.Drives = fakeDriveAgent{dir: mount}
	keys := t.TempDir()
	recovery := filepath.Join(keys, "recovery")
	created := false
	m.HasRecoveryKey = func() bool { _, err := os.Stat(recovery); return err == nil }
	m.EnsureRecoveryKey = func() error { created = true; return os.WriteFile(recovery, []byte("key\n"), 0o600) }
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		if !strings.HasPrefix(url, "/") {
			url = filepath.Join(keys, "other-repo") // vps's store, as a folder
		}
		return &backup.Repo{Bin: bin, URL: url, PasswordFile: recovery, CacheDir: filepath.Join(keys, "cache")}
	}
	ctx := context.Background()
	wait := func() {
		t.Helper()
		if log := waitDone(t, m, activity.BackupSetupFor("n-0000000001")); log.Err != "" {
			t.Fatalf("setup: %s\n%s", log.Err, strings.Join(log.Lines, "\n"))
		}
	}
	var fe *usererr.Error
	if err := m.SetBackupDestination(ctx, "n-0000000002", "drive:uuid:64B1"); !errors.As(err, &fe) {
		t.Errorf("another server can't use this server's drive: %v", err)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000001", "drive:uuid:64B1"); err != nil {
		t.Fatal(err)
	}
	wait()
	here.mu.Lock()
	d := here.dest
	here.mu.Unlock()
	if !created || d == nil || !strings.HasPrefix(d.Repository, filepath.Join(mount, "roostack")+"/") || d.Drive.UUID != "64B1" ||
		d.Drive.Name != "USB drive BACKUP" || d.Key != "" {
		t.Fatalf("the first backup creates the recovery key and goes on the drive: %v %+v", created, d)
	}
	if _, title, _ := m.BackupTargetOf(ctx, "n-0000000001"); title != "USB drive BACKUP" {
		t.Errorf("title = %q", title)
	}

	// A copy on vps's disk, then made backup 1.
	if err := m.AddBackupCopy(ctx, "n-0000000001", "server:n-0000000002"); err != nil {
		t.Fatal(err)
	}
	wait()
	if err := m.PromoteBackupCopy(ctx, "n-0000000001", "server:n-0000000002"); err != nil {
		t.Fatal(err)
	}
	main, _, _ := m.BackupTargetOf(ctx, "n-0000000001")
	copies := m.BackupCopiesOf(ctx, "n-0000000001")
	if main.Provider != "server:n-0000000002" || len(copies) != 1 || copies[0].Ref != "drive:uuid:64B1" ||
		here.promoted != "server:n-0000000002 <-> drive:uuid:64B1" {
		t.Errorf("after promoting: main %+v, copies %+v, server told %q", main, copies, here.promoted)
	}
}

// A repository that's locked (its server still backing up) is tried again
// later, up to pruneTries in all; the others aren't.
func TestPruneRetriesLocked(t *testing.T) {
	m, _ := newManager(t)
	m.Activity = activity.New()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	dir := t.TempDir()
	// restic exits 11 when it can't lock the repository.
	locked := filepath.Join(dir, "locked-restic")
	os.WriteFile(locked, []byte("#!/bin/sh\nexit 11\n"), 0o755)
	fine := filepath.Join(dir, "fine-restic")
	os.WriteFile(fine, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	var ran []string
	m.OpenRepo = func(url string, _ []string) *backup.Repo {
		ran = append(ran, filepath.Base(url))
		bin := fine
		if strings.HasSuffix(url, "busy") {
			bin = locked
		}
		return &backup.Repo{Bin: bin, URL: url}
	}
	for _, id := range []string{"busy", "idle"} {
		st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-000000000" + map[string]string{"busy": "2", "idle": "3"}[id],
			Provider: "local:" + filepath.Join(dir, id), Repository: filepath.Join(dir, id), Created: time.Now()})
	}
	pruneRetry = time.Millisecond
	defer func() { pruneRetry = time.Hour }()
	submitted := make(chan maintenance.Job, 5)
	m.Submit = func(j maintenance.Job) { submitted <- j }

	job := m.PruneJob()
	if err := job.Run(ctx, nil); err != nil {
		t.Fatalf("a locked repository isn't an error yet: %v", err)
	}
	for try := 2; try <= pruneTries; try++ {
		select {
		case j := <-submitted:
			ran = nil
			err := j.Run(ctx, nil)
			if len(ran) != 1 || ran[0] != "busy" {
				t.Errorf("try %d prunes only the busy one: %v", try, ran)
			}
			if try == pruneTries && (err == nil || !strings.Contains(err.Error(), "locked")) {
				t.Errorf("the last try reports it: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("try %d wasn't queued", try)
		}
	}
	select {
	case <-submitted:
		t.Error("no more tries after the last")
	case <-time.After(50 * time.Millisecond):
	}
}

// fakeCloudBackup is Roostack Cloud backup: a quota, usage, and keys
// (recording what each was asked for).
type fakeCloudBackup struct {
	quota int64
	// accountUsed is what the account's labs store together (0: not
	// said, as a Cloud from before 2026-10-19).
	accountUsed int64
	asked       []cloudapi.StorageRequest
	// registered counts RegisterIdentity calls, and what the key file held.
	registered []string
	keyFile    string
}

func (f *fakeCloudBackup) RegisterIdentity(context.Context) error {
	data, _ := os.ReadFile(f.keyFile)
	f.registered = append(f.registered, strings.TrimSpace(string(data)))
	return nil
}

func (f *fakeCloudBackup) StorageKeys(_ context.Context, req cloudapi.StorageRequest) (cloudapi.StorageKeys, error) {
	f.asked = append(f.asked, req)
	repo := "s3:https://s3.example/backups/labs/lab_x"
	if req.Server != "" {
		repo += "/" + req.Server
	}
	return cloudapi.StorageKeys{Repository: repo, AccessKeyID: "TEMP", SecretAccessKey: "s", SessionToken: "tok",
		Region: "r", Expires: time.Now().Add(12 * time.Hour)}, nil
}

func (f *fakeCloudBackup) StorageUsage(context.Context) (cloudapi.StorageUsage, error) {
	return cloudapi.StorageUsage{QuotaBytes: f.quota, UsedBytes: 120_000_000_000, AccountUsedBytes: f.accountUsed}, nil
}

// Roostack Cloud backup as a destination (SPEC.md §6.9): offered only with
// a quota, with the lab's usage; the vps's repository is its folder in
// Cloud, reached with keys that expire (no credentials of its own kept);
// the controller creates and prunes it with the lab's prune keys.
func TestCloudBackupDestination(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	m.Lab.Paths.StateDir = t.TempDir()
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore = st
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	var opened []string
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		opened = append(opened, url)
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	ctx := context.Background()
	cloud := &fakeCloudBackup{}
	m.CloudBackup = cloud
	choices, _ := m.BackupChoices(ctx)
	if slices.ContainsFunc(choices, func(c BackupChoice) bool { return c.Ref == CloudChoice }) {
		t.Error("offered without a quota")
	}
	cloud.quota = 500_000_000_000
	choices, _ = m.BackupChoices(ctx)
	i := slices.IndexFunc(choices, func(c BackupChoice) bool { return c.Ref == CloudChoice })
	if i < 0 || !strings.Contains(choices[i].Note, "120.0 GB of 500.0 GB") || !strings.Contains(choices[i].Note, "can't read it") {
		t.Fatalf("choices: %+v", choices)
	}
	// With the account's other labs: their total, and this lab's share.
	cloud.accountUsed = 300_000_000_000
	choices, _ = m.BackupChoices(ctx)
	if i := slices.IndexFunc(choices, func(c BackupChoice) bool { return c.Ref == CloudChoice }); i < 0 ||
		!strings.Contains(choices[i].Note, "Your account's labs use 300.0 GB of 500.0 GB; this lab 120.0 GB.") {
		t.Fatalf("choices with the account's labs: %+v", choices)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", CloudChoice); err != nil {
		t.Fatal(err)
	}
	if l := waitDone(t, m, activity.BackupSetupFor("n-0000000002")); l.Err != "" {
		t.Fatalf("setup: %s", l.Err)
	}
	d := vps.settings.dest
	if d == nil || !d.Cloud || len(d.Env) != 0 || d.Repository != "s3:https://s3.example/backups/labs/lab_x/n-0000000002" || d.Key == "" || !d.PrunedByController {
		t.Fatalf("the vps's destination: %+v", d)
	}
	if len(cloud.asked) < 2 || cloud.asked[0].Server != "n-0000000002" || cloud.asked[0].Purpose != cloudapi.StorageBackup ||
		cloud.asked[1].Purpose != cloudapi.StoragePrune {
		t.Errorf("asked Cloud for: %+v", cloud.asked)
	}
	// The weekly prune: the lab's prune keys (kept, so not asked again).
	n := len(cloud.asked)
	if err := m.PruneJob().Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(cloud.asked) != n {
		t.Errorf("prune keys asked again while fresh: %+v", cloud.asked[n:])
	}
	if t2, _, ok := m.BackupTargetOf(ctx, "n-0000000002"); !ok || t2.Provider != CloudChoice {
		t.Errorf("target: %+v", t2)
	}
}

// TestServerNames: a lab of one server doesn't name it in its backups'
// logs (the name is the host's, which the Owner may never have chosen);
// a lab of several does.
func TestServerNames(t *testing.T) {
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "roostack-dev"}
	alone := []string{m.serverName("n-0000000001"), m.onServer("n-0000000001"), m.aboutServer("n-0000000001", "keeping 7 daily"),
		m.backupsOf("n-0000000001")}
	if want := []string{"this server", "", "Keeping 7 daily", "the"}; !slices.Equal(alone, want) {
		t.Errorf("one server: %q, want %q", alone, want)
	}
	m.Others = func() []RemoteServer { return []RemoteServer{&fakeRemote{settings: &fakeServerSettings{}}} }
	several := []string{m.serverName("n-0000000001"), m.onServer("n-0000000001"), m.aboutServer("n-0000000001", "keeping 7 daily"),
		m.backupsOf("n-0000000001")}
	if want := []string{"roostack-dev", " on roostack-dev", "roostack-dev: keeping 7 daily", "roostack-dev's"}; !slices.Equal(several, want) {
		t.Errorf("several servers: %q, want %q", several, want)
	}
	// A sentence starts with "This server" or "The", never "Roostack-dev".
	for in, want := range map[string]string{"this server": "This server", "the": "The", "roostack-dev's": "roostack-dev's"} {
		if got := capital(in); got != want {
			t.Errorf("capital(%q) = %q, want %q", in, got, want)
		}
	}
}
