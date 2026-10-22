package agent

import (
	"errors"
	"io"
	"os"
	"syscall"
)

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
