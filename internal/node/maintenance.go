package node

import (
	"context"

	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/osupdate"
)

// A machine's maintenance jobs (SPEC.md §3.2, §6.9): each node backs up
// its own services and keeps its own OS up to date. The jobs share the
// machine's maintenance coordinator, so they never overlap.

// Backups is a machine's nightly backup.
type Backups interface {
	Status(ctx context.Context) (backup.Status, error)
	RunNow() error
}

// Restores restores a machine's services from its backups.
type Restores interface {
	Snapshots(ctx context.Context, service string) ([]backup.Snapshot, error)
	Restore(service, snapshotID string) error
	Pending() bool
	Kept(service string) ([]backup.KeptCopy, error)
	RemoveKept(ctx context.Context, service, path string) error
}

// Updates is a machine's OS updates and restarts.
type Updates interface {
	Last(ctx context.Context) (*osupdate.Result, error)
	Pending() bool
	Submit() error
	RestartStatus() osupdate.RestartStatus
	RestartNow(ctx context.Context) error
}

// Maintenance is this machine's maintenance jobs. A nil job isn't
// available (e.g. backups aren't set up yet).
type Maintenance struct {
	Backups  Backups
	Restores Restores
	Updates  Updates
}

// Backups is this machine's backup job, or nil.
func (n *Local) Backups() Backups { return n.Jobs.Backups }

// Restores is this machine's restores, or nil.
func (n *Local) Restores() Restores { return n.Jobs.Restores }

// Updates is this machine's OS updates, or nil.
func (n *Local) Updates() Updates { return n.Jobs.Updates }
