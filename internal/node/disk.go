package node

import (
	"context"
	"net/http"
	"path/filepath"
	"syscall"
	"time"
)

// DiskSpace is the space on the disk holding a server's Roostack data
// (its services' data and the backups it keeps), in bytes: the
// filesystem's numbers (statfs, as df), and what Roostack's parts of it
// take. A size is -1 until it has been counted (details.go: daily).
type DiskSpace struct {
	Total, Used, Free int64 // Free is what the roostack user can still write
	// Services is its services' data; Backups the backups it keeps for
	// the lab's servers (its backup store); Guests those it keeps for
	// other labs.
	Services, Backups, Guests int64
	// Counted is when the sizes were counted (zero: not yet); Counting:
	// a count is running now (pages showing it refresh until it ends).
	Counted  time.Time `json:",omitzero"`
	Counting bool      `json:",omitempty"`
}

// Disks is a server that can report its disk space (Local and Remote).
type Disks interface {
	DiskSpace(ctx context.Context) (DiskSpace, error)
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
	d.Backups, d.Guests, d.Counted = n.storeSize()
	n.sizes.mu.Lock()
	d.Counting = n.sizes.counting
	n.sizes.mu.Unlock()
	return d, nil
}

// DiskSpace asks the server.
func (r *Remote) DiskSpace(ctx context.Context) (DiskSpace, error) {
	var out DiskSpace
	err := r.call(ctx, http.MethodGet, "/v1/disk", nil, &out)
	return out, err
}
