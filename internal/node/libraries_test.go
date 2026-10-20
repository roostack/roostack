package node

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
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
func (f *fakeLibraries) CheckShare(context.Context, agent.Share) (agent.FolderList, error) {
	return agent.FolderList{}, nil
}
func (f *fakeLibraries) SaveShareLogin(context.Context, string, agent.Share) error { return nil }
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
func (f *fakeLibraries) SetLibraryViews(_ context.Context, svc string, libs []string, at map[string]string) error {
	f.calls = append(f.calls, "view "+svc+" "+strings.Join(libs, ","))
	for _, name := range slices.Sorted(maps.Keys(at)) {
		f.calls = append(f.calls, "at "+svc+" "+name+"="+at[name])
	}
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

// A service binding a library at a path of its own restarts once the
// library is mounted anew (its drive came back), not at the first look,
// nor while it's gone, nor when it's stopped (SPEC.md §8.4).
func TestLibraryWatch(t *testing.T) {
	p := playerPackage(t)
	sd := &fakeUnits{active: true}
	n := &Local{Lab: config.Lab{Paths: config.Paths{ServicesDir: t.TempDir(), MediaDir: t.TempDir()}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Activity: activity.New(), Units: sd,
		Reconciler: &reconcile.Reconciler{Systemd: sd}}
	n.assignment = Assignment{Services: []state.Service{{Package: p, Instance: &instance.Instance{Package: "player", Version: "1",
		Updates: "notify", Libraries: []string{"films"}, LibraryPaths: map[string]string{"films": "/movies"}}}}}
	at := func(id string) func() string {
		return func() string {
			if id == "" {
				return "40 1 0:50 / /mnt/roostack-library-views/player/films rw - autofs systemd-1 rw\n"
			}
			return "40 1 0:50 / /mnt/roostack-library-views/player/films rw - autofs systemd-1 rw\n" +
				id + " 40 8:17 /films /mnt/roostack-library-views/player/films ro - ext4 /dev/sdb1 ro\n"
		}
	}
	for i, step := range []struct {
		table   string
		active  bool
		restart bool
	}{
		{"51", true, false}, // the first look
		{"51", true, false},
		{"", true, false},   // unplugged
		{"77", true, true},  // back
		{"77", true, false}, // and seen
		{"", false, false},
		{"80", false, false}, // stopped: no restart
		{"80", true, false},
	} {
		sd.active, sd.restarted = step.active, nil
		n.checkLibraryMounts(t.Context(), at(step.table))
		if got := len(sd.restarted) > 0; got != step.restart {
			t.Errorf("step %d: restarted %v, want %v", i, sd.restarted, step.restart)
		}
	}
	if n.libWatch.seen["player/films"] != "80" {
		t.Errorf("seen %v", n.libWatch.seen)
	}
}

// playerPackage reads media libraries.
func playerPackage(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: player
title: Player
version: "1"
storage:
  config: { class: critical }
containers:
  app:
    image: example/player:1
    mounts:
      - { storage: config, path: /config }
      - { libraries: true, path: /media }
`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeUnits is systemd: every unit active or not, and what was restarted.
type fakeUnits struct {
	active    bool
	restarted []string
}

func (f *fakeUnits) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	var out []systemd.UnitStatus
	for _, u := range units {
		st := systemd.UnitStatus{Unit: u, ActiveState: "inactive"}
		if f.active {
			st.ActiveState = "active"
		}
		out = append(out, st)
	}
	return out, nil
}
func (f *fakeUnits) DaemonReload(context.Context) error               { return nil }
func (f *fakeUnits) Start(context.Context, ...string) error           { return nil }
func (f *fakeUnits) StartNoBlock(context.Context, ...string) error    { return nil }
func (f *fakeUnits) Stop(context.Context, ...string) error            { return nil }
func (f *fakeUnits) ReloadOrRestart(context.Context, ...string) error { return nil }
func (f *fakeUnits) Restart(_ context.Context, units ...string) error {
	f.restarted = append(f.restarted, units...)
	return nil
}
