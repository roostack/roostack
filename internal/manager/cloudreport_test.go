package manager

import (
	"context"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// TestCloudBackups: the servers the lab reports to Roostack Cloud are
// those backing up there, by their main backup or a copy, each with its
// last backup there (not another destination's).
func TestCloudBackups(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	vps := &fakeRemote{settings: &fakeServerSettings{}}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	st := storetest.Open(t)
	m.BackupStore = st
	if got, err := m.CloudBackups(ctx); err != nil || len(got) != 0 {
		t.Fatalf("nothing in Cloud: %+v %v", got, err)
	}

	t0 := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	cloudRepo := "s3:https://s3.test/backups/labs/lab_1/n-0000000002"
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000001", Provider: destination.Cloud, Repository: "s3:https://s3.test/backups/labs/lab_1/n-0000000001"})
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:2", Repository: "s3:https://s3.wasabisys.com/b/x"})
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: destination.Cloud, Repository: cloudRepo})
	local(m).Jobs.Backups = fakeBackups{backup.Status{LastOK: &store.BackupRun{OK: true, Finished: t0}}}
	vps.backups = fakeBackups{backup.Status{LastOK: &store.BackupRun{OK: true, Finished: t0.Add(time.Hour)},
		Copies: []backup.CopyStatus{{Name: "copy-1", Repository: cloudRepo, LastOK: &store.BackupRun{OK: true, Finished: t0.Add(-time.Hour)}}}}}
	got, err := m.CloudBackups(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("reported: %+v %v", got, err)
	}
	if b := got[0]; b.Server != "n-0000000001" || b.Name != "nuc" || !b.Last.Equal(t0) {
		t.Errorf("nuc, backing up to Cloud: %+v", b)
	}
	if b := got[1]; b.Server != "n-0000000002" || b.Name != "vps" || !b.Last.Equal(t0.Add(-time.Hour)) {
		t.Errorf("vps, its copy in Cloud: %+v", b)
	}
}

// TestCloudPaused: the lab tells Cloud how many public services are
// paused and since the earliest pause, never which.
func TestCloudPaused(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if p, err := m.CloudPaused(); err != nil || p == nil || p.Services != 0 || !p.Since.IsZero() {
		t.Fatalf("nothing paused: %+v %v", p, err)
	}
	before := time.Now().Add(-time.Second)
	if err := m.PausePublic("tom", PauseRequest{Services: []string{"vaultwarden"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	own, _ := m.CloudPaused()
	if err := m.PausePublic("tom", PauseRequest{All: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	p, err := m.CloudPaused()
	if err != nil || p.Services != 1 || p.Since.Before(before) || !p.Since.Equal(own.Since) {
		t.Errorf("paused twice, counted once since the first: %+v %v (first %v)", p, err, own.Since)
	}
}
