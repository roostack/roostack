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
// root. The agent prepares it so Roostack needs no command line: it creates
// the folder if needed, and inside it a folder named "roostack" that the
// roostack user owns. Nothing that already exists changes owner, except
// that "roostack" folder when it's empty.
//
// The roostack user can write inside some of these places (/srv/roostack),
// so it could plant a symlink there, even while the agent is working. Every
// folder is therefore made and handed over through an os.Root, which never
// follows a symlink out of the allowed place, and with Lchown, which never
// follows one at all. Checking the path first isn't enough: the symlink
// can appear between the check and the change.

// FolderRoots are where backup folders may be prepared.
var FolderRoots = []string{"/mnt", "/media", "/srv"}

// BackupFolderName is the folder Roostack keeps its backups in.
const BackupFolderName = "roostack"

// prepareFolder creates dir (if needed) under one of roots, and dir/roostack
// owned by uid:gid. It returns dir/roostack, with symlinks resolved.
func prepareFolder(dir string, roots []string, uid, gid int) (string, error) {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return "", errors.New("the folder must be a full path")
	}
	top := rootOf(dir, roots)
	if top == "" {
		return "", fmt.Errorf("choose a folder under %s", strings.Join(roots, ", "))
	}
	outside := fmt.Errorf("%s leads outside %s", dir, top)
	// Symlinks in the part that exists are resolved first: that gives a
	// clear message for one leading elsewhere, and lets os.Root use one that
	// stays inside (it refuses absolute symlinks). A symlink swapped in
	// after this is still kept inside by the os.Root.
	existing := existingAncestor(dir)
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if rootOf(real, []string{top}) == "" {
		return "", outside
	}
	rest, err := filepath.Rel(existing, dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(top, filepath.Join(real, rest))
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// Folders made here are Roostack's, like the one inside them.
	p := "."
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			p = filepath.Join(p, part)
			switch err := root.Mkdir(p, 0o755); {
			case err == nil:
				if err := root.Lchown(p, uid, gid); err != nil {
					return "", err
				}
			case errors.Is(err, fs.ErrExist):
			default:
				return "", fmt.Errorf("%w (%w)", outside, err)
			}
		}
	}
	if fi, err := root.Stat(p); err != nil {
		return "", fmt.Errorf("%w (%w)", outside, err)
	} else if !fi.IsDir() {
		return "", fmt.Errorf("%s isn't a folder", dir)
	}
	target := filepath.Join(p, BackupFolderName)
	fi, err := root.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := root.Mkdir(target, 0o750); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s exists and isn't a folder", filepath.Join(dir, BackupFolderName))
	default:
		st, _ := fi.Sys().(*syscall.Stat_t)
		if st != nil && int(st.Uid) == uid {
			return resolved(dir)
		}
		if empty, err := emptyDir(root, target); err != nil || !empty {
			return "", fmt.Errorf("%s already exists and belongs to someone else", filepath.Join(dir, BackupFolderName))
		}
	}
	if err := root.Lchown(target, uid, gid); err != nil {
		return "", err
	}
	return resolved(dir)
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

// existingAncestor is p, or its closest parent that exists.
func existingAncestor(p string) string {
	for {
		if _, err := os.Lstat(p); err == nil || p == "/" {
			return p
		}
		p = filepath.Dir(p)
	}
}

// emptyDir reports whether the folder name in root has nothing in it.
func emptyDir(root *os.Root, name string) (bool, error) {
	f, err := root.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// resolved is dir's roostack folder, with symlinks resolved (for the
// caller to record; it's Roostack's own folder by now).
func resolved(dir string) (string, error) {
	return filepath.EvalSymlinks(filepath.Join(dir, BackupFolderName))
}

// chownIn gives name (a path under top) to uid:gid without following
// symlinks, and without leaving top.
func chownIn(top, name string, uid, gid int) error {
	root, err := os.OpenRoot(top)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(top, name)
	if err != nil {
		return err
	}
	return root.Lchown(rel, uid, gid)
}

// PrepareBackupFolder readies a backup folder for the roostack user.
func (h LinuxHost) PrepareBackupFolder(dir string) (string, error) {
	if h.UID == 0 {
		return "", errors.New("the roostack user isn't known")
	}
	return prepareFolder(dir, FolderRoots, h.UID, h.GID)
}
