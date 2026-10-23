package node

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/state"
)

// fakePhotoAgent mounts nothing: the test puts the export there itself.
type fakePhotoAgent struct{ mounted, unmounted []string }

func (f *fakePhotoAgent) MountPhotos(_ context.Context, service, who string, l agent.Library) (string, error) {
	f.mounted = append(f.mounted, service+" "+who+" "+l.Drive+"/"+l.Sub)
	return filepath.Join(photoImportRoot, agent.PhotoSpotName(service, who)), nil
}
func (f *fakePhotoAgent) UnmountPhotos(_ context.Context, service, who string) error {
	f.unmounted = append(f.unmounted, service+" "+who)
	return nil
}

// fakePhotoUnits runs immich-go as a test would want it: its log written
// at once, then the unit ends (or vanishes, as after a restart: vanish
// is how many runs do).
type fakePhotoUnits struct {
	mu      sync.Mutex
	args    []string
	log     string // written to the run's log file when it starts
	vanish  int
	code    int // its exit status
	stopped bool
	polls   int
}

func (f *fakePhotoUnits) RunDetached(_ context.Context, unit, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = append([]string{unit, name}, args...)
	for i, a := range args {
		if a == "--volume" && strings.HasSuffix(args[i+1], ":/work") {
			os.WriteFile(filepath.Join(strings.TrimSuffix(args[i+1], ":/work"), "immich-go.log"), []byte(f.log), 0o600)
		}
	}
	return nil
}
func (f *fakePhotoUnits) Exited(context.Context, string) (bool, int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if f.vanish > 0 {
		f.vanish--
		return false, 0, false, nil
	}
	return f.polls < 2, f.code, true, nil
}
func (f *fakePhotoUnits) Stop(context.Context, ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}
func (f *fakePhotoUnits) Forget(context.Context, string) error { return nil }

// immichAPI answers AccountScript as Immich would, by key.
func immichAPI(_ context.Context, container string, env map[string]string, cmd []string) ([]byte, []byte, error) {
	if container != "roostack-immich-server" || env["PORT"] != "2283" || len(cmd) < 3 || cmd[2] != photoimport.AccountScript {
		return nil, []byte("unexpected exec"), exec.ErrNotFound
	}
	perms := `["all"]`
	switch env["API_KEY"] {
	case "narrow-key":
		perms = `["asset.read","asset.upload"]`
	case "wrong-key":
		return []byte(`{"/api/api-keys/me":{"status":401,"json":{"message":"Invalid API key"}}}`), nil, nil
	}
	return []byte(`{"/api/api-keys/me":{"status":200,"json":{"id":"k1","name":"Roostack migration","permissions":` + perms + `}},
		"/api/users/me":{"status":200,"json":{"id":"u1","name":"Anna Smith","email":"anna@example.com"}},
		"/api/assets/statistics":{"status":200,"json":{"images":10,"videos":2,"total":12}}}`), nil, nil
}

// photoLauncher answers podman as the server would: the tool's image is
// there, and podman unshare runs its command.
func photoLauncher(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	if name == "podman" && len(args) > 0 && args[0] == "unshare" {
		return exec.CommandContext(ctx, args[1], args[2:]...)
	}
	return exec.CommandContext(ctx, "true")
}

// writeTakeout writes a small Takeout part.
func writeTakeout(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for n, data := range files {
		fw, _ := w.Create(n)
		fw.Write([]byte(data))
	}
	w.Close()
	f.Close()
}

func waitPhotos(t *testing.T, n *Local, done func(*PhotoState) bool) *PhotoState {
	t.Helper()
	for range 500 {
		if st := n.photos().loadPhotos(t.Context(), "immich").People["anna"]; st != nil && done(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited too long: %+v", n.photos().loadPhotos(t.Context(), "immich").People["anna"])
	return nil
}

// Migrating a Takeout into Immich (SPEC.md §8.5): the export mounted and
// looked in, the key checked with Immich and kept as a secret, the check,
// then immich-go run in its own container on Immich's network, its log
// counted, and the key forgotten and the export unmounted at the end.
func TestPhotoMigration(t *testing.T) {
	root := t.TempDir()
	photoImportRoot, photoLibRoot, photoPoll = filepath.Join(root, "import"), filepath.Join(root, "library"), 10*time.Millisecond
	t.Cleanup(func() {
		photoImportRoot, photoLibRoot, photoPoll = config.ImportRoot, config.LibraryRoot, 5*time.Second
	})
	lab := config.Lab{Paths: config.Paths{StateDir: filepath.Join(root, "state"), ServicesDir: filepath.Join(root, "srv"),
		MediaDir: filepath.Join(root, "media")}}
	ag, units, secrets := &fakePhotoAgent{}, &fakePhotoUnits{}, &memSecrets{m: map[string][]byte{}}
	var ended []PhotoSummary
	n := &Local{Lab: lab, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Activity: activity.New(),
		State: &syncKV{m: map[string]string{}}, Secrets: secrets, Exec: immichAPI, Launch: photoLauncher,
		PhotoAgent: ag, PhotoUnits: units,
		OnPhotos: func(_ context.Context, s PhotoSummary) { ended = append(ended, s) }}
	p := immichPackage(t)
	in := &instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
		Routes:   map[string]instance.RouteOverride{"web": {Port: 20001}},
		Settings: map[string]any{"machine_learning": true, "hardware_acceleration": false}}
	n.assignment = Assignment{Services: []state.Service{{Package: p, Instance: in}}}
	os.MkdirAll(filepath.Join(lab.Paths.MediaDir, "immich", "library"), 0o750)
	ctx := t.Context()
	x := n.Photos()
	if x == nil {
		t.Fatal("photos can't be migrated here")
	}

	// Another service isn't one photos go into.
	if err := x.SetPhotoKind(ctx, "vaultwarden", "anna", photoimport.KindGoogle); err != ErrNotFound {
		t.Errorf("another service: %v", err)
	}
	if err := x.SetPhotoKind(ctx, "immich", "../x", photoimport.KindGoogle); err == nil {
		t.Error("a name that isn't an account's was taken")
	}

	// 1-3: a Takeout on a drive, looked in.
	x.SetPhotoKind(ctx, "immich", "anna", photoimport.KindGoogle)
	x.SetPhotoGuided(ctx, "immich", "anna")
	gp := "Takeout/Google Photos/"
	export := filepath.Join(photoImportRoot, "immich.photos.anna")
	writeTakeout(t, export, "takeout-20261019T091234Z-001.zip", map[string]string{
		gp + "Photos from 2014/IMG_1.JPG":      "photo one",
		gp + "Photos from 2014/IMG_1.JPG.json": `{"title":"IMG_1.JPG","photoTakenTime":{"timestamp":"1402477950"}}`,
	})
	writeTakeout(t, export, "takeout-20261019T091234Z-003.zip", map[string]string{gp + "Photos from 2015/IMG_2.JPG": "photo two"})
	src := PhotoSource{Kind: "drive", Library: agent.Library{Drive: "1111", Sub: "Takeout"}, Title: "T7 › Takeout"}
	if err := x.SetPhotoSource(ctx, "immich", "anna", src); err != nil {
		t.Fatal(err)
	}
	st := waitPhotos(t, n, func(s *PhotoState) bool { return !s.Scanning })
	if st.Scan == nil || st.Scan.Photos != 2 || len(st.Scan.Parts) != 2 || len(ag.mounted) != 1 {
		t.Fatalf("scan %+v, mounted %v", st.Scan, ag.mounted)
	}
	if p := st.Problems(); len(p) != 1 || !strings.HasPrefix(p[0], "Part 2 isn't here") {
		t.Errorf("problems %q", p)
	}

	// 4: keys: one Immich refuses, one missing permissions, then a good one.
	if err := x.SetPhotoKey(ctx, "immich", "anna", "wrong-key"); err == nil || !strings.Contains(err.Error(), "didn't accept that key") {
		t.Errorf("a wrong key: %v", err)
	}
	if err := x.SetPhotoKey(ctx, "immich", "anna", "narrow-key"); err != nil {
		t.Fatal(err)
	}
	st = n.photos().loadPhotos(ctx, "immich").People["anna"]
	if st.Account == nil || len(st.Account.Missing) != len(photoimport.Permissions)-2 || st.Account.Name != "Anna Smith" {
		t.Errorf("account %+v", st.Account)
	}
	if err := x.SetPhotoKey(ctx, "immich", "anna", "good-key"); err != nil {
		t.Fatal(err)
	}
	if string(secrets.m["roostack-photos-immich-anna"]) != "good-key" {
		t.Error("the key isn't kept as a secret")
	}

	// 5-6: choices and the check; the missing part stops the start.
	x.SetPhotoChoices(ctx, "immich", "anna", photoimport.DefaultChoices())
	x.CheckPhotos(ctx, "immich", "anna")
	st = waitPhotos(t, n, func(s *PhotoState) bool { return !s.Checking })
	if st.Check == nil || st.Check.Account.Photos != 10 || st.Check.Needs == 0 {
		t.Fatalf("check %+v (%s)", st.Check, st.CheckErr)
	}
	if err := x.StartPhotos(ctx, "immich", "anna"); err == nil || !strings.Contains(err.Error(), "Part 2") {
		t.Errorf("started with a part missing: %v", err)
	}
	writeTakeout(t, export, "takeout-20261019T091234Z-002.zip", map[string]string{gp + "Photos from 2015/IMG_3.JPG": "photo three"})
	x.CheckPhotos(ctx, "immich", "anna")
	waitPhotos(t, n, func(s *PhotoState) bool { return !s.Checking })

	// 7: the run.
	units.log = strings.Join([]string{
		`{"time":"2026-10-21T10:00:00Z","level":"INFO","msg":"","--upload-api-key":"********-key"}`,
		`{"time":"2026-10-21T10:00:01Z","level":"INFO","msg":"discovered image","file":"takeout-001.zip:IMG_1.JPG"}`,
		`{"time":"2026-10-21T10:00:01Z","level":"INFO","msg":"discovered image","file":"takeout-002.zip:IMG_3.JPG"}`,
		`{"time":"2026-10-21T10:00:01Z","level":"INFO","msg":"discovered image","file":"takeout-003.zip:IMG_2.JPG"}`,
		`{"time":"2026-10-21T10:00:02Z","level":"INFO","msg":"uploaded successfully","file":"takeout-001.zip:IMG_1.JPG","size":9}`,
		`{"time":"2026-10-21T10:00:02Z","level":"INFO","msg":"uploaded successfully","file":"takeout-002.zip:IMG_3.JPG","size":11}`,
		`{"time":"2026-10-21T10:00:02Z","level":"INFO","msg":"server has duplicate","file":"takeout-003.zip:IMG_2.JPG"}`,
	}, "\n") + "\n"
	if err := x.StartPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	st = waitPhotos(t, n, func(s *PhotoState) bool { return s.Run != nil && !s.Run.Running })
	if r := st.Run; r.Err != "" || r.Tally.Sent != 2 || r.Tally.Had != 1 || r.Tally.Found != 3 || r.After.Photos != 10 || r.Passes != 1 {
		t.Errorf("run %+v", r)
	}
	// Following it again (as after Roostack restarts) counts its log once.
	n.photos().readPhotoLog(ctx, "immich", "anna")
	if r := n.photos().loadPhotos(ctx, "immich").People["anna"].Run; r.Tally.Sent != 2 || r.Tally.Found != 3 {
		t.Errorf("the log was counted twice: %+v", r.Tally)
	}
	args := strings.Join(units.args, " ")
	for _, want := range []string{"roostack-photos-immich podman run --rm", "--network roostack-immich", "--oom-score-adj 1000",
		export + ":/export:ro,rslave", "--secret roostack-photos-immich-anna,type=env,target=IMMICH_GO_UPLOAD_API_KEY",
		photoimport.ToolImage + " upload from-google-photos", "--server=http://roostack-immich-server:2283",
		"/export/takeout-20261019T091234Z-001.zip /export/takeout-20261019T091234Z-002.zip /export/takeout-20261019T091234Z-003.zip"} {
		if !strings.Contains(args, want) {
			t.Errorf("podman lacks %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "good-key") {
		t.Error("the key is on a command line")
	}
	if _, kept := secrets.m["roostack-photos-immich-anna"]; kept || st.HasKey {
		t.Error("the key is kept after the migration")
	}
	if len(ag.unmounted) != 1 || len(ended) != 1 || ended[0].Who != "anna" {
		t.Errorf("unmounted %v, ended %v", ag.unmounted, ended)
	}
	v, _ := x.PhotoMigration(ctx, "immich", "anna")
	if strings.Contains(v.Log, "key") || !strings.Contains(v.Log, "uploaded successfully file=takeout-001.zip:IMG_1.JPG") {
		t.Errorf("the log shown:\n%s", v.Log)
	}

	// A run whose unit vanished (the server restarted) while its export
	// isn't there (a drive not back yet) is interrupted, and carries on
	// from the page.
	x.SetPhotoKey(ctx, "immich", "anna", "good-key") // a new key: check again
	x.CheckPhotos(ctx, "immich", "anna")
	waitPhotos(t, n, func(s *PhotoState) bool { return !s.Checking })
	units.vanish, units.polls = 1, 0
	os.Rename(export, export+".away")
	if err := x.StartPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	st = waitPhotos(t, n, func(s *PhotoState) bool { return s.Run != nil && !s.Run.Running })
	if !st.Run.Interrupted || st.Run.Passes != 1 || st.Run.Auto {
		t.Errorf("interrupted run %+v", st.Run)
	}
	os.Rename(export+".away", export)

	// Carrying on is another pass of the same migration: what the first
	// sent is added up, and isn't counted as what Immich had before.
	n.photos().updatePhotos(ctx, "immich", "anna", func(st *PhotoState) error {
		st.Run.Tally.Sent = 2
		return nil
	})
	units.polls = 0
	units.log = strings.Join([]string{
		`{"msg":"discovered image","file":"a"}`, `{"msg":"discovered image","file":"b"}`, `{"msg":"discovered image","file":"c"}`,
		`{"msg":"server has duplicate","file":"a"}`, `{"msg":"server has duplicate","file":"b"}`, `{"msg":"uploaded successfully","file":"c"}`,
	}, "\n") + "\n"
	if err := x.StartPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	st = waitPhotos(t, n, func(s *PhotoState) bool { return s.Run != nil && !s.Run.Running })
	if r := st.Run; r.Passes != 2 || r.Earlier != 2 || r.Migrated() != 3 || r.HadBefore() != 0 || r.FirstStarted.IsZero() {
		t.Errorf("carried on: passes %d, earlier %d, migrated %d, had before %d", r.Passes, r.Earlier, r.Migrated(), r.HadBefore())
	}

	// With its export there, a run the restart stopped carries on by
	// itself, as another pass.
	x.SetPhotoKey(ctx, "immich", "anna", "good-key")
	x.CheckPhotos(ctx, "immich", "anna")
	waitPhotos(t, n, func(s *PhotoState) bool { return !s.Checking })
	units.vanish, units.polls = 1, 0
	if err := x.StartPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	st = waitPhotos(t, n, func(s *PhotoState) bool { return s.Run != nil && !s.Run.Running && s.Run.Passes == 2 })
	if r := st.Run; !r.Auto || r.Interrupted || r.Err != "" || r.Migrated() != 2 {
		t.Errorf("carried on by itself: %+v", r)
	}

	// Killed for memory (Podman's 137), it says so, with its limit: half
	// the server's memory here, at least 1 GB (GOMEMLIMIT three quarters
	// of it), and immich-go first to go should the server run short.
	x.SetPhotoKey(ctx, "immich", "anna", "good-key")
	x.CheckPhotos(ctx, "immich", "anna")
	waitPhotos(t, n, func(s *PhotoState) bool { return !s.Checking })
	units.code, units.polls = 137, 0
	if err := x.StartPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	st = waitPhotos(t, n, func(s *PhotoState) bool { return s.Run != nil && !s.Run.Running })
	limit, total := n.photos().PhotoMemory(ctx, "immich")
	if r := st.Run; !r.OutOfMemory || !strings.Contains(r.Err, "ran out of memory") || r.Memory != limit || limit != max(1<<30, total/2) {
		t.Errorf("out of memory: %+v (limit %d of %d)", r, limit, total)
	}
	args = strings.Join(units.args, " ")
	if !strings.Contains(args, fmt.Sprintf("--memory %d --oom-score-adj 1000 --env GOMEMLIMIT=%dMiB", limit, limit*3/4>>20)) {
		t.Errorf("podman's memory flags:\n%s", args)
	}
	if err := x.SetPhotoMemory(ctx, "immich", "most"); err != nil {
		t.Fatal(err)
	}
	if l, total := n.photos().PhotoMemory(ctx, "immich"); l != max(1<<30, total*3/4) {
		t.Errorf("three quarters: %d of %d", l, total)
	}
	if err := x.SetPhotoMemory(ctx, "immich", "all"); err == nil {
		t.Error("a share that isn't a choice was taken")
	}

	// Forgetting it unmounts and forgets the key.
	if err := x.ForgetPhotos(ctx, "immich", "anna"); err != nil {
		t.Fatal(err)
	}
	if n.photos().loadPhotos(ctx, "immich").People["anna"] != nil {
		t.Error("forgotten migration still there")
	}
}

// Someone else's migration running is shown, and stops another starting.
func TestPhotoOneAtATime(t *testing.T) {
	n := &Local{State: memKV{}, Exec: immichAPI, Launch: photoLauncher, Secrets: &memSecrets{m: map[string][]byte{}},
		PhotoAgent: &fakePhotoAgent{}, PhotoUnits: &fakePhotoUnits{}}
	p := immichPackage(t)
	n.assignment = Assignment{Services: []state.Service{{Package: p, Instance: &instance.Instance{Package: "immich", Version: p.Version}}}}
	ctx := t.Context()
	n.photos().updatePhotos(ctx, "immich", "ben", func(st *PhotoState) error {
		st.Run = &PhotoRun{Running: true, Started: time.Now()}
		return nil
	})
	n.pho.resumed = true // nothing to follow
	v, err := n.photos().PhotoMigration(ctx, "immich", "anna")
	if err != nil || v.Busy == nil || v.Busy.Who != "ben" {
		t.Errorf("busy %+v %v", v.Busy, err)
	}
	if err := n.photos().StartPhotos(ctx, "immich", "anna"); err == nil || !strings.Contains(err.Error(), "ben is migrating photos now") {
		t.Errorf("a second migration: %v", err)
	}
}
