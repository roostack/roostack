package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// Each disk Roostack uses is reported once, with the parts on it: here
// the test's folders (services, media, the backup store and a backup
// drive's repository) share one disk, which may also be the system's.
func TestDiskSpaces(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("space is counted with GNU du and ionice, as on the servers")
	}
	root := t.TempDir()
	var lab config.Lab
	lab.Paths.ServicesDir, lab.Paths.MediaDir = filepath.Join(root, "services"), filepath.Join(root, "media")
	os.MkdirAll(lab.Paths.ServicesDir, 0o755)
	store := lab.Paths.BackupStoreDir()
	os.MkdirAll(filepath.Join(store, "n-1", "lab"), 0o755)
	os.WriteFile(filepath.Join(store, "n-1", "lab", "pack"), []byte(strings.Repeat("x", 3000)), 0o600)
	os.MkdirAll(filepath.Join(store, "guests", "g1"), 0o755)
	os.WriteFile(filepath.Join(store, "guests", "g1", "pack"), []byte(strings.Repeat("y", 1000)), 0o600)
	// A backup drive (here a folder on the same disk).
	lab.Backup.Drive = config.BackupDrive{Mount: filepath.Join(root, "drive"), Name: "BACKUP"}
	lab.Backup.Repository = filepath.Join(root, "drive", "roostack")
	os.MkdirAll(lab.Backup.Repository, 0o755)
	os.WriteFile(filepath.Join(lab.Backup.Repository, "pack"), []byte(strings.Repeat("z", 2000)), 0o600)
	n := &Local{Lab: lab}

	disks, err := n.DiskSpaces(context.Background())
	dev := func(p string) uint64 { fi, _ := os.Stat(p); return uint64(fi.Sys().(*syscall.Stat_t).Dev) }
	want := 1
	if dev("/") != dev(root) {
		want = 2
	}
	if err != nil || len(disks) != want {
		t.Fatalf("disks = %+v, %v (want %d)", disks, err, want)
	}
	d, ok := StoreDisk(disks)
	if !ok || d.Total <= 0 || d.Free <= 0 || d.Used < 0 || d.Free > d.Total || d.Mount == "" {
		t.Fatalf("store disk = %+v", d)
	}
	if want == 2 && (disks[0].Kind != DiskSystem || d.Kind != DiskData) {
		t.Errorf("kinds: %s, %s", disks[0].Kind, d.Kind)
	}
	if d.Services != 0 {
		t.Errorf("no services: %d", d.Services)
	}
	// Counted in the background (du, at idle priority).
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if disks, _ = n.DiskSpaces(context.Background()); !disks[0].Counting && !disks[0].Counted.IsZero() {
			break
		}
	}
	d, _ = StoreDisk(disks)
	// Space on the disk: whole blocks, at least the files' sizes; the
	// drive's backups count with the store's.
	if d.Backups < 5000 || d.Guests < 1000 || d.Backups > 5000+128<<10 || d.Counted.IsZero() {
		t.Errorf("backups %d, guests %d, counted %v", d.Backups, d.Guests, d.Counted)
	}
	// Counted once; asking again doesn't count again.
	at := d.Counted
	if disks, _ = n.DiskSpaces(context.Background()); !disks[0].Counted.Equal(at) {
		t.Error("counted again")
	}
	// An unplugged drive is left out.
	os.RemoveAll(filepath.Join(root, "drive"))
	if disks, _ = n.DiskSpaces(context.Background()); len(disks) != want {
		t.Errorf("unplugged: %+v", disks)
	}
}
