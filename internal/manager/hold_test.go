package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// ownRestores is a server's own restores (node.Restores): each queued job
// stays pending until the test ends it with an outcome.
type ownRestores struct {
	mu       sync.Mutex
	pending  bool
	outcome  backup.RestoreOutcome
	restored []string
	putBack  []string
}

func (r *ownRestores) Snapshots(context.Context, string) ([]backup.Snapshot, error) { return nil, nil }
func (r *ownRestores) Restore(_ context.Context, name, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending, r.restored = true, append(r.restored, name+" "+id)
	return nil
}
func (r *ownRestores) Pending(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending, nil
}
func (r *ownRestores) Outcome(context.Context, string) (backup.RestoreOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outcome, nil
}
func (r *ownRestores) PutBack(_ context.Context, _, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending, r.putBack = true, append(r.putBack, path)
	return nil
}

// end ends the job that's pending with an outcome.
func (r *ownRestores) end(o backup.RestoreOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending, r.outcome = false, o
}

// restoringServer is a server that restores its own services.
type restoringServer struct {
	*movingServer
	restores *ownRestores
}

func (s *restoringServer) Restores() node.Restores { return s.restores }

// waitHeld waits until the service's hold is (or isn't) there.
func waitHeld(t *testing.T, m *Manager, held bool) *instance.Instance {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if in, err := instance.Load(state.InstanceFile(m.Lab, "forgejo")); err == nil && (in.Held != nil) == held {
			return in
		}
	}
	t.Fatalf("held should be %v", held)
	return nil
}

// A server's own restore whose databases don't load holds the service
// stopped once the controller sees how it ended; putting back what was
// there, or restoring again, ends the hold (SPEC.md §6.9).
func TestOwnRestoreHeld(t *testing.T) {
	m, a, b := setUpMove(t)
	ctx := context.Background()
	restoreWatch = 5 * time.Millisecond
	defer func() { restoreWatch = 2 * time.Second }()
	home := &restoringServer{movingServer: a, restores: &ownRestores{}}
	m.Others = func() []RemoteServer { return []RemoteServer{home, b} }
	var mu sync.Mutex
	var audited []string
	m.Audit = func(_ context.Context, action, detail string) {
		mu.Lock()
		defer mu.Unlock()
		audited = append(audited, detail)
	}

	if err := m.RestoreService(ctx, "forgejo", "backup:n-0000000002@9a6817f7aaaa", ""); err != nil {
		t.Fatal(err)
	}
	if len(home.restores.restored) != 1 || home.restores.restored[0] != "forgejo 9a6817f7aaaa" {
		t.Fatalf("home restored %v", home.restores.restored)
	}
	taken := time.Now().Add(-26 * time.Hour).Truncate(time.Second)
	kept := "/srv/roostack/services/forgejo.before-restore-20261022-141200"
	home.restores.end(backup.RestoreOutcome{Finished: true, DumpsFailed: true, Err: "pg_restore: disk full", Taken: taken, Kept: kept})
	in := waitHeld(t, m, true)
	if h := in.Held; h.Backup != "backup:n-0000000002@9a6817f7aaaa" || !h.Taken.Equal(taken) || h.Error != "pg_restore: disk full" ||
		h.Kept != kept || !h.KeptByRestore {
		t.Errorf("held %+v", h)
	}

	// Put back what was there: the server does it, then the hold ends.
	if err := m.PutBackHeld(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	if len(home.restores.putBack) != 1 || home.restores.putBack[0] != kept {
		t.Fatalf("put back %v", home.restores.putBack)
	}
	time.Sleep(20 * time.Millisecond)
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo")); in.Held == nil {
		t.Fatal("held until the put back has finished")
	}
	home.restores.end(backup.RestoreOutcome{Finished: true})
	waitHeld(t, m, false)

	// Held again, then restored again: the hold ends, recorded.
	m.RestoreService(ctx, "forgejo", "backup:n-0000000002@9a6817f7aaaa", "")
	home.restores.end(backup.RestoreOutcome{Finished: true, DumpsFailed: true, Err: "pg_restore: disk full", Taken: taken})
	waitHeld(t, m, true)
	var fe *usererr.Error
	if err := m.PutBackHeld(ctx, "forgejo"); !errors.As(err, &fe) {
		t.Errorf("nothing kept to put back: %v", err)
	}
	if err := m.RestoreService(ctx, "forgejo", "backup:n-0000000002@9a6817f7aaaa", ""); err != nil {
		t.Fatal(err)
	}
	home.restores.end(backup.RestoreOutcome{Finished: true})
	waitHeld(t, m, false)
	// Recorded just after the hold ends.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(audited)
		mu.Unlock()
		if n > 0 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(audited) != 1 || audited[0] != "Restored Forgejo, so it's no longer stopped" {
		t.Errorf("audited %q", audited)
	}
}
