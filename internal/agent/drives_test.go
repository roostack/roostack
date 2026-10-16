package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const lsblkFixture = `{"blockdevices": [
 {"name":"sr0","path":"/dev/sr0","type":"rom","size":1073741312,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"sata","rm":true,"hotplug":false,"model":"QEMU DVD-ROM","fsavail":null},
 {"name":"vda","path":"/dev/vda","type":"disk","size":42949672960,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":null,
  "children":[
   {"name":"vda1","path":"/dev/vda1","type":"part","size":42815438336,"fstype":"ext4","label":null,"uuid":"4d07a1b2-0000-4000-8000-000000000001","mountpoints":["/"],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":36482535424},
   {"name":"vda15","path":"/dev/vda15","type":"part","size":130023424,"fstype":"vfat","label":null,"uuid":"B1C2","mountpoints":["/boot/efi"],"tran":"virtio","rm":false,"hotplug":false,"model":null,"fsavail":120000000}]},
 {"name":"sdb","path":"/dev/sdb","type":"disk","size":128043712512,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"SanDisk Ultra","fsavail":null,
  "children":[
   {"name":"sdb1","path":"/dev/sdb1","type":"part","size":128042663936,"fstype":"exfat","label":"BACKUP","uuid":"64B1-2F3C","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"nvme1n1","path":"/dev/nvme1n1","type":"disk","size":1000204886016,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"nvme","rm":false,"hotplug":false,"model":"Samsung SSD 980","fsavail":null},
 {"name":"sdc","path":"/dev/sdc","type":"disk","size":2000398934016,"fstype":"LVM2_member","label":null,"uuid":"lvm","mountpoints":[],"tran":"sata","rm":false,"hotplug":false,"model":"WD Red","fsavail":null},
 {"name":"sde","path":"/dev/sde","type":"disk","size":64000000000,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"Evil\u000aStick","fsavail":null,
  "children":[
   {"name":"sde1","path":"/dev/sde1","type":"part","size":64000000000,"fstype":"ext4","label":"x\u000aUUID=0 / ext4","uuid":"3e2f6a1c-0000-4000-8000-000000000003","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null},
   {"name":"sde2","path":"/dev/sde2","type":"part","size":1000,"fstype":"ext4","label":null,"uuid":"0 / ext4 defaults\u000aUUID=1","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"sdf","path":"/dev/sdf","type":"disk","size":32000000000,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"Mac drive","fsavail":null,
  "children":[
   {"name":"sdf1","path":"/dev/sdf1","type":"part","size":32000000000,"fstype":"hfsplus","label":null,"uuid":"5f6e7d8c-0000-4000-8000-000000000004","partuuid":"11111111-02","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"sdg","path":"/dev/sdg","type":"disk","size":32000000000,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"Old Mac drive","fsavail":null,
  "children":[
   {"name":"sdg1","path":"/dev/sdg1","type":"part","size":32000000000,"fstype":"hfsplus","label":null,"uuid":"6a7b8c9d-0000-4000-8000-000000000005","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"sdh","path":"/dev/sdh","type":"disk","size":32000000000,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":"Odd drive","fsavail":null,
  "children":[
   {"name":"sdh1","path":"/dev/sdh1","type":"part","size":32000000000,"fstype":"ext4","label":null,"uuid":"not-a-uuid","mountpoints":[],"tran":"usb","rm":true,"hotplug":true,"model":null,"fsavail":null}]},
 {"name":"sdd","path":"/dev/sdd","type":"disk","size":500107862016,"fstype":null,"label":null,"uuid":null,"mountpoints":[],"tran":"sata","rm":false,"hotplug":false,"model":"Old disk","fsavail":null,
  "children":[
   {"name":"sdd1","path":"/dev/sdd1","type":"part","size":500106813440,"fstype":"ext4","label":"photos","uuid":"aa11a1b2-0000-4000-8000-000000000002","mountpoints":["/data"],"tran":"sata","rm":false,"hotplug":false,"model":null,"fsavail":400000000000}]}
]}`

// sdf's filesystem is in fstab (by PARTUUID), so it's in use.
const fstabFixture = "PARTUUID=d578426d-01 / ext4 rw 0 1\n# comment\nPARTUUID=11111111-02 /mnt/mac hfsplus ro,nofail 0 2\n"

func TestParseDrives(t *testing.T) {
	drives, err := parseDrives([]byte(lsblkFixture), 30<<30, fstabFixture, func(p string) string { return p })
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Drive{}
	for _, d := range drives {
		byID[d.ID] = d
	}
	if len(drives) != 8 {
		t.Fatalf("drives = %+v", drives)
	}
	if s := byID["system"]; s.Kind != "system" || !s.Usable || s.Erasable || s.Free != 30<<30 || s.Size != 42949672960 {
		t.Errorf("system = %+v", s)
	}
	// A drive with a filesystem backups can use is never erased (M18).
	if u := byID["uuid:64B1-2F3C"]; u.Kind != "usb" || u.Name != "BACKUP" || u.FSType != "exfat" || !u.Usable || u.Erasable ||
		u.Disk != "/dev/sdb" || u.Mount != "" || u.Free != -1 {
		t.Errorf("usb = %+v", u)
	}
	if n := byID["disk:/dev/nvme1n1"]; n.Kind != "disk" || n.Usable || !n.Erasable || n.Name != "Samsung SSD 980" {
		t.Errorf("blank disk = %+v", n)
	}
	if d := byID["uuid:aa11a1b2-0000-4000-8000-000000000002"]; d.Mount != "/data" || d.Free != 400000000000 || d.Name != "photos" || d.Erasable {
		t.Errorf("mounted disk = %+v", d)
	}
	// A crafted label and model lose their newlines; a filesystem with a
	// UUID that isn't one isn't offered (M17).
	if e := byID["uuid:3e2f6a1c-0000-4000-8000-000000000003"]; e.Name != "xUUID=0 / ext4" || e.Model != "EvilStick" || e.Erasable {
		t.Errorf("crafted label = %+v", e)
	}
	for _, d := range drives {
		if d.Device == "/dev/sde2" || d.Device == "/dev/sdh1" {
			t.Errorf("a filesystem with an odd UUID is offered: %+v", d)
		}
	}
	if h := byID["disk:/dev/sdh"]; h.Usable || h.Erasable {
		t.Errorf("a disk with an ext4 filesystem (odd UUID) isn't erased: %+v", h)
	}
	// Something backups can't use: erasable unless fstab has it.
	if f := byID["disk:/dev/sdf"]; f.Erasable {
		t.Errorf("a disk in fstab = %+v", f)
	}
	if g := byID["disk:/dev/sdg"]; !g.Erasable {
		t.Errorf("an unused Mac drive = %+v", g)
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

func TestInFstab(t *testing.T) {
	dev := []lsblkDevice{{Path: "/dev/sdb1", UUID: ptr("64B1-2F3C"), Label: ptr("BACKUP")}}
	resolve := func(p string) string {
		if p == "/dev/disk/by-id/usb-1" {
			return "/dev/sdb1"
		}
		return p
	}
	for tab, want := range map[string]bool{
		"UUID=64b1-2f3c /mnt/x exfat defaults 0 2":   true,
		`LABEL="BACKUP" /mnt/x exfat defaults 0 2`:   true,
		"/dev/sdb1 /mnt/x exfat defaults 0 2":        true,
		"/dev/disk/by-id/usb-1 /mnt/x exfat 0 2":     true,
		"# UUID=64B1-2F3C /mnt/x exfat defaults 0 2": false,
		"UUID=64B1-2F3D /mnt/x exfat defaults 0 2":   false,
		"LABEL= /mnt/x exfat defaults 0 2":           false,
	} {
		if got := inFstab(dev, tab, resolve); got != want {
			t.Errorf("%q: %v, want %v", tab, got, want)
		}
	}
}

func ptr(s string) *string { return &s }

func TestFstabLine(t *testing.T) {
	usb := Drive{UUID: "64B1-2F3C", FSType: "exfat"}
	if got, _ := fstabLine(usb, "/mnt/roostack-backup", 999, 989); got !=
		"UUID=64B1-2F3C /mnt/roostack-backup exfat defaults,nofail,x-systemd.automount,x-systemd.device-timeout=10s,uid=999,gid=989,umask=077 0 2" {
		t.Errorf("exfat: %s", got)
	}
	if got, _ := fstabLine(Drive{UUID: "64B1-2F3C", FSType: "ext4"}, "/mnt/x", 1, 1); strings.Contains(got, "uid=") || !strings.Contains(got, "nofail") {
		t.Errorf("ext4: %s", got)
	}
	if got, _ := fstabLine(Drive{UUID: "0123456789ABCDEF", FSType: "ntfs"}, "/mnt/x", 1, 1); !strings.Contains(got, " ntfs3 ") {
		t.Errorf("ntfs uses the kernel's driver: %s", got)
	}
	// Nothing that could split the line or add one (M17).
	for _, bad := range []Drive{{UUID: "a b", FSType: "ext4"}, {UUID: "a\nUUID=b", FSType: "ext4"}, {UUID: "", FSType: "ext4"},
		{UUID: "a#b", FSType: "ext4"}} {
		if line, err := fstabLine(bad, "/mnt/x", 1, 1); err == nil {
			t.Errorf("%q: %q, want an error", bad.UUID, line)
		}
	}
	if _, err := fstabLine(Drive{UUID: "64B1-2F3C", FSType: "ext4"}, "/mnt/a b", 1, 1); err == nil {
		t.Error("a mount point with a space")
	}
}

func TestValidUUID(t *testing.T) {
	for s, want := range map[string]bool{
		"120695a4-65b6-43c4-b138-ec5c87087ad8": true, "64B1-2F3C": true, "0123456789ABCDEF": true,
		"": false, "4d07": false, "64B1-2F3C\n": false, "../../etc": false, "120695a4-65b6-43c4-b138-ec5c87087ad8 x": false,
	} {
		if ValidUUID(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

func TestPrepareFolder(t *testing.T) {
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	got, err := prepareFolder(filepath.Join(root, "backup"), []string{root}, uid, gid)
	if err != nil || got != filepath.Join(root, "backup", "roostack") {
		t.Fatalf("prepare: %q %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "backup")); err != nil || fi.Sys().(*syscall.Stat_t).Uid != uint32(uid) {
		t.Error("a folder made on the way is Roostack's")
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
	// Nor a folder beneath one: nothing may be made out there before the
	// request is refused (the roostack user can plant symlinks in
	// /srv/roostack, and a folder it owns in /etc would give it root).
	if _, err := prepareFolder(filepath.Join(root, "link", "made"), []string{root}, uid, gid); err == nil {
		t.Error("a folder beneath a symlink out of the allowed places")
	}
	if _, err := os.Lstat(filepath.Join(outside, "made")); err == nil {
		t.Error("a folder was made outside the allowed places")
	}
	// A symlink that stays inside is fine.
	os.Mkdir(filepath.Join(root, "real"), 0o755)
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "inside"))
	if got, err := prepareFolder(filepath.Join(root, "inside"), []string{root}, uid, gid); err != nil ||
		got != filepath.Join(root, "real", "roostack") {
		t.Errorf("a symlink inside: %q %v", got, err)
	}
	// Someone else's folder with things in it isn't taken over.
	other := filepath.Join(root, "other", "roostack")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "theirs"), nil, 0o644)
	if _, err := prepareFolder(filepath.Join(root, "other"), []string{root}, uid+1, gid); err == nil {
		t.Error("someone else's folder")
	}
}

// chownIn never follows a symlink, even one the roostack user swapped in
// for its own folder.
func TestChownInKeepsToItsRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "backups")); err != nil {
		t.Fatal(err)
	}
	if err := chownIn(root, filepath.Join(root, "backups"), os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("chown the link itself: %v", err)
	}
	if err := chownIn(root, filepath.Join(root, "backups", "x"), os.Getuid(), os.Getgid()); err == nil {
		t.Error("a path through a symlink out of the root")
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
