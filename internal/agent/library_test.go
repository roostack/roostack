package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A library's fstab line: its folder bind-mounted read-only after the
// mount it's on, spaces escaped, and idmapped when read as its owner.
func TestLibraryLine(t *testing.T) {
	line, err := libraryLine("/mnt/roostack-media-data/Big Films", "/mnt/roostack-library/films", "/mnt/roostack-media-data", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := `/mnt/roostack-media-data/Big\040Films /mnt/roostack-library/films none bind,ro,nofail,x-systemd.automount,x-systemd.requires-mounts-for=/mnt/roostack-media-data 0 0`; line != want {
		t.Errorf("line =\n%s\nwant\n%s", line, want)
	}
	line, err = libraryLine("/mnt/d/Films", "/mnt/roostack-library/films", "/mnt/d", "u:1000:999:1 g:1000:989:1")
	if err != nil || !strings.HasSuffix(line, `,X-mount.idmap=u:1000:999:1\040g:1000:989:1 0 0`) {
		t.Errorf("idmapped: %q %v", line, err)
	}
	for _, bad := range []string{"/mnt/d/a#b", "/mnt/d/a\nb", "/mnt/d/a\x00b"} {
		if _, err := libraryLine(bad, "/mnt/roostack-library/x", "/mnt/d", ""); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// fstab blocks Roostack adds are found, read back and removed by their tag,
// leaving everything else alone.
func TestLibraryFstab(t *testing.T) {
	tab := "UUID=abcd / ext4 defaults 0 1\n# Added by Roostack: a backup drive (BACKUP)\nUUID=1111-2222 /mnt/roostack-backup ext4 defaults 0 2"
	if !backupDrive(tab, "1111-2222") || backupDrive(tab, "abcd") {
		t.Error("backup drives")
	}
	tab = withBlock(tab, mediaDriveTag("/mnt/roostack-media-data", "DATA"), "UUID=3333-4444 /mnt/roostack-media-data ext4 ro,nofail 0 2")
	line, _ := libraryLine("/mnt/roostack-media-data/Big Films", "/mnt/roostack-library/films", "/mnt/roostack-media-data", "u:1000:999:1 g:1000:989:1")
	tab = withBlock(tab, libraryTag("films"), line)
	entries := libraryEntries(tab)
	if len(entries) != 1 || entries[0] != (libraryEntry{name: "films", source: "/mnt/roostack-media-data/Big Films",
		base: "/mnt/roostack-media-data", asOwner: "u:1000:999:1 g:1000:989:1"}) {
		t.Errorf("entries = %+v", entries)
	}
	if got := mediaDriveMounts(tab); len(got) != 1 || got[0] != "/mnt/roostack-media-data" {
		t.Errorf("media drives = %q", got)
	}
	if got := mediaDriveDevice(tab, "/mnt/roostack-media-data"); got != "UUID=3333-4444" {
		t.Errorf("device = %q", got)
	}
	tab = withBlock(tab, viewTag("plex", "films"), "/mnt/roostack-library/films /mnt/roostack-library-views/plex/films none bind 0 0")
	if v := viewEntries(tab); len(v) != 1 || v[0] != (viewEntry{"plex", "films"}) {
		t.Errorf("views = %+v", v)
	}
	if e := libraryEntries(tab); len(e) != 1 {
		t.Errorf("a view isn't a library: %+v", e)
	}
	tab = removeBlock(tab, viewTag("plex", "films"))
	tab = removeBlock(tab, libraryTag("films"))
	tab = removeMediaDrive(tab, "/mnt/roostack-media-data")
	if want := "UUID=abcd / ext4 defaults 0 1\n# Added by Roostack: a backup drive (BACKUP)\nUUID=1111-2222 /mnt/roostack-backup ext4 defaults 0 2\n"; tab != want {
		t.Errorf("after removing:\n%q\nwant\n%q", tab, want)
	}
}

// A folder on the server must be under the allowed places, not one of
// Roostack's own, and not reached through a link.
func TestLibraryFolder(t *testing.T) {
	dir := t.TempDir()
	defer func(roots []string) { LibraryFolderRoots = roots }(LibraryFolderRoots)
	LibraryFolderRoots = []string{dir}
	os.Mkdir(filepath.Join(dir, "music"), 0o755)
	os.Symlink(filepath.Join(dir, "music"), filepath.Join(dir, "link"))
	if got, err := libraryFolder(filepath.Join(dir, "music")); err != nil || got != filepath.Join(dir, "music") {
		t.Errorf("music: %q %v", got, err)
	}
	for _, bad := range []string{filepath.Join(dir, "link"), filepath.Join(dir, "missing"), "/etc", "relative", "/srv/roostack/services"} {
		if _, err := libraryFolder(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	LibraryFolderRoots = []string{"/srv", "/mnt"}
	if !ownFolder("/srv/roostack") || !ownFolder("/mnt/roostack-media-data") || ownFolder("/srv/media") || ownFolder("/mnt/nas") {
		t.Error("ownFolder")
	}
	for _, own := range []string{"/srv/roostack", "/srv/roostack/media", LibraryRoot + "/x", "/mnt/roostack-media-data"} {
		if _, err := libraryFolder(own); err == nil || !strings.Contains(err.Error(), "Roostack's own") {
			t.Errorf("%q: %v", own, err)
		}
	}
}

// A folder on a drive has no symlinks on the way, and can't climb out.
func TestSafeSub(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "Films", "Classics"), 0o755)
	os.Symlink("/etc", filepath.Join(base, "etc"))
	if got, err := safeSub(base, "Films/Classics"); err != nil || got != filepath.Join(base, "Films", "Classics") {
		t.Errorf("%q %v", got, err)
	}
	for _, bad := range []string{"etc", "Films/../../x", "missing"} {
		if _, err := safeSub(base, bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if got := cleanSub("/../Films//Classics/"); got != "Films/Classics" {
		t.Errorf("cleanSub = %q", got)
	}
	if got := cleanSub("/"); got != "" {
		t.Errorf("cleanSub(/) = %q", got)
	}
}

// Who can read what, by owner, group and others.
func TestCanRead(t *testing.T) {
	for _, c := range []struct {
		mode         fs.FileMode
		owner, group int
		dir, want    bool
	}{
		{0o644, 1000, 1000, false, true},
		{0o600, 1000, 1000, false, false},
		{0o600, 999, 1000, false, true},
		{0o640, 1000, 989, false, true},
		{0o700, 1000, 1000, true, false},
		{0o711, 1000, 1000, true, false}, // can enter, not list
		{0o755, 1000, 1000, true, true},
	} {
		if got := canRead(c.mode, c.owner, c.group, 999, []int{989}, c.dir); got != c.want {
			t.Errorf("%v %d:%d dir %v = %v", c.mode, c.owner, c.group, c.dir, got)
		}
	}
	if regularUser(0, 0) || regularUser(999, 989) || regularUser(65534, 65534) || !regularUser(1000, 1000) || !regularUser(1001, 100) {
		t.Error("regularUser")
	}
}

// The sample counts files Roostack's user couldn't read, and a folder it
// couldn't open as one; it names the owner of most.
func TestSample(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "open"), 0o755)
	os.MkdirAll(filepath.Join(dir, "closed"), 0o700)
	for _, f := range []string{"open/a.mkv", "open/b.mkv", "closed/c.mkv"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	os.Chmod(filepath.Join(dir, "open", "b.mkv"), 0o600)
	me := os.Getuid()
	// As another user (not the files' owner, not in their group).
	r := sample(dir, me+1, []int{-1}, 100, time.Now().Add(time.Minute))
	if r.Sampled != 3 || r.Unreadable != 2 || r.Partial {
		t.Errorf("as someone else: %+v", r)
	}
	if regularUser(me, os.Getgid()) && (r.Owner == nil || r.Owner.UID != me || r.OwnerFiles != 2) {
		t.Errorf("owner: %+v", r.Owner)
	}
	// As their owner: everything.
	if r := sample(dir, me, []int{os.Getgid()}, 100, time.Now().Add(time.Minute)); r.Unreadable != 0 || r.Owner != nil {
		t.Errorf("as the owner: %+v", r)
	}
	// A folder that can't be opened at all says so.
	if r := sample(filepath.Join(dir, "closed"), me+1, []int{-1}, 100, time.Now().Add(time.Minute)); !r.Closed || r.Sampled != 1 {
		t.Errorf("closed: %+v", r)
	}
	// A limit stops it, and says so.
	if r := sample(dir, me, nil, 1, time.Now().Add(time.Minute)); !r.Partial || r.Sampled != 1 {
		t.Errorf("limited: %+v", r)
	}
	list, err := listFolders(dir, "", me, []int{os.Getgid()})
	if err != nil || len(list.Folders) != 2 || list.Folders[1].Name != "open" || list.Folders[1].Files != 2 || list.Files != 0 {
		t.Errorf("list: %+v %v", list, err)
	}
	// The place's own files are counted too.
	if list, err := listFolders(dir, "open", me, []int{os.Getgid()}); err != nil || len(list.Folders) != 0 || list.Files != 2 {
		t.Errorf("a folder's files: %+v %v", list, err)
	}
}

// The API passes libraries through to the host.
func TestLibraryAPI(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	if err := c.MountLibrary(t.Context(), Library{Name: "films", Drive: "1111-2222", Sub: "Films", AsOwner: &Owner{UID: 1000, GID: 1000}}); err != nil {
		t.Fatal(err)
	}
	if host.library == nil || host.library.Name != "films" || host.library.Sub != "Films" || host.library.AsOwner.UID != 1000 {
		t.Errorf("mounted %+v", host.library)
	}
	if list, err := c.LibraryFolders(t.Context(), Library{Drive: "1111-2222", Sub: "x"}); err != nil || list.Folders[0].Name != "Films" {
		t.Errorf("folders %+v %v", list, err)
	}
	if libs, err := c.Libraries(t.Context()); err != nil || len(libs) != 1 || !libs[0].Mounted {
		t.Errorf("libraries %+v %v", libs, err)
	}
	if err := c.SetLibraryViews(t.Context(), "plex", []string{"films", "music"}); err != nil || strings.Join(host.views, " ") != "plex films music" {
		t.Errorf("views %q %v", host.views, err)
	}
	if err := c.RemoveLibraryViews(t.Context(), "plex"); err != nil || strings.Join(host.views, " ") != "removed plex" {
		t.Errorf("views removed %q %v", host.views, err)
	}
	if err := c.UnmountLibrary(t.Context(), "films"); err != nil || host.unmounted != "films" {
		t.Errorf("unmount %q %v", host.unmounted, err)
	}
}

// A share is checked before anything is mounted: a server's name or
// address, an SMB share's name or an NFS export's path, no line breaks in
// a login, no login for NFS.
func TestCheckShare(t *testing.T) {
	ok := func(s Share) bool {
		err := checkShare(s)
		return err == nil || strings.Contains(err.Error(), "needs the") // mount.cifs/mount.nfs may be missing here
	}
	for _, s := range []Share{{Type: "smb", Server: "nas.local", Path: "My Media", Username: "sam", Password: "p@ss w0rd"},
		{Type: "smb", Server: "192.168.1.20", Path: "media"}, {Type: "nfs", Server: "nas", Path: "/volume1/media"}} {
		if !ok(s) {
			t.Errorf("%+v: %v", s, checkShare(s))
		}
	}
	for _, s := range []Share{{Type: "smb", Server: "nas", Path: "a/b"}, {Type: "smb", Server: "nas local", Path: "m"},
		{Type: "smb", Server: "nas", Path: "m", Password: "a\nb"}, {Type: "smb", Server: "nas", Path: "m", Username: "a,b"},
		{Type: "nfs", Server: "nas", Path: "volume1"}, {Type: "nfs", Server: "nas", Path: "/v", Username: "sam"},
		{Type: "afp", Server: "nas", Path: "m"}, {Type: "smb", Server: "nas", Path: "m#x"}} {
		if err := checkShare(s); err == nil || strings.Contains(err.Error(), "needs the") {
			t.Errorf("%+v must be refused: %v", s, err)
		}
	}
}

// A share mounts read-only, automounted, an SMB share's files Roostack's
// and never writable, its login from its file (or as a guest).
func TestShareFstab(t *testing.T) {
	h := LinuxHost{UID: 999, GID: 989}
	smb := Share{Type: "smb", Server: "nas.local", Path: "My Media", Username: "sam"}
	opts := h.shareOptions(smb, "/etc/roostack/shares/media.cred")
	for _, want := range []string{"ro,", "x-systemd.automount", "_netdev", "nofail", "credentials=/etc/roostack/shares/media.cred",
		"uid=999,gid=989,file_mode=0444,dir_mode=0555"} {
		if !strings.Contains(opts, want) {
			t.Errorf("smb options lack %q: %s", want, opts)
		}
	}
	if opts := h.shareOptions(Share{Type: "smb", Server: "nas", Path: "m"}, ""); !strings.Contains(opts, ",guest") {
		t.Errorf("a guest share: %s", opts)
	}
	if opts := h.shareOptions(Share{Type: "nfs", Server: "nas", Path: "/v"}, ""); strings.Contains(opts, "uid=") || !strings.HasPrefix(opts, "ro,") {
		t.Errorf("nfs options: %s", opts)
	}
	if got := fstabEscape(shareSource(smb)); got != `//nas.local/My\040Media` {
		t.Errorf("source %q", got)
	}
	tab := withBlock("", shareTag("media"), `//nas.local/My\040Media /mnt/roostack-library/media cifs ro 0 0`)
	tab = withBlock(tab, libraryTag("films"), "/mnt/d/Films /mnt/roostack-library/films none bind,ro 0 0")
	if e := shareEntries(tab); len(e) != 1 || e[0].name != "media" || e[0].source != "//nas.local/My Media" || !e[0].share {
		t.Errorf("shares %+v", e)
	}
	if e := libraryEntries(tab); len(e) != 1 || e[0].name != "films" {
		t.Errorf("a share isn't a drive's library: %+v", e)
	}
	if got := removeBlock(tab, shareTag("media")); strings.Contains(got, "nas.local") {
		t.Errorf("removing the share left %q", got)
	}
	for out, want := range map[string]string{"mount error(13): Permission denied": "refused the login",
		"mount error(2): No such file or directory": "no share by that name", "mount error(112): Host is down": "didn't answer"} {
		if got := shareFailure(out); !strings.Contains(got, want) {
			t.Errorf("%q: %q", out, got)
		}
	}
}
