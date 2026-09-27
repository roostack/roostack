package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/state"
)

type file struct {
	Data string
	Mod  time.Time
}

// movingMachine is a machine whose data lives in memory; "tar" is JSON.
type movingMachine struct {
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
}

func newMoving(id, name string) *movingMachine {
	m := &movingMachine{fakeRemote: &fakeRemote{settings: &fakeMachineSettings{}}, id: nodeid.Identity{ID: id, Name: name},
		secrets: map[string][]byte{}}
	for i := range dataDirs {
		m.data[i], m.staged[i] = map[string]file{}, map[string]file{}
	}
	return m
}

func (m *movingMachine) Identity() nodeid.Identity { return m.id }
func (m *movingMachine) Address() string           { return "100.96.0.9" }
func (m *movingMachine) did(op string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, op)
	if op == m.failOn {
		return errors.New(op + " failed")
	}
	return nil
}
func (m *movingMachine) DataDirs(string) []string { return nil }
func (m *movingMachine) ExportData(_ context.Context, _ string, dir int, since time.Time, w io.Writer) error {
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
func (m *movingMachine) ImportData(_ context.Context, _ string, dir int, r io.Reader) error {
	var in map[string]file
	if err := json.NewDecoder(r).Decode(&in); err != nil {
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
func (m *movingMachine) ListData(_ context.Context, _ string, dir int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.data[dir] {
		out = append(out, p)
	}
	return out, nil
}
func (m *movingMachine) PruneData(_ context.Context, _ string, dir int, keep []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p := range m.staged[dir] {
		if !slices.Contains(keep, p) {
			delete(m.staged[dir], p)
		}
	}
	return nil
}
func (m *movingMachine) CommitData(context.Context, string) ([]string, error) {
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
func (m *movingMachine) SetAsideData(context.Context, string, string) ([]string, error) {
	return []string{"/srv/mylab/services/forgejo.moved"}, m.did("set aside")
}
func (m *movingMachine) StopService(context.Context, string) error {
	if m.beforeStop != nil {
		m.beforeStop()
	}
	return m.did("stop")
}
func (m *movingMachine) StartService(context.Context, string) error { return m.did("start") }
func (m *movingMachine) ExportSecrets(context.Context, string) (map[string][]byte, error) {
	return m.secrets, m.did("export secrets")
}
func (m *movingMachine) ImportSecrets(_ context.Context, s map[string][]byte) error {
	m.mu.Lock()
	for k, v := range s {
		m.secrets[k] = v
	}
	m.mu.Unlock()
	return m.did("import secrets")
}
func (m *movingMachine) DataSize(context.Context, string) (int64, error) { return 1234, nil }

func setUpMove(t *testing.T) (*Manager, *movingMachine, *movingMachine) {
	t.Helper()
	m, _ := newManager(t)
	addForgejo(t, m)
	m.Lab.NodeID = "n-0000000001"
	a, b := newMoving("n-0000000002", "home"), newMoving("n-0000000003", "vps")
	m.Others = func() []RemoteMachine { return []RemoteMachine{a, b} }
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
	a.secrets["mylab-forgejo-db_password"] = []byte("generated")
	var fe *FormError
	if err := m.MoveService(ctx, "forgejo", "n-0000000002"); !errors.As(err, &fe) {
		t.Errorf("moving to where it runs: %v", err)
	}
	if info, err := m.MoveInfo(ctx, "forgejo"); err != nil || info.From.Name != "home" || info.Size != 1234 {
		t.Errorf("info = %+v, %v", info, err)
	}
	if err := m.MoveService(ctx, "forgejo", "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	l := waitMove(t, m)
	if l.Err != "" {
		t.Fatalf("move: %s\n%s", l.Err, strings.Join(l.Lines, "\n"))
	}
	if b.data[0]["data/app.ini"].Data != "config" || b.data[1]["photos/1.jpg"].Data != "jpeg" ||
		string(b.secrets["mylab-forgejo-db_password"]) != "generated" {
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
	if err := m.MoveService(ctx, "forgejo", "n-0000000003"); err != nil {
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
	if err := m.MoveService(context.Background(), "forgejo", "n-0000000003"); err != nil {
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
