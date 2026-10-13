package node

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
)

func newSettings(t *testing.T) (*HostSettings, *string, *int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lab.yaml")
	os.WriteFile(path, []byte("domain: lab.example.com\n"), 0o640)
	zone, restarts := "Etc/UTC", 0
	return &HostSettings{
		Lab: config.Default(), LabPath: path,
		SetZone: func(_ context.Context, name string) error { zone = name; return nil },
		Zone:    func() string { return zone },
		Zones:   func() ([]string, error) { return []string{"Australia/Melbourne", "Etc/UTC", "UTC"}, nil },
		SSHClients: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil // someone connected from outside
		},
		Restart: func() { restarts++ },
	}, &zone, &restarts
}

func TestTimezone(t *testing.T) {
	h, zone, restarts := newSettings(t)
	ctx := context.Background()
	var fe *FormError
	if _, err := h.SetTimezone(ctx, "Mars/Olympus_Mons"); !errors.As(err, &fe) {
		t.Errorf("unknown zone: want a FormError, got %v", err)
	}
	if changed, err := h.SetTimezone(ctx, "Etc/UTC"); changed || err != nil || *restarts != 0 {
		t.Errorf("same zone: changed=%v err=%v restarts=%d", changed, err, *restarts)
	}
	changed, err := h.SetTimezone(ctx, " Australia/Melbourne ")
	if err != nil || !changed || *zone != "Australia/Melbourne" || *restarts != 1 {
		t.Errorf("changed=%v err=%v zone=%q restarts=%d: want the zone set and Roostack restarted", changed, err, *zone, *restarts)
	}
}

func TestSSH(t *testing.T) {
	h, _, restarts := newSettings(t)
	ctx := context.Background()
	var fe *FormError
	if err := h.SetSSH(ctx, "sometimes", false); !errors.As(err, &fe) {
		t.Errorf("bad choice: %v", err)
	}
	// Home network only would cut off the connection from 203.0.113.9.
	if err := h.SetSSH(ctx, config.SSHFromLAN, false); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "203.0.113.9") {
		t.Fatalf("want a warning about the current connection, got %v", err)
	}
	if *restarts != 0 {
		t.Fatal("a refused choice must change nothing")
	}
	if err := h.SetSSH(ctx, config.SSHFromLAN, true); err != nil {
		t.Fatal(err)
	}
	lab, err := config.Load(h.LabPath)
	if err != nil || lab.Firewall.SSH != config.SSHFromLAN || *restarts != 1 {
		t.Errorf("lab.yaml firewall = %+v, %v, restarts %d", lab.Firewall, err, *restarts)
	}
	// Anywhere cuts nobody off: no confirmation needed.
	if err := h.SetSSH(ctx, config.SSHFromAny, false); err != nil {
		t.Error(err)
	}

	// With UseSSH (a running Roostack), the firewall is rebuilt at once:
	// no restart, so the server stays reachable.
	var used []string
	h.UseSSH = func(c string) { used = append(used, c) }
	if err := h.SetSSH(ctx, config.SSHOff, true); err != nil {
		t.Fatal(err)
	}
	if *restarts != 2 || len(used) != 1 || used[0] != config.SSHOff {
		t.Errorf("restarts %d, used %v", *restarts, used)
	}
	if a, _ := h.SSHAccess(); a.Choice != config.SSHOff {
		t.Errorf("the page shows the new choice at once: %q", a.Choice)
	}
}

func TestRestartPolicy(t *testing.T) {
	h, _, restarts := newSettings(t)
	ctx := context.Background()
	var fe *FormError
	if err := h.SetRestartPolicy(ctx, "auto", 24); !errors.As(err, &fe) {
		t.Errorf("hour 24: %v", err)
	}
	if err := h.SetRestartPolicy(ctx, config.RestartNotify, 3); err != nil {
		t.Fatal(err)
	}
	lab, _ := config.Load(h.LabPath)
	if lab.Maintenance.Restart != config.RestartNotify || lab.Maintenance.Hour() != 3 || *restarts != 1 {
		t.Errorf("maintenance = %+v, restarts %d", lab.Maintenance, *restarts)
	}
}

func TestBackupDestination(t *testing.T) {
	h, _, restarts := newSettings(t)
	h.Lab.Paths.StateDir = t.TempDir()
	ctx := context.Background()
	d := BackupDestination{Repository: "rest:http://100.96.0.1:8000/n-0000000002/lab",
		Env: []string{"RESTIC_REST_USERNAME=n-0000000002", "RESTIC_REST_PASSWORD=pw"}, Key: "server key"}
	if err := h.SetBackupDestination(ctx, d); err != nil {
		t.Fatal(err)
	}
	lab, _ := config.Load(h.LabPath)
	key, _ := os.ReadFile(backup.KeyFile(h.Lab.Paths))
	if lab.Backup.Repository != d.Repository || len(lab.Backup.Env) != 2 || string(key) != "server key\n" || *restarts != 1 {
		t.Errorf("backup = %+v, key %q, restarts %d", lab.Backup, key, *restarts)
	}
	// The controller keeps its key (the recovery key).
	if err := h.SetBackupDestination(ctx, BackupDestination{Repository: "/srv/backup"}); err != nil {
		t.Fatal(err)
	}
	if key, _ := os.ReadFile(backup.KeyFile(h.Lab.Paths)); string(key) != "server key\n" {
		t.Error("an empty key must keep the key file")
	}
}

func TestBackupCopies(t *testing.T) {
	h, _, restarts := newSettings(t)
	h.Lab.Paths.StateDir = t.TempDir()
	ctx := context.Background()
	b2 := BackupCopy{Name: "provider:1", Title: "Backblaze", Repository: "s3:https://s3.example.com/b/roostack/l/n-2",
		Env: []string{"AWS_ACCESS_KEY_ID=id", "AWS_SECRET_ACCESS_KEY=secret"}, Key: "copy key"}
	if err := h.SetBackupCopies(ctx, []BackupCopy{b2}); err != nil {
		t.Fatal(err)
	}
	lab, _ := config.Load(h.LabPath)
	if len(lab.Backup.Copies) != 1 || lab.Backup.Copies[0].Title != "Backblaze" || lab.Backup.Copies[0].KeyFile == "" || *restarts != 1 {
		t.Fatalf("copies = %+v", lab.Backup.Copies)
	}
	keyFile := lab.Backup.Copies[0].KeyFile
	if key, _ := os.ReadFile(keyFile); string(key) != "copy key\n" {
		t.Errorf("key = %q", key)
	}
	// Sent again without credentials or key (another copy was added): it
	// keeps both.
	disk := BackupCopy{Name: "server:n-1", Title: "nuc's disk", Repository: "rest:http://100.96.0.1:8000/n-2/l", Key: "disk key"}
	if err := h.SetBackupCopies(ctx, []BackupCopy{{Name: "provider:1", Title: "Backblaze", Repository: b2.Repository}, disk}); err != nil {
		t.Fatal(err)
	}
	lab, _ = config.Load(h.LabPath)
	if len(lab.Backup.Copies) != 2 || len(lab.Backup.Copies[0].Env) != 2 || lab.Backup.Copies[0].KeyFile != keyFile ||
		lab.Backup.Copies[1].KeyFile == keyFile {
		t.Errorf("copies = %+v", lab.Backup.Copies)
	}
	if err := h.SetBackupCopies(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if lab, _ = config.Load(h.LabPath); len(lab.Backup.Copies) != 0 {
		t.Error("no copies")
	}
}
