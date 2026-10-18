package manager

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
)

// hostAgent is the host lab's WireGuard, for its guest hub.
type hostAgent struct{}

func (hostAgent) ApplyWireGuard(context.Context, agent.WireGuard) (string, error) {
	return "SGVsbG8sIFdpcmVHdWFyZCBob3N0IGtleSEhISEhISE=", nil
}

// Each of a guest lab's servers backs up to a friend's lab as its own
// user, made when its backups are set up there, and removed when it
// leaves the lab (review M21).
func TestRemoteStorageServerUsers(t *testing.T) {
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	ctx := context.Background()
	// The friend's lab: its guest API over httptest.
	hst, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hst.Close()
	hub := &labnet.GuestHub{LabName: func() string { return "Mum's lab" }, Store: hst, Agent: hostAgent{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now}
	inv, err := hub.NewInvite(ctx, "Sam's lab", 10<<30, "203.0.113.7:51821")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(hub.Handler())
	defer api.Close()
	defer func(f func(netip.Addr) string) { labnet.GuestAPI = f }(labnet.GuestAPI)
	labnet.GuestAPI = func(netip.Addr) string { return api.URL }
	hostUsers := func() map[string]string {
		guests, _ := hst.Guests(ctx, time.Now())
		return guests[0].Users
	}

	// This lab, with a vps, stores there.
	m, _ := newManager(t)
	m.Lab.NodeID, m.Lab.OverlayAddr = "n-0000000001", "100.96.0.1"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	local(m).Activity = m.Activity
	m.LinkAgent = fakeLinks{}
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.BackupStore, m.Settings = st, st
	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	var urls, envs []string
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		urls, envs = append(urls, url), append(envs, strings.Join(env, " "))
		return &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	}
	if err := m.AddRemoteStorage(ctx, inv.String()); err != nil {
		t.Fatal(err)
	}
	ref := m.RemoteStorages(ctx)[0].Ref
	if len(hostUsers()) != 0 {
		t.Errorf("users before any server backs up there: %v", hostUsers())
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", ref); err != nil {
		t.Fatal(err)
	}
	var log activity.Snapshot
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if log, _ = m.Activity.Latest(activity.BackupSetupFor("n-0000000002")); !log.Running() {
			break
		}
	}
	if log.Err != "" {
		t.Fatalf("setup: %s\n%s", log.Err, strings.Join(log.Lines, "\n"))
	}
	// The vps backs up through the controller's proxy, as itself.
	d := vps.settings.dest
	pw := m.RemoteStorages(ctx)[0].Servers["n-0000000002"]
	if d == nil || !strings.HasPrefix(d.Repository, "rest:http://100.96.0.1:8200/n-0000000002/lab-") || pw == "" ||
		!strings.Contains(strings.Join(d.Env, " "), "RESTIC_REST_USERNAME=n-0000000002 RESTIC_REST_PASSWORD="+pw) {
		t.Fatalf("destination = %+v", d)
	}
	if hash := hostUsers()["n-0000000002"]; bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) != nil {
		t.Errorf("the host's user for the vps doesn't match: %v", hostUsers())
	}
	// The host runs the guest's store with that user.
	hm := &Manager{GuestHub: hub, GuestRecords: hst, Log: m.Log}
	if stores, _ := hm.guestStores(ctx); len(stores) != 1 || stores[0].Users["n-0000000002"] == "" || stores[0].StoreHash != "" {
		t.Errorf("host's stores: %+v", stores)
	}
	// The controller creates and prunes it with the lab's admin access.
	if len(urls) == 0 || !strings.Contains(urls[0], ":8101/n-0000000002/lab-") || !strings.Contains(envs[0], "RESTIC_REST_USERNAME="+inv.User) {
		t.Errorf("created through %v %v", urls, envs)
	}
	urls = nil
	if err := m.PruneJob().Run(ctx, nil); err != nil || len(urls) != 1 || !strings.Contains(urls[0], ":8101/n-0000000002/lab-") {
		t.Errorf("prune: %v through %v", err, urls)
	}
	if c := m.SharedCredentials(ctx, "n-0000000002"); len(c) != 0 {
		t.Errorf("the vps shares %v", c)
	}

	// A backup set up before servers had their own users keeps its path.
	t2 := store.BackupTarget{ServerID: "n-0000000002", Provider: ref,
		Repository: "rest:http://100.96.0.1:8200/" + inv.User + "/n-0000000002"}
	if r, err := m.adminRepo(ctx, t2); err != nil || urls[len(urls)-1] != "rest:http://"+inv.HostAddr.String()+":8101/"+inv.User+"/n-0000000002" || r == nil {
		t.Errorf("an older backup's admin access: %v %v", urls, err)
	}

	// The vps leaves the lab: its user there goes.
	if err := m.RemoveServer(ctx, "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if len(hostUsers()) != 0 || m.RemoteStorages(ctx)[0].Servers["n-0000000002"] != "" {
		t.Errorf("after removing the vps: host %v, here %v", hostUsers(), m.RemoteStorages(ctx)[0].Servers)
	}
}
