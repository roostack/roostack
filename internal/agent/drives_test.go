package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lsblkFixture = `{"blockdevices": [
 {"name":"sr0","path":"/dev/sr0","type":"rom","size":1073741312,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"sata","rm":true,"hotplug":false,"model":"QEMU DVD-ROM","fsavail":null},
 {"name":"vda","path":"/dev/vda","type":"disk","size":42949672960,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":null,
  "children":[
   {"name":"vda1","path":"/dev/vda1","type":"part","size":42815438336,"fstype":"ext4","label":null,"uuid":"4d07","mountpoints":["/"],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":36482535424},
   {"name":"vda15","path":"/dev/vda15","type":"part","size":130023424,"fstype":"vfat","label":null,"uuid":"B1C2","mountpoints":["/boot/efi"],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":120000000}]},
 {"name":"sdb","path":"/dev/sdb","type":"disk","size":128043712512,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"SanDisk Ultra","fsavail":null,
  "children":[
   {"name":"sdb1","path":"/dev/sdb1","type":"part","size":128042663936,"fstype":"exfat","label":"BACKUP","uuid":"64B1-2F3C","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"nvme1n1","path":"/dev/nvme1n1","type":"disk","size":1000204886016,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"nvme","rm":false,"hotplug":false,"model":"Samsung SSD 980","fsavail":null},
 {"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"fstype":"LVM2_member","label":null,"uuid":"lvm","mountpoints":[],"tran":"sata","rm":false,"hotplug":false,"model":"WD Red","fsavail":null},
 {"name":"sdd","path":"/dev/sdd","type":"disk","size":500107862016,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"sata","rm":false,"hotplug":false,"model":"Old disk","fsavail":null,
  "children":[
   {"name":"sdd1","path":"/dev/sdd1","type":"part","size":500106813440,"fstype":"ext4","label":"photos","uuid":"aa11","mountpoints":["/data"],"tran":"sata","rm":false,"hotplug":false,"model":null,"fsavail":400000000000}]}
]}`

func TestParseDrives(t *testing.T) {
	drives, err := parseDrives([]byte(lsblkFixture), 30<<30)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Drive{}
	for _, d := range drives {
		byID[d.ID] = d
	}
	if len(drives) != 4 {
		t.Fatalf("drives = %+v", drives)
	}
	if s := byID["system"]; s.Kind != "system" || !s.Usable || s.Erasable || s.Free != 30<<30 || s.Size != 42949672960 {
		t.Errorf("system = %+v", s)
	}
	if u := byID["uuid:64B1-2F3C"]; u.Kind != "usb" || u.Name != "BACKUP" || u.FSType != "exfat" || !u.Usable || !u.Erasable ||
		u.Disk != "/dev/sdb" || u.Mount != "" || u.Free != -1 {
		t.Errorf("usb = %+v", u)
	}
	if n := byID["disk:/dev/nvme1n1"]; n.Kind != "disk" || n.Usable || !n.Erasable || n.Name != "Samsung SSD 980" {
		t.Errorf("blank disk = %+v", n)
	}
	if d := byID["uuid:aa11"]; d.Mount != "/data" || d.Free != 400000000000 || d.Name != "photos" {
		t.Errorf("mounted disk = %+v", d)
	}
	if _, ok := byID["uuid:lvm"]; ok {
		t.Error("an LVM disk isn't offered")
	}
}

func TestNetworkDrives(t *testing.T) {
	mounts := "/dev/vda1 / ext4 rw 0 0\n//nas.local/backups /mnt/nas cifs rw,relatime 0 0\nnas:/export /mnt/nfs\\040share nfs4 rw 0 0\n"
	got := networkDrives(mounts, func(string) (int64, int64) { return 5, 10 })
	if len(got) != 2 || got[0].ID != "mount:/mnt/nas" || got[0].Name != "//nas.local/backups" || got[1].Mount != "/mnt/nfs share" ||
		got[0].Kind != "network" || got[0].Free != 5 {
		t.Errorf("network = %+v", got)
	}
}

func TestFstabLine(t *testing.T) {
	usb := Drive{UUID: "64B1-2F3C", FSType: "exfat"}
	if got := fstabLine(usb, "/mnt/roostack-backup", 999, 989); got !=
		"UUID=64B1-2F3C /mnt/roostack-backup exfat defaults,nofail,x-systemd.automount,x-systemd.device-timeout=10s,uid=999,gid=989,umask=077 0 2" {
		t.Errorf("exfat: %s", got)
	}
	if got := fstabLine(Drive{UUID: "aa", FSType: "ext4"}, "/mnt/x", 1, 1); strings.Contains(got, "uid=") || !strings.Contains(got, "nofail") {
		t.Errorf("ext4: %s", got)
	}
	if got := fstabLine(Drive{UUID: "n", FSType: "ntfs"}, "/mnt/x", 1, 1); !strings.Contains(got, " ntfs3 ") {
		t.Errorf("ntfs uses the kernel's driver: %s", got)
	}
}

func TestPrepareFolder(t *testing.T) {
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	got, err := prepareFolder(filepath.Join(root, "backup"), []string{root}, uid, gid)
	if err != nil || got != filepath.Join(root, "backup", "roostack") {
		t.Fatalf("prepare: %q %v", got, err)
	}
	if again, err := prepareFolder(filepath.Join(root, "backup"), []string{root}, uid, gid); err != nil || again != got {
		t.Errorf("again: %q %v", again, err)
	}
	for _, bad := range []string{"relative", "/etc", filepath.Join(root, "..", "escape")} {
		if _, err := prepareFolder(bad, []string{root}, uid, gid); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
	// A symlink can't lead elsewhere.
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link"))
	if _, err := prepareFolder(filepath.Join(root, "link"), []string{root}, uid, gid); err == nil {
		t.Error("a symlink out of the allowed places")
	}
	// Someone else's folder with things in it isn't taken over.
	other := filepath.Join(root, "other", "roostack")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "theirs"), nil, 0o644)
	if _, err := prepareFolder(filepath.Join(root, "other"), []string{root}, uid+1, gid); err == nil {
		t.Error("someone else's folder")
	}
}

func TestPartitionIn(t *testing.T) {
	// As lsblk prints one disk (flat, even without --list).
	out := `{"blockdevices": [{"path": "/dev/sdb", "type": "disk"},{"path": "/dev/sdb1", "type": "part"}]}`
	if got := partitionIn([]byte(out)); got != "/dev/sdb1" {
		t.Errorf("partition = %q", got)
	}
	if got := partitionIn([]byte(`{"blockdevices": [{"path": "/dev/sdb", "type": "disk"}]}`)); got != "" {
		t.Errorf("no partition yet: %q", got)
	}
}

func TestFstabMount(t *testing.T) {
	fstab := "PARTUUID=1efb / ext4 rw 0 1\n# Added by Roostack: a backup drive (TEST)\nUUID=1725 /mnt/roostack-test ext4 defaults,nofail 0 2\n"
	if got := fstabMount(fstab, "1725"); got != "/mnt/roostack-test" {
		t.Errorf("known: %q", got)
	}
	if got := fstabMount(fstab, "17"); got != "" {
		t.Errorf("unknown: %q", got)
	}
}

func TestUpdateFstab(t *testing.T) {
	fstab := "UUID=1725 /data ext4 defaults 0 2\n# Added by Roostack: a backup drive (TEST)\nUUID=1725 /mnt/roostack-test ext4 old 0 2\n"
	got := updateFstab(fstab, "1725", "UUID=1725 /mnt/roostack-test ext4 new 0 2")
	if !strings.Contains(got, "ext4 new") || !strings.Contains(got, "/data ext4 defaults") {
		t.Errorf("only Roostack's own line changes:\n%s", got)
	}
}
