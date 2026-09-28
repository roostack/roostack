package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Drives for backups (SPEC.md §6.9): the setup wizard lists the drives a
// server has (USB drives, second disks, network shares, its own disk), so
// people pick one instead of typing a path. The agent mounts a chosen drive
// by its filesystem's UUID (for good: it comes back after a reboot, and a
// missing drive doesn't hold up booting) and can erase an empty or unwanted
// disk and prepare it.

// Drive is a place backups can go.
type Drive struct {
	// ID picks it: "system" (this server's own disk), "uuid:<filesystem
	// UUID>", "disk:<device>" (no filesystem yet) or "mount:<mountpoint>"
	// (a network share).
	ID string `json:"id"`
	// Kind is "usb", "disk", "network" or "system".
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	// Device is the partition (or disk) holding it; Disk the whole disk,
	// for erasing it.
	Device string `json:"device,omitempty"`
	Disk   string `json:"disk,omitempty"`
	Size   int64  `json:"size"`
	// Free is -1 while it isn't mounted.
	Free   int64  `json:"free"`
	FSType string `json:"fstype,omitempty"`
	UUID   string `json:"uuid,omitempty"`
	Mount  string `json:"mount,omitempty"`
	// Usable: backups can go on it as it is. Erasable: its disk can be
	// erased and prepared for backups (never the system's disk).
	Usable   bool `json:"usable"`
	Erasable bool `json:"erasable"`
}

// SystemBackupDir is where backups go on the server's own disk.
const SystemBackupDir = "/srv/roostack/backups"

// usableFS are filesystems backups can go on as they are.
var usableFS = []string{"ext4", "ext3", "xfs", "btrfs", "exfat", "vfat", "ntfs", "ntfs3", "f2fs"}

// foreignFS are filesystems without Unix owners: mounted so the roostack
// user owns every file.
var foreignFS = []string{"exfat", "vfat", "ntfs", "ntfs3"}

// networkFS are network shares that are already mounted.
var networkFS = []string{"nfs", "nfs4", "cifs", "smb3", "fuse.sshfs"}

// systemMounts mark the disk the server runs from.
var systemMounts = []string{"/", "/boot", "/boot/efi", "/usr", "/var", "[SWAP]"}

// notForBackups are filesystems (or signatures) that belong to something
// else: the disk isn't offered, nor erased.
var notForBackups = []string{"LVM2_member", "linux_raid_member", "crypto_LUKS", "zfs_member", "swap"}

type lsblkDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        int64         `json:"size"`
	FSType      *string       `json:"fstype"`
	Label       *string       `json:"label"`
	UUID        *string       `json:"uuid"`
	Mountpoints []*string     `json:"mountpoints"`
	Tran        *string       `json:"tran"`
	RM          bool          `json:"rm"`
	Hotplug     bool          `json:"hotplug"`
	Model       *string       `json:"model"`
	FSAvail     *int64        `json:"fsavail"`
	Children    []lsblkDevice `json:"children"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func (d lsblkDevice) mounts() []string {
	var out []string
	for _, m := range d.Mountpoints {
		if m != nil && *m != "" {
			out = append(out, *m)
		}
	}
	return out
}

// all is the device and everything on it.
func (d lsblkDevice) all() []lsblkDevice {
	out := []lsblkDevice{d}
	for _, c := range d.Children {
		out = append(out, c.all()...)
	}
	return out
}

// parseDrives turns `lsblk --json --bytes` output into drives. The system
// disk becomes the one "system" drive; free is the free space on it.
func parseDrives(data []byte, systemFree int64) ([]Drive, error) {
	var out struct {
		Blockdevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("reading lsblk: %w", err)
	}
	drives := []Drive{{ID: "system", Kind: "system", Name: "This server's own disk", Free: systemFree, Usable: true,
		Mount: SystemBackupDir}}
	for _, disk := range out.Blockdevices {
		if disk.Type != "disk" || strings.HasPrefix(disk.Name, "zram") {
			continue
		}
		system, foreign := false, false
		for _, d := range disk.all() {
			for _, m := range d.mounts() {
				if slices.Contains(systemMounts, m) {
					system = true
				}
			}
			if slices.Contains(notForBackups, str(d.FSType)) {
				foreign = true
			}
		}
		if system {
			drives[0].Size += disk.Size
			if drives[0].Model == "" {
				drives[0].Model = str(disk.Model)
			}
			continue
		}
		if foreign {
			continue // LVM, RAID, encrypted: set up by someone who knows
		}
		kind := "disk"
		if str(disk.Tran) == "usb" || disk.RM || disk.Hotplug {
			kind = "usb"
		}
		base := Drive{Kind: kind, Model: str(disk.Model), Disk: disk.Path, Erasable: true}
		name := func(d lsblkDevice) string {
			if l := str(d.Label); l != "" {
				return l
			}
			if base.Model != "" {
				return base.Model
			}
			if kind == "usb" {
				return "USB drive"
			}
			return "Disk " + disk.Name
		}
		var found bool
		for _, d := range disk.all() {
			fs := str(d.FSType)
			if !slices.Contains(usableFS, fs) || str(d.UUID) == "" {
				continue
			}
			found = true
			dr := base
			dr.ID, dr.Name, dr.Device, dr.Size, dr.FSType, dr.UUID = "uuid:"+str(d.UUID), name(d), d.Path, d.Size, fs, str(d.UUID)
			dr.Usable, dr.Free = true, -1
			if ms := d.mounts(); len(ms) > 0 {
				dr.Mount = ms[0]
				if d.FSAvail != nil {
					dr.Free = *d.FSAvail
				}
			}
			drives = append(drives, dr)
		}
		// Nothing usable on it (new, or a filesystem backups can't use):
		// offered for erasing.
		if !found {
			dr := base
			dr.ID, dr.Name, dr.Device, dr.Size, dr.Free = "disk:"+disk.Path, name(disk), disk.Path, disk.Size, -1
			drives = append(drives, dr)
		}
	}
	return drives, nil
}

// networkDrives lists network shares from /proc/mounts.
func networkDrives(mounts string, free func(string) (int64, int64)) []Drive {
	var out []Drive
	sc := bufio.NewScanner(strings.NewReader(mounts))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !slices.Contains(networkFS, f[2]) {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		avail, size := free(mp)
		out = append(out, Drive{ID: "mount:" + mp, Kind: "network", Name: f[0], Mount: mp, FSType: f[2],
			Size: size, Free: avail, Usable: true})
	}
	return out
}

func statfs(path string) (free, size int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, 0
	}
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize
}

// Drives lists the places backups can go.
func (h LinuxHost) Drives() ([]Drive, error) {
	out, err := exec.Command("lsblk", "--json", "--bytes", "-o",
		"NAME,PATH,TYPE,SIZE,FSTYPE,LABEL,UUID,MOUNTPOINTS,TRAN,RM,HOTPLUG,MODEL,FSAVAIL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	free, _ := statfs("/srv")
	drives, err := parseDrives(out, free)
	if err != nil {
		return nil, err
	}
	if mounts, err := os.ReadFile("/proc/mounts"); err == nil {
		drives = append(drives, networkDrives(string(mounts), statfs)...)
	}
	return drives, nil
}

func (h LinuxHost) drive(id string) (Drive, error) {
	drives, err := h.Drives()
	if err != nil {
		return Drive{}, err
	}
	for _, d := range drives {
		if d.ID == id {
			return d, nil
		}
	}
	return Drive{}, errors.New("that drive isn't connected any more")
}

// UsedDrive is a drive readied for backups: Dir has a folder named
// roostack that the roostack user owns; UUID identifies the filesystem
// (so backups can check the drive is there).
type UsedDrive struct {
	Dir  string `json:"dir"`
	UUID string `json:"uuid,omitempty"`
}

var mountName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// UseDrive readies a drive for backups: mounted for good (if it isn't),
// with a roostack folder the roostack user owns.
func (h LinuxHost) UseDrive(id string) (UsedDrive, error) {
	if h.UID == 0 {
		return UsedDrive{}, errors.New("the roostack user isn't known")
	}
	if id == "system" {
		if _, err := prepareFolder(SystemBackupDir, []string{"/srv"}, h.UID, h.GID); err != nil {
			return UsedDrive{}, err
		}
		return UsedDrive{Dir: SystemBackupDir}, nil
	}
	d, err := h.drive(id)
	if err != nil {
		return UsedDrive{}, err
	}
	if !d.Usable {
		return UsedDrive{}, errors.New("that drive has no filesystem yet: prepare it first")
	}
	if d.Mount == "" {
		if d.Mount, err = h.mountForGood(d); err != nil {
			return UsedDrive{}, err
		}
	}
	if _, err := prepareFolder(d.Mount, []string{d.Mount}, h.UID, h.GID); err != nil {
		if d.Kind == "network" && errors.Is(err, os.ErrPermission) {
			return UsedDrive{}, fmt.Errorf("the share at %s doesn't let this server create a folder owned by Roostack: "+
				"allow it on the NAS (e.g. no root squashing), or ask for help", d.Mount)
		}
		return UsedDrive{}, err
	}
	return UsedDrive{Dir: d.Mount, UUID: d.UUID}, nil
}

// fstab is the file mounts are recorded in (tests replace it).
var fstab = "/etc/fstab"

// mountForGood mounts a filesystem under /mnt, recorded in fstab by UUID
// (nofail: a missing drive doesn't hold up booting).
func (h LinuxHost) mountForGood(d Drive) (string, error) {
	name := strings.Trim(mountName.ReplaceAllString(strings.ToLower(d.Name), "-"), "-")
	if name == "" || len(name) > 32 {
		name = d.UUID[:8]
	}
	data, err := os.ReadFile(fstab)
	if err != nil {
		return "", err
	}
	// Known already (e.g. chosen before, then unplugged): the same place.
	mp := fstabMount(string(data), d.UUID)
	known := mp != ""
	if !known {
		mp = "/mnt/roostack-" + name
		if entries, err := os.ReadDir(mp); err == nil && len(entries) > 0 {
			mp += "-" + strings.ReplaceAll(d.UUID, "-", "")[:6]
		}
	}
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	line := fstabLine(d, mp, h.UID, h.GID)
	if known {
		// A line Roostack wrote before is brought up to date.
		if updated := updateFstab(string(data), d.UUID, line); updated != string(data) {
			if err := os.WriteFile(fstab, []byte(updated), 0o644); err != nil {
				return "", err
			}
		}
	} else {
		f, err := os.OpenFile(fstab, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return "", err
		}
		prefix := ""
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			prefix = "\n"
		}
		_, err = fmt.Fprintf(f, "%s# Added by Roostack: a backup drive (%s)\n%s\n", prefix, d.Name, line)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	// systemd mounts it (from fstab): a plain mount would stay inside this
	// agent's own mount namespace (PrivateTmp).
	exec.Command("systemctl", "daemon-reload").Run()
	unit, err := exec.Command("systemd-escape", "--path", mp).Output()
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(string(unit))
	if out, err := exec.Command("systemctl", "start", name+".automount", name+".mount").CombinedOutput(); err != nil {
		return "", fmt.Errorf("mounting it: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// updateFstab replaces the line for uuid with line, if Roostack added it.
func updateFstab(fstab, uuid, line string) string {
	lines := strings.Split(fstab, "\n")
	for i, l := range lines {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "UUID="+uuid && i > 0 &&
			strings.HasPrefix(lines[i-1], "# Added by Roostack") {
			lines[i] = line
		}
	}
	return strings.Join(lines, "\n")
}

// fstabMount is where fstab mounts the filesystem with this UUID, or "".
func fstabMount(fstab, uuid string) string {
	for _, line := range strings.Split(fstab, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "UUID="+uuid {
			return f[1]
		}
	}
	return ""
}

// fstabLine records a backup drive's mount.
func fstabLine(d Drive, mp string, uid, gid int) string {
	// Mounted when first used (e.g. by the nightly backup), so a drive
	// unplugged and plugged back in is there again.
	fs, opts := d.FSType, "defaults,nofail,x-systemd.automount,x-systemd.device-timeout=10s"
	if fs == "ntfs" {
		fs = "ntfs3" // the kernel's driver
	}
	if slices.Contains(foreignFS, d.FSType) {
		opts += fmt.Sprintf(",uid=%d,gid=%d,umask=077", uid, gid)
	}
	return fmt.Sprintf("UUID=%s %s %s %s 0 2", d.UUID, mp, fs, opts)
}

// EraseDrive erases a disk that isn't the system's and prepares it for
// backups: one partition with an ext4 filesystem labelled label.
func (h LinuxHost) EraseDrive(disk, label string) error {
	drives, err := h.Drives()
	if err != nil {
		return err
	}
	ok := false
	for _, d := range drives {
		if d.Disk == disk && d.Erasable {
			if d.Mount != "" {
				return fmt.Errorf("%s is in use (mounted at %s)", d.Name, d.Mount)
			}
			ok = true
		}
	}
	if !ok {
		return errors.New("that disk can't be erased")
	}
	label = strings.ToUpper(mountName.ReplaceAllString(label, ""))
	if label == "" || len(label) > 16 {
		label = "ROOSTACK"
	}
	run := func(name string, args ...string) error {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("wipefs", "--all", disk); err != nil {
		return err
	}
	cmd := exec.Command("sfdisk", "--label", "gpt", disk)
	cmd.Stdin = strings.NewReader("type=linux\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sfdisk: %w: %s", err, strings.TrimSpace(string(out)))
	}
	run("udevadm", "settle")
	part, err := firstPartition(disk)
	if err != nil {
		return err
	}
	if err := run("mkfs.ext4", "-F", "-q", "-L", label, part); err != nil {
		return err
	}
	run("udevadm", "settle")
	return nil
}

// firstPartition finds a disk's new partition (its name depends on the
// kind of disk: sdb1, nvme0n1p1, …).
func firstPartition(disk string) (string, error) {
	for range 20 {
		out, err := exec.Command("lsblk", "--json", "--list", "-o", "PATH,TYPE", disk).Output()
		if err == nil {
			if part := partitionIn(out); part != "" {
				return part, nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", fmt.Errorf("the new partition on %s didn't appear", filepath.Base(disk))
}

// partitionIn picks the first partition from `lsblk --json --list` output.
func partitionIn(data []byte) string {
	var l struct {
		Blockdevices []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal(data, &l) != nil {
		return ""
	}
	for _, d := range l.Blockdevices {
		if d.Type == "part" {
			return d.Path
		}
	}
	return ""
}
