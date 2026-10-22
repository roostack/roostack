package node

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/testwait"
)

// syncKV is settings in memory, safe for the copy's goroutine.
type syncKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *syncKV) Setting(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}
func (k *syncKV) SetSetting(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}

// fakeImportAgent mounts nothing: the test puts the files there itself.
type fakeImportAgent struct{ mounted, unmounted []string }

func (f *fakeImportAgent) MountImport(_ context.Context, service string, l agent.Library) (string, error) {
	f.mounted = append(f.mounted, service+" "+l.Drive+"/"+l.Sub)
	return filepath.Join(importRoot, service), nil
}
func (f *fakeImportAgent) UnmountImport(_ context.Context, service string) error {
	f.unmounted = append(f.unmounted, service)
	return nil
}

// fakePodman answers the podman commands the check and the load run, as
// the package's Postgres image would, and runs podman unshare's command.
func fakePodman(t *testing.T) Launcher {
	list, err := os.ReadFile("testdata/pg-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		all := strings.Join(args, " ")
		switch {
		case name == "podman" && len(args) > 0 && args[0] == "unshare":
			return exec.CommandContext(ctx, args[1], args[2:]...)
		case strings.Contains(all, "--list"):
			cmd := exec.CommandContext(ctx, "sh", "-c", "cat >/dev/null; cat \"$1\"", "sh", "testdata/pg-list.txt")
			_ = list
			return cmd
		case strings.Contains(all, "--version"):
			return exec.CommandContext(ctx, "echo", "postgres (PostgreSQL) 14.19 (Debian 14.19-1.pgdg120+1)")
		case strings.Contains(all, "extension"):
			return exec.CommandContext(ctx, "echo", "cube.control earthdistance.control vchord.control vector.control")
		case strings.Contains(all, "image inspect"):
			return exec.CommandContext(ctx, "echo", `["PATH=/bin","NODE_ENV=production"]`)
		case strings.Contains(all, "-f -"):
			return exec.CommandContext(ctx, "cat")
		}
		return exec.CommandContext(ctx, "cat") // pg_restore loading: reads the dump
	}
}

func immichPackage(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// waitImport waits until a service's check or copy has finished.
func waitImport(t *testing.T, n *Local, service string) ImportState {
	t.Helper()
	testwait.For(t, 30*time.Second, func() bool { return !n.importBusy(service) })
	return n.loadImport(t.Context(), service)
}

// Bringing Immich's data in: the files mounted, the old setup read and
// its folder guessed, the check, the copy into staging, then the switch's
// steps put it in place, set the old data aside and load the dump; undo
// puts the old data back (SPEC.md §8.4).
func TestImportImmich(t *testing.T) {
	root := t.TempDir()
	importRoot, libraryRoot = filepath.Join(root, "import"), filepath.Join(root, "library")
	t.Cleanup(func() { importRoot, libraryRoot = config.ImportRoot, config.LibraryRoot })
	lab := config.Lab{Paths: config.Paths{ServicesDir: filepath.Join(root, "srv"), MediaDir: filepath.Join(root, "media")}}
	sd := &fakeUnits{active: true}
	ag := &fakeImportAgent{}
	n := &Local{Lab: lab, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Activity: activity.New(), Units: sd,
		Reconciler: &reconcile.Reconciler{Systemd: sd}, State: &syncKV{m: map[string]string{}}, ImportAgent: ag, Launch: fakePodman(t)}
	p := immichPackage(t)
	in := &instance.Instance{Package: "immich", Version: p.Version, Updates: "notify", AwaitingData: true,
		Routes:   map[string]instance.RouteOverride{"web": {Port: 20001}},
		Settings: map[string]any{"machine_learning": true, "hardware_acceleration": false}}
	n.assignment = Assignment{Services: []state.Service{{Package: p, Instance: in}}}
	ctx := t.Context()

	// The person's copy, as the agent would mount it.
	src := filepath.Join(importRoot, "immich")
	for f, data := range map[string]string{
		"immich/library/upload/u1/a.jpg":        "photo",
		"immich/library/thumbs/u1/a.webp":       "thumb",
		"immich/library/encoded-video/u1/a.mp4": "video",
		"immich/main.dump":                      "PGDMP… /usr/src/app/upload/u1/a.jpg …",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755)
		os.WriteFile(filepath.Join(src, f), []byte(data), 0o644)
	}
	// What the service has now: its empty database, which runs while it
	// waits; and its media folder, only empty folders.
	os.MkdirAll(filepath.Join(lab.Paths.ServicesDir, "immich", "db", "main"), 0o750)
	os.WriteFile(filepath.Join(lab.Paths.ServicesDir, "immich", "db", "main", "PG_VERSION"), []byte("14\n"), 0o600)
	os.MkdirAll(filepath.Join(lab.Paths.MediaDir, "immich", "library"), 0o750)

	if err := n.SetImportSource(ctx, "immich", ImportSource{Kind: "drive", Title: "T7 Shield › immich",
		Library: agent.Library{Drive: "1111-2222", Sub: "immich"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile("../importer/testdata/immich-inspect.json")
	setup, err := importer.ParseInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SetImportSetup(ctx, "immich", &setup); err != nil {
		t.Fatal(err)
	}
	v, err := n.Import(ctx, "immich")
	if err != nil || !v.Mounted || !v.Guessed || len(v.Prefixes) != 1 || v.Prefixes[0] != (importer.Prefix{Old: "/opt", Now: "."}) &&
		v.Prefixes[0] != (importer.Prefix{Old: "/opt/immich", Now: "immich"}) {
		t.Fatalf("import %+v %v", v.ImportState, err)
	}
	if err := n.CopyImport(ctx, "immich"); err == nil {
		t.Error("copied before checking")
	}

	if err := n.CheckImport(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	st := waitImport(t, n, "immich")
	if st.CheckErr != "" || !st.Check.Ready() || st.Check.Files != 3 {
		t.Fatalf("check %+v %v", st.Check, st.CheckErr)
	}
	var texts []string
	for _, it := range st.Check.Items {
		texts = append(texts, it.Level+": "+it.Text+" "+it.More)
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"ok: Database \"main\": a dump from Postgres 14.19", "ok: Immich has no data yet",
		"warn: 2 settings the old immich_server had that Roostack doesn't set:", "info: Smart search models: not brought."} {
		if !strings.Contains(all, want) {
			t.Errorf("the check lacks %q:\n%s", want, all)
		}
	}

	if err := n.CopyImport(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	st = waitImport(t, n, "immich")
	if st.Copy == nil || st.Copy.Err != "" || st.Copy.Passes != 1 || st.Copy.Progress.Copied == 0 {
		t.Fatalf("copy %+v", st.Copy)
	}
	for _, f := range []string{"media/immich.incoming/library/upload/u1/a.jpg", "media/immich.incoming/thumbs/u1/a.webp",
		"srv/immich.incoming/db-dump/main.sql"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("not copied: %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "media/immich.incoming/library/thumbs")); err == nil {
		t.Error("thumbs copied into the library too")
	}

	step := func(name string) ImportState {
		t.Helper()
		if err := n.SwitchImport(ctx, "immich", name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		st := waitImport(t, n, "immich")
		if st.Step == nil || st.Step.Name != name || st.Step.Running || st.Step.Err != "" {
			t.Fatalf("%s: %+v", name, st.Step)
		}
		return st
	}
	for _, name := range []string{SwitchStop, SwitchCopy, SwitchCommit, SwitchLoad} {
		st := step(name)
		if name == SwitchCommit && (len(st.Kept) != 1 || !strings.Contains(st.Kept[0], "srv/immich.before-import-")) {
			t.Errorf("kept %v", st.Kept)
		}
	}
	if _, err := os.Stat(filepath.Join(lab.Paths.MediaDir, "immich/library/upload/u1/a.jpg")); err != nil {
		t.Error("the copy isn't in place")
	}
	if _, err := os.Stat(filepath.Join(lab.Paths.ServicesDir, "immich/db/main")); err != nil {
		t.Error("the database's folder wasn't made again")
	}
	folders, _ := n.KeptData(ctx)
	if len(folders) != 1 || !folders[0].Imported {
		t.Errorf("kept folders %+v", folders)
	}
	step(SwitchUndo)
	if _, err := os.Stat(filepath.Join(lab.Paths.ServicesDir, "immich/db/main")); err != nil {
		t.Error("the data from before isn't back")
	}
	if _, err := os.Stat(filepath.Join(lab.Paths.MediaDir, "immich")); err == nil {
		t.Error("what was brought is still in place")
	}
	if folders, _ := n.KeptData(ctx); !slices.ContainsFunc(folders, func(k KeptFolder) bool { return k.FailedImport }) {
		t.Errorf("what was brought isn't kept: %+v", folders)
	}

	if err := n.CancelImport(ctx, "immich"); err != nil || len(ag.unmounted) != 1 {
		t.Errorf("cancel %v %v", err, ag.unmounted)
	}
	if st := n.loadImport(ctx, "immich"); st.Source != nil {
		t.Errorf("forgotten? %+v", st)
	}
}
