package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

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
	// outcomes are how each service's last restore or put back ended.
	outcomes map[string]RestoreOutcome
}

// RestoreOutcome is how a service's last restore, or put back, on this
// server ended, for the controller to hold it stopped if its databases
// didn't load (SPEC.md §6.9, "A restore that didn't finish").
type RestoreOutcome struct {
	// Finished: one has ended since Roostack started (the zero value:
	// none has).
	Finished bool
	// Err is why it failed ("" if it worked): with DumpsFailed, why the
	// databases didn't load.
	Err string
	// DumpsFailed: its files are the backup's (taken at Taken), but its
	// databases didn't load. Kept is a copy of its data from before, for
	// PutBack ("" if there was none).
	DumpsFailed bool
	Taken       time.Time
	Kept        string
}

// Outcome is how the service's last restore or put back ended.
func (r *Restores) Outcome(_ context.Context, name string) (RestoreOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outcomes[name], nil
}

// ended records how a job on a service ended.
func (r *Restores) ended(name string, o RestoreOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = false
	if r.outcomes == nil {
		r.outcomes = map[string]RestoreOutcome{}
	}
	r.outcomes[name] = o
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
			out := RestoreOutcome{Finished: true}
			defer func() {
				if err != nil && out.Err == "" {
					out.Err = err.Error()
				}
				r.ended(name, out)
				act.Finish(err)
			}()
			restorer := *r.Restorer
			restorer.Log = act
			res, err := restorer.RestoreService(ctx, svc, snapshotID)
			if res != nil && len(res.MovedAside) > 0 {
				act.Printf("The previous data is kept; remove it from the restore page once you're happy.")
			}
			if de := (*DumpsError)(nil); errors.As(err, &de) {
				// The controller holds it stopped (SPEC.md §6.9).
				out.DumpsFailed, out.Taken, out.Err = true, res.Snapshot.Time, de.Err.Error()
				for _, dir := range svc.Backup.Include {
					if aside := res.MovedAside[dir]; aside != "" && out.Kept == "" {
						out.Kept = aside
					}
				}
				act.Printf("%v", de.Err)
				return errors.New(NotLoadedMsg(title, res.Snapshot.Time))
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
				out := RestoreOutcome{Finished: true}
				if err != nil {
					out.Err = err.Error()
				}
				r.ended(name, out)
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
