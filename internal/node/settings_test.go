package node

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
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
		t.Errorf("changed=%v err=%v zone=%q restarts=%d: want the zone set and MyLab restarted", changed, err, *zone, *restarts)
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
