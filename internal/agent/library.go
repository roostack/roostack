package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"roostack.cloud/roostack/internal/config"
)

// Media libraries (SPEC.md §8.1, "Media libraries"): folders of media
// people already have, on a drive or already on the server, mounted
// read-only for services to play. The agent lists a drive's folders, says
// how many files Roostack couldn't read, and mounts a library from fstab
// (the drive by its UUID, then a bind mount of the folder), so it comes
// back after a reboot and a missing drive never holds up booting.
//
// The roostack user asks for these, so the agent holds the line on what a
// library can show it: mount points are under LibraryRoot, which is root's
// (the roostack user could swap a folder in /srv/roostack for a symlink);
// a folder on the server must be under LibraryFolderRoots and outside
// Roostack's own folders; a path may hold no symlinks; and "read as its
// owner" (an idmapped mount, docs/ASSUMPTIONS.md M2) only maps a regular
// user's files, on a drive that isn't the system's.

// LibraryRoot holds the libraries' mount points, one folder per library.
const LibraryRoot = config.LibraryRoot

// LibraryViews holds a folder per service, with bind mounts of the
// libraries it may read: its containers bind that folder (ro,rslave). A
// container binding a library's mount point itself would keep its own copy
// of the mount, stale once the drive is unplugged and never remounted;
// mounts inside a bound folder reach the container as they come and go
// (docs/ASSUMPTIONS.md M1, M3).
const LibraryViews = config.LibraryViews

// serviceName is a service's ID (instance names), for its view folder.
var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// LibraryFolderRoots are where a library can be a folder already on the
// server (the agent can't see /home: its sandbox hides it).
var LibraryFolderRoots = []string{"/mnt", "/media", "/srv"}

// libraryName is a library's name on disk and in services.
var libraryName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidLibraryName says whether name can name a library.
func ValidLibraryName(name string) bool { return libraryName.MatchString(name) }

// Library is one media library to mount.
type Library struct {
	Name string `json:"name"`
	// Drive is the filesystem UUID of the drive it's on, and Sub the folder
	// on it ("" for the whole drive); or Folder is a folder on the server.
	Drive  string `json:"drive,omitempty"`
	Sub    string `json:"sub,omitempty"`
	Folder string `json:"folder,omitempty"`
	// AsOwner, if set, shows this owner's files as Roostack's (drives only).
	AsOwner *Owner `json:"as_owner,omitempty"`
	// Share, if set, is a network share: the whole share is the library,
	// mounted read-only (shares.go).
	Share *Share `json:"share,omitempty"`
}

// Owner is a user whose files a library reads as Roostack's.
type Owner struct {
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Name string `json:"name,omitempty"`
}

// LibraryStatus is a mounted library, from fstab and the mount table.
type LibraryStatus struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Mounted: its folder is mounted now. Connected: its drive is plugged
	// in (or its folder exists).
	Mounted   bool   `json:"mounted"`
	Connected bool   `json:"connected"`
	AsOwner   string `json:"as_owner,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Free      int64  `json:"free,omitempty"`
	// ReadBy are the services whose views have it.
	ReadBy []string `json:"read_by,omitempty"`
}

// FolderList is the folders in a place a library could be, and how many of
// the files there Roostack could read.
type FolderList struct {
	Path    string        `json:"path"`
	Folders []FolderEntry `json:"folders"`
	// Files are the files in the place itself (not in its folders).
	Files int         `json:"files"`
	Check Readability `json:"check"`
}

// FolderEntry is one folder, with what's in it (counts stop at a limit).
type FolderEntry struct {
	Name    string `json:"name"`
	Folders int    `json:"folders"`
	Files   int    `json:"files"`
}

// Readability is what a sample of a folder's files says: how many
// Roostack's user couldn't read, and who owns most of those.
type Readability struct {
	Sampled    int  `json:"sampled"`
	Unreadable int  `json:"unreadable"`
	Partial    bool `json:"partial"` // the sample stopped before the end
	// Closed: the folder itself can't be opened, so nothing in it could.
	Closed bool `json:"closed,omitempty"`
	// Owner owns most of the unreadable files, if a library can read them
	// as that owner (a regular user); OwnerFiles are theirs.
	Owner      *Owner `json:"owner,omitempty"`
	OwnerFiles int    `json:"owner_files,omitempty"`
}

// Limits on looking through a folder, which may hold a whole film
// collection on a slow USB disk.
const (
	sampleFiles  = 5000
	sampleTime   = 3 * time.Second
	countEntries = 10000
)

// LibraryFolders lists the folders in a library's place: a folder on a
// drive (mounted read-only to look, if it isn't mounted), or a folder on
// the server; with what a sample of its files says.
func (h LinuxHost) LibraryFolders(l Library) (FolderList, error) {
	base, sub, err := h.libraryBase(l, true)
	if err != nil {
		return FolderList{}, err
	}
	list, err := listFolders(base, sub, h.UID, h.groups())
	if err == nil && l.Folder != "" {
		// Roostack's own folders can't be libraries: they aren't offered.
		list.Folders = slices.DeleteFunc(list.Folders, func(f FolderEntry) bool { return ownFolder(filepath.Join(base, f.Name)) })
	}
	return list, err
}

// MountLibrary mounts a library for good (fstab), replacing one of the
// same name.
func (h LinuxHost) MountLibrary(l Library) error {
	if h.UID == 0 {
		return errors.New("the roostack user isn't known")
	}
	if !ValidLibraryName(l.Name) {
		return fmt.Errorf("%q can't name a library: lower-case letters, digits and -", printable(l.Name))
	}
	if l.Share != nil {
		return h.mountShare(l)
	}
	base, sub, err := h.libraryBase(l, false)
	if err != nil {
		return err
	}
	if _, err := safeSub(base, sub); err != nil {
		return err
	}
	var idmap string
	if l.AsOwner != nil {
		if l.Drive == "" {
			return errors.New("only a library on a drive can be read as its owner")
		}
		if !regularUser(l.AsOwner.UID, l.AsOwner.GID) {
			return fmt.Errorf("files of user %d can't be read as Roostack's: only a regular user's", l.AsOwner.UID)
		}
		d, err := h.driveByUUID(l.Drive)
		if err != nil {
			return err
		}
		if slices.Contains(foreignFS, d.FSType) {
			return errors.New("this drive's files are Roostack's already: it has no owners to map")
		}
		idmap = fmt.Sprintf("u:%d:%d:1 g:%d:%d:1", l.AsOwner.UID, h.UID, l.AsOwner.GID, h.GID)
	}
	if err := os.MkdirAll(LibraryRoot, 0o755); err != nil {
		return err
	}
	target := filepath.Join(LibraryRoot, l.Name)
	if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if fi, err := os.Lstat(target); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder", target)
	}
	line, err := libraryLine(filepath.Join(base, sub), target, base, idmap)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fstab)
	if err != nil {
		return err
	}
	unit, err := mountUnit(target)
	if err != nil {
		return err
	}
	automount := strings.TrimSuffix(unit, ".mount") + ".automount"
	exec.Command("systemctl", "stop", automount, unit).Run() // replacing one, or a stale mount
	tab := withBlock(removeBlock(string(data), libraryTag(l.Name)), libraryTag(l.Name), line)
	if err := os.WriteFile(fstab, []byte(tab), 0o644); err != nil {
		return err
	}
	exec.Command("systemctl", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "start", automount, unit).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting it: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UnmountLibrary unmounts a library and forgets it; a drive Roostack
// mounted for media goes too once no library uses it.
func (h LinuxHost) UnmountLibrary(name string) error {
	if !ValidLibraryName(name) {
		return fmt.Errorf("%q isn't a library's name", printable(name))
	}
	target := filepath.Join(LibraryRoot, name)
	unit, err := mountUnit(target)
	if err != nil {
		return err
	}
	exec.Command("systemctl", "stop", strings.TrimSuffix(unit, ".mount")+".automount", unit).Run()
	data, err := os.ReadFile(fstab)
	if err != nil {
		return err
	}
	tab := removeBlock(string(data), libraryTag(name))
	tab = removeBlock(tab, shareTag(name))
	os.Remove(shareLoginFile(name))
	// Services' views of it go first.
	for _, v := range viewEntries(tab) {
		if v.library == name {
			tab = removeBlock(tab, viewTag(v.service, v.library))
			stopMount(filepath.Join(LibraryViews, v.service, v.library))
			os.Remove(filepath.Join(LibraryViews, v.service, v.library))
		}
	}
	for _, v := range atEntries(tab) {
		if v.library == name {
			tab = removeBlock(tab, atTag(v.service, v.library, v.name))
			stopMount(filepath.Join(LibraryViews, v.service+".at", v.name))
			os.Remove(filepath.Join(LibraryViews, v.service+".at", v.name))
		}
	}
	// Media drives no library mounts from any more.
	for _, mp := range mediaDriveMounts(tab) {
		if !strings.Contains(tab, "x-systemd.requires-mounts-for="+fstabEscape(mp)) {
			tab = removeMediaDrive(tab, mp)
			if u, err := mountUnit(mp); err == nil {
				exec.Command("systemctl", "stop", strings.TrimSuffix(u, ".mount")+".automount", u).Run()
			}
			os.Remove(mp)
		}
	}
	if err := os.WriteFile(fstab, []byte(tab), 0o644); err != nil {
		return err
	}
	exec.Command("systemctl", "daemon-reload").Run()
	os.Remove(target)
	return nil
}

// Libraries lists the libraries in fstab: mounted or not, connected or not.
func (h LinuxHost) Libraries() ([]LibraryStatus, error) {
	data, err := os.ReadFile(fstab)
	if err != nil {
		return nil, err
	}
	mounts, _ := os.ReadFile("/proc/self/mountinfo")
	var out []LibraryStatus
	for _, e := range append(libraryEntries(string(data)), shareEntries(string(data))...) {
		st := LibraryStatus{Name: e.name, Source: e.source, AsOwner: e.asOwner}
		// Libraries are mounted when first looked at: look (briefly: a NAS
		// that's off, or a drive unplugged, would hold the page up).
		target := filepath.Join(LibraryRoot, e.name)
		if !mountedAt(string(mounts), target) {
			done := make(chan struct{})
			go func() { statfs(target); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
			mounts, _ = os.ReadFile("/proc/self/mountinfo")
		}
		st.Mounted = mountedAt(string(mounts), target)
		if e.share {
			st.Connected = st.Mounted // a share is there when it's mounted
		} else if dev := mediaDriveDevice(string(data), e.base); dev != "" {
			_, err := os.Stat("/dev/disk/by-uuid/" + strings.TrimPrefix(dev, "UUID="))
			st.Connected = err == nil
		} else {
			_, err := os.Stat(e.source)
			st.Connected = err == nil
		}
		if st.Mounted && st.Connected {
			st.Free, st.Size = statfs(filepath.Join(LibraryRoot, e.name))
		}
		for _, v := range viewEntries(string(data)) {
			if v.library == e.name {
				st.ReadBy = append(st.ReadBy, v.service)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// libraryBase is where a library's folder is found: a drive's mount point
// (mounting it read-only for media first if it isn't mounted; just to look
// when browse is set) or a folder on the server, and the folder in it.
func (h LinuxHost) libraryBase(l Library, browse bool) (base, sub string, err error) {
	switch {
	case l.Drive != "" && l.Folder == "":
		d, err := h.driveByUUID(l.Drive)
		if err != nil {
			return "", "", err
		}
		if d.Kind == "system" || d.Kind == "network" {
			return "", "", errors.New("pick a USB drive or second disk, or a folder on the server")
		}
		if !d.Usable {
			return "", "", errors.New("that drive has no filesystem Roostack can read")
		}
		data, err := os.ReadFile(fstab)
		if err != nil {
			return "", "", err
		}
		if backupDrive(string(data), d.UUID) {
			return "", "", errors.New("that's a backup drive: keep media on another drive")
		}
		// Where it's mounted: its fstab line if it has one (Roostack's
		// media mount, or the server's own), else where it is now, but not
		// a library's bind mount of it, nor a mount just to look.
		mp := fstabMount(string(data), d.UUID)
		if mp == "" && d.Mount != "" && !strings.HasPrefix(d.Mount, LibraryRoot+"/") &&
			(browse || !strings.HasPrefix(d.Mount, lookPrefix)) {
			mp = d.Mount
		}
		if mp == "" {
			if browse {
				mp, err = browseMount(d)
			} else {
				mp, err = h.mountMediaDrive(d)
			}
			if err != nil {
				return "", "", err
			}
		}
		return mp, cleanSub(l.Sub), nil
	case l.Folder != "" && l.Drive == "":
		dir, err := libraryFolder(l.Folder)
		if err != nil {
			return "", "", err
		}
		return dir, "", nil
	}
	return "", "", errors.New("a library is a folder on a drive, or a folder on the server")
}

// ownFolder says whether a folder on the server is, or is in, one of
// Roostack's own (its data, the libraries' mount points, its drives').
func ownFolder(p string) bool {
	for _, own := range []string{"/srv/roostack", LibraryRoot, "/mnt/roostack-"} {
		if p == own || strings.HasPrefix(p, own+"/") || (strings.HasSuffix(own, "-") && strings.HasPrefix(p, own)) {
			return true
		}
	}
	return false
}

// driveByUUID finds a connected drive by its filesystem's UUID.
func (h LinuxHost) driveByUUID(uuid string) (Drive, error) {
	if !ValidUUID(uuid) {
		return Drive{}, fmt.Errorf("%q isn't a filesystem UUID", printable(uuid))
	}
	return h.drive("uuid:" + uuid)
}

// libraryFolder checks a folder on the server can be a library: an
// existing folder under LibraryFolderRoots, outside Roostack's own folders
// and other libraries, with no symlinks on the way.
func libraryFolder(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || rootOf(p, LibraryFolderRoots) == "" {
		return "", fmt.Errorf("pick a folder under %s", strings.Join(LibraryFolderRoots, ", "))
	}
	if ownFolder(p) {
		return "", errors.New("that's one of Roostack's own folders")
	}
	if real, err := filepath.EvalSymlinks(p); err != nil {
		return "", err
	} else if real != p {
		return "", errors.New("pick the folder itself, not a link to it")
	}
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s isn't a folder", p)
	}
	return p, nil
}

// cleanSub makes a folder on a drive relative and clean ("" for the whole
// drive).
func cleanSub(sub string) string {
	sub = strings.Trim(filepath.Clean("/"+sub), "/")
	if sub == "." {
		return ""
	}
	return sub
}

// safeSub checks a folder inside base exists and has no symlinks on the
// way, so the bind mount (whose path the kernel resolves) stays inside.
func safeSub(base, sub string) (string, error) {
	dir := base
	if sub != "" {
		for _, part := range strings.Split(sub, "/") {
			if part == "" || part == "." || part == ".." {
				return "", fmt.Errorf("%q isn't a folder on the drive", printable(sub))
			}
			dir = filepath.Join(dir, part)
			fi, err := os.Lstat(dir)
			if err != nil {
				return "", fmt.Errorf("%q isn't on the drive (any more?)", printable(sub))
			}
			if !fi.IsDir() {
				return "", fmt.Errorf("%q isn't a folder (a link, or a file)", printable(sub))
			}
		}
	}
	return dir, nil
}

// listFolders lists the folders in base/sub and samples its files.
func listFolders(base, sub string, uid int, gids []int) (FolderList, error) {
	dir, err := safeSub(base, sub)
	if err != nil {
		return FolderList{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return FolderList{}, err
	}
	list := FolderList{Path: sub}
	for _, e := range entries {
		if !e.IsDir() {
			list.Files++
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || e.Name() == "lost+found" || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		fe := FolderEntry{Name: e.Name()}
		if f, err := os.Open(filepath.Join(dir, e.Name())); err == nil {
			names, _ := f.ReadDir(countEntries)
			f.Close()
			for _, n := range names {
				if n.IsDir() {
					fe.Folders++
				} else {
					fe.Files++
				}
			}
		}
		list.Folders = append(list.Folders, fe)
	}
	list.Check = sample(dir, uid, gids, sampleFiles, time.Now().Add(sampleTime))
	return list, nil
}

// sample looks at up to limit files under dir (until deadline): how many
// Roostack's user (uid, gids) couldn't read, folders it couldn't open
// counting for every file it then didn't see, and who owns most of them.
func sample(dir string, uid int, gids []int, limit int, deadline time.Time) Readability {
	var r Readability
	owners := map[[2]int]int{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if r.Sampled >= limit || time.Now().After(deadline) {
			r.Partial = true
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "lost+found" && p != dir {
				return fs.SkipDir // the filesystem's own, root's: never media
			}
			if !canRead(info.Mode(), int(st.Uid), int(st.Gid), uid, gids, true) {
				// What's inside can't be reached: count the folder as one.
				r.Sampled++
				r.Unreadable++
				owners[[2]int{int(st.Uid), int(st.Gid)}]++
				if p == dir {
					r.Closed = true
					return fs.SkipAll
				}
				return fs.SkipDir
			}
			return nil
		}
		r.Sampled++
		if !canRead(info.Mode(), int(st.Uid), int(st.Gid), uid, gids, false) {
			r.Unreadable++
			owners[[2]int{int(st.Uid), int(st.Gid)}]++
		}
		return nil
	})
	// The regular user owning most of them (root's or a system account's
	// can't be read as Roostack's anyway).
	var best [2]int
	for o, n := range owners {
		if regularUser(o[0], o[1]) && (n > owners[best] || (n == owners[best] && o[0] < best[0])) {
			best = o
		}
	}
	if n := owners[best]; n > 0 {
		o := &Owner{UID: best[0], GID: best[1]}
		if u, err := user.LookupId(strconv.Itoa(best[0])); err == nil {
			o.Name = u.Username
		}
		r.Owner, r.OwnerFiles = o, n
	}
	return r
}

// canRead says whether a user (uid, in gids) can read a file, or list and
// enter a folder, with these permissions.
func canRead(mode fs.FileMode, owner, group, uid int, gids []int, dir bool) bool {
	need := fs.FileMode(0o4)
	if dir {
		need = 0o5
	}
	perm := mode.Perm()
	switch {
	case owner == uid:
		return perm>>6&need == need
	case slices.Contains(gids, group):
		return perm>>3&need == need
	}
	return perm&need == need
}

// regularUser says whether files of uid and gid may be read as Roostack's
// (an idmapped mount): a regular user's and group's, not root's, a
// system account's or nobody's.
func regularUser(uid, gid int) bool {
	return uid >= 1000 && uid < 60000 && gid >= 100 && gid < 60000
}

// groups are the roostack user's groups (for canRead).
func (h LinuxHost) groups() []int {
	gids := []int{h.GID}
	if u, err := user.LookupId(strconv.Itoa(h.UID)); err == nil {
		if ids, err := u.GroupIds(); err == nil {
			for _, id := range ids {
				if g, err := strconv.Atoi(id); err == nil && !slices.Contains(gids, g) {
					gids = append(gids, g)
				}
			}
		}
	}
	return gids
}

// lookPrefix starts the mount points of drives mounted just to look at
// their folders: in /run, so nothing is left behind after a reboot.
const lookPrefix = "/run/roostack-look-"

// browseMount mounts a drive read-only just to look at its folders: a
// transient automount, which systemd unmounts once it's idle a minute.
func browseMount(d Drive) (string, error) {
	if !ValidUUID(d.UUID) {
		return "", fmt.Errorf("the drive's filesystem has an odd UUID (%q)", printable(d.UUID))
	}
	mp := lookPrefix + strings.ReplaceAll(d.UUID, "-", "")[:8]
	if _, err := os.Stat(mp); err == nil {
		if entries, _ := os.ReadDir(mp); len(entries) > 0 {
			return mp, nil // still mounted from a moment ago
		}
	}
	opts := "ro,nosuid,nodev,noexec"
	if out, err := exec.Command("systemd-mount", "--no-ask-password", "--collect", "--automount=yes",
		"--timeout-idle-sec=60", "-o", opts, "/dev/disk/by-uuid/"+d.UUID, mp).CombinedOutput(); err != nil {
		return "", fmt.Errorf("looking at the drive: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// mountMediaDrive mounts a drive read-only for good, for media: in fstab by
// its UUID (nofail, automounted, as backup drives).
func (h LinuxHost) mountMediaDrive(d Drive) (string, error) {
	if !ValidUUID(d.UUID) {
		return "", fmt.Errorf("the drive's filesystem has an odd UUID (%q)", printable(d.UUID))
	}
	data, err := os.ReadFile(fstab)
	if err != nil {
		return "", err
	}
	if mp := fstabMount(string(data), d.UUID); mp != "" {
		return mp, nil // in fstab already (e.g. unplugged now, or another library's)
	}
	name := strings.Trim(mountName.ReplaceAllString(strings.ToLower(d.Name), "-"), "-")
	if name == "" || len(name) > 32 {
		name = d.UUID[:8]
	}
	mp := "/mnt/roostack-media-" + name
	if entries, err := os.ReadDir(mp); err == nil && len(entries) > 0 {
		mp += "-" + strings.ReplaceAll(d.UUID, "-", "")[:6]
	}
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	fsType, opts := d.FSType, "ro,nosuid,nodev,noexec,nofail,x-systemd.automount,x-systemd.device-timeout=10s"
	if fsType == "ntfs" {
		fsType = "ntfs3"
	}
	if slices.Contains(foreignFS, d.FSType) {
		opts += fmt.Sprintf(",uid=%d,gid=%d,umask=022", h.UID, h.GID)
	}
	line, err := checkedFstabLine([]string{"UUID=" + d.UUID, mp, fsType, opts, "0", "2"})
	if err != nil {
		return "", err
	}
	tab := withBlock(string(data), mediaDriveTag(mp, d.Name), line)
	if err := os.WriteFile(fstab, []byte(tab), 0o644); err != nil {
		return "", err
	}
	exec.Command("systemctl", "daemon-reload").Run()
	unit, err := mountUnit(mp)
	if err != nil {
		return "", err
	}
	if out, err := exec.Command("systemctl", "start", strings.TrimSuffix(unit, ".mount")+".automount").CombinedOutput(); err != nil {
		return "", fmt.Errorf("mounting it: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// libraryLine is a library's bind mount in fstab: read-only, after the
// mount holding it (base), optionally idmapped.
func libraryLine(source, target, base, idmap string) (string, error) {
	// Automounted too, so a drive unplugged and plugged back in comes back
	// the next time anything (a service) looks in the library.
	opts := "bind,ro,nofail,x-systemd.automount,x-systemd.requires-mounts-for=" + fstabEscape(base)
	if idmap != "" {
		opts += ",X-mount.idmap=" + fstabEscape(idmap)
	}
	return checkedFstabLine([]string{fstabEscape(source), target, "none", opts, "0", "0"})
}

// fstabEscape writes a path or value for fstab, whose fields are separated
// by whitespace: spaces and tabs as octal escapes.
func fstabEscape(s string) string {
	return strings.NewReplacer(`\`, `\134`, " ", `\040`, "\t", `\011`).Replace(s)
}

// fstabUnescape reads an escaped fstab field.
func fstabUnescape(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(s)
}

// checkedFstabLine joins fields into a line, refusing anything that could
// change its meaning or add a line: an empty field, whitespace left after
// escaping, a control character or "#".
func checkedFstabLine(fields []string) (string, error) {
	for _, f := range fields {
		if f == "" || strings.ContainsFunc(f, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '#' }) {
			return "", fmt.Errorf("can't add %q to /etc/fstab", printable(f))
		}
	}
	return strings.Join(fields, " "), nil
}

// Comments marking the fstab lines Roostack adds for media.
func libraryTag(name string) string { return "# Added by Roostack: media library " + name }
func mediaDriveTag(mp, label string) string {
	return "# Added by Roostack: a media drive (" + printable(label) + ") at " + mp
}

// backupDrive says whether fstab has the UUID as one of Roostack's backup
// drives.
func backupDrive(tab, uuid string) bool {
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "UUID="+uuid && i > 0 &&
			strings.HasPrefix(lines[i-1], "# Added by Roostack: a backup drive") {
			return true
		}
	}
	return false
}

// withBlock appends a tagged line to fstab.
func withBlock(tab, tag, line string) string {
	if tab != "" && !strings.HasSuffix(tab, "\n") {
		tab += "\n"
	}
	return tab + tag + "\n" + line + "\n"
}

// removeBlock removes a tag and the line after it.
func removeBlock(tab, tag string) string {
	lines := strings.Split(tab, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if lines[i] == tag {
			i++ // and its line
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// mediaDriveMounts are the mount points of the media drives in fstab.
func mediaDriveMounts(tab string) []string {
	var mps []string
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "# Added by Roostack: a media drive") && i+1 < len(lines) {
			if f := strings.Fields(lines[i+1]); len(f) >= 2 {
				mps = append(mps, fstabUnescape(f[1]))
			}
		}
	}
	return mps
}

// mediaDriveDevice is the device (UUID=…) of the media drive mounted at
// mp, or "" if mp isn't one.
func mediaDriveDevice(tab, mp string) string {
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "# Added by Roostack: a media drive") && i+1 < len(lines) {
			if f := strings.Fields(lines[i+1]); len(f) >= 2 && fstabUnescape(f[1]) == mp {
				return f[0]
			}
		}
	}
	return ""
}

// removeMediaDrive removes the media drive mounted at mp from fstab.
func removeMediaDrive(tab, mp string) string {
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "# Added by Roostack: a media drive") && i+1 < len(lines) {
			if f := strings.Fields(lines[i+1]); len(f) >= 2 && fstabUnescape(f[1]) == mp {
				return removeBlock(tab, l)
			}
		}
	}
	return tab
}

// libraryEntry is a library's line in fstab.
type libraryEntry struct {
	name, source, base, asOwner string
	share                       bool
}

// libraryEntries reads the libraries' lines in fstab.
func libraryEntries(tab string) []libraryEntry {
	var out []libraryEntry
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		name, ok := strings.CutPrefix(l, "# Added by Roostack: media library ")
		if !ok || i+1 >= len(lines) || strings.HasSuffix(name, " (a share)") {
			continue // a share's (shareEntries)
		}
		f := strings.Fields(lines[i+1])
		if len(f) < 4 {
			continue
		}
		e := libraryEntry{name: name, source: fstabUnescape(f[0])}
		for _, o := range strings.Split(f[3], ",") {
			if v, ok := strings.CutPrefix(o, "x-systemd.requires-mounts-for="); ok {
				e.base = fstabUnescape(v)
			}
			if v, ok := strings.CutPrefix(o, "X-mount.idmap="); ok {
				e.asOwner = fstabUnescape(v)
			}
		}
		out = append(out, e)
	}
	return out
}

// mountedAt says whether something is mounted at target (mountinfo's fifth
// field, escaped like fstab's).
func mountedAt(mountinfo, target string) bool {
	for _, l := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(l)
		// An automount's own entry (autofs) doesn't count: only what it mounted.
		if len(f) > 4 && fstabUnescape(f[4]) == target && !slices.Contains(f, "autofs") {
			return true
		}
	}
	return false
}

// mountUnit is systemd's mount unit for a path.
func mountUnit(path string) (string, error) {
	out, err := exec.Command("systemd-escape", "--path", "--suffix=mount", path).Output()
	if err != nil {
		return "", fmt.Errorf("systemd-escape: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SetLibraryViews sets which libraries a service may read: bind mounts of
// them in its view folder (made even when empty, as its containers bind
// it), automounted after its library. at fills its second view folder, for
// libraries at their old paths' shared parent (SPEC.md §8.4): the old
// folder's name → a library it may read.
func (h LinuxHost) SetLibraryViews(service string, libraries []string, at map[string]string) error {
	if !serviceName.MatchString(service) {
		return fmt.Errorf("%q isn't a service's name", printable(service))
	}
	data, err := os.ReadFile(fstab)
	if err != nil {
		return err
	}
	tab := string(data)
	known := map[string]bool{}
	for _, e := range append(libraryEntries(tab), shareEntries(tab)...) {
		known[e.name] = true
	}
	want := map[string]bool{}
	for _, l := range libraries {
		if !ValidLibraryName(l) || !known[l] {
			return fmt.Errorf("there's no library %q on this server", printable(l))
		}
		want[l] = true
	}
	at = maps.Clone(at)
	for name, l := range at {
		if !atName.MatchString(name) {
			return fmt.Errorf("%q can't name a folder of libraries", printable(name))
		}
		if !want[l] {
			return fmt.Errorf("%s doesn't read the library %q", service, printable(l))
		}
	}
	view := filepath.Join(LibraryViews, service)
	var start []string
	for _, v := range viewEntries(tab) {
		if v.service == service && !want[v.library] {
			tab = removeBlock(tab, viewTag(service, v.library))
			stopMount(filepath.Join(view, v.library))
			os.Remove(filepath.Join(view, v.library))
		}
		if v.service == service {
			delete(want, v.library) // there already
		}
	}
	for _, dir := range []string{LibraryViews, view} {
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	for _, l := range slices.Sorted(maps.Keys(want)) {
		target := filepath.Join(view, l)
		if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if tab, err = withView(tab, viewTag(service, l), l, target); err != nil {
			return err
		}
		start = append(start, target)
	}
	// The second view folder: what's there and not wanted goes, then what's
	// missing comes.
	atView := filepath.Join(LibraryViews, service+".at")
	for _, v := range atEntries(tab) {
		if v.service != service {
			continue
		}
		if at[v.name] == v.library {
			delete(at, v.name) // there already
			continue
		}
		tab = removeBlock(tab, atTag(service, v.library, v.name))
		stopMount(filepath.Join(atView, v.name))
		os.Remove(filepath.Join(atView, v.name))
	}
	if len(at) > 0 {
		if err := os.Mkdir(atView, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(at)) {
		target := filepath.Join(atView, name)
		if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if tab, err = withView(tab, atTag(service, at[name], name), at[name], target); err != nil {
			return err
		}
		start = append(start, target)
	}
	if !slices.ContainsFunc(atEntries(tab), func(v atEntry) bool { return v.service == service }) {
		os.Remove(atView) // only once empty
	}
	if tab != string(data) {
		if err := os.WriteFile(fstab, []byte(tab), 0o644); err != nil {
			return err
		}
		exec.Command("systemctl", "daemon-reload").Run()
	}
	for _, target := range start {
		unit, err := mountUnit(target)
		if err != nil {
			return err
		}
		if out, err := exec.Command("systemctl", "start", strings.TrimSuffix(unit, ".mount")+".automount").CombinedOutput(); err != nil {
			return fmt.Errorf("mounting it: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// withView adds a view of library l at target to fstab, under tag.
func withView(tab, tag, l, target string) (string, error) {
	lib := filepath.Join(LibraryRoot, l)
	line, err := checkedFstabLine([]string{lib, target, "none",
		"bind,ro,nofail,x-systemd.automount,x-systemd.requires-mounts-for=" + lib, "0", "0"})
	if err != nil {
		return "", err
	}
	return withBlock(tab, tag, line), nil
}

// RemoveLibraryViews removes a service's view folders and their mounts
// (the service was removed, or reads no libraries any more).
func (h LinuxHost) RemoveLibraryViews(service string) error {
	if err := h.SetLibraryViews(service, nil, nil); err != nil {
		return err
	}
	for _, dir := range []string{filepath.Join(LibraryViews, service), filepath.Join(LibraryViews, service+".at")} {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// atName is a folder's name in a second view folder: an old folder's name
// (instance.CheckLibraryPath allows the same).
var atName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

// atTag marks a library in a service's second view folder, under a name.
func atTag(service, library, name string) string {
	return "# Added by Roostack: " + service + " reads media library " + library + " as " + name
}

// atEntry is a library in a service's second view folder.
type atEntry struct{ service, library, name string }

// atEntries reads the second view folders' entries in fstab.
func atEntries(tab string) []atEntry {
	var out []atEntry
	for _, l := range strings.Split(tab, "\n") {
		rest, ok := strings.CutPrefix(l, "# Added by Roostack: ")
		if !ok {
			continue
		}
		svc, rest, ok := strings.Cut(rest, " reads media library ")
		if !ok {
			continue
		}
		lib, name, ok := strings.Cut(rest, " as ")
		if ok && serviceName.MatchString(svc) && ValidLibraryName(lib) && atName.MatchString(name) {
			out = append(out, atEntry{svc, lib, name})
		}
	}
	return out
}

// viewTag marks a service's view of a library in fstab.
func viewTag(service, library string) string {
	return "# Added by Roostack: " + service + " reads media library " + library
}

// viewEntry is a service's view of a library.
type viewEntry struct{ service, library string }

// viewEntries reads the services' views in fstab.
func viewEntries(tab string) []viewEntry {
	var out []viewEntry
	for _, l := range strings.Split(tab, "\n") {
		rest, ok := strings.CutPrefix(l, "# Added by Roostack: ")
		if !ok {
			continue
		}
		svc, lib, ok := strings.Cut(rest, " reads media library ")
		if ok && serviceName.MatchString(svc) && ValidLibraryName(lib) {
			out = append(out, viewEntry{svc, lib})
		}
	}
	return out
}

// stopMount stops a path's automount and mount.
func stopMount(path string) {
	if unit, err := mountUnit(path); err == nil {
		exec.Command("systemctl", "stop", strings.TrimSuffix(unit, ".mount")+".automount", unit).Run()
	}
}
