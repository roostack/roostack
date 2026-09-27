package manager

import (
	"context"
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

func TestBackupDestinationOnAMachinesDisk(t *testing.T) {
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
	vps := &fakeRemote{settings: &fakeMachineSettings{}}
	m.Others = func() []RemoteMachine { return []RemoteMachine{vps} }
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
	if len(choices) != 2 || choices[0].Ref != "machine:n-0000000001" || choices[0].Title != "nuc's disk" {
		t.Fatalf("choices = %+v", choices)
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "machine:nope"); err == nil {
		t.Error("unknown choice")
	}
	if err := m.SetBackupDestination(ctx, "n-0000000002", "machine:n-0000000001"); err != nil {
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
	machine := filepath.Join(dir, "machine")
	os.WriteFile(machine, []byte(d.Key+"\n"), 0o600)
	r := &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: machine, CacheDir: filepath.Join(dir, "cache")}
	if _, err := r.Snapshots(ctx); err != nil {
		t.Errorf("machine key: %v", err)
	}
	// nuc's store lets the vps and the controller in.
	stg := m.storageFor(ctx, "n-0000000001")
	if stg == nil || stg.Users["n-0000000002"] == "" || stg.Admin == "" {
		t.Errorf("storage = %+v", stg)
	}
	if m.storageFor(ctx, "n-0000000002") != nil {
		t.Error("the vps stores nothing")
	}
}
