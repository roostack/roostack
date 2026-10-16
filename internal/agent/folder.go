package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Backup folders: the setup wizard offers "a disk or folder on this
// server" (e.g. where a second disk is mounted), which usually belongs to
// root. The agent prepares it so Roostack needs no command line: inside
// the folder, which must exist already, it makes a folder named
// "roostack" that the roostack user owns. Nothing that already exists
// changes owner, except that "roostack" folder when it's empty.
//
// The roostack user can write inside some of these places (/srv/roostack),
// so it could plant a symlink there, even while the agent is working. So
// the agent makes only that one last folder (security review 2026-10-16:
// it used to make any missing folders on the way), inside a handle on the
// folder it was given, opened through an os.Root that never leaves the
// allowed place; it opens the new folder without following a symlink and
// changes its owner through that handle (fchown), never by path.
// Checking a path first isn't enough: a symlink can appear between the
// check and the change.

// FolderRoots are where backup folders may be prepared.
var FolderRoots = []string{"/mnt", "/media", "/srv"}

// BackupFolderName is the folder Roostack keeps its backups in.
const BackupFolderName = "roostack"

// prepareFolder makes dir/roostack, owned by uid:gid, in dir (a folder
// that exists, under one of roots). It returns dir/roostack, with symlinks
// resolved.
func prepareFolder(dir string, roots []string, uid, gid int) (string, error) {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return "", errors.New("the folder must be a full path")
	}
	top := rootOf(dir, roots)
	if top == "" {
		return "", fmt.Errorf("choose a folder under %s", strings.Join(roots, ", "))
	}
	// Symlinks are resolved first: that gives a clear message for one
	// leading elsewhere, and lets os.Root use one that stays inside (it
	// refuses absolute symlinks). A symlink swapped in after this is still
	// kept inside by the os.Root.
	real, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s doesn't exist: make it first (sudo mkdir -p %s)", dir, dir)
	}
	if err != nil {
		return "", err
	}
	if rootOf(real, []string{top}) == "" {
		return "", fmt.Errorf("%s leads outside %s", dir, top)
	}
	rel, err := filepath.Rel(top, real)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return "", err
	}
	defer root.Close()
	parent, err := root.Open(rel)
	if err != nil {
		return "", fmt.Errorf("%s leads outside %s (%w)", dir, top, err)
	}
	defer parent.Close()
	if fi, err := parent.Stat(); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", fmt.Errorf("%s isn't a folder", dir)
	}
	if err := ownedFolder(parent, BackupFolderName, uid, gid); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, BackupFolderName), err)
	}
	return filepath.Join(real, BackupFolderName), nil
}

// ownedFolder makes the folder name inside parent (if it isn't there) and
// gives it to uid:gid. It opens it without following a symlink and changes
// its owner through that handle. A folder already there that someone else
// owns is taken over only when it's empty.
func ownedFolder(parent *os.File, name string, uid, gid int) error {
	pfd := int(parent.Fd())
	if err := syscall.Mkdirat(pfd, name, 0o750); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	fd, err := syscall.Openat(pfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return errors.New("it exists and isn't a folder")
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if int(st.Uid) == uid {
		return nil
	}
	if _, err := f.Readdirnames(1); !errors.Is(err, io.EOF) {
		return errors.New("it already exists and belongs to someone else")
	}
	return f.Chown(uid, gid)
}

// rootOf returns the root in roots that p is in (or is), or "".
func rootOf(p string, roots []string) string {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return r
		}
	}
	return ""
}

// systemBackupDir makes SystemBackupDir (if need be), the roostack user's,
// and the roostack folder inside it. Its parent, /srv/roostack, is the
// roostack user's already.
func systemBackupDir(uid, gid int) error {
	root, err := os.OpenRoot("/srv")
	if err != nil {
		return err
	}
	defer root.Close()
	parent, err := root.Open(filepath.Base(filepath.Dir(SystemBackupDir)))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := ownedFolder(parent, filepath.Base(SystemBackupDir), uid, gid); err != nil {
		return fmt.Errorf("%s: %w", SystemBackupDir, err)
	}
	_, err = prepareFolder(SystemBackupDir, []string{"/srv"}, uid, gid)
	return err
}

// PrepareBackupFolder readies a backup folder for the roostack user.
func (h LinuxHost) PrepareBackupFolder(dir string) (string, error) {
	if h.UID == 0 {
		return "", errors.New("the roostack user isn't known")
	}
	return prepareFolder(dir, FolderRoots, h.UID, h.GID)
}
