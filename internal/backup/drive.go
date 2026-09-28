package backup

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckDrive reports whether the backup drive is there: the filesystem
// with this UUID is connected and mounted at mount. Otherwise a backup
// would write into the empty mount folder, on this server's own disk.
func CheckDrive(mount, uuid, name string) error {
	name = cmp.Or(name, "the backup drive")
	dev, err := os.Stat(filepath.Join(DiskByUUID, uuid))
	if err != nil {
		return fmt.Errorf("%s isn't connected: plug it in (backups carry on as soon as it's back)", name)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(mount, &st); err != nil {
		return fmt.Errorf("%s isn't mounted at %s: %w", name, mount, err)
	}
	raw, ok := dev.Sys().(*syscall.Stat_t)
	if !ok || st.Dev != raw.Rdev {
		return fmt.Errorf("%s is connected but not mounted at %s: restart the server, or ask for help", name, mount)
	}
	return nil
}

// DiskByUUID is where the system names filesystems by UUID (tests replace it).
var DiskByUUID = "/dev/disk/by-uuid"
