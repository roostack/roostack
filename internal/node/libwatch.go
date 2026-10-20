package node

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
)

// Libraries bound one by one at paths of their own (SPEC.md §8.4, "Media
// libraries at their old paths"): a container binding a library at its
// own path keeps that mount, which breaks for good once the library's
// drive is unplugged, even after it's plugged back in (docs/ASSUMPTIONS.md
// W5). So once a minute the server looks at each such library's mount in
// the service's view folder, and when it's a different mount than before
// (the drive came back, or arrived after the service started), restarts
// the containers binding it. The first look (at start) only notes what's
// there: services start after their drives.

// libraryWatchEvery is how often the libraries are looked at.
const libraryWatchEvery = time.Minute

// libraryLookTimeout bounds looking at one library: a share whose NAS is
// off can hang.
const libraryLookTimeout = 3 * time.Second

// libraryWatch is what the watch saw last: service/library → the mount's
// ID ("" when nothing was mounted).
type libraryWatch struct {
	mu   sync.Mutex
	seen map[string]string
}

// WatchLibraries looks at libraries bound at their own paths every minute
// until ctx ends.
func (n *Local) WatchLibraries(ctx context.Context) {
	t := time.NewTicker(libraryWatchEvery)
	defer t.Stop()
	for {
		n.checkLibraryMounts(ctx, readMountinfo)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// readMountinfo reads this process's mount table.
func readMountinfo() string {
	b, _ := os.ReadFile("/proc/self/mountinfo")
	return string(b)
}

// checkLibraryMounts looks once (WatchLibraries), restarting what needs it.
func (n *Local) checkLibraryMounts(ctx context.Context, mountinfo func() string) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	lab := n.lab(a)
	type bound struct {
		service, title, library, path string
		units                         []string
	}
	var all []bound
	for _, s := range a.Services {
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil || len(out.LibraryBinds) == 0 {
			continue
		}
		for _, l := range out.LibraryBinds {
			all = append(all, bound{s.Name(), s.Title(), l, filepath.Join(config.LibraryView(s.Name()), l), out.LibraryUnits})
		}
	}
	for _, b := range all {
		lookAt(b.path) // an automount mounts when looked at
	}
	table := mountinfo()
	n.libWatch.mu.Lock()
	defer n.libWatch.mu.Unlock()
	if n.libWatch.seen == nil {
		n.libWatch.seen = map[string]string{}
	}
	restart := map[string][]string{} // service → libraries back
	titles, units := map[string]string{}, map[string][]string{}
	for _, b := range all {
		key := b.service + "/" + b.library
		id := mountID(table, b.path)
		before, known := n.libWatch.seen[key]
		n.libWatch.seen[key] = id
		if known && id != "" && id != before && n.running(ctx, b.units) {
			restart[b.service] = append(restart[b.service], b.library)
			titles[b.service], units[b.service] = b.title, b.units
		}
	}
	for _, svc := range slices.Sorted(maps.Keys(restart)) {
		var act *activity.Log
		if n.Activity != nil {
			act = n.Activity.Start(activity.LibraryRestart, "Restarting "+titles[svc])
		}
		libs := restart[svc]
		act.Printf("The media %s %s %s back. %s shows %s at a place of its own, and sees %s again only once it restarts.",
			oneOrMany(len(libs), "library", "libraries"), strings.Join(libs, ", "), oneOrMany(len(libs), "is", "are"),
			titles[svc], oneOrMany(len(libs), "it", "them"), oneOrMany(len(libs), "it", "them"))
		err := n.Reconciler.Systemd.Restart(ctx, units[svc]...)
		if err != nil {
			n.Log.Error("restarting a service for its library", "service", svc, "err", err)
		}
		act.Finish(err)
	}
}

// oneOrMany picks one's word or many's.
func oneOrMany(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// running says whether the units are all active (a service stopped on
// purpose, waiting for its data say, isn't started by a library).
func (n *Local) running(ctx context.Context, units []string) bool {
	if n.Units == nil || len(units) == 0 {
		return false
	}
	st, err := n.Units.Status(ctx, units...)
	if err != nil {
		return false
	}
	for _, u := range st {
		if u.ActiveState != "active" {
			return false
		}
	}
	return true
}

// lookAt stats a path, giving up after libraryLookTimeout.
func lookAt(path string) {
	done := make(chan struct{})
	go func() { os.Stat(path); close(done) }()
	select {
	case <-done:
	case <-time.After(libraryLookTimeout):
	}
}

// mountID is the ID of what's mounted at path in a mount table
// (/proc/self/mountinfo: the ID first, the mount point fifth), the last
// one there if several are; an automount's own entry (autofs) doesn't
// count. "" when nothing is.
func mountID(table, path string) string {
	id := ""
	for _, l := range strings.Split(table, "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && fstabUnescape(f[4]) == path && !slices.Contains(f, "autofs") {
			id = f[0]
		}
	}
	return id
}

// fstabUnescape reads an escaped mount table field (spaces as \040).
func fstabUnescape(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(s)
}
