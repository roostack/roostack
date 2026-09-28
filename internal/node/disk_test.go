package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

func TestDiskSpace(t *testing.T) {
	root := t.TempDir()
	var lab config.Lab
	lab.Paths.ServicesDir, lab.Paths.MediaDir = filepath.Join(root, "services"), filepath.Join(root, "media")
	store := lab.Paths.BackupStoreDir()
	os.MkdirAll(filepath.Join(store, "n-1", "lab"), 0o755)
	os.WriteFile(filepath.Join(store, "n-1", "lab", "pack"), []byte(strings.Repeat("x", 3000)), 0o600)
	os.MkdirAll(filepath.Join(store, "guests", "g1"), 0o755)
	os.WriteFile(filepath.Join(store, "guests", "g1", "pack"), []byte(strings.Repeat("y", 1000)), 0o600)
	n := &Local{Lab: lab}

	d, err := n.DiskSpace(context.Background())
	if err != nil || d.Total <= 0 || d.Free <= 0 || d.Used < 0 || d.Free > d.Total {
		t.Fatalf("disk = %+v, %v", d, err)
	}
	if d.Services != 0 {
		t.Errorf("no services: %d", d.Services)
	}
	// The store is counted in the background (du, at idle priority).
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if d, _ = n.DiskSpace(context.Background()); d.Backups >= 0 {
			break
		}
	}
	// Space on the disk: whole blocks, at least the files' sizes.
	if d.Backups < 3000 || d.Guests < 1000 || d.Backups > 3000+64<<10 || d.Counted.IsZero() {
		t.Errorf("backups %d, guests %d, counted %v", d.Backups, d.Guests, d.Counted)
	}
	// Counted once; asking again doesn't count again.
	at := d.Counted
	if d, _ = n.DiskSpace(context.Background()); !d.Counted.Equal(at) {
		t.Error("counted again")
	}
}
