package node

import (
	"context"

	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/osupdate"
)

// A server's maintenance jobs (SPEC.md §3.2, §6.9): each node backs up
// its own services and keeps its own OS up to date. The jobs share the
// server's maintenance coordinator, so they never overlap.

// Backups is a server's nightly backup.
type Backups interface {
	Status(ctx context.Context) (backup.Status, error)
	RunNow() error
}

// Restores restores a server's services from its backups.
type Restores interface {
	Snapshots(ctx context.Context, service string) ([]backup.Snapshot, error)
	Restore(service, snapshotID string) error
	Pending() bool
	Kept(service string) ([]backup.KeptCopy, error)
	RemoveKept(ctx context.Context, service, path string) error
}

// Updates is a server's OS updates and restarts.
type Updates interface {
	Last(ctx context.Context) (*osupdate.Result, error)
	Pending() bool
	Submit() error
	RestartStatus() osupdate.RestartStatus
	RestartNow(ctx context.Context) error
	// HostChanges are changes to the host MyLab didn't make (SPEC.md §3.2).
	HostChanges(ctx context.Context) (osupdate.HostCheck, error)
	AcknowledgeHostChanges(ctx context.Context) error
}

// Maintenance is this server's maintenance jobs. A nil job isn't
// available (e.g. backups aren't set up yet).
type Maintenance struct {
	Backups  Backups
	Restores Restores
	Updates  Updates
}

// Backups is this server's backup job, or nil.
func (n *Local) Backups() Backups { return n.Jobs.Backups }

// Restores is this server's restores, or nil.
func (n *Local) Restores() Restores { return n.Jobs.Restores }

// Updates is this server's OS updates, or nil.
func (n *Local) Updates() Updates { return n.Jobs.Updates }
