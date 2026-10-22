package node

import (
	"cmp"
	"context"
	"maps"
	"slices"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/store"
)

// Media libraries (SPEC.md §8.1, "Media libraries"): each apply mounts the
// server's libraries through its agent, unmounts those taken away, and sets
// each service's view of the ones it may read, before services start (their
// containers bind their views).

// LibraryAgent is roostack-agent's media library operations.
type LibraryAgent interface {
	Drives(ctx context.Context) ([]agent.Drive, error)
	LibraryFolders(ctx context.Context, l agent.Library) (agent.FolderList, error)
	Libraries(ctx context.Context) ([]agent.LibraryStatus, error)
	MountLibrary(ctx context.Context, l agent.Library) error
	UnmountLibrary(ctx context.Context, name string) error
	SetLibraryViews(ctx context.Context, service string, libraries []string, at map[string]string) error
	RemoveLibraryViews(ctx context.Context, service string) error
	CheckShare(ctx context.Context, s agent.Share) (agent.FolderList, error)
	SaveShareLogin(ctx context.Context, name string, s agent.Share) error
}

// Runtime settings (State) recording what was applied, so an apply only
// remounts what changed.
const (
	librariesKey = "media.libraries" // JSON: name → agent.Library
	viewsKey     = "media.views"     // JSON: services with a view
)

// applyLibraries brings the server's libraries and services' views in line
// with the assignment. A library that can't be mounted (its drive
// unplugged, say) is logged and tried again next time; it doesn't stop the
// apply, as its services run, seeing an empty folder.
func (n *Local) applyLibraries(ctx context.Context, act *activity.Log, a Assignment, desired reconcile.Desired) {
	readers := map[string][]string{} // service → libraries it may read
	at := map[string]map[string]string{}
	for _, out := range desired.Services {
		if !out.ReadsLibraries {
			continue
		}
		at[out.Service] = out.LibraryAt
		var mine []string
		for _, s := range a.Services {
			if s.Name() == out.Service {
				mine = s.Instance.Libraries
			}
		}
		readers[out.Service] = mine
	}
	if n.Libraries == nil {
		if len(a.Libraries) > 0 || len(readers) > 0 {
			n.Log.Warn("media libraries can't be mounted: no agent")
		}
		return
	}
	// What was mounted and shown before (none without State).
	applied, views := map[string]agent.Library{}, map[string]bool{}
	if n.State != nil {
		a, err := store.GetJSON[map[string]agent.Library](ctx, n.State, librariesKey)
		maps.Copy(applied, a)
		v, verr := store.GetJSON[map[string]bool](ctx, n.State, viewsKey)
		maps.Copy(views, v)
		if err := cmp.Or(err, verr); err != nil {
			n.Log.Error("reading the media libraries", "err", err)
		}
	}
	current := map[string]bool{}
	if libs, err := n.Libraries.Libraries(ctx); err == nil {
		for _, l := range libs {
			current[l.Name] = true
		}
	} else {
		act.Printf("Reading the media libraries: %v", err)
		return
	}

	want := map[string]agent.Library{}
	for _, l := range a.Libraries {
		want[l.Name] = l
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		l := want[name]
		if prev, ok := applied[name]; ok && current[name] && sameLibrary(prev, l) {
			continue
		}
		act.Step("Mounting the media library %s", name)
		if err := n.Libraries.MountLibrary(ctx, l); err != nil {
			act.Printf("The media library %s isn't mounted: %v", name, err)
			n.Log.Error("mounting a media library", "library", name, "err", err)
			delete(applied, name)
			continue
		}
		applied[name] = l
	}
	// Views go before the libraries they show.
	for _, svc := range slices.Sorted(maps.Keys(readers)) {
		var libs []string
		for _, l := range readers[svc] {
			if _, ok := want[l]; ok {
				libs = append(libs, l)
			}
		}
		if err := n.Libraries.SetLibraryViews(ctx, svc, libs, at[svc]); err != nil {
			act.Printf("Setting the media libraries %s reads: %v", svc, err)
			n.Log.Error("setting a service's media libraries", "service", svc, "err", err)
			continue
		}
		views[svc] = true
	}
	for _, svc := range slices.Sorted(maps.Keys(views)) {
		if _, ok := readers[svc]; ok {
			continue
		}
		if err := n.Libraries.RemoveLibraryViews(ctx, svc); err != nil {
			n.Log.Error("removing a service's media libraries", "service", svc, "err", err)
			continue
		}
		delete(views, svc)
	}
	for name := range current {
		if _, ok := want[name]; ok {
			continue
		}
		act.Step("Removing the media library %s", name)
		if err := n.Libraries.UnmountLibrary(ctx, name); err != nil {
			act.Printf("The media library %s is still mounted: %v", name, err)
			n.Log.Error("unmounting a media library", "library", name, "err", err)
			continue
		}
		delete(applied, name)
	}
	if n.State != nil {
		err := cmp.Or(store.PutJSON(ctx, n.State, librariesKey, applied), store.PutJSON(ctx, n.State, viewsKey, views))
		if err != nil {
			n.Log.Error("recording the media libraries", "err", err)
		}
	}
}

// sameLibrary says whether two libraries mount the same thing the same way.
func sameLibrary(a, b agent.Library) bool {
	if a.Name != b.Name || a.Drive != b.Drive || a.Sub != b.Sub || a.Folder != b.Folder || (a.AsOwner == nil) != (b.AsOwner == nil) {
		return false
	}
	return a.AsOwner == nil || (a.AsOwner.UID == b.AsOwner.UID && a.AsOwner.GID == b.AsOwner.GID)
}
