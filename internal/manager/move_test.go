package manager

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

type file struct {
	Data string
	Mod  time.Time
}

// movingServer is a server whose data lives in memory; "tar" is JSON.
type movingServer struct {
	*fakeRemote
	id      nodeid.Identity
	mu      sync.Mutex
	data    [dataDirs]map[string]file
	staged  [dataDirs]map[string]file
	secrets map[string][]byte
	log     []string
	failOn  string
	// beforeStop runs as the service stops: it changed data while it ran.
	beforeStop func()
	backups    node.Backups
	status     node.ApplyStatus
}

func (m *movingServer) Backups() node.Backups { return m.backups }
func (m *movingServer) Status() node.ApplyStatus {
	if m.status.Finished {
		return m.status
	}
	return node.ApplyStatus{Finished: true}
}

func newMoving(id, name string) *movingServer {
	m := &movingServer{fakeRemote: &fakeRemote{settings: &fakeServerSettings{}}, id: nodeid.Identity{ID: id, Name: name},
		secrets: map[string][]byte{}}
	for i := range dataDirs {
		m.data[i], m.staged[i] = map[string]file{}, map[string]file{}
	}
	return m
}

func (m *movingServer) Identity() nodeid.Identity { return m.id }
func (m *movingServer) Address() string           { return "100.96.0.9" }
func (m *movingServer) did(op string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, op)
	if op == m.failOn {
		return errors.New(op + " failed")
	}
	return nil
}
func (m *movingServer) DataDirs(string) []string { return nil }

// Mover is the server itself (fakeRemote's is nil).
func (m *movingServer) Mover() node.Mover { return m }

func (m *movingServer) ExportData(_ context.Context, _ string, dir int, since time.Time, w io.Writer) error {
	m.mu.Lock()
	out := map[string]file{}
	for p, f := range m.data[dir] {
		if since.IsZero() || f.Mod.After(since) {
			out[p] = f
		}
	}
	m.mu.Unlock()
	return json.NewEncoder(w).Encode(out)
}
func (m *movingServer) ImportData(_ context.Context, _ string, dir int, r io.Reader, strip int) error {
	var in map[string]file
	if strip > 0 { // a real tar stream, from a backup
		in = map[string]file{}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			parts := strings.Split(strings.Trim(h.Name, "/"), "/")
			if h.Typeflag != tar.TypeReg || len(parts) <= strip {
				continue
			}
			data, _ := io.ReadAll(tr)
			in[strings.Join(parts[strip:], "/")] = file{string(data), h.ModTime}
		}
	} else if err := json.NewDecoder(r).Decode(&in); err != nil {
		return err
	}
	io.Copy(io.Discard, r)
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, f := range in {
		m.staged[dir][p] = f
	}
	return nil
}
func (m *movingServer) ListData(_ context.Context, _ string, dir int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.data[dir] {
		out = append(out, p)
	}
	return out, nil
}
func (m *movingServer) PruneData(_ context.Context, _ string, dir int, keep []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p := range m.staged[dir] {
		if !slices.Contains(keep, p) {
			delete(m.staged[dir], p)
		}
	}
	return nil
}
func (m *movingServer) CommitData(context.Context, string) ([]string, error) {
	if err := m.did("commit"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range dataDirs {
		m.data[i], m.staged[i] = m.staged[i], map[string]file{}
	}
	return nil, nil
}
func (m *movingServer) SetAsideData(context.Context, string, string) ([]string, error) {
	return []string{"/srv/roostack/services/forgejo.moved"}, m.did("set aside")
}
func (m *movingServer) StopService(context.Context, string) error {
	if m.beforeStop != nil {
		m.beforeStop()
	}
	return m.did("stop")
}
func (m *movingServer) StartService(context.Context, string) error { return m.did("start") }
func (m *movingServer) ExportSecrets(context.Context, string) (map[string][]byte, error) {
	return m.secrets, m.did("export secrets")
}
func (m *movingServer) ImportSecrets(_ context.Context, s map[string][]byte) error {
	m.mu.Lock()
	for k, v := range s {
		m.secrets[k] = v
	}
	m.mu.Unlock()
	return m.did("import secrets")
}
func (m *movingServer) DataSize(context.Context, string) (int64, error) { return 1234, nil }
func (m *movingServer) LoadDumps(context.Context, string) error         { return m.did("load dumps") }
func (m *movingServer) KeptData(context.Context) ([]node.KeptFolder, error) {
	return []node.KeptFolder{{Path: "/srv/roostack/services/forgejo.moved-to-vps-20260928-002806", Service: "forgejo", MovedAway: true}}, nil
}
func (m *movingServer) RemoveKeptData(_ context.Context, path string) error {
	return m.did("remove " + path)
}
func (m *movingServer) PutBackKeptData(_ context.Context, path string) error {
	return m.did("put back " + path)
}

func setUpMove(t *testing.T) (*Manager, *movingServer, *movingServer) {
	t.Helper()
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	a, b := newMoving("n-0000000002", "home"), newMoving("n-0000000003", "vps")
	m.Others = func() []RemoteServer { return []RemoteServer{a, b} }
	m.Registry = &fakeRegistry{}
	m.Activity = activity.New()
	req := forgejoRequest()
	req.Host = "n-0000000002"
	if err := m.Install(context.Background(), "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	return m, a, b
}

func waitMove(t *testing.T, m *Manager) activity.Snapshot {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if l, ok := m.Activity.Latest(activity.Move); ok && !l.Running() {
			return l
		}
	}
	t.Fatal("move didn't finish")
	return activity.Snapshot{}
}

func TestMoveService(t *testing.T) {
	m, a, b := setUpMove(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	a.data[0]["data/app.ini"] = file{"config", old}
	a.data[0]["db/base"] = file{"v1", old}
	a.data[1]["photos/1.jpg"] = file{"jpeg", old}
	a.secrets["roostack-forgejo-db-main-password"] = []byte("generated")
	// Only the service's own secrets are brought (review M20).
	a.secrets["roostack-caddy-dns-api_token"] = []byte("planted")
	var fe *FormError
	if err := m.MoveService(ctx, "forgejo", "n-0000000002", MoveCopy); !errors.As(err, &fe) {
		t.Errorf("moving to where it runs: %v", err)
	}
	if info, err := m.MoveInfo(ctx, "forgejo"); err != nil || info.From.Name != "home" || info.Size != 1234 {
		t.Errorf("info = %+v, %v", info, err)
	}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003", MoveCopy); err != nil {
		t.Fatal(err)
	}
	l := waitMove(t, m)
	if l.Err != "" {
		t.Fatalf("move: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if b.data[0]["data/app.ini"].Data != "config" || b.data[1]["photos/1.jpg"].Data != "jpeg" ||
		string(b.secrets["roostack-forgejo-db-main-password"]) != "generated" || b.secrets["roostack-caddy-dns-api_token"] != nil {
		t.Errorf("vps has %v, secrets %v", b.data, b.secrets)
	}
	if !slices.Equal(a.log, []string{"stop", "export secrets", "set aside"}) || !slices.Contains(b.log, "commit") {
		t.Errorf("home did %v, vps %v", a.log, b.log)
	}
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	if in.Host != "n-0000000003" || in.Routes["web"].Port == 0 {
		t.Errorf("instance host %q routes %+v", in.Host, in.Routes)
	}
	if m.NodeFor("forgejo").Identity().Name != "vps" {
		t.Error("forgejo should run on the vps now")
	}
}

func TestMoveCopiesChangesAndDeletions(t *testing.T) {
	m, a, b := setUpMove(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	a.data[0]["keep"] = file{"old", old}
	a.data[0]["gone"] = file{"soon deleted", old}
	// The service changes data between the first copy and stopping.
	a.beforeStop = func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.data[0]["keep"] = file{"new", time.Now()}
		delete(a.data[0], "gone")
	}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003", MoveCopy); err != nil {
		t.Fatal(err)
	}
	if l := waitMove(t, m); l.Err != "" {
		t.Fatal(l.Err)
	}
	if b.data[0]["keep"].Data != "new" {
		t.Errorf("changed file: %q", b.data[0]["keep"].Data)
	}
	if _, ok := b.data[0]["gone"]; ok {
		t.Error("a file deleted before stopping must not come back")
	}
}

func TestMoveFailureRestartsService(t *testing.T) {
	m, a, b := setUpMove(t)
	b.failOn = "commit"
	if err := m.MoveService(context.Background(), "forgejo", "n-0000000003", MoveCopy); err != nil {
		t.Fatal(err)
	}
	if l := waitMove(t, m); !strings.Contains(l.Err, "commit failed") {
		t.Errorf("err = %q", l.Err)
	}
	if !slices.Contains(a.log, "start") {
		t.Errorf("home should start forgejo again: %v", a.log)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Host != "n-0000000002" {
		t.Errorf("host changed to %q after a failed move", in.Host)
	}
}

// realBackups backs up a folder with real restic when asked.
type realBackups struct {
	repo *backup.Repo
	dir  string
	mu   sync.Mutex
	last *store.BackupRun
}

func (b *realBackups) RunNow(context.Context) error {
	started := time.Now()
	sum, err := b.repo.Backup(context.Background(), backup.BackupOptions{Paths: []string{b.dir},
		Tags: []string{backup.TagRoostack, backup.ServiceTag(filepath.Base(b.dir))}})
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = &store.BackupRun{Started: started, Finished: time.Now(), OK: err == nil}
	if err == nil {
		b.last.SnapshotID = sum.SnapshotID
	}
	return err
}

func (b *realBackups) Status(context.Context) (backup.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backup.Status{Configured: true, Last: b.last, Progress: -1}, nil
}

// setUpBackupMove is setUpMove with home backing up to a real restic
// repository, and vps applying at once.
func setUpBackupMove(t *testing.T) (*Manager, *movingServer, *movingServer) {
	t.Helper()
	bin := os.Getenv("ROOSTACK_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
		}
	}
	m, a, b := setUpMove(t)
	ctx := context.Background()
	applyWait = 5 * time.Second
	t.Cleanup(func() { applyWait = 20 * time.Minute })
	svcDir := filepath.Join(m.Lab.Paths.ServicesDir, "forgejo")
	os.MkdirAll(filepath.Join(svcDir, "data"), 0o755)
	os.WriteFile(filepath.Join(svcDir, "data", "app.ini"), []byte("from the backup"), 0o644)
	os.MkdirAll(filepath.Join(svcDir, "db-dump"), 0o755)
	os.WriteFile(filepath.Join(svcDir, "db-dump", "main.sql"), []byte("dump"), 0o644)

	dir := t.TempDir()
	recovery := filepath.Join(dir, "recovery")
	os.WriteFile(recovery, []byte("the recovery key\n"), 0o600)
	repo := &backup.Repo{Bin: bin, URL: filepath.Join(dir, "repo"), PasswordFile: recovery, CacheDir: filepath.Join(dir, "cache")}
	if err := repo.Init(ctx); err != nil {
		t.Fatal(err)
	}
	a.backups = &realBackups{repo: repo, dir: svcDir}
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.BackupStore, m.Settings = st, st
	m.OpenRepo = func(string, []string) *backup.Repo { return repo }
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "local:" + repo.URL,
		Repository: repo.URL, Created: time.Now()})
	// The new server "applies" at once.
	b.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour)}
	return m, a, b
}

func TestMoveThroughBackup(t *testing.T) {
	m, a, b := setUpBackupMove(t)
	ctx := context.Background()

	if info, _ := m.MoveInfo(ctx, "forgejo"); !info.ViaBackup {
		t.Error("through a backup should be possible")
	}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003", MoveBackup); err != nil {
		t.Fatal(err)
	}
	l := waitMove(t, m)
	if l.Err != "" {
		t.Fatalf("move: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if b.data[0]["data/app.ini"].Data != "from the backup" || b.data[0]["db-dump/main.sql"].Data != "dump" {
		t.Errorf("vps has %v", b.data[0])
	}
	if !slices.Contains(b.log, "load dumps") || !slices.Contains(a.log, "stop") {
		t.Errorf("home did %v, vps %v", a.log, b.log)
	}
}

// A move through a backup whose databases don't load on the new server
// goes back to the old one, whose copy is still whole, and what was copied
// is set aside; if going back fails too, it's held stopped on the new
// server (SPEC.md §6.9, §10.10).
func TestMoveThroughBackupGoesBack(t *testing.T) {
	m, a, b := setUpBackupMove(t)
	ctx := context.Background()
	b.failOn = "load dumps"
	a.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour)}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003", MoveBackup); err != nil {
		t.Fatal(err)
	}
	l := waitMove(t, m)
	if !strings.Contains(l.Err, "Forgejo couldn't be moved to vps: its database didn't load there. It's running on home again, as before. "+
		"What was copied to vps will stay there, at /srv/roostack/services/forgejo.moved, until you delete it.") {
		t.Fatalf("move: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	lines := strings.Join(l.Lines, "\n")
	if !strings.Contains(lines, "Its database didn't load on vps: load dumps failed") || !strings.Contains(lines, "Moving Forgejo back to home") {
		t.Errorf("log:\n%s", lines)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Host != "n-0000000002" || in.Held != nil {
		t.Errorf("host %q, held %+v", in.Host, in.Held)
	}
	if !slices.Contains(b.log, "set aside") || slices.Contains(a.log, "set aside") {
		t.Errorf("home did %v, vps %v", a.log, b.log)
	}
	if f, ok := m.FailedMoves(ctx)["forgejo"]; !ok || f.ToName != "vps" || f.FromName != "home" {
		t.Errorf("failed moves %+v", m.FailedMoves(ctx))
	}
	// Deleting what was copied forgets it.
	m.RemoveKeptData(ctx, "n-0000000003", "/srv/roostack/services/forgejo.failed-move-20261022-141200")
	if _, ok := m.FailedMoves(ctx)["forgejo"]; ok {
		t.Error("the failed move should be forgotten once its copy is deleted")
	}

	// Going back fails too: held stopped on vps, until moved again.
	a.status = node.ApplyStatus{Finished: true, LastRun: time.Now().Add(time.Hour), LastErr: errors.New("home's apply failed")}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003", MoveBackup); err != nil {
		t.Fatal(err)
	}
	l = waitMove(t, m)
	if !strings.Contains(l.Err, "Forgejo is stopped on vps: its database didn't load there, and moving it back to home failed too: home's apply failed. "+
		"Roostack will keep it stopped until you choose.") {
		t.Fatalf("move: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	if in.Host != "n-0000000003" || in.Held == nil || in.Held.MovedFrom != "n-0000000002" || in.Held.Error != "home's apply failed" {
		t.Fatalf("host %q, held %+v", in.Host, in.Held)
	}
	services, _ := state.LoadServices(m.Lab)
	for _, s := range services {
		if out, _ := state.Output(m.Lab, s); s.Name() == "forgejo" && slices.ContainsFunc(out.Units, func(u string) bool { return u == "roostack-forgejo-app.service" }) {
			t.Error("a held service's app doesn't run")
		}
	}
	// Starting it anyway ends the hold.
	if err := m.StartHeld(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Held != nil {
		t.Error("still held")
	}
	var fe *FormError
	if err := m.StartHeld(ctx, "forgejo"); !errors.As(err, &fe) {
		t.Errorf("starting one that isn't held: %v", err)
	}
}

// A reader that returns before reading everything (as tar stops at its
// end-of-archive marker) fails the copy rather than leaving the writer
// waiting for ever, and a whole copy still succeeds.
func TestPipeReaderStopsEarly(t *testing.T) {
	data := make([]byte, 64<<10)
	done := make(chan error, 1)
	go func() {
		done <- pipe(context.Background(), func(w io.Writer) error { _, err := w.Write(data); return err },
			func(r io.Reader) error { _, err := io.ReadFull(r, make([]byte, 1024)); return err })
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a copy that wasn't all read succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the writer is still waiting")
	}
	err := pipe(context.Background(), func(w io.Writer) error { _, err := w.Write(data); return err },
		func(r io.Reader) error { _, err := io.Copy(io.Discard, r); return err })
	if err != nil {
		t.Error(err)
	}
}
