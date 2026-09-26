package backup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/maintenance"
	"github.com/mylab/mylab/internal/store"
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

	mu      sync.Mutex
	pending bool // queued or running
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
			start := time.Now()
			sum, err := Run(ctx, *s.Deps, h)
			run := store.BackupRun{Started: start, Finished: time.Now(), OK: err == nil}
			if sum != nil {
				run.SnapshotID, run.FilesNew, run.DataAdded = sum.SnapshotID, sum.FilesNew, sum.DataAdded
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
			return err
		},
	}
}

// MaintenanceJob prunes old snapshots and checks a sample of the stored
// data (SPEC.md §6.9), weekly.
func (s *Service) MaintenanceJob() maintenance.Job {
	return maintenance.Job{
		Name: "backup maintenance",
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			if err := s.Deps.Repo.Forget(ctx, DefaultRetention); err != nil {
				return err
			}
			return s.Deps.Repo.Check(ctx, "5%")
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
	// LocalOnly: the only copy is on this machine (SPEC.md §6.9 warning).
	LocalOnly bool
	Running   bool
	Queued    bool
	Progress  int // -1 if unknown
	Last      *store.BackupRun
	LastOK    *store.BackupRun
	Next      time.Time
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
	if s.NextRun != nil {
		st.Next = s.NextRun()
	}
	if r := s.Coord.Status().Running; r != nil && r.Name == JobName {
		st.Running, st.Progress = true, r.Progress
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
