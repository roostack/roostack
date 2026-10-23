package manager

import (
	"context"
	"slices"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/store"
)

// The 3-2-1 checks, and what a plan lacks first (SPEC.md §6.9).
func TestPlanChecks(t *testing.T) {
	recent := &store.BackupRun{OK: true, Finished: time.Now().Add(-time.Hour)}
	old := &store.BackupRun{OK: true, Finished: time.Now().Add(-3 * 24 * time.Hour)}
	usb := PlanBackup{Title: "USB drive BACKUP", Kind: KindUSB, AtHome: true, Access: Access{CanDelete: true}, LastOK: recent}
	b2 := PlanBackup{Title: "Backblaze B2", Kind: KindOnline, Away: true, Access: Access{B2: true, CanDelete: true}}
	safe := PlanBackup{Title: "Sam's lab", Kind: KindFriend, Away: true}

	if c := PlanChecks(nil, false); c.Copies || c.BackedUp || c.Missing != "A backup away from home" {
		t.Errorf("no backups: %+v", c)
	}
	if c := PlanChecks(nil, true); c.Missing != "A backup away from this server" {
		t.Errorf("a cloud server: %+v", c)
	}
	// A USB drive is a disk, like the one the data is on.
	c := PlanChecks([]PlanBackup{usb}, false)
	if c.Copies || c.TwoKinds || c.Away || c.Safe || !c.BackedUp || c.Missing != "A backup away from home" {
		t.Errorf("a USB drive: %+v", c)
	}
	// Online storage with the account's key: away, but not safe, and a
	// better key is the way there.
	c = PlanChecks([]PlanBackup{usb, b2}, false)
	if !c.Copies || !c.Away || c.Safe || c.Missing != "A backup safe from ransomware" ||
		c.Suggest != "Suggested: give Roostack a Backblaze key that can make keys (see Backblaze B2), or use another server in your lab, "+
			"a friend's lab or Roostack Cloud. This server can add backups there but not delete them." || c.Hint != "" {
		t.Errorf("USB and B2: %+v", c)
	}
	c = PlanChecks([]PlanBackup{usb, safe}, false)
	if !c.Copies || !c.TwoKinds || !c.Away || !c.Safe || !c.BackedUp || c.Missing != "" || c.Suggest != "" {
		t.Errorf("3-2-1 met: %+v", c)
	}
	// Disks only: one kind of storage, however many.
	disk := PlanBackup{Title: "vps's disk", Kind: KindServer, Away: true, LastOK: old}
	if c := PlanChecks([]PlanBackup{disk, {Kind: KindSystem, AtHome: true}}, false); c.TwoKinds || c.Missing != "A different kind of storage" || c.BackedUp {
		t.Errorf("disks only, the main one stale: %+v", c)
	}
	// The main backup away and another at home: make that one the main.
	if c := PlanChecks([]PlanBackup{safe, usb}, false); c.Hint != "Restores use the main backup first, so it's best at home, "+
		"where restoring is quick: consider making USB drive BACKUP the main backup." {
		t.Errorf("hint: %q", c.Hint)
	}
	if c := PlanChecks([]PlanBackup{safe, usb}, true); c.Hint != "" {
		t.Errorf("a cloud server has no home: %q", c.Hint)
	}
}

// Where a backup on another server is, from where each server is.
func TestServerPlace(t *testing.T) {
	ctx := context.Background()
	kv := linkKV{}
	m := &Manager{Settings: kv}
	if home, away, unknown := m.ServerPlace(ctx, "a", "b"); home || away || !slices.Equal(unknown, []string{"a", "b"}) {
		t.Errorf("nobody has said: %v %v %v", home, away, unknown)
	}
	kv[LocationKey+"a"] = LocationHome
	kv[LocationKey+"b"] = "moon"
	if _, _, unknown := m.ServerPlace(ctx, "a", "b"); !slices.Equal(unknown, []string{"b"}) || m.ServerLocation(ctx, "b") != "" {
		t.Errorf("an unknown answer is no answer: %v", unknown)
	}
	kv[LocationKey+"b"] = LocationHome
	if home, away, unknown := m.ServerPlace(ctx, "a", "b"); !home || away || unknown != nil {
		t.Error("both at home: at home")
	}
	kv[LocationKey+"a"] = LocationAway
	if home, away, _ := m.ServerPlace(ctx, "a", "b"); home || !away {
		t.Error("different places: away")
	}
	kv[LocationKey+"b"] = LocationAway
	if home, away, _ := m.ServerPlace(ctx, "a", "b"); home || !away {
		t.Error("both away from home: away")
	}
	if err := m.SetServerLocation(ctx, "a", "moon"); err == nil {
		t.Error("only at home or away")
	}
}

// A new service's names, and the server it starts on.
func TestNewServiceDefaults(t *testing.T) {
	p := &pkgspec.Package{Name: "immich", Title: "Immich", Routes: map[string]*pkgspec.Route{"web": {Subdomain: "photos"}}}
	if sub, title, suffix := NewServiceNames(p, false); sub != "photos" || title != "Immich" || suffix != "" {
		t.Errorf("first: %q %q %q", sub, title, suffix)
	}
	if sub, title, suffix := NewServiceNames(p, true); sub != "photos-2" || title != "Immich (2)" || suffix != "2" {
		t.Errorf("another: %q %q %q", sub, title, suffix)
	}

	servers := []Server{{ID: "n-1", Name: "nuc", This: true}, {ID: "n-2", Name: "pi"}, {ID: "n-3", Name: "vps"}}
	if got := InstallServer(servers, nil, ""); got != "" {
		t.Errorf("this server: %q", got)
	}
	if got := InstallServer(servers, []NoBuild{{ID: "n-1", Arch: "amd64"}}, ""); got != "n-2" {
		t.Errorf("not this server: %q", got)
	}
	if got := InstallServer(servers, []NoBuild{{ID: "n-2", Arch: "arm64"}}, "n-2"); got != "n-1" {
		t.Errorf("not the chosen one: %q", got)
	}
	if got := InstallServer(servers, []NoBuild{{ID: "n-1"}, {ID: "n-2"}, {ID: "n-3"}}, "n-3"); got != "n-3" {
		t.Errorf("nowhere it can run: %q", got)
	}
}
