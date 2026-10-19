package node

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
)

// fakeLibraries records the agent calls an apply makes; mounted is what
// the agent says is in fstab.
type fakeLibraries struct {
	mounted map[string]bool
	calls   []string
}

func (f *fakeLibraries) Drives(context.Context) ([]agent.Drive, error) { return nil, nil }
func (f *fakeLibraries) LibraryFolders(context.Context, agent.Library) (agent.FolderList, error) {
	return agent.FolderList{}, nil
}
func (f *fakeLibraries) Libraries(context.Context) ([]agent.LibraryStatus, error) {
	var out []agent.LibraryStatus
	for name := range f.mounted {
		out = append(out, agent.LibraryStatus{Name: name})
	}
	return out, nil
}
func (f *fakeLibraries) MountLibrary(_ context.Context, l agent.Library) error {
	f.mounted[l.Name] = true
	f.calls = append(f.calls, "mount "+l.Name)
	return nil
}
func (f *fakeLibraries) UnmountLibrary(_ context.Context, name string) error {
	delete(f.mounted, name)
	f.calls = append(f.calls, "unmount "+name)
	return nil
}
func (f *fakeLibraries) SetLibraryViews(_ context.Context, svc string, libs []string) error {
	f.calls = append(f.calls, "view "+svc+" "+strings.Join(libs, ","))
	return nil
}
func (f *fakeLibraries) RemoveLibraryViews(_ context.Context, svc string) error {
	f.calls = append(f.calls, "no view "+svc)
	return nil
}

// Each apply mounts the server's libraries that are new or changed (not
// the rest again), sets the views of services reading libraries (only
// libraries this server has), removes views of services gone, and
// unmounts libraries taken away (SPEC.md §8.1).
func TestApplyLibraries(t *testing.T) {
	libs := &fakeLibraries{mounted: map[string]bool{"old": true}}
	n := &Local{Libraries: libs, State: memKV{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	act := activity.New().Start(activity.Apply, "test")
	plex := state.Service{Instance: &instance.Instance{Package: "plex", Libraries: []string{"films", "elsewhere"}}}
	apply := func(a Assignment, readers ...string) []string {
		libs.calls = nil
		var d reconcile.Desired
		for _, s := range readers {
			d.Services = append(d.Services, &generate.Output{Service: s, ReadsLibraries: true})
		}
		d.Services = append(d.Services, &generate.Output{Service: "vaultwarden"})
		n.applyLibraries(context.Background(), act, a, d)
		return libs.calls
	}
	films := agent.Library{Name: "films", Drive: "1111-2222", Sub: "Films"}
	music := agent.Library{Name: "music", Folder: "/srv/music"}
	a := Assignment{Services: []state.Service{plex}, Libraries: []agent.Library{films, music}}
	if got, want := apply(a, "plex"), []string{"mount films", "mount music", "view plex films", "unmount old"}; !slices.Equal(got, want) {
		t.Errorf("first apply: %q, want %q", got, want)
	}
	if got, want := apply(a, "plex"), []string{"view plex films"}; !slices.Equal(got, want) {
		t.Errorf("nothing changed: %q, want %q", got, want)
	}
	films.AsOwner = &agent.Owner{UID: 1000, GID: 1000}
	a.Libraries = []agent.Library{films, music}
	if got, want := apply(a, "plex"), []string{"mount films", "view plex films"}; !slices.Equal(got, want) {
		t.Errorf("read as its owner now: %q, want %q", got, want)
	}
	a = Assignment{Libraries: []agent.Library{music}}
	if got, want := apply(a), []string{"no view plex", "unmount films"}; !slices.Equal(got, want) {
		t.Errorf("plex removed, films taken away: %q, want %q", got, want)
	}
}
