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
}

// Configured reports whether a backup destination is set.
func (s *Service) Configured() bool { return s.Deps != nil }

// Job is the backup job for the coordinator. It records every attempt.
func (s *Service) Job() maintenance.Job {
	return maintenance.Job{
		Name:      JobName,
		Resumable: true,
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			defer s.setPending(false)
			s.mu.Lock()
			act := s.act
			s.act = nil
			s.mu.Unlock()
			if act == nil { // resumed after a reboot, or run directly
				act = s.Activity.Start(activity.Backup, "Backup")
			}
			start := time.Now()
			deps := *s.Deps
			deps.Log = act
			deps.OnCopy = func(c Copy, started time.Time, sum *Summary, err error) {
				run := store.BackupRun{Started: started, Finished: time.Now(), OK: err == nil, Destination: c.Name}
				if sum != nil {
					run.SnapshotID, run.FilesNew, run.DataAdded, run.Stored = sum.SnapshotID, sum.FilesNew, sum.DataAdded, sum.Stored
				}
				if err != nil {
					run.Error = err.Error()
				}
				s.Store.RecordBackup(context.WithoutCancel(ctx), run)
			}
			sum, err := Run(ctx, deps, h)
			if ctx.Err() != nil && err != nil {
				act.Printf("Paused for a restart; the backup continues afterwards.")
			}
			act.Finish(err)
			run := store.BackupRun{Started: start, Finished: time.Now(), OK: err == nil}
			if sum != nil {
				run.SnapshotID, run.FilesNew, run.DataAdded, run.Stored = sum.SnapshotID, sum.FilesNew, sum.DataAdded, sum.Stored
			}
			if err != nil {
				run.Error = err.Error()
				if ctx.Err() != nil {
					run.Error = "paused for a reboot; it resumes afterwards"
				}
			}
			if rerr := s.Store.RecordBackup(context.WithoutCancel(ctx), run); rerr != nil && err == nil {
				err = rerr
			}
			if err == nil && s.AfterBackup != nil {
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
				act.Step("Old backups are removed by the controller")
			} else {
				act.Step("Removing old backups (keeping 7 daily, 4 weekly and 6 monthly)")
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

// RunNow queues a backup ("Back up now").
func (s *Service) RunNow() error { return s.Submit() }

// Submit queues a backup unless one is already queued or running.
func (s *Service) Submit(chain ...maintenance.Job) error {
	if !s.Configured() {
		return errors.New("backups are not set up")
	}
	s.mu.Lock()
	if s.pending {
		s.mu.Unlock()
		return ErrAlreadyQueued
	}
	s.pending = true
	// Start the log now, so a backup queued behind other work shows as waiting.
	s.act = s.Activity.Start(activity.Backup, "Backup")
	s.act.Step("Waiting for other maintenance to finish")
	s.mu.Unlock()
	s.Coord.Submit(append([]maintenance.Job{s.Job()}, chain...)...)
	return nil
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
	Step   string
	Last   *store.BackupRun
	LastOK *store.BackupRun
	Next   time.Time
	// Copies are the extra copies somewhere else, with their own results.
	Copies []CopyStatus
}

// CopyStatus is an extra copy's latest results.
type CopyStatus struct {
	Name, Title  string
	Repository   string
	Last, LastOK *store.BackupRun
}

// Status reports the current backup state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{Configured: s.Configured(), LocalOnly: isLocal(s.Repository), Progress: -1}
	if !st.Configured {
		return st, nil
	}
	var err error
	if st.Last, st.LastOK, err = s.Store.LastBackup(ctx); err != nil {
		return st, err
	}
	for _, c := range s.Deps.Copies {
		cs := CopyStatus{Name: c.Name, Title: c.Title, Repository: c.Repo.URL}
		if cs.Last, cs.LastOK, err = s.Store.LastBackupTo(ctx, c.Name); err != nil {
			return st, err
		}
		st.Copies = append(st.Copies, cs)
		if !isLocal(c.Repo.URL) {
			st.LocalOnly = false
		}
	}
	if s.NextRun != nil {
		st.Next = s.NextRun()
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
