package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
)

// ErrRestoreQueued means a restore is already queued or running.
var ErrRestoreQueued = errors.New("a restore is already running or queued")

// Restores restores services from the UI through the maintenance
// coordinator, so a restore never overlaps a backup, an update or a
// reboot (SPEC.md §3.2).
type Restores struct {
	Coord    *maintenance.Coordinator
	Restorer *Restorer
	Repo     *Repo
	// Lookup returns an installed service's generated output and title.
	Lookup   func(name string) (*generate.Output, string, error)
	Activity *activity.Registry

	mu      sync.Mutex
	pending bool
}

// Snapshots lists the backups that contain a service's data, newest first.
func (r *Restores) Snapshots(ctx context.Context, name string) ([]Snapshot, error) {
	if _, _, err := r.Lookup(name); err != nil {
		return nil, err
	}
	snaps, err := r.Repo.Snapshots(ctx, TagRoostack, ServiceTag(name))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(snaps, func(a, b Snapshot) int { return b.Time.Compare(a.Time) })
	return snaps, nil
}

// Pending reports whether a restore is queued or running.
func (r *Restores) Pending(_ context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending, nil
}

// Restore queues restoring a service from a snapshot.
func (r *Restores) Restore(_ context.Context, name, snapshotID string) error {
	svc, title, err := r.Lookup(name)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.pending {
		r.mu.Unlock()
		return ErrRestoreQueued
	}
	r.pending = true
	r.mu.Unlock()

	act := r.Activity.Start(activity.Restore, "Restoring "+title)
	act.Step("Waiting for other maintenance to finish")
	r.Coord.Submit(maintenance.Job{
		Name: "restoring " + title, // never interrupted: not resumable, never marked interruptible
		Run: func(ctx context.Context, h *maintenance.Handle) (err error) {
			defer func() {
				r.mu.Lock()
				r.pending = false
				r.mu.Unlock()
				act.Finish(err)
			}()
			restorer := *r.Restorer
			restorer.Log = act
			res, err := restorer.RestoreService(ctx, svc, snapshotID)
			if res != nil && len(res.MovedAside) > 0 {
				act.Printf("The previous data is kept; remove it from the restore page once you're happy.")
			}
			if err == nil {
				act.Step("%s is back to the backup from %s", title, res.Snapshot.Time.Local().Format("Mon 2 Jan 15:04"))
			}
			return err
		},
	})
	return nil
}

// Kept lists the copies restores left behind for a service.
func (r *Restores) Kept(_ context.Context, name string) ([]KeptCopy, error) {
	svc, _, err := r.Lookup(name)
	if err != nil {
		return nil, err
	}
	return KeptCopies(svc)
}

// PutBack puts one kept copy back (Restorer.PutBack), as a piece of
// maintenance, in the background.
func (r *Restores) PutBack(ctx context.Context, name, path string) error {
	svc, title, err := r.Lookup(name)
	if err != nil {
		return err
	}
	if kept, err := KeptCopies(svc); err != nil || !slices.ContainsFunc(kept, func(k KeptCopy) bool { return k.Path == path }) {
		return fmt.Errorf("%s is not a copy kept by a restore", path)
	}
	r.mu.Lock()
	if r.pending {
		r.mu.Unlock()
		return ErrRestoreQueued
	}
	r.pending = true
	r.mu.Unlock()
	act := r.Activity.Start(activity.Restore, "Putting back "+title+"'s kept copy")
	act.Step("Waiting for other maintenance to finish")
	r.Coord.Submit(maintenance.Job{
		Name: "putting back " + title + "'s kept copy",
		Run: func(ctx context.Context, h *maintenance.Handle) (err error) {
			defer func() {
				r.mu.Lock()
				r.pending = false
				r.mu.Unlock()
				act.Finish(err)
			}()
			restorer := *r.Restorer
			restorer.Log = act
			return restorer.PutBack(ctx, svc, path)
		},
	})
	return nil
}

// RemoveKept deletes one kept copy.
func (r *Restores) RemoveKept(ctx context.Context, name, path string) error {
	svc, _, err := r.Lookup(name)
	if err != nil {
		return err
	}
	return r.Restorer.RemoveKept(ctx, svc, path)
}
