package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The controller's firewall knows its Caddy from the roostack user's other
// processes by Caddy's cgroup (Firewall.CaddyOnly): a pasta-network
// container's connections leave from its own unit's cgroup, and a
// bridge-network container's from the user's rootless-netns scope
// (docs/ASSUMPTIONS.md J7). nft turns a cgroup's path into its ID when
// it's added, and a cgroup made again (Caddy restarting) has a new one,
// so the path is never in the ruleset, where a missing cgroup would stop
// the whole ruleset loading: the agent puts it in the caddy set after
// each load, and again whenever Caddy's cgroup is made again.

const (
	caddySetName = "caddy"
	// caddyUnit is Caddy's systemd unit, as Roostack names it
	// (generate.CaddyUnit, which a test checks).
	caddyUnit = "roostack-caddy"
	// caddyCgroupLevel is how deep Caddy's unit is in the cgroup tree
	// (CaddyCgroup): its pasta and container are below it.
	caddyCgroupLevel = 5
	// caddyCheckEvery is how often the agent looks for Caddy's cgroup
	// being made again: for up to that long after Caddy starts, it can't
	// reach the lab's other servers.
	caddyCheckEvery = time.Second
)

// CaddyCgroup is Caddy's unit's cgroup under the roostack user's systemd
// manager, relative to the cgroup root.
func CaddyCgroup(uid int) string {
	return fmt.Sprintf("user.slice/user-%d.slice/user@%d.service/app.slice/%s.service", uid, uid, caddyUnit)
}

var caddySet = &caddyCgroup{root: "/sys/fs/cgroup", stat: cgroupID, nft: runNft}

type caddyCgroup struct {
	mu sync.Mutex
	// want: the loaded ruleset has the caddy set. path is Caddy's cgroup
	// (CaddyCgroup), "" until the agent knows the roostack user; inSet
	// the ID put in the set, 0 for none.
	want  bool
	path  string
	inSet uint64
	root  string
	stat  func(path string) (uint64, error)
	nft   func(args ...string) error
	log   *slog.Logger
}

// cgroupID is a cgroup's ID: its folder's inode number in cgroup2.
func cgroupID(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return st.Ino, nil
}

// loaded notes a ruleset was loaded, which emptied the set, and fills it.
func (c *caddyCgroup) loaded(ruleset string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.want = strings.Contains(ruleset, "\n\tset "+caddySetName+" {\n")
	c.inSet = 0
	c.check()
}

// check puts Caddy's cgroup in the set if it isn't there yet, or has been
// made again since. Without the cgroup (Caddy isn't running), the set is
// emptied: the ID it held won't come back.
func (c *caddyCgroup) check() {
	if !c.want || c.path == "" {
		return
	}
	set := "inet roostack " + caddySetName
	id, err := c.stat(filepath.Join(c.root, c.path))
	if err != nil {
		if c.inSet != 0 && c.nft("flush set "+set) == nil {
			c.inSet = 0
		}
		return
	}
	if id == c.inSet {
		return
	}
	// One transaction, so the set is never empty while Caddy runs.
	if err := c.nft(fmt.Sprintf("flush set %s; add element %s { %q }", set, set, c.path)); err != nil {
		if c.log != nil {
			c.log.Warn("letting Caddy reach the lab's other servers", "err", err)
		}
		return
	}
	c.inSet = id
}

// WatchCaddy keeps the controller's Caddy in the firewall's caddy set
// until ctx ends, uid being the roostack user's.
func WatchCaddy(ctx context.Context, uid int, log *slog.Logger) {
	caddySet.watch(ctx, CaddyCgroup(uid), log, caddyCheckEvery)
}

func (c *caddyCgroup) watch(ctx context.Context, path string, log *slog.Logger, every time.Duration) {
	c.mu.Lock()
	c.path, c.log = path, log
	c.check()
	c.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.mu.Lock()
			c.check()
			c.mu.Unlock()
		}
	}
}
