package manager

import (
	"context"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// Updates chosen together on the Updates page (SPEC.md §7.1): one with
// notes needs them read, and one whose offer changed is refused; now,
// they run one at a time with one backup of their server; tonight, they
// wait for the nightly backup whatever the policy says, and can be
// cancelled until then.
func TestUpdateServices(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	ctx := context.Background()
	backups, _ := updateSetup(t, m)
	for _, name := range []string{"vaultwarden", "stirling-pdf"} {
		if err := m.Install(ctx, name, InstallRequest{Settings: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
	}
	m.Wait()
	st := storetest.Open(t)
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "local:usb", Repository: "/mnt/usb/backups"})
	m.BackupStore = st
	version := func(name string) string {
		in, _ := instance.Load(state.InstanceFile(m.Lab, name))
		return in.Version
	}
	vw, sp := version("vaultwarden"), version("stirling-pdf")
	ix := withdrawIndex(t, m, nil)
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	offerVersion(t, m, "vaultwarden", vw, "1.34.4-roostack.1")
	spTo := pkgspec.Upstream(sp) + "-roostack.9"
	offerVersion(t, m, "stirling-pdf", sp, spTo, pkgspec.UpgradeNote{Version: spTo, Note: "Sign in again"})

	err := m.UpdateServices(ctx, []UpdateChoice{{Service: "vaultwarden", To: "1.34.4-roostack.1"}, {Service: "stirling-pdf", To: spTo}}, "alice")
	if err == nil || !strings.Contains(err.Error(), "Read the notes") {
		t.Fatalf("notes not read: %v", err)
	}
	if err := m.UpdateServices(ctx, []UpdateChoice{{Service: "vaultwarden", To: "1.34.9-roostack.1"}}, "alice"); err == nil {
		t.Error("a version not on offer is refused")
	}
	if m.Updating("vaultwarden") != nil {
		t.Fatal("nothing starts when one is refused")
	}

	// Now: both, one backup.
	if err := m.UpdateServices(ctx, []UpdateChoice{{Service: "vaultwarden", To: "1.34.4-roostack.1"},
		{Service: "stirling-pdf", To: spTo, ReadNotes: true}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if su := m.Updating("vaultwarden"); su == nil || su.Waiting != "Waiting its turn: 2 of 2" {
		t.Fatalf("queued: %+v", su)
	}
	waitUpdated(t, m, "stirling-pdf")
	waitUpdated(t, m, "vaultwarden")
	if version("vaultwarden") != "1.34.4-roostack.1" || version("stirling-pdf") != spTo {
		t.Fatalf("updated: %s, %s", version("vaultwarden"), version("stirling-pdf"))
	}
	if backups.runs != 1 {
		t.Errorf("one backup of the server for both: %d", backups.runs)
	}
	for _, name := range []string{"vaultwarden", "stirling-pdf"} {
		if rec := m.LastUpdate(ctx, name); rec == nil || rec.Result != UpdateDone || rec.By != "alice" || rec.Automatic {
			t.Errorf("%s: %+v", name, rec)
		}
	}
	// Stirling-PDF sorts first, so it took the backup.
	if rec := m.LastUpdate(ctx, "stirling-pdf"); rec.Backup.IsZero() {
		t.Error("the first update on the server backed up")
	}

	// Tonight: on notify, it waits for the nightly backup all the same.
	offerVersion(t, m, "vaultwarden", "1.34.4-roostack.1", "1.34.5-roostack.1")
	if u := m.AvailableUpdates(ctx)["vaultwarden"]; u == nil || u.Auto {
		t.Fatalf("on notify, not automatic: %+v", u)
	}
	choice := []UpdateChoice{{Service: "vaultwarden", To: "1.34.5-roostack.1"}}
	if err := m.UpdateTonight(ctx, choice, "bob"); err != nil {
		t.Fatal(err)
	}
	if got := m.UpdatesTonight(ctx); got["vaultwarden"].To != "1.34.5-roostack.1" || got["vaultwarden"].By != "bob" {
		t.Fatalf("tonight: %+v", got)
	}
	if err := m.CancelTonight(ctx, "vaultwarden"); err != nil || len(m.UpdatesTonight(ctx)) != 0 {
		t.Fatalf("cancelled: %v %+v", err, m.UpdatesTonight(ctx))
	}
	if err := m.UpdateTonight(ctx, choice, "bob"); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	backups.Submit()
	m.AutoUpdate(ctx, since)
	waitUpdated(t, m, "vaultwarden")
	if rec := m.LastUpdate(ctx, "vaultwarden"); version("vaultwarden") != "1.34.5-roostack.1" || rec.By != "bob" || rec.Automatic || backups.runs != 2 {
		t.Fatalf("tonight's: %s %+v (backups %d)", version("vaultwarden"), rec, backups.runs)
	}
	if len(m.UpdatesTonight(ctx)) != 0 {
		t.Error("done, it's off tonight's list")
	}

	// A newer offer drops the choice: it wasn't seen.
	offerVersion(t, m, "vaultwarden", "1.34.5-roostack.1", "1.34.6-roostack.1")
	m.UpdateTonight(ctx, []UpdateChoice{{Service: "vaultwarden", To: "1.34.6-roostack.1"}}, "bob")
	offerVersion(t, m, "vaultwarden", "1.34.6-roostack.1", "1.34.7-roostack.1")
	if len(m.UpdatesTonight(ctx)) != 0 {
		t.Error("a newer offer replaces tonight's choice")
	}
}
