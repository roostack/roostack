package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
)

// Updating a service to a newer package version (SPEC.md §7). The
// controller drives it (manager/update.go); the service's server does
// what has to happen there: download the new images while the old
// version runs, back up, then hold its maintenance queue while the
// controller switches the version and checks the service, so no backup,
// restore, OS update or restart starts in the middle.

// Updater is what updating a service needs from its server.
type Updater interface {
	// StartUpdate downloads images, then (after a backup, if asked)
	// holds the server's maintenance queue. It returns at once; the
	// controller follows it with UpdateStatus.
	StartUpdate(ctx context.Context, req UpdateRequest) (string, error)
	// UpdateStatus says how far it got. Asking also keeps the hold: one
	// nobody asks about for HoldIdle ends by itself.
	UpdateStatus(ctx context.Context, id string) (UpdateStatus, error)
	// FinishUpdate releases the hold.
	FinishUpdate(ctx context.Context, id string) error
	// CheckHealth checks a service's units and containers once.
	CheckHealth(ctx context.Context, service string) (Health, error)
}

// Updater is this server, which updates its own services.
func (n *Local) Updater() Updater { return n }

// UpdateRequest is an update about to happen on this server.
type UpdateRequest struct {
	// Title names it in the maintenance queue ("Updating Immich").
	Title string
	// Images are the new version's images, downloaded first.
	Images []string
	// Backup: back the server up before holding the queue.
	Backup bool
}

// Stages of an update on its server.
const (
	UpdateDownloading = "downloading"
	UpdateBackingUp   = "backing-up"
	// UpdateWaiting: queued behind other maintenance.
	UpdateWaiting  = "waiting"
	UpdateHeld     = "held"
	UpdateFailed   = "failed"
	UpdateReleased = "released"
)

// UpdateStatus is an update's stage on its server.
type UpdateStatus struct {
	Stage string `json:"stage"`
	// Error says why it failed.
	Error string `json:"error,omitempty"`
	// Backup is when the backup made for it finished (zero: none).
	Backup time.Time `json:"backup,omitzero"`
}

// Health is one check of a service.
type Health struct {
	// Healthy: every unit is active and every container check passed.
	Healthy bool `json:"healthy"`
	// Problem says what isn't, for people ("immich-server isn't running").
	Problem string `json:"problem,omitempty"`
	// Restarts adds up how often systemd restarted its units: a count
	// that climbs while it looks healthy means it keeps failing.
	Restarts int `json:"restarts"`
	// Failed: a unit is in systemd's failed state (it stopped restarting
	// it, after too many tries in a row).
	Failed bool `json:"failed,omitempty"`
}

// HoldIdle ends a hold nobody has asked about for this long (the
// controller went away mid-update). A variable for tests.
var HoldIdle = 10 * time.Minute

// ErrNoUpdate is an update ID the server doesn't know (it restarted).
var ErrNoUpdate = errors.New("this server has no such update (it may have restarted)")

// update is one update in progress on this server.
type update struct {
	status  UpdateStatus
	touched time.Time
	release chan struct{}
}

// updates are this server's updates in progress.
type updates struct {
	mu sync.Mutex
	m  map[string]*update
}

func (u *updates) get(id string) (*update, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x, ok := u.m[id]
	return x, ok
}

func (u *updates) set(x *update, f func(s *UpdateStatus)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(&x.status)
}

// chainedBackups is a backup that runs jobs after it (backup.Service).
type chainedBackups interface {
	Submit(chain ...maintenance.Job) error
}

func (n *Local) StartUpdate(ctx context.Context, req UpdateRequest) (string, error) {
	if n.Jobs.Coord == nil {
		return "", errors.New("this server's maintenance isn't running")
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := hex.EncodeToString(b)
	x := &update{status: UpdateStatus{Stage: UpdateDownloading}, touched: time.Now(), release: make(chan struct{})}
	n.updates.mu.Lock()
	if n.updates.m == nil {
		n.updates.m = map[string]*update{}
	}
	n.updates.m[id] = x
	n.updates.mu.Unlock()
	go n.runUpdate(x, req)
	return id, nil
}

// runUpdate downloads, backs up and queues the hold.
func (n *Local) runUpdate(x *update, req UpdateRequest) {
	ctx := context.Background()
	fail := func(err error) {
		n.updates.set(x, func(s *UpdateStatus) { s.Stage, s.Error = UpdateFailed, err.Error() })
	}
	for _, image := range req.Images {
		if n.Reconciler == nil || n.Reconciler.Pull == nil {
			break
		}
		if err := n.Reconciler.Pull(ctx, image, io.Discard); err != nil {
			fail(fmt.Errorf("downloading %s: %w", image, err))
			return
		}
	}
	hold := maintenance.Job{Name: req.Title, Run: func(jctx context.Context, h *maintenance.Handle) error {
		return n.hold(jctx, x)
	}}
	b, ok := n.Jobs.Backups.(chainedBackups)
	if !req.Backup || !ok {
		n.updates.set(x, func(s *UpdateStatus) { s.Stage = UpdateWaiting })
		n.Jobs.Coord.Submit(hold)
		return
	}
	n.updates.set(x, func(s *UpdateStatus) { s.Stage = UpdateBackingUp })
	started := time.Now()
	err := b.Submit(hold)
	if errors.Is(err, backup.ErrAlreadyQueued) {
		// One is already queued or running: wait for it, and use it if it
		// started after this update did.
		run, werr := n.waitBackup(ctx, started)
		if werr != nil {
			fail(werr)
			return
		}
		n.updates.set(x, func(s *UpdateStatus) { s.Stage, s.Backup = UpdateWaiting, run })
		if err := b.Submit(hold); errors.Is(err, backup.ErrAlreadyQueued) {
			n.Jobs.Coord.Submit(hold) // the queued one may be older; this one is fresh enough
		} else if err != nil {
			fail(err)
		}
		return
	}
	if err != nil {
		fail(fmt.Errorf("backing up first: %w", err))
		return
	}
	// The hold runs after the backup, or not at all if it fails.
	if _, err := n.waitBackup(ctx, started); err != nil {
		fail(err)
	}
}

// waitBackup waits for a backup that finished after t, and says when.
func (n *Local) waitBackup(ctx context.Context, t time.Time) (time.Time, error) {
	for {
		st, err := n.Jobs.Backups.Status(ctx)
		if err != nil {
			return time.Time{}, err
		}
		if !st.Running && !st.Queued && st.Last != nil && st.Last.Finished.After(t) {
			if !st.Last.OK {
				return time.Time{}, fmt.Errorf("the backup before updating didn't finish: %s", st.Last.Error)
			}
			return st.Last.Finished, nil
		}
		time.Sleep(2 * time.Second)
	}
}

// hold is the maintenance job that keeps the queue while the controller
// switches the service and checks it.
func (n *Local) hold(ctx context.Context, x *update) error {
	n.updates.mu.Lock()
	if x.status.Stage == UpdateFailed || time.Since(x.touched) > HoldIdle {
		n.updates.mu.Unlock()
		return nil
	}
	x.status.Stage = UpdateHeld
	if x.status.Backup.IsZero() && x.status.Error == "" {
		if st, err := n.Jobs.Backups.Status(ctx); err == nil && st.Last != nil && st.Last.OK {
			x.status.Backup = st.Last.Finished
		}
	}
	n.updates.mu.Unlock()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-x.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			n.updates.mu.Lock()
			idle := time.Since(x.touched) > HoldIdle
			if idle {
				x.status.Stage = UpdateReleased
			}
			n.updates.mu.Unlock()
			if idle {
				return nil
			}
		}
	}
}

func (n *Local) UpdateStatus(ctx context.Context, id string) (UpdateStatus, error) {
	x, ok := n.updates.get(id)
	if !ok {
		return UpdateStatus{}, ErrNoUpdate
	}
	n.updates.mu.Lock()
	defer n.updates.mu.Unlock()
	x.touched = time.Now()
	return x.status, nil
}

func (n *Local) FinishUpdate(ctx context.Context, id string) error {
	x, ok := n.updates.get(id)
	if !ok {
		return nil
	}
	n.updates.mu.Lock()
	delete(n.updates.m, id)
	if x.status.Stage != UpdateReleased {
		x.status.Stage = UpdateReleased
		close(x.release)
	}
	n.updates.mu.Unlock()
	return nil
}

// checkTimeout bounds one container check.
const checkTimeout = 10 * time.Second

func (n *Local) CheckHealth(ctx context.Context, service string) (Health, error) {
	out, err := n.output(service)
	if err != nil {
		return Health{}, err
	}
	var h Health
	if n.Units != nil {
		units, err := n.Units.Status(ctx, out.Units...)
		if err != nil {
			return Health{}, err
		}
		for _, u := range units {
			h.Restarts += u.NRestarts
			if u.ActiveState != "active" && h.Problem == "" {
				name := strings.TrimPrefix(strings.TrimSuffix(u.Unit, ".service"), "roostack-")
				switch u.ActiveState {
				case "failed":
					h.Problem, h.Failed = name+" has stopped", true
				case "activating", "reloading":
					h.Problem = name + " is still starting"
				default:
					h.Problem = name + " isn't running"
				}
			}
		}
	}
	if h.Problem == "" {
		h.Problem = n.runChecks(ctx, out.Checks)
	}
	h.Healthy = h.Problem == ""
	return h, nil
}

// runChecks runs a service's container checks, saying what failed first.
func (n *Local) runChecks(ctx context.Context, checks []generate.HealthCheck) string {
	for _, c := range checks {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		problem := ""
		switch {
		case c.HTTP != "":
			problem = httpCheck(cctx, c.HTTP)
		case c.Cmd != "" && n.Exec != nil:
			if out, errOut, err := n.Exec(cctx, c.Container, nil, []string{"/bin/sh", "-c", c.Cmd}); err != nil {
				problem = "its health check failed: " + lastLines(append(out, errOut...), err)
			}
		}
		cancel()
		if problem != "" {
			return strings.TrimPrefix(c.Container, "roostack-") + ": " + problem
		}
	}
	return ""
}

// httpCheck asks a container's health address; anything below 500 works
// (a login page's redirect too).
func httpCheck(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err.Error()
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return "it isn't answering on its web address"
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Sprintf("its web address is answering with an error (%d)", resp.StatusCode)
	}
	return ""
}
