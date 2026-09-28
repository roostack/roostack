package agent

import (
	"errors"
	"fmt"
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

// FolderRoots are where backup folders may be prepared.
var FolderRoots = []string{"/mnt", "/media", "/srv"}

// BackupFolderName is the folder Roostack keeps its backups in.
const BackupFolderName = "roostack"

// prepareFolder creates dir (if needed) under one of roots, and dir/roostack
// owned by uid:gid. It returns dir/roostack.
func prepareFolder(dir string, roots []string, uid, gid int) (string, error) {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return "", errors.New("the folder must be a full path")
	}
	under := func(p string) bool {
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+"/") {
				return true
			}
		}
		return false
	}
	if !under(dir) {
		return "", fmt.Errorf("choose a folder under %s", strings.Join(roots, ", "))
	}
	// Folders made here are Roostack's, like the one inside them.
	var made []string
	for p := dir; under(p); p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		made = append(made, p)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, p := range made {
		if err := os.Chown(p, uid, gid); err != nil {
			return "", err
		}
	}
	// No symlink may lead outside the allowed places.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !under(real) {
		return "", fmt.Errorf("%s leads outside %s", dir, strings.Join(roots, ", "))
	}
	target := filepath.Join(real, BackupFolderName)
	fi, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(target, 0o750); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s exists and isn't a folder", target)
	default:
		st, _ := fi.Sys().(*syscall.Stat_t)
		if st != nil && int(st.Uid) == uid {
			return target, nil // already Roostack's
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) > 0 {
			return "", fmt.Errorf("%s already exists and belongs to someone else", target)
		}
	}
	if err := os.Chown(target, uid, gid); err != nil {
		return "", err
	}
	return target, nil
}

// PrepareBackupFolder readies a backup folder for the roostack user.
func (h LinuxHost) PrepareBackupFolder(dir string) (string, error) {
	if h.UID == 0 {
		return "", errors.New("the roostack user isn't known")
	}
	return prepareFolder(dir, FolderRoots, h.UID, h.GID)
}
