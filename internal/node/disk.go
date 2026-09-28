package node

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// DiskSpace is the space on the disk holding a server's Roostack data
// (its services' data and the backups it keeps), in bytes: the
// filesystem's numbers, and what Roostack's parts of it take. A size is
// -1 until it has been counted (in the background, every sizeTTL).
type DiskSpace struct {
	Total, Used, Free int64 // Free is what the roostack user can still write
	// Services is its services' data; Backups the backups it keeps for
	// the lab's servers (its backup store); Guests those it keeps for
	// other labs.
	Services, Backups, Guests int64
}

// Disks is a server that can report its disk space (Local and Remote).
type Disks interface {
	DiskSpace(ctx context.Context) (DiskSpace, error)
}

// storeSizes caches the sizes of the backup store's parts.
type storeSizes struct {
	mu              sync.Mutex
	backups, guests int64
	at              time.Time
	counting        bool
}

// DiskSpace reports the disk holding Roostack's data (statfs of the data
// folder, /srv/roostack by default); the sizes come from caches.
func (n *Local) DiskSpace(context.Context) (DiskSpace, error) {
	root := filepath.Dir(n.Lab.Paths.MediaDir)
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return DiskSpace{}, err
	}
	bs := int64(st.Bsize)
	d := DiskSpace{Total: int64(st.Blocks) * bs, Used: int64(st.Blocks-st.Bfree) * bs, Free: int64(st.Bavail) * bs}

	_, services := n.Assigned()
	d.Services = 0
	for _, s := range services {
		size := n.dataSize(s.Package.Name)
		if size < 0 {
			d.Services = -1
			break
		}
		d.Services += size
	}
	d.Backups, d.Guests = n.storeSize()
	return d, nil
}

// storeSize returns the backup store's cached sizes (for the lab's
// servers, for other labs), counting them again in the background when
// they're old.
func (n *Local) storeSize() (backups, guests int64) {
	c := &n.store
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() && !c.counting {
		c.backups, c.guests = -1, -1
	}
	if !c.counting && time.Since(c.at) > sizeTTL {
		c.counting = true
		go func() {
			dir := n.Lab.Paths.BackupStoreDir()
			guests, gerr := treeSize(filepath.Join(dir, "guests"))
			all, err := treeSize(dir)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.counting, c.at = false, time.Now()
			if err == nil && gerr == nil {
				c.backups, c.guests = all-guests, guests
			}
		}()
	}
	return c.backups, c.guests
}

// treeSize adds up the sizes of the files under dir (0 if there's no dir).
func treeSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// DiskSpace asks the server.
func (r *Remote) DiskSpace(ctx context.Context) (DiskSpace, error) {
	var out DiskSpace
	err := r.call(ctx, http.MethodGet, "/v1/disk", nil, &out)
	return out, err
}
