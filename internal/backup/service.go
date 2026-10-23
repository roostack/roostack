package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store"
)

// ErrAlreadyQueued means a backup is already running or waiting to run.
var ErrAlreadyQueued = errors.New("a backup is already running or queued")

// Service runs backups through the maintenance coordinator and records
// their results for the dashboard.
type Service struct {
	Coord *maintenance.Coordinator
	// Deps builds the job; nil means backups are not set up.
	Deps  *JobDeps
	Store *store.Store
	// Repository is shown to decide the local-only warning.
	Repository string
	// NextRun reports when the nightly backup runs next.
	NextRun func() time.Time
	// Activity shows runs live in the UI (optional).
	Activity *activity.Registry
	// PrunedElsewhere: the controller removes old backups (SPEC.md
	// §10.10); the weekly job only checks.
	PrunedElsewhere bool
	// AfterBackup runs after a nightly backup that worked (e.g. counting
	// disk use while restic's reads are still cached); optional.
	AfterBackup func()

	mu      sync.Mutex
	pending bool          // queued or running
	act     *activity.Log // the queued run's log
	only    []string      // the queued run's destinations (nil: all)
	retries map[string]*retryState
	// catchUpAt is when CatchUp next looks for a missed backup (zero:
	// unknown), so the next backup's time can say when that runs one.
	catchUpAt time.Time
}

// Configured reports whether a backup destination is set.
func (s *Service) Configured() bool { return s.Deps != nil }

// Job is the backup job for the coordinator. It records every attempt,
// and plans another try of the destinations that failed (retry.go).
func (s *Service) Job() maintenance.Job {
	return maintenance.Job{
		Name:      JobName,
		Resumable: true,
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			defer s.setPending(false)
			s.mu.Lock()
			act, only := s.act, s.only
			s.act, s.only = nil, nil
			s.mu.Unlock()
			if act == nil { // resumed after a reboot, or run directly
				act = s.Activity.Start(activity.Backup, "Backup")
			}
			start := time.Now()
			deps := *s.Deps
			deps.Log, deps.Only = act, only
			results := map[string]error{}
			deps.OnCopy = func(c Copy, started time.Time, sum *Summary, err error) {
				results[c.Name] = err
				// Recorded by repository, not as a copy: a copy can become
				// the main backup and keeps its results (SPEC.md §6.9).
				run := store.BackupRun{Started: started, Finished: time.Now(), OK: err == nil, Repository: c.Repo.URL}
				if sum != nil {
					run.SnapshotID, run.FilesNew, run.DataAdded, run.Stored = sum.SnapshotID, sum.FilesNew, sum.DataAdded, sum.Stored
					run.Services = sum.Services
				}
				if err != nil {
					run.Error = err.Error()
				}
				s.Store.RecordBackup(context.WithoutCancel(ctx), run)
			}
			sum, err := Run(ctx, deps, h)
			// Out of date: stand down until the Owner says otherwise.
			var fenced *FenceError
			if errors.As(err, &fenced) {
				s.setFence(context.WithoutCancel(ctx), &fenced.Fence)
				act.Finish(err)
				return nil // not a failure to retry: there's nothing to do
			}
			if ctx.Err() != nil && err != nil {
				act.Printf("Paused for a restart: the backup will carry on afterwards.")
			}
			if deps.wants("") {
				results[""] = err
			}
			// A run that failed before reaching a copy (preparing the
			// databases) failed it too.
			for _, c := range deps.Copies {
				if _, ok := results[c.Name]; !ok && err != nil && deps.wants(c.Name) {
					results[c.Name] = err
				}
			}
			if ctx.Err() == nil {
				s.updateRetries(time.Now(), results, only == nil)
				s.logRetries(act)
			}
			act.Finish(err)
			if !deps.wants("") {
				return nil // a retry of copies only: the main backup's result stands
			}
			run := store.BackupRun{Started: start, Finished: time.Now(), OK: err == nil, Repository: deps.Repo.URL}
			if sum != nil {
				run.SnapshotID, run.FilesNew, run.DataAdded, run.Stored = sum.SnapshotID, sum.FilesNew, sum.DataAdded, sum.Stored
				run.Services = sum.Services
			}
			if err != nil {
				run.Error = err.Error()
				if ctx.Err() != nil {
					run.Error = "paused for a restart: it will carry on afterwards"
				}
			}
			if rerr := s.Store.RecordBackup(context.WithoutCancel(ctx), run); rerr != nil && err == nil {
				err = rerr
			}
			if err == nil && only == nil && s.AfterBackup != nil {
				s.AfterBackup()
			}
			return err
		},
	}
}

// MaintenanceJob prunes old snapshots and checks a sample of the stored
// data (SPEC.md §6.9), weekly.
func (s *Service) MaintenanceJob() maintenance.Job {
	return maintenance.Job{
		Name: "backup maintenance",
		Run: func(ctx context.Context, h *maintenance.Handle) (err error) {
			act := s.Activity.Start(activity.Backup, "Backup maintenance")
			defer func() { act.Finish(err) }()
			if s.PrunedElsewhere {
				act.Step("Not removing old backups here: the controller will do that")
			} else {
				act.Step("Removing old backups (keeping 7 daily, 4 weekly, and 6 monthly)")
				if err := s.Deps.Repo.Forget(ctx, DefaultRetention); err != nil {
					return err
				}
				for _, c := range s.Deps.Copies {
					if err := c.Repo.Forget(ctx, DefaultRetention); err != nil {
						return fmt.Errorf("the copy at %s: %w", c.Title, err)
					}
				}
			}
			act.Step("Checking a sample (5%%) of the stored backups can be read")
			if err := s.Deps.Repo.Check(ctx, "5%"); err != nil {
				return err
			}
			for _, c := range s.Deps.Copies {
				act.Step("Checking a sample of the copy at %s", c.Title)
				if err := c.Repo.Check(ctx, "5%"); err != nil {
					return fmt.Errorf("the copy at %s: %w", c.Title, err)
				}
			}
			return nil
		},
	}
}

// MissedAfter is how long after the last backup attempt one counts as
// missed: a nightly backup plus a couple of hours' slack.
const MissedAfter = 26 * time.Hour

// CatchUp queues a backup if none was attempted for MissedAfter (or ever):
// the server was off, or asleep, at the nightly time, which would
// otherwise only come round again the next night; and the first backup
// runs soon after backups are set up, not that night (SPEC.md §6.9). It
// says why it queued one ("": it didn't). Failed backups have their own
// retries (retry.go), so an attempt counts, not a success.
func (s *Service) CatchUp(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", nil
	}
	last, _, err := s.Store.LastBackup(ctx, s.Deps.Repo.URL)
	if err != nil {
		return "", err
	}
	if last != nil && time.Since(last.Started) < MissedAfter {
		return "", nil
	}
	err = s.Submit()
	if errors.Is(err, ErrAlreadyQueued) || errors.Is(err, ErrFenced) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if last == nil {
		return "no backup yet", nil
	}
	return "none for over a day", nil
}

// CatchUpAt says when CatchUp next runs, for the next backup's time.
func (s *Service) CatchUpAt(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catchUpAt = t
}

// RunNow queues a backup ("Back up now").
func (s *Service) RunNow(_ context.Context) error { return s.Submit() }

// Submit queues a backup to every destination unless one is already
// queued or running.
func (s *Service) Submit(chain ...maintenance.Job) error {
	if !s.Configured() {
		return errors.New("backups are not set up")
	}
	if s.loadFence(context.Background()) != nil {
		return ErrFenced
	}
	if !s.submit(nil, chain...) {
		return ErrAlreadyQueued
	}
	return nil
}

// submit queues a backup to destinations only (nil: all); false if one is
// already queued or running.
func (s *Service) submit(only []string, chain ...maintenance.Job) bool {
	s.mu.Lock()
	if s.pending {
		s.mu.Unlock()
		return false
	}
	s.pending, s.only = true, only
	// Start the log now, so a backup queued behind other work shows as waiting.
	title := "Backup"
	if only != nil {
		title = "Backup (trying again)"
	}
	s.act = s.Activity.Start(activity.Backup, title)
	s.act.Step("Waiting for other maintenance to finish")
	s.mu.Unlock()
	s.Coord.Submit(append([]maintenance.Job{s.Job()}, chain...)...)
	return true
}

// logRetries says in a run's log when its failed destinations are tried
// again.
func (s *Service) logRetries(act *activity.Log) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := map[string]string{"": "the main destination"}
	for _, c := range s.Deps.Copies {
		names[c.Name] = c.Title
	}
	for name, st := range s.retries {
		if st.Drive {
			act.Printf("Roostack will back up to %s as soon as its drive is plugged in.", names[name])
		} else {
			act.Printf("Roostack will try %s again at %s.", names[name], st.At.Local().Format("15:04"))
		}
	}
}

func (s *Service) setPending(v bool) {
	s.mu.Lock()
	s.pending = v
	s.mu.Unlock()
}

// Status is what the dashboard shows about backups.
type Status struct {
	Configured bool
	// LocalOnly: the only copy is on this server (SPEC.md §6.9 warning).
	LocalOnly bool
	Running   bool
	Queued    bool
	Progress  int // -1 if unknown
	// Step is what a running backup is doing ("Copying to vps's disk").
	Step string
	// Repository is the main backup's; Last and LastOK are its results.
	Repository string
	// Retry is the main destination's next try after a failure (nil: none).
	Retry  *Retry
	Last   *store.BackupRun
	LastOK *store.BackupRun
	Next   time.Time
	// Copies are the extra copies somewhere else, with their own results.
	Copies []CopyStatus
	// Fenced says why backups stood down: this copy of the lab is out of
	// date (fence.go). Nil normally.
	Fenced *Fence
}

// For returns the results of the destination with a repository, main
// backup or copy. Pages match their entries by repository rather than by
// role, so a page read while a swap of the main backup and a copy is
// still reaching the server shows each its own (SPEC.md §6.9).
func (st Status) For(repository string) (last, lastOK *store.BackupRun, retry *Retry) {
	if repository == "" {
		return nil, nil, nil
	}
	if st.Repository == repository {
		return st.Last, st.LastOK, st.Retry
	}
	for _, c := range st.Copies {
		if c.Repository == repository {
			return c.Last, c.LastOK, c.Retry
		}
	}
	return nil, nil, nil
}

// CopyStatus is an extra copy's latest results.
type CopyStatus struct {
	Name, Title  string
	Repository   string
	Last, LastOK *store.BackupRun
	Retry        *Retry
}

// Status reports the current backup state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{Configured: s.Configured(), LocalOnly: isLocal(s.Repository), Progress: -1}
	if !st.Configured {
		return st, nil
	}
	// Results are found by repository, so after the main backup and a
	// copy swap each shows its own (SPEC.md §6.9).
	var err error
	st.Repository = s.Deps.Repo.URL
	if st.Last, st.LastOK, err = s.Store.LastBackup(ctx, st.Repository); err != nil {
		return st, err
	}
	for _, c := range s.Deps.Copies {
		cs := CopyStatus{Name: c.Name, Title: c.Title, Repository: c.Repo.URL, Retry: s.retryFor(c.Name)}
		if cs.Last, cs.LastOK, err = s.Store.LastBackup(ctx, c.Repo.URL); err != nil {
			return st, err
		}
		st.Copies = append(st.Copies, cs)
		if !isLocal(c.Repo.URL) {
			st.LocalOnly = false
		}
	}
	st.Retry = s.retryFor("")
	st.Fenced = s.loadFence(ctx)
	if s.NextRun != nil {
		st.Next = s.NextRun()
	}
	// The catch-up runs one before the night when none was tried for a
	// day, or ever (a new lab's first backup).
	s.mu.Lock()
	at := s.catchUpAt
	s.mu.Unlock()
	if !at.IsZero() && (st.Next.IsZero() || at.Before(st.Next)) && (st.Last == nil || at.Sub(st.Last.Started) >= MissedAfter) {
		st.Next = at
	}
	if r := s.Coord.Status().Running; r != nil && r.Name == JobName {
		st.Running, st.Progress = true, r.Progress
		if snap, ok := s.Activity.Latest(activity.Backup); ok && snap.Running() {
			st.Step = snap.Status
		}
	}
	s.mu.Lock()
	st.Queued = s.pending && !st.Running
	s.mu.Unlock()
	return st, nil
}

// isLocal reports whether a restic repository is a local path rather than
// a remote backend (s3:, sftp:, rest:, b2:, rclone:, ...).
func isLocal(repo string) bool {
	if repo == "" {
		return false
	}
	scheme, _, ok := strings.Cut(repo, ":")
	return !ok || strings.Contains(scheme, "/")
}
