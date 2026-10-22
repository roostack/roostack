package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// Disk kinds: what a disk is to Roostack, which names it on the page.
const (
	DiskSystem = "system" // holds the operating system (/)
	DiskData   = "data"   // a separate disk for services' data or the backup store
	DiskMedia  = "media"  // a separate disk for the media folder
	DiskBackup = "backup" // a drive backups go on
)

// DiskSpace is one disk (filesystem) a server's Roostack uses, in bytes:
// the filesystem's numbers (statfs, as df), and what Roostack's parts of
// it take. A size is -1 until it has been counted (details.go: daily).
type DiskSpace struct {
	// Kind is a Disk* kind; Name is a backup drive's name (empty
	// otherwise); Mount is where the disk is mounted.
	Kind, Name, Mount string
	Total, Used, Free int64 // Free is what the roostack user can still write
	// Services is its services' data on this disk; Backups the backups of
	// the lab's servers on it (the backup store, or a backup drive);
	// Guests those it keeps for other labs.
	Services, Backups, Guests int64
	// Store: the backup store is on this disk (other labs' backups, and
	// the space promised to them).
	Store bool `json:",omitempty"`
	// Counted is when the sizes were counted (zero: not yet); Counting:
	// a count is running now (pages showing it refresh until it ends).
	Counted  time.Time `json:",omitzero"`
	Counting bool      `json:",omitempty"`
}

// Disks is a server that can report its disks (Local and Remote).
type Disks interface {
	DiskSpaces(ctx context.Context) ([]DiskSpace, error)
}

// StoreDisk is the disk holding the backup store, if it's in disks.
func StoreDisk(disks []DiskSpace) (DiskSpace, bool) {
	for _, d := range disks {
		if d.Store {
			return d, true
		}
	}
	return DiskSpace{}, false
}

// diskFolder is a folder Roostack uses, and what it holds.
type diskFolder struct {
	path string
	kind string // the Disk* kind it makes its disk, unless a more important folder is there too
	name string // a backup drive's name
	role string // "", "services", "media", "store" or "drive"
}

// folders are the folders Roostack uses on this server (SPEC.md §6.7):
// the system's, services' data, the media folder, the backup store, and
// the repository on each backup drive.
func (n *Local) folders() []diskFolder {
	p := n.Lab.Paths
	out := []diskFolder{{path: "/", kind: DiskSystem}, {path: p.ServicesDir, kind: DiskData, role: "services"},
		{path: p.MediaDir, kind: DiskMedia, role: "media"}, {path: p.BackupStoreDir(), kind: DiskData, role: "store"}}
	drive := func(d config.BackupDrive, repo string) {
		if d.Mount != "" && filepath.IsAbs(repo) {
			out = append(out, diskFolder{path: repo, kind: DiskBackup, name: d.Name, role: "drive"})
		}
	}
	drive(n.Lab.Backup.Drive, n.Lab.Backup.Repository)
	for _, c := range n.Lab.Backup.Copies {
		drive(c.Drive, c.Repository)
	}
	return out
}

// kindRank orders kinds: a disk is named by the most important folder on it.
var kindRank = map[string]int{DiskSystem: 0, DiskData: 1, DiskMedia: 2, DiskBackup: 3}

// DiskSpaces reports each disk holding a folder Roostack uses: the
// system's, a separate data or media disk, and backup drives. Folders on
// the same disk count once; a missing folder (a drive that's unplugged)
// is left out. The sizes come from caches.
func (n *Local) DiskSpaces(context.Context) ([]DiskSpace, error) {
	_, services := n.Assigned()
	n.sizes.mu.Lock()
	if n.sizes.at.IsZero() || time.Since(n.sizes.at) > sizeTTL {
		n.countSizesLocked()
	}
	dirs, at, counting := n.sizes.dirs, n.sizes.at, n.sizes.counting
	backups, guests := n.sizes.backups, n.sizes.guests
	n.sizes.mu.Unlock()
	size := func(path string) int64 {
		if s, ok := dirs[path]; ok {
			return s
		}
		return -1
	}
	if at.IsZero() {
		backups, guests = -1, -1
	}
	add := func(total *int64, n int64) {
		if *total < 0 || n < 0 {
			*total = -1
		} else {
			*total += n
		}
	}
	byDev := map[uint64]int{}
	var out []DiskSpace
	for _, f := range n.folders() {
		fi, err := os.Stat(f.path)
		if err != nil {
			continue
		}
		dev := uint64(fi.Sys().(*syscall.Stat_t).Dev)
		i, ok := byDev[dev]
		if !ok {
			var st syscall.Statfs_t
			if err := syscall.Statfs(f.path, &st); err != nil {
				continue
			}
			bs := int64(st.Bsize)
			out = append(out, DiskSpace{Kind: f.kind, Name: f.name, Mount: mountPoint(f.path),
				Total: int64(st.Blocks) * bs, Used: int64(st.Blocks-st.Bfree) * bs, Free: int64(st.Bavail) * bs,
				Counted: at, Counting: counting})
			i = len(out) - 1
			byDev[dev] = i
		}
		d := &out[i]
		if kindRank[f.kind] < kindRank[d.Kind] {
			d.Kind, d.Name = f.kind, ""
		}
		switch f.role {
		case "services", "media":
			for _, s := range services {
				add(&d.Services, size(filepath.Join(f.path, s.Name())))
			}
		case "store":
			d.Store = true
			add(&d.Backups, backups)
			add(&d.Guests, guests)
		case "drive":
			if !strings.HasPrefix(f.path, n.Lab.Paths.BackupStoreDir()+"/") { // not counted twice
				add(&d.Backups, size(f.path))
			}
		}
	}
	return out, nil
}

// mountPoint is where the disk holding path is mounted: the highest
// folder above it on the same device.
func mountPoint(path string) string {
	dev := func(p string) (uint64, bool) {
		fi, err := os.Stat(p)
		if err != nil {
			return 0, false
		}
		return uint64(fi.Sys().(*syscall.Stat_t).Dev), true
	}
	want, ok := dev(path)
	if !ok {
		return path
	}
	for {
		up := filepath.Dir(path)
		if up == path {
			return path
		}
		if d, ok := dev(up); !ok || d != want {
			return path
		}
		path = up
	}
}
