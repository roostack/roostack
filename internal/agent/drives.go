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
	"unicode"
)

// Drives for backups (SPEC.md §6.9): the setup wizard lists the drives a
// server has (USB drives, second disks, network shares, its own disk), so
// people pick one instead of typing a path. The agent mounts a chosen drive
// by its filesystem's UUID (for good: it comes back after a reboot, and a
// missing drive doesn't hold up booting) and can erase an empty or unwanted
// disk and prepare it.
//
// What lsblk says about a drive comes from the drive itself (its label,
// model and UUID), so a crafted USB stick chooses it: names lose their
// control characters, UUIDs must look like one, and a line for fstab is
// refused if any of its fields could split it (review M17, SPEC.md §6.1).
// Only a disk with nothing usable on it, not mounted and not in fstab, can
// be erased: the roostack user asks for it, and an idle backup drive is
// only automounted, so "not mounted now" isn't enough (review M18).

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
	// erased and prepared for backups: never the system's disk, nor one
	// with a filesystem backups can use, mounted, or in /etc/fstab.
	Usable   bool `json:"usable"`
	Erasable bool `json:"erasable"`
	// Backup: one of Roostack's backup drives (in fstab as one), which
	// can't hold media libraries.
	Backup bool `json:"backup,omitempty"`
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
	PartUUID    *string       `json:"partuuid"`
	PartLabel   *string       `json:"partlabel"`
	Mountpoints []*string     `json:"mountpoints"`
	Tran        *string       `json:"tran"`
	RM          bool          `json:"rm"`
	Hotplug     bool          `json:"hotplug"`
	Model       *string       `json:"model"`
	FSAvail     *int64        `json:"fsavail"`
	Children    []lsblkDevice `json:"children"`
}

// str is one of lsblk's values, without control characters (lsblk's
// JSON escapes them, and decoding brings them back).
func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(printable(*p))
}

// printable is s without control characters.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// fsUUID is a filesystem UUID as lsblk shows it: ext4, XFS, Btrfs and F2FS
// (8-4-4-4-12 hex digits), FAT and exFAT (XXXX-XXXX), NTFS (16 digits).
var fsUUID = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{4}-[0-9a-fA-F]{4}|[0-9a-fA-F]{16})$`)

// ValidUUID reports whether s is a filesystem UUID (fsUUID).
func ValidUUID(s string) bool { return fsUUID.MatchString(s) }

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

// inFstab reports whether fstab mounts (or swaps on) any of devs, by UUID,
// PARTUUID, LABEL, PARTLABEL or device path. resolve follows /dev/disk/…
// symlinks.
func inFstab(devs []lsblkDevice, fstab string, resolve func(string) string) bool {
	for _, line := range strings.Split(fstab, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		key, val, tagged := strings.Cut(f[0], "=")
		val = strings.Trim(val, `"`)
		for _, d := range devs {
			switch {
			case !tagged:
				if f[0] == d.Path || resolve(f[0]) == d.Path {
					return true
				}
			case key == "UUID" && strings.EqualFold(val, str(d.UUID)),
				key == "PARTUUID" && strings.EqualFold(val, str(d.PartUUID)),
				key == "LABEL" && val != "" && val == str(d.Label),
				key == "PARTLABEL" && val != "" && val == str(d.PartLabel):
				return true
			}
		}
	}
	return false
}

// resolvePath follows symlinks, or gives p back.
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// parseDrives turns `lsblk --json --bytes` output into drives. The system
// disk becomes the one "system" drive; free is the free space on it.
// fstab is /etc/fstab's contents: a disk in it is never erasable.
func parseDrives(data []byte, systemFree int64, fstab string, resolve func(string) string) ([]Drive, error) {
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
		system, foreign, mounted, hasFS := false, false, false, false
		for _, d := range disk.all() {
			for _, m := range d.mounts() {
				mounted = true
				if slices.Contains(systemMounts, m) {
					system = true
				}
			}
			if slices.Contains(notForBackups, str(d.FSType)) {
				foreign = true
			}
			if slices.Contains(usableFS, str(d.FSType)) {
				hasFS = true
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
		base := Drive{Kind: kind, Model: str(disk.Model), Disk: disk.Path}
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
			if !slices.Contains(usableFS, fs) || !ValidUUID(str(d.UUID)) {
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
		// offered for erasing, unless it's in use.
		if !found {
			dr := base
			dr.ID, dr.Name, dr.Device, dr.Size, dr.Free = "disk:"+disk.Path, name(disk), disk.Path, disk.Size, -1
			dr.Erasable = !hasFS && !mounted && !inFstab(disk.all(), fstab, resolve)
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
		out = append(out, Drive{ID: "mount:" + mp, Kind: "network", Name: printable(f[0]), Mount: mp, FSType: f[2],
			Size: size, Free: avail, Usable: true})
	}
	return out
}

func statfs(path string) (free, size int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}

// Drives lists the places backups can go.
func (h LinuxHost) Drives() ([]Drive, error) {
	out, err := exec.Command("lsblk", "--json", "--bytes", "-o",
		"NAME,PATH,TYPE,SIZE,FSTYPE,LABEL,UUID,PARTUUID,PARTLABEL,MOUNTPOINTS,TRAN,RM,HOTPLUG,MODEL,FSAVAIL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	tab, err := readFstab()
	if err != nil {
		return nil, err
	}
	free, _ := statfs("/srv")
	drives, err := parseDrives(out, free, tab, resolvePath)
	if err != nil {
		return nil, err
	}
	for i := range drives {
		drives[i].Backup = drives[i].UUID != "" && backupDrive(string(tab), drives[i].UUID)
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
		// The folder is Roostack's own backup store too (other servers'
		// backups, its password file), so it's the roostack user's.
		if err := systemBackupDir(h.UID, h.GID); err != nil {
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

// mountForGood mounts a filesystem under /mnt, recorded in fstab by UUID
// (nofail: a missing drive doesn't hold up booting).
func (h LinuxHost) mountForGood(d Drive) (string, error) {
	if !ValidUUID(d.UUID) {
		return "", fmt.Errorf("the drive's filesystem has an odd UUID (%q)", printable(d.UUID))
	}
	name := strings.Trim(mountName.ReplaceAllString(strings.ToLower(d.Name), "-"), "-")
	if name == "" || len(name) > 32 {
		name = d.UUID[:8]
	}
	var mp string
	err := editFstab(func(tab string) (string, error) {
		// Known already (e.g. chosen before, then unplugged): the same place.
		mp = fstabMount(tab, d.UUID)
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
		line, err := fstabLine(d, mp, h.UID, h.GID)
		if err != nil {
			return "", err
		}
		if known {
			// A line Roostack wrote before is brought up to date.
			return updateFstab(tab, d.UUID, line), nil
		}
		prefix := ""
		if len(tab) > 0 && !strings.HasSuffix(tab, "\n") {
			prefix = "\n"
		}
		return tab + fmt.Sprintf("%s# Added by Roostack: a backup drive (%s)\n%s\n", prefix, printable(d.Name), line), nil
	})
	if err != nil {
		return "", err
	}
	// systemd mounts it (from fstab): a plain mount would stay inside this
	// agent's own mount namespace (PrivateTmp).
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

// fstabLine records a backup drive's mount. It refuses a field that's
// empty or holds whitespace, a control character or "#": any of them would
// change what the line means, or add a line.
func fstabLine(d Drive, mp string, uid, gid int) (string, error) {
	// Mounted when first used (e.g. by the nightly backup), so a drive
	// unplugged and plugged back in is there again.
	fs, opts := d.FSType, "defaults,nofail,x-systemd.automount,x-systemd.device-timeout=10s"
	if fs == "ntfs" {
		fs = "ntfs3" // the kernel's driver
	}
	if slices.Contains(foreignFS, d.FSType) {
		opts += fmt.Sprintf(",uid=%d,gid=%d,umask=077", uid, gid)
	}
	if !ValidUUID(d.UUID) {
		return "", fmt.Errorf("can't add a drive with the UUID %q to /etc/fstab", printable(d.UUID))
	}
	fields := []string{"UUID=" + d.UUID, mp, fs, opts, "0", "2"}
	for _, f := range fields {
		if f == "" || strings.ContainsFunc(f, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '#' }) {
			return "", fmt.Errorf("can't add %q to /etc/fstab", printable(f))
		}
	}
	return strings.Join(fields, " "), nil
}

// EraseDrive erases a disk and prepares it for backups: one partition with
// an ext4 filesystem labelled label. Only an Erasable disk: nothing on it
// backups could use, not mounted, not in fstab.
func (h LinuxHost) EraseDrive(disk, label string) error {
	drives, err := h.Drives()
	if err != nil {
		return err
	}
	ok := false
	for _, d := range drives {
		if d.Disk == disk && d.Erasable {
			ok = true
		}
	}
	if !ok {
		return errors.New("that disk can't be erased: Roostack only prepares a disk with nothing on it it could use, " +
			"that isn't mounted or in /etc/fstab")
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
